using System;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Net;
using System.Security.Cryptography;
using System.Text.RegularExpressions;
using System.Threading;

namespace FamilyCinema.Setup
{
    internal sealed class Downloads
    {
        private readonly Action<string> report;
        private readonly CancellationToken cancel;
        public Downloads(Action<string> report, CancellationToken cancel) { this.report = report; this.cancel = cancel; }
        public static string Hash(string path)
        {
            using (var input = File.OpenRead(path))
            using (var hash = SHA256.Create())
                return BitConverter.ToString(hash.ComputeHash(input)).Replace("-", "").ToLowerInvariant();
        }
        public static bool HashMatches(string path, string expected)
        {
            return File.Exists(path) && string.Equals(Hash(path), expected, StringComparison.OrdinalIgnoreCase);
        }
        public static IEnumerable<string> Github(string url)
        {
            Uri parsed;
            if (!Uri.TryCreate(url, UriKind.Absolute, out parsed) || parsed.Scheme != "https")
                throw new InvalidDataException("发布下载地址必须使用 HTTPS。");
            if (parsed.Host == "github.com")
            {
                yield return "https://gh-proxy.org/" + url;
                yield return "https://gh-proxy.com/" + url;
                yield return "https://ghfast.top/" + url;
                yield return "https://ghproxy.net/" + url;
            }
            yield return url;
        }
        public string Get(IEnumerable<string> urls, string expected, string cache, string label)
        {
            if (string.IsNullOrEmpty(expected) || !Regex.IsMatch(expected, "^[a-fA-F0-9]{64}$"))
                throw new InvalidDataException(label + " 缺少可信校验值，已停止安装。");
            Directory.CreateDirectory(cache);
            var target = Path.Combine(cache, expected.ToLowerInvariant() + ".zip");
            if (HashMatches(target, expected)) { report(label + "：使用已校验的本地缓存"); return target; }
            Exception last = null;
            foreach (var url in MirrorSelection.Rank(urls, cache, report, cancel))
            {
                cancel.ThrowIfCancellationRequested();
                var temporary = target + ".part";
                try
                {
                    report("下载" + label + "：" + new Uri(url).Host);
                    Download(url, temporary, label);
                    if (!HashMatches(temporary, expected)) throw new InvalidDataException(label + " 校验不一致");
                    if (File.Exists(target)) File.Delete(target);
                    File.Move(temporary, target);
                    MirrorSelection.RecordSuccess(url, cache);
                    return target;
                }
                catch (OperationCanceledException) { throw; }
                catch (Exception error)
                {
                    cancel.ThrowIfCancellationRequested();
                    last = error;
                    report(label + " 当前下载源失败，尝试下一个：" + error.Message);
                }
                finally { if (File.Exists(temporary)) File.Delete(temporary); }
            }
            throw new IOException(label + " 下载失败，请稍后点安装/修复重试。", last);
        }
        private void Download(string url, string path, string label)
        {
            var request = (HttpWebRequest)WebRequest.Create(url);
            request.Timeout = 20000; request.ReadWriteTimeout = 30000;
            request.UserAgent = "FamilyCinema-Windows-Setup/1";
            request.KeepAlive = false;
            request.AutomaticDecompression = DecompressionMethods.GZip | DecompressionMethods.Deflate;
            DownloadRouting.Configure(request, new Uri(url));
            using (cancel.Register(request.Abort))
            using (var response = (HttpWebResponse)request.GetResponse())
            using (var input = response.GetResponseStream())
            using (var output = File.Create(path))
            {
                if (response.ResponseUri.Scheme != "https" && !(new Uri(url).IsLoopback && response.ResponseUri.IsLoopback))
                    throw new IOException("下载源跳转到不安全地址。");
                var buffer = new byte[128 * 1024]; long total = 0; var last = DateTime.MinValue;
                int read;
                while ((read = input.Read(buffer, 0, buffer.Length)) > 0)
                {
                    cancel.ThrowIfCancellationRequested();
                    output.Write(buffer, 0, read); total += read;
                    if ((DateTime.UtcNow - last).TotalSeconds >= 3)
                    {
                        report(label + " " + (total / 1048576) + " MB" + (response.ContentLength > 0 ? " / " + (response.ContentLength / 1048576) + " MB" : ""));
                        last = DateTime.UtcNow;
                    }
                }
            }
        }
        public void Extract(string archive, string destination)
        {
            Directory.CreateDirectory(destination);
            string root = Path.GetFullPath(destination).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar;
            using (var zip = ZipFile.OpenRead(archive))
            {
                foreach (var entry in zip.Entries)
                {
                    cancel.ThrowIfCancellationRequested();
                    string target = Path.GetFullPath(Path.Combine(root, entry.FullName));
                    if (!target.StartsWith(root, StringComparison.OrdinalIgnoreCase))
                        throw new InvalidDataException("压缩包包含越界路径。");
                    if (entry.FullName.EndsWith("/")) { Directory.CreateDirectory(target); continue; }
                    Directory.CreateDirectory(Path.GetDirectoryName(target));
                    entry.ExtractToFile(target, true);
                }
            }
        }
    }
}
