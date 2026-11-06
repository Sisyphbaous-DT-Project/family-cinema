using System;
using System.Linq;
using System.Net;

namespace FamilyCinema.Setup
{
    internal static class DownloadRouting
    {
        private static readonly string[] DirectHosts = {
            "goproxy.cn", "mirrors.aliyun.com", "mirrors.ustc.edu.cn",
            "registry.npmmirror.com", "cdn.npmmirror.com"
        };

        public static void Configure(HttpWebRequest request, Uri uri)
        {
            if (uri.IsLoopback || DirectHosts.Any(host =>
                string.Equals(uri.Host, host, StringComparison.OrdinalIgnoreCase) ||
                uri.Host.EndsWith("." + host, StringComparison.OrdinalIgnoreCase)))
            {
                request.Proxy = null;
                return;
            }
            string configured = Environment.GetEnvironmentVariable("HTTPS_PROXY") ??
                Environment.GetEnvironmentVariable("HTTP_PROXY");
            Uri proxy;
            if (!string.IsNullOrWhiteSpace(configured) &&
                Uri.TryCreate(configured, UriKind.Absolute, out proxy) &&
                (proxy.Scheme == "http" || proxy.Scheme == "https"))
                request.Proxy = new WebProxy(proxy);
            else request.Proxy = WebRequest.DefaultWebProxy;
        }

        public static string GoNoProxy()
        {
            string previous = Environment.GetEnvironmentVariable("NO_PROXY") ??
                Environment.GetEnvironmentVariable("no_proxy") ?? "";
            return string.Join(",", new[] { previous, "localhost", "127.0.0.1", "::1" }
                .Concat(DirectHosts).Where(value => !string.IsNullOrWhiteSpace(value)).Distinct());
        }
    }
}
