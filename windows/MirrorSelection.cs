using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Globalization;
using System.IO;
using System.Linq;
using System.Net;
using System.Net.NetworkInformation;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using System.Web.Script.Serialization;

namespace FamilyCinema.Setup
{
    internal static class MirrorSelection
    {
        private const int SampleBytes = 64 * 1024;
        private const int ProbeMilliseconds = 4000;
        private const int RankMilliseconds = 5000;
        private const string CacheName = "mirror-selection.json";
        private static readonly object CacheLock = new object();
        private static readonly TimeSpan CacheLifetime = TimeSpan.FromMinutes(15);

        public static IList<string> Rank(IEnumerable<string> urls, string cache, Action<string> report, CancellationToken token)
        {
            token.ThrowIfCancellationRequested();
            var candidates = (urls ?? Enumerable.Empty<string>()).Where(u => !string.IsNullOrWhiteSpace(u)).Distinct(StringComparer.Ordinal).ToList();
            if (candidates.Count == 0) return candidates;
            string network = NetworkKey();
            var previous = ReadCache(cache, network);
            var probes = new List<Task<ProbeResult>>();
            using (var deadline = CancellationTokenSource.CreateLinkedTokenSource(token))
            {
                deadline.CancelAfter(RankMilliseconds);
                CancellationToken deadlineToken = deadline.Token;
                try
                {
                    // 独立后台任务同时取真实文件的一小段，不让慢源阻塞其他源。
                    foreach (string candidate in candidates)
                    {
                        string url = candidate;
                        probes.Add(Task.Factory.StartNew(() => Probe(url, deadlineToken), CancellationToken.None, TaskCreationOptions.LongRunning, TaskScheduler.Default));
                    }
                    Task.WaitAll(probes.Cast<Task>().ToArray(), RankMilliseconds, token);
                    token.ThrowIfCancellationRequested();
                }
                finally
                {
                    deadline.Cancel();
                }
            }
            token.ThrowIfCancellationRequested();

            var results = new List<ProbeResult>();
            for (int i = 0; i < candidates.Count; i++)
            {
                var task = probes[i];
                var result = task.Status == TaskStatus.RanToCompletion ? task.Result : new ProbeResult { Url = candidates[i], Failure = "测速超时" };
                result.Order = i;
                results.Add(result);
            }
            var ranked = results.OrderByDescending(r => r.Valid)
                .ThenByDescending(r => r.Valid ? r.BytesPerSecond : 0)
                .ThenBy(r => r.Valid ? r.FirstByteMilliseconds : 0)
                .ThenByDescending(r => PreviousSuccess(previous, r.Url))
                .ThenByDescending(r => PreviousSpeed(previous, r.Url))
                .ThenBy(r => r.Order).ToList();

            SaveProbes(cache, network, results);
            foreach (var result in ranked)
            {
                token.ThrowIfCancellationRequested();
                if (result.Valid)
                {
                    Notify(report, "镜像测速：" + Host(result.Url) + " " + Speed(result.BytesPerSecond) + "，首包 " + result.FirstByteMilliseconds.ToString("0", CultureInfo.InvariantCulture) + " ms");
                }
                else Notify(report, "镜像测速：" + Host(result.Url) + " " + result.Failure + "，保留为备用源");
            }
            Notify(report, ranked[0].Valid ? "自动选择下载源：" + Host(ranked[0].Url) : "测速未确认可用源，将依次尝试备用源并校验文件");
            return ranked.Select(r => r.Url).ToList();
        }

        public static void RecordSuccess(string url, string cache)
        {
            if (string.IsNullOrWhiteSpace(url)) return;
            lock (CacheLock)
            {
                string network = NetworkKey();
                CacheData data = ReadCacheUnlocked(cache, network);
                var entry = data.Entries.FirstOrDefault(e => e.Key == Key(url));
                if (entry == null) { entry = new CacheEntry { Key = Key(url) }; data.Entries.Add(entry); }
                entry.SuccessUtcTicks = DateTime.UtcNow.Ticks;
                WriteCacheUnlocked(cache, data);
            }
        }

        private static ProbeResult Probe(string url, CancellationToken token)
        {
            var result = new ProbeResult { Url = url, Failure = "测速失败" };
            Uri uri;
            if (!Uri.TryCreate(url, UriKind.Absolute, out uri) || (uri.Scheme != "https" && uri.Scheme != "http"))
            { result.Failure = "地址无效"; return result; }
            HttpWebRequest request = null;
            HttpWebResponse response = null;
            Stream input = null;
            try
            {
                token.ThrowIfCancellationRequested();
                request = (HttpWebRequest)WebRequest.Create(uri);
                request.Method = "GET";
                request.AddRange(0, SampleBytes - 1);
                request.Timeout = ProbeMilliseconds;
                request.ReadWriteTimeout = ProbeMilliseconds;
                request.MaximumAutomaticRedirections = 5;
                request.UserAgent = "FamilyCinema-Windows-Setup/1";
                request.AutomaticDecompression = DecompressionMethods.None;
                request.KeepAlive = false;
                request.ConnectionGroupName = "FamilyCinema-MirrorProbe-" + Guid.NewGuid().ToString("N");
                ConfigureProxy(request, uri);
                var clock = Stopwatch.StartNew();
                using (var timeout = CancellationTokenSource.CreateLinkedTokenSource(token))
                {
                    timeout.CancelAfter(ProbeMilliseconds);
                    using (timeout.Token.Register(request.Abort))
                    {
                        response = (HttpWebResponse)request.GetResponse();
                        if (response.StatusCode != HttpStatusCode.OK && response.StatusCode != HttpStatusCode.PartialContent)
                        { result.Failure = "HTTP " + ((int)response.StatusCode).ToString(CultureInfo.InvariantCulture); return result; }
                        if (!uri.IsLoopback && response.ResponseUri.Scheme != "https")
                        { result.Failure = "下载源跳转到不安全地址"; return result; }
                        string contentType = response.ContentType ?? "";
                        if (contentType.IndexOf("text/html", StringComparison.OrdinalIgnoreCase) >= 0 || contentType.IndexOf("application/xhtml", StringComparison.OrdinalIgnoreCase) >= 0)
                        { result.Failure = "返回验证页或错误页面"; return result; }
                        input = response.GetResponseStream();
                        var sample = new byte[SampleBytes];
                        int count = 0;
                        while (count < sample.Length)
                        {
                            timeout.Token.ThrowIfCancellationRequested();
                            int read = input.Read(sample, count, Math.Min(8192, sample.Length - count));
                            if (read == 0) break;
                            if (count == 0) result.FirstByteMilliseconds = clock.Elapsed.TotalMilliseconds;
                            count += read;
                        }
                        clock.Stop();
                        if (count < 4)
                        { result.Failure = "未返回有效文件内容"; return result; }
                        bool zip = sample[0] == 'P' && sample[1] == 'K' && ((sample[2] == 3 && sample[3] == 4) || (sample[2] == 5 && sample[3] == 6) || (sample[2] == 7 && sample[3] == 8));
                        string path = uri.AbsolutePath;
                        if ((path.EndsWith(".zip", StringComparison.OrdinalIgnoreCase) || path.EndsWith(".apk", StringComparison.OrdinalIgnoreCase)) && !zip)
                        { result.Failure = "文件头不符合 ZIP/APK 格式"; return result; }
                        string head = Encoding.ASCII.GetString(sample, 0, Math.Min(count, 256)).TrimStart('\uFEFF', ' ', '\t', '\r', '\n');
                        if (head.StartsWith("<!doctype html", StringComparison.OrdinalIgnoreCase) || head.StartsWith("<html", StringComparison.OrdinalIgnoreCase))
                        { result.Failure = "返回验证页或错误页面"; return result; }
                        result.Valid = true;
                        result.BytesPerSecond = count / Math.Max(clock.Elapsed.TotalSeconds, 0.001);
                        result.Bytes = count;
                        result.Milliseconds = clock.Elapsed.TotalMilliseconds;
                        result.Failure = null;
                        return result;
                    }
                }
            }
            catch (WebException error)
            {
                var status = error.Response as HttpWebResponse;
                if (status != null)
                {
                    result.Failure = "HTTP " + ((int)status.StatusCode).ToString(CultureInfo.InvariantCulture);
                    status.Close();
                }
                else result.Failure = token.IsCancellationRequested || error.Status == WebExceptionStatus.Timeout || error.Status == WebExceptionStatus.RequestCanceled ? "测速超时或已取消" : "连接失败";
            }
            catch (OperationCanceledException) { result.Failure = "测速超时或已取消"; }
            catch (Exception) { result.Failure = "测速失败"; }
            finally
            {
                // 不继续接收完整大文件；先中止，再关闭流，避免服务器忽略 Range 时排空响应。
                if (request != null) { try { request.Abort(); } catch { } }
                if (input != null) { try { input.Dispose(); } catch { } }
                if (response != null) { try { response.Close(); } catch { } }
                if (request != null) { try { request.ServicePoint.CloseConnectionGroup(request.ConnectionGroupName); } catch { } }
            }
            return result;
        }

        private static void ConfigureProxy(HttpWebRequest request, Uri uri)
        {
            DownloadRouting.Configure(request, uri);
        }

        private static string Host(string url)
        {
            Uri uri;
            return Uri.TryCreate(url, UriKind.Absolute, out uri) ? uri.Host : "无效下载源";
        }

        private static string Speed(double bytes)
        {
            return bytes >= 1048576 ? (bytes / 1048576).ToString("0.00", CultureInfo.InvariantCulture) + " MB/s" : (bytes / 1024).ToString("0.0", CultureInfo.InvariantCulture) + " KB/s";
        }

        private static void Notify(Action<string> report, string text)
        {
            if (report != null) report(text);
        }

        private static long PreviousSuccess(CacheData cache, string url)
        {
            string key = Key(url);
            var entry = cache.Entries.FirstOrDefault(e => e.Key == key);
            return entry == null ? 0 : entry.SuccessUtcTicks;
        }

        private static double PreviousSpeed(CacheData cache, string url)
        {
            string key = Key(url);
            var entry = cache.Entries.FirstOrDefault(e => e.Key == key);
            return entry == null || entry.ProbeUtcTicks == 0 || entry.Milliseconds <= 0 ? 0 : entry.Bytes * 1000 / entry.Milliseconds;
        }

        private static string Key(string value)
        {
            using (var hash = SHA256.Create()) return BitConverter.ToString(hash.ComputeHash(Encoding.UTF8.GetBytes(value))).Replace("-", "").ToLowerInvariant();
        }

        private static string NetworkKey()
        {
            try
            {
                var parts = NetworkInterface.GetAllNetworkInterfaces().Where(n => n.OperationalStatus == OperationalStatus.Up && n.NetworkInterfaceType != NetworkInterfaceType.Loopback)
                    .Select(n => n.Id + ":" + string.Join(",", n.GetIPProperties().GatewayAddresses.Select(a => a.Address.ToString()).OrderBy(a => a, StringComparer.Ordinal)))
                    .OrderBy(n => n, StringComparer.Ordinal);
                return Key(string.Join(";", parts) + "|" + Environment.GetEnvironmentVariable("HTTPS_PROXY") + "|" + Environment.GetEnvironmentVariable("HTTP_PROXY"));
            }
            catch { return Key("unknown-network"); }
        }

        private static CacheData ReadCache(string cache, string network)
        {
            lock (CacheLock) return ReadCacheUnlocked(cache, network);
        }

        private static CacheData ReadCacheUnlocked(string cache, string network)
        {
            var empty = new CacheData { Network = network, Entries = new List<CacheEntry>() };
            try
            {
                string path = Path.Combine(cache, CacheName);
                if (!File.Exists(path) || new FileInfo(path).Length > 256 * 1024) return empty;
                var data = new JavaScriptSerializer().Deserialize<CacheData>(File.ReadAllText(path, Encoding.UTF8));
                if (data == null || data.Network != network || data.Entries == null) return empty;
                long now = DateTime.UtcNow.Ticks;
                data.Entries = data.Entries.Where(e => e != null && !string.IsNullOrEmpty(e.Key) && Math.Max(e.ProbeUtcTicks, e.SuccessUtcTicks) <= now && now - Math.Max(e.ProbeUtcTicks, e.SuccessUtcTicks) <= CacheLifetime.Ticks).Take(80).ToList();
                foreach (var entry in data.Entries)
                {
                    if (now - entry.ProbeUtcTicks > CacheLifetime.Ticks) entry.ProbeUtcTicks = 0;
                    if (now - entry.SuccessUtcTicks > CacheLifetime.Ticks) entry.SuccessUtcTicks = 0;
                }
                return data;
            }
            catch { return empty; }
        }

        private static void SaveProbes(string cache, string network, IEnumerable<ProbeResult> results)
        {
            lock (CacheLock)
            {
                var data = ReadCacheUnlocked(cache, network);
                foreach (var result in results.Where(r => r.Valid))
                {
                    string key = Key(result.Url);
                    var entry = data.Entries.FirstOrDefault(e => e.Key == key);
                    if (entry == null) { entry = new CacheEntry { Key = key }; data.Entries.Add(entry); }
                    entry.ProbeUtcTicks = DateTime.UtcNow.Ticks;
                    entry.Bytes = result.Bytes;
                    entry.Milliseconds = result.Milliseconds;
                    entry.FirstByteMilliseconds = result.FirstByteMilliseconds;
                }
                WriteCacheUnlocked(cache, data);
            }
        }

        private static void WriteCacheUnlocked(string cache, CacheData data)
        {
            string temporary = null;
            try
            {
                if (string.IsNullOrWhiteSpace(cache)) return;
                Directory.CreateDirectory(cache);
                data.Entries = data.Entries.OrderByDescending(e => Math.Max(e.ProbeUtcTicks, e.SuccessUtcTicks)).Take(80).ToList();
                string path = Path.Combine(cache, CacheName);
                temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
                File.WriteAllText(temporary, new JavaScriptSerializer().Serialize(data), new UTF8Encoding(false));
                if (File.Exists(path)) File.Replace(temporary, path, null);
                else File.Move(temporary, path);
            }
            catch { }
            finally { if (temporary != null) { try { if (File.Exists(temporary)) File.Delete(temporary); } catch { } } }
        }

        private sealed class ProbeResult
        {
            public string Url;
            public bool Valid;
            public string Failure;
            public int Order;
            public int Bytes;
            public double BytesPerSecond;
            public double Milliseconds;
            public double FirstByteMilliseconds;
        }

        public sealed class CacheData
        {
            public string Network { get; set; }
            public List<CacheEntry> Entries { get; set; }
        }

        public sealed class CacheEntry
        {
            public string Key { get; set; }
            public long ProbeUtcTicks { get; set; }
            public long SuccessUtcTicks { get; set; }
            public int Bytes { get; set; }
            public double Milliseconds { get; set; }
            public double FirstByteMilliseconds { get; set; }
        }
    }
}
