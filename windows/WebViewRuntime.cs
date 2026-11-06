using System;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Runtime.InteropServices;
using System.Security.Cryptography.X509Certificates;
using System.Threading;

namespace FamilyCinema.Setup
{
    internal static class WebViewRuntime
    {
        private const string BootstrapUrl = "https://go.microsoft.com/fwlink/p/?LinkId=2124703";

        public static void Install(Action<string> report, CancellationToken cancel)
        {
            string folder = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinemaSetup", "webview", "runtime-bootstrap");
            Directory.CreateDirectory(folder);
            string file = Path.Combine(folder, "MicrosoftEdgeWebview2Setup-" + Guid.NewGuid().ToString("N") + ".exe");
            try
            {
                report("正在从微软下载网页界面运行时");
                Download(file, report, cancel);
                cancel.ThrowIfCancellationRequested();
                report("正在验证微软数字签名");
                VerifyMicrosoftSignature(file);
                cancel.ThrowIfCancellationRequested();
                report("正在准备网页界面，首次安装可能需要几分钟");
                using (var process = Process.Start(new ProcessStartInfo(file, "/silent /install") {
                    UseShellExecute = false, CreateNoWindow = true, WorkingDirectory = folder
                }))
                {
                    var watch = Stopwatch.StartNew();
                    while (!process.WaitForExit(250))
                    {
                        if (cancel.IsCancellationRequested || watch.Elapsed > TimeSpan.FromMinutes(10))
                        {
                            // 只终止本次启动的引导进程，不按名称操作微软更新服务。
                            try { process.Kill(); process.WaitForExit(5000); } catch { }
                            cancel.ThrowIfCancellationRequested();
                            throw new IOException("界面运行时准备超时，请检查网络后重试。");
                        }
                    }
                    if (process.ExitCode != 0 && process.ExitCode != 3010)
                        throw new IOException("微软运行时安装未完成（代码 " + process.ExitCode + "），请检查网络后重试。");
                }
            }
            finally
            {
                try { if (File.Exists(file)) File.Delete(file); } catch { }
            }
        }

        private static void Download(string file, Action<string> report, CancellationToken cancel)
        {
            var request = (HttpWebRequest)WebRequest.Create(BootstrapUrl);
            request.Timeout = 20000;
            request.ReadWriteTimeout = 20000;
            request.MaximumAutomaticRedirections = 6;
            request.UserAgent = "FamilyCinema-Windows-Setup/1";
            string proxy = Environment.GetEnvironmentVariable("HTTPS_PROXY") ?? Environment.GetEnvironmentVariable("HTTP_PROXY");
            Uri proxyUri;
            if (!string.IsNullOrWhiteSpace(proxy) && Uri.TryCreate(proxy, UriKind.Absolute, out proxyUri) && (proxyUri.Scheme == "http" || proxyUri.Scheme == "https"))
                request.Proxy = new WebProxy(proxyUri);
            using (cancel.Register(request.Abort))
            using (var response = (HttpWebResponse)request.GetResponse())
            using (var input = response.GetResponseStream())
            using (var output = File.Create(file))
            {
                if (response.StatusCode != HttpStatusCode.OK || response.ResponseUri.Scheme != "https")
                    throw new IOException("微软运行时下载没有返回安全的文件。");
                long total = 0;
                var last = DateTime.MinValue;
                var buffer = new byte[64 * 1024];
                int read;
                while ((read = input.Read(buffer, 0, buffer.Length)) > 0)
                {
                    cancel.ThrowIfCancellationRequested();
                    total += read;
                    if (total > 20 * 1024 * 1024) throw new IOException("运行时引导程序大小异常，已停止。");
                    output.Write(buffer, 0, read);
                    if ((DateTime.UtcNow - last).TotalSeconds > 1)
                    { report("正在下载界面运行时：" + (total / 1024) + " KB"); last = DateTime.UtcNow; }
                }
                if (total < 64 * 1024) throw new IOException("运行时引导程序不完整，请重试。");
            }
        }

        public static void VerifyMicrosoftSignature(string path)
        {
            var info = new WinTrustFileInfo {
                Size = (uint)Marshal.SizeOf(typeof(WinTrustFileInfo)),
                FilePath = path
            };
            IntPtr file = Marshal.AllocHGlobal(Marshal.SizeOf(typeof(WinTrustFileInfo)));
            var trust = new WinTrustData {
                Size = (uint)Marshal.SizeOf(typeof(WinTrustData)),
                UIChoice = 2, RevocationChecks = 1, UnionChoice = 1,
                File = file, StateAction = 1,
                ProviderFlags = 0x00000080
            };
            Marshal.StructureToPtr(info, file, false);
            var action = new Guid("00AAC56B-CD44-11d0-8CC2-00C04FC295EE");
            try
            {
                int status = WinVerifyTrust(new IntPtr(-1), action, ref trust);
                if (status != 0) throw new IOException("运行时引导程序的数字签名未通过 Windows 信任验证，已停止。");
                using (var certificate = new X509Certificate2(X509Certificate.CreateFromSignedFile(path)))
                    if (!string.Equals(certificate.GetNameInfo(X509NameType.SimpleName, false), "Microsoft Corporation", StringComparison.Ordinal))
                        throw new IOException("运行时引导程序不是微软签名，已停止。");
            }
            finally
            {
                trust.StateAction = 2;
                WinVerifyTrust(new IntPtr(-1), action, ref trust);
                Marshal.DestroyStructure(file, typeof(WinTrustFileInfo));
                Marshal.FreeHGlobal(file);
            }
        }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        private struct WinTrustFileInfo
        {
            public uint Size;
            [MarshalAs(UnmanagedType.LPWStr)] public string FilePath;
            public IntPtr File;
            public IntPtr KnownSubject;
        }

        [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
        private struct WinTrustData
        {
            public uint Size;
            public IntPtr PolicyCallbackData;
            public IntPtr SIPClientData;
            public uint UIChoice;
            public uint RevocationChecks;
            public uint UnionChoice;
            public IntPtr File;
            public uint StateAction;
            public IntPtr StateData;
            [MarshalAs(UnmanagedType.LPWStr)] public string URLReference;
            public uint ProviderFlags;
            public uint UIContext;
            public IntPtr SignatureSettings;
        }

        [DllImport("wintrust.dll", ExactSpelling = true, CharSet = CharSet.Unicode)]
        private static extern int WinVerifyTrust(IntPtr window, [MarshalAs(UnmanagedType.LPStruct)] Guid action, ref WinTrustData data);
    }
}
