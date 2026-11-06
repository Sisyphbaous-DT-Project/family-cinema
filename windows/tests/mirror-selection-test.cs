using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;
using FamilyCinema.Setup;

internal static class MirrorSelectionTest
{
    private static int Main()
    {
        string root = Path.Combine(Path.GetTempPath(), "family-cinema-mirror-test-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        try
        {
            using (var fixture = new Fixture())
            {
                var log = new List<string>();
                Action<string> report = text => log.Add(text);
                string fast = fixture.Url("fast.zip?token=do-not-log-this");
                string slow = fixture.Url("slow.zip");
                string hang = fixture.Url("hang.zip");
                string html = fixture.Url("html.zip");
                string noRange = fixture.Url("norange.apk");
                var watch = Stopwatch.StartNew();
                var ranked = MirrorSelection.Rank(new[] { slow, hang, html, fast, noRange }, root, report, CancellationToken.None);
                watch.Stop();
                Require(ranked.Count == 5 && ranked[0] == fast && ranked[1] == noRange && ranked[2] == slow, "valid sources were not sorted by real transfer time");
                Require(ranked.Skip(3).Contains(html) && ranked.Skip(3).Contains(hang), "failed mirrors must remain last for actual download fallback");
                Require(watch.Elapsed.TotalSeconds < 5.9, "parallel probing exceeded the six-second bound");
                Require(fixture.MaximumConcurrent >= 3, "mirror probes did not run concurrently");
                Require(fixture.RangeRequests >= 5, "real download URLs were not sampled using Range");
                Require(!log.Any(text => text.Contains("do-not-log-this") || text.Contains("fast.zip")), "log leaked URL paths or tokens");
                Require(File.Exists(Path.Combine(root, "mirror-selection.json")), "probe cache was not saved");
                Require(!File.ReadAllText(Path.Combine(root, "mirror-selection.json")).Contains("do-not-log-this"), "cache leaked a download token");
                Console.WriteLine("PASS parallel ranking, 200 without Range support, HTML rejection, failure fallback, bounded sampling: " + watch.Elapsed.TotalSeconds.ToString("0.00") + "s");

                ranked = MirrorSelection.Rank(new[] { fixture.Url("fake.zip"), fixture.Url("error.zip"), fast }, root, report, CancellationToken.None);
                Require(ranked[0] == fast && ranked.Count == 3, "a disguised HTML download or HTTP error outranked the valid archive");
                Require(log.Any(text => text.Contains("ZIP/APK")) && log.Any(text => text.Contains("HTTP 404")), "archive signature and HTTP error paths were not checked");
                Console.WriteLine("PASS archive magic, disguised HTML and HTTP status rejection");

                MirrorSelection.RecordSuccess(slow, root);
                fixture.FailAll = true;
                int before = fixture.Requests;
                ranked = MirrorSelection.Rank(new[] { fast, slow }, root, report, CancellationToken.None);
                Require(ranked[0] == slow && ranked.Count == 2, "recent same-network success did not order failed fallback mirrors");
                Require(fixture.Requests - before >= 2, "cache prevented fresh parallel measurements");
                File.WriteAllText(Path.Combine(root, "mirror-selection.json"), "broken json");
                ranked = MirrorSelection.Rank(new[] { fast, slow }, root, report, CancellationToken.None);
                Require(ranked[0] == fast, "malformed optional cache broke deterministic fallback");
                Console.WriteLine("PASS success cache, fresh measurement, corrupt-cache recovery");

                fixture.FailAll = false;
                using (var cancel = new CancellationTokenSource())
                {
                    cancel.CancelAfter(200);
                    watch.Restart();
                    bool cancelled = false;
                    try { MirrorSelection.Rank(new[] { hang, fixture.Url("hang2.zip") }, root, report, cancel.Token); }
                    catch (OperationCanceledException) { cancelled = true; }
                    watch.Stop();
                    Require(cancelled && watch.Elapsed.TotalSeconds < 1.5, "cancellation did not abort promptly");
                    Console.WriteLine("PASS prompt cancellation: " + watch.Elapsed.TotalSeconds.ToString("0.00") + "s");
                }
                Require(MirrorSelection.Rank(new string[0], root, report, CancellationToken.None).Count == 0, "empty input failed");
            }
            Console.WriteLine("All mirror-selection tests passed.");
            return 0;
        }
        catch (Exception error)
        {
            Console.Error.WriteLine(error);
            return 1;
        }
        finally { if (Directory.Exists(root)) Directory.Delete(root, true); }
    }

    private static void Require(bool condition, string message)
    { if (!condition) throw new InvalidOperationException(message); }

    private sealed class Fixture : IDisposable
    {
        private readonly TcpListener listener;
        private readonly Thread accept;
        private readonly List<TcpClient> clients = new List<TcpClient>();
        private readonly ManualResetEvent stop = new ManualResetEvent(false);
        private int running;
        private int maximum;
        private int ranges;
        private int requests;
        public volatile bool FailAll;
        public int MaximumConcurrent { get { return maximum; } }
        public int RangeRequests { get { return ranges; } }
        public int Requests { get { return requests; } }

        public Fixture()
        {
            listener = new TcpListener(IPAddress.Loopback, 0);
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
                    ThreadPool.QueueUserWorkItem(_ => Handle(client));
                }
                catch { return; }
            }
        }

        private void Handle(TcpClient client)
        {
            int active = Interlocked.Increment(ref running);
            int seen;
            do { seen = maximum; } while (active > seen && Interlocked.CompareExchange(ref maximum, active, seen) != seen);
            try
            {
                client.ReceiveTimeout = 2000;
                client.SendTimeout = 2000;
                var stream = client.GetStream();
                var buffer = new List<byte>();
                while (buffer.Count < 16384)
                {
                    int value = stream.ReadByte();
                    if (value < 0) return;
                    buffer.Add((byte)value);
                    int count = buffer.Count;
                    if (count >= 4 && buffer[count - 4] == 13 && buffer[count - 3] == 10 && buffer[count - 2] == 13 && buffer[count - 1] == 10) break;
                }
                string request = Encoding.ASCII.GetString(buffer.ToArray());
                string route = request.Split(' ')[1];
                Interlocked.Increment(ref requests);
                if (request.IndexOf("Range: bytes=0-65535", StringComparison.OrdinalIgnoreCase) >= 0) Interlocked.Increment(ref ranges);
                if (FailAll)
                { Write(stream, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); return; }
                if (route.Contains("hang")) { stop.WaitOne(8000); return; }
                if (route.Contains("html"))
                { Write(stream, "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\nContent-Length: 28\r\nConnection: close\r\n\r\n<html>verification</html>"); return; }
                if (route.Contains("fake"))
                { Write(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Length: 25\r\nConnection: close\r\n\r\n<html>verification</html>"); return; }
                if (route.Contains("error"))
                { Write(stream, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); return; }
                if (route.Contains("slow")) { if (stop.WaitOne(850)) return; }
                else if (route.Contains("norange")) { if (stop.WaitOne(300)) return; }
                else { if (stop.WaitOne(25)) return; }
                int size = route.Contains("norange") ? 256 * 1024 : 64 * 1024;
                Write(stream, "HTTP/1.1 " + (route.Contains("norange") ? "200 OK" : "206 Partial Content") + "\r\nContent-Type: application/octet-stream\r\nContent-Length: " + size + "\r\nConnection: close\r\n\r\n");
                var body = new byte[size];
                body[0] = (byte)'P'; body[1] = (byte)'K'; body[2] = 3; body[3] = 4;
                stream.Write(body, 0, body.Length);
                stream.Flush();
            }
            catch { }
            finally
            {
                client.Close();
                lock (clients) clients.Remove(client);
                Interlocked.Decrement(ref running);
            }
        }

        private static void Write(Stream stream, string text)
        { byte[] data = Encoding.ASCII.GetBytes(text); stream.Write(data, 0, data.Length); stream.Flush(); }

        public void Dispose()
        {
            stop.Set();
            listener.Stop();
            lock (clients) foreach (var client in clients.ToArray()) client.Close();
            accept.Join(300);
        }
    }
}
