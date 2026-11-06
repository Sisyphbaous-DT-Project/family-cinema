using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.IO.Compression;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using FamilyCinema.Setup;

internal static class DownloadsTest
{
    private static int Main()
    {
        string root = Path.Combine(Path.GetTempPath(), "family-cinema-download-test-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            byte[] archive = CreateArchive();
            string expected = Hash(archive);
            using (var fixture = new Fixture(archive))
            {
                var log = new List<string>();
                var downloads = new Downloads(text => log.Add(text), CancellationToken.None);
                string cache = Path.Combine(root, "verified-cache");
                // 输入顺序故意把好源放前面，确认实际下载顺序由测速决定。
                string[] urls = { fixture.Url("good.zip"), fixture.Url("bad.zip") };
                string target = downloads.Get(urls, expected, cache, "测试源码");
                Require(Downloads.HashMatches(target, expected), "the final archive did not match the trusted hash");
                Require(File.ReadAllBytes(target).SequenceEqual(archive), "the corrupted archive reached the download cache");
                Require(fixture.FullDownloads.SequenceEqual(new[] { "/bad.zip", "/good.zip" }),
                    "the fastest tampered source was not tried before the slower valid source");
                Require(fixture.RangeRequests == 2, "the real mirror URLs were not sampled before downloading");
                Require(log.Any(text => text.Contains("校验不一致") && text.Contains("尝试下一个")),
                    "hash rejection and automatic fallback were not reported");
                Require(Directory.GetFiles(root, "*.part", SearchOption.AllDirectories).Length == 0,
                    "a partial file remained after fallback");
                using (var zip = ZipFile.OpenRead(target))
                    Require(zip.GetEntry("payload.bin") != null, "the accepted payload was not a complete ZIP archive");
                Console.WriteLine("PASS fastest tampered mirror rejected by SHA256, slower valid mirror accepted, partial files removed");

                int before = fixture.Requests;
                string cached = downloads.Get(urls, expected, cache, "测试源码");
                Require(cached == target && fixture.Requests == before, "a verified cache hit still made network requests");
                Require(log.Any(text => text.Contains("使用已校验的本地缓存")), "the verified cache hit was not reported");
                Console.WriteLine("PASS verified cache reused without probing or downloading again");

                using (var cancel = new CancellationTokenSource())
                {
                    string cancelledCache = Path.Combine(root, "cancelled-cache");
                    Exception failure = null;
                    bool completed = false;
                    var worker = new Thread(() => {
                        try
                        {
                            new Downloads(text => { }, cancel.Token).Get(new[] { fixture.Url("cancel.zip") },
                                Hash(fixture.LargeArchive), cancelledCache, "取消测试");
                            completed = true;
                        }
                        catch (Exception error) { failure = error; }
                    }) { IsBackground = true };
                    worker.Start();
                    try
                    {
                        Require(fixture.LargeDownloadStarted.WaitOne(5000), "the cancellation test did not reach the full download");
                        var writing = Stopwatch.StartNew();
                        while (!HasPartialData(cancelledCache) && writing.ElapsedMilliseconds < 1500) Thread.Sleep(10);
                        Require(HasPartialData(cancelledCache), "cancellation occurred before any partial data was saved");
                        var elapsed = Stopwatch.StartNew();
                        cancel.Cancel();
                        Require(worker.Join(2000), "cancelling an active download did not finish within two seconds");
                        elapsed.Stop();
                        Require(!completed && failure is OperationCanceledException, "an active download did not report cancellation");
                        Require(Directory.GetFiles(cancelledCache, "*.part").Length == 0, "the cancelled download left a partial file");
                        Require(Directory.GetFiles(cancelledCache, "*.zip").Length == 0, "an incomplete download was published to the verified cache");
                        Require(Downloads.HashMatches(target, expected), "cancellation damaged a previously verified file");
                        Console.WriteLine("PASS active chunked download cancelled and cleaned in " + elapsed.Elapsed.TotalSeconds.ToString("0.00") + "s");
                    }
                    finally
                    {
                        cancel.Cancel();
                        if (worker.IsAlive) worker.Join(3000);
                    }
                }
            }
            Console.WriteLine("All real Downloads.Get integration tests passed.");
            return 0;
        }
        catch (Exception error)
        {
            Console.Error.WriteLine(error);
            return 1;
        }
        finally
        {
            string resolved = Path.GetFullPath(root);
            string parent = Path.GetFullPath(Path.GetTempPath()).TrimEnd(Path.DirectorySeparatorChar);
            Require(string.Equals(Path.GetDirectoryName(resolved), parent, StringComparison.OrdinalIgnoreCase)
                && Path.GetFileName(resolved).StartsWith("family-cinema-download-test-", StringComparison.Ordinal),
                "refusing to clean a directory outside the temporary test workspace");
            if (Directory.Exists(resolved)) Directory.Delete(resolved, true);
        }
    }

    private static byte[] CreateArchive()
    {
        var content = new byte[96 * 1024];
        new Random(20261106).NextBytes(content);
        using (var output = new MemoryStream())
        {
            using (var archive = new ZipArchive(output, ZipArchiveMode.Create, true))
            using (var entry = archive.CreateEntry("payload.bin", CompressionLevel.NoCompression).Open())
                entry.Write(content, 0, content.Length);
            return output.ToArray();
        }
    }

    private static bool HasPartialData(string directory)
    {
        return Directory.Exists(directory) && Directory.GetFiles(directory, "*.part").Any(path => new FileInfo(path).Length > 0);
    }

    private static string Hash(byte[] data)
    {
        using (var hash = SHA256.Create())
            return BitConverter.ToString(hash.ComputeHash(data)).Replace("-", "").ToLowerInvariant();
    }

    private static void Require(bool condition, string message)
    { if (!condition) throw new InvalidOperationException(message); }

    private sealed class Fixture : IDisposable
    {
        private readonly TcpListener listener = new TcpListener(IPAddress.Loopback, 0);
        private readonly ManualResetEvent stop = new ManualResetEvent(false);
        private readonly List<TcpClient> clients = new List<TcpClient>();
        private readonly List<Thread> handlers = new List<Thread>();
        private readonly List<string> fullDownloads = new List<string>();
        private readonly byte[] good;
        private readonly byte[] bad;
        private readonly Thread accept;
        private int requests;
        private int rangeRequests;
        public readonly byte[] LargeArchive = new byte[16 * 1024 * 1024];
        public readonly ManualResetEvent LargeDownloadStarted = new ManualResetEvent(false);
        public int Requests { get { return Volatile.Read(ref requests); } }
        public int RangeRequests { get { return Volatile.Read(ref rangeRequests); } }
        public string[] FullDownloads { get { lock (fullDownloads) return fullDownloads.ToArray(); } }

        public Fixture(byte[] good)
        {
            this.good = good;
            bad = (byte[])good.Clone();
            bad[bad.Length / 2] ^= 0x5a;
            LargeArchive[0] = (byte)'P'; LargeArchive[1] = (byte)'K'; LargeArchive[2] = 3; LargeArchive[3] = 4;
            listener.Start();
            accept = new Thread(Accept) { IsBackground = true };
            accept.Start();
        }

        public string Url(string path)
        { return "http://127.0.0.1:" + ((IPEndPoint)listener.LocalEndpoint).Port + "/" + path; }

        private void Accept()
        {
            while (!stop.WaitOne(0))
            {
                try
                {
                    var client = listener.AcceptTcpClient();
                    lock (clients) clients.Add(client);
                    var handler = new Thread(() => Handle(client)) { IsBackground = true };
                    lock (handlers) handlers.Add(handler);
                    handler.Start();
                }
                catch (SocketException) { return; }
                catch (ObjectDisposedException) { return; }
            }
        }

        private void Handle(TcpClient client)
        {
            try
            {
                client.ReceiveTimeout = 2000;
                client.SendTimeout = 2000;
                var stream = client.GetStream();
                var header = new List<byte>();
                while (header.Count < 16384)
                {
                    int value = stream.ReadByte();
                    if (value < 0) return;
                    header.Add((byte)value);
                    int length = header.Count;
                    if (length >= 4 && header[length - 4] == 13 && header[length - 3] == 10
                        && header[length - 2] == 13 && header[length - 1] == 10) break;
                }
                string request = Encoding.ASCII.GetString(header.ToArray());
                string route = request.Split(' ')[1];
                bool range = request.IndexOf("Range: bytes=0-65535", StringComparison.OrdinalIgnoreCase) >= 0;
                Interlocked.Increment(ref requests);
                if (range) Interlocked.Increment(ref rangeRequests);
                else lock (fullDownloads) fullDownloads.Add(route);
                byte[] body = route == "/cancel.zip" ? LargeArchive : route == "/bad.zip" ? bad : good;
                if (stop.WaitOne(route == "/good.zip" ? 350 : 10)) return;
                int size = range ? 64 * 1024 : body.Length;
                string response = "HTTP/1.1 " + (range ? "206 Partial Content" : "200 OK")
                    + "\r\nContent-Type: application/zip\r\nContent-Length: " + size
                    + (range ? "\r\nContent-Range: bytes 0-" + (size - 1) + "/" + body.Length : "")
                    + "\r\nConnection: close\r\n\r\n";
                byte[] responseBytes = Encoding.ASCII.GetBytes(response);
                stream.Write(responseBytes, 0, responseBytes.Length);
                for (int offset = 0; offset < size; offset += 8192)
                {
                    if (!range && route == "/cancel.zip" && stop.WaitOne(80)) return;
                    stream.Write(body, offset, Math.Min(8192, size - offset));
                    stream.Flush();
                    if (!range && route == "/cancel.zip") LargeDownloadStarted.Set();
                }
            }
            catch (IOException) { }
            catch (ObjectDisposedException) { }
            finally
            {
                client.Close();
                lock (clients) clients.Remove(client);
            }
        }

        public void Dispose()
        {
            stop.Set();
            listener.Stop();
            accept.Join(1000);
            lock (clients) foreach (var client in clients.ToArray()) client.Close();
            Thread[] workers;
            lock (handlers) workers = handlers.ToArray();
            foreach (var worker in workers)
                Require(worker.Join(2000), "a local fixture handler did not stop");
            LargeDownloadStarted.Dispose();
            stop.Dispose();
        }
    }
}
