using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Security.Cryptography;
using System.Text;
using System.Threading;
using System.Windows.Forms;

namespace FamilyCinema.Setup
{
    internal static class Program
    {
        [STAThread]
        private static int Main(string[] args)
        {
            ServicePointManager.SecurityProtocol = SecurityProtocolType.Tls12;
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            try
            {
                var input = Arguments(args);
                if (!Environment.Is64BitOperatingSystem || Environment.OSVersion.Version.Major < 10)
                    throw new IOException("此安装包需要 Windows 10 或 11 的 64 位系统。");
                var defaults = InstallOptions.Defaults();
                string root = Value(input, "--root", Installation.LastRoot() ?? defaults.Root);
                if (input.ContainsKey("--host")) { ChildProcesses.Host(root, Value(input, "--component", "")); return 0; }
                if (input.ContainsKey("--self-test")) return SelfTest();
                if (input.ContainsKey("--firewall")) { ConfigureFirewall(root); return 0; }
                string key;
                using (var hash = SHA256.Create()) key = BitConverter.ToString(hash.ComputeHash(Encoding.UTF8.GetBytes(Path.GetFullPath(root).ToLowerInvariant()))).Replace("-", "");
                bool created;
                using (var mutex = new Mutex(true, @"Local\FamilyCinema-" + key.Substring(0, 24), out created))
                {
                    if (!created) throw new IOException("此安装目录已经有管理窗口或操作在运行，请先关闭已有窗口。");
                    try
                    {
                        var release = ReleaseSettings.Load();
                        if (input.ContainsKey("--stop")) { ChildProcesses.Stop(root, ConsoleReport); return 0; }
                        if (input.ContainsKey("--start")) { ChildProcesses.Start(InstallState.Load(root), ConsoleReport, CancellationToken.None); return 0; }
                        if (input.ContainsKey("--install"))
                        {
                            var existing = InstallState.Load(root);
                            if (existing != null) defaults = existing.Options;
                            defaults.Root = root; defaults.Media = Value(input, "--media", defaults.Media);
                            defaults.Pansou = Boolean(input, "--pansou", defaults.Pansou); defaults.FFmpeg = Boolean(input, "--ffmpeg", defaults.FFmpeg);
                            defaults.Lan = Boolean(input, "--lan", defaults.Lan); defaults.AutoStart = Boolean(input, "--autostart", defaults.AutoStart);
                            defaults.Port = int.Parse(Value(input, "--port", defaults.Port.ToString()));
                            new Installation(release, ConsoleReport, CancellationToken.None).Install(defaults, !input.ContainsKey("--no-start"));
                            return 0;
                        }
                        Application.Run(new MainForm(release, root));
                    }
                    finally { mutex.ReleaseMutex(); }
                }
                return 0;
            }
            catch (Exception e)
            {
                if (Array.Exists(args, p => p == "--install" || p == "--start" || p == "--stop" || p == "--self-test" || p == "--host"))
                { ConsoleReport("失败：" + e.Message); return 1; }
                MessageBox.Show(e.Message, "家庭影院", MessageBoxButtons.OK, MessageBoxIcon.Error);
                return 1;
            }
        }
        private static Dictionary<string, string> Arguments(string[] args)
        {
            var result = new Dictionary<string, string>();
            for (int i = 0; i < args.Length; i++)
            {
                if (!args[i].StartsWith("--")) throw new ArgumentException("未知参数：" + args[i]);
                result[args[i]] = i + 1 < args.Length && !args[i + 1].StartsWith("--") ? args[++i] : "true";
            }
            return result;
        }
        private static string Value(Dictionary<string, string> args, string key, string fallback)
        { string value; return args.TryGetValue(key, out value) ? value : fallback; }
        private static bool Boolean(Dictionary<string, string> args, string key, bool fallback)
        { return bool.Parse(Value(args, key, fallback.ToString())); }
        private static void ConsoleReport(string value)
        {
            Console.WriteLine(value);
            string path = Environment.GetEnvironmentVariable("FAMILY_CINEMA_SETUP_LOG");
            if (!string.IsNullOrWhiteSpace(path)) File.AppendAllText(path, DateTime.Now.ToString("s") + " " + value + Environment.NewLine, new UTF8Encoding(false));
        }
        private static void ConfigureFirewall(string root)
        {
            var state = InstallState.Load(root);
            if (state == null || !state.Options.Lan) throw new IOException("尚未启用局域网访问。");
            string binary = Path.Combine(state.ActiveDirectory, "media-server.exe");
            string netsh = Path.Combine(Environment.SystemDirectory, "netsh.exe");
            string installKey;
            using (var hash = SHA256.Create())
                installKey = BitConverter.ToString(hash.ComputeHash(Encoding.UTF8.GetBytes(Path.GetFullPath(root).ToLowerInvariant()))).Replace("-", "").Substring(0, 12);
            foreach (string protocol in new[] { "TCP", "UDP" })
            {
                string name = "FamilyCinema-" + protocol + "-" + installKey;
                // 首次安装没有旧规则，netsh 的非零退出码不应阻断添加新规则。
                try { ChildProcesses.Run(netsh, "advfirewall firewall delete rule name=" + ChildProcesses.Quote(name), root, null, ConsoleReport, CancellationToken.None); }
                catch (IOException) { }
                ChildProcesses.Run(netsh, "advfirewall firewall add rule name=" + ChildProcesses.Quote(name) +
                    " dir=in action=allow program=" + ChildProcesses.Quote(binary) + " protocol=" + protocol +
                    " localport=" + (protocol == "TCP" ? state.Options.Port.ToString() : "45222") +
                    " profile=private remoteip=localsubnet", root, null, ConsoleReport, CancellationToken.None);
            }
            MessageBox.Show("已允许专用网络中同一局域网的设备访问。公共网络未开放。", "家庭影院", MessageBoxButtons.OK, MessageBoxIcon.Information);
        }
        private static int SelfTest()
        {
            string temporary = Path.Combine(Path.GetTempPath(), "FamilyCinema-setup-test-" + Guid.NewGuid().ToString("N"));
            Directory.CreateDirectory(temporary);
            try
            {
                string file = Path.Combine(temporary, "中文 file.json");
                Json.Save(file, new InstallOptions { Root = @"D:\App Folder\家庭影院", Pansou = true });
                if (Json.Load<InstallOptions>(file).Root != @"D:\App Folder\家庭影院") throw new Exception("中文路径状态保存失败。");
                bool refused = false;
                try { Installation.SafeRemove(Path.GetTempPath(), temporary); } catch (IOException) { refused = true; }
                if (!refused) throw new Exception("清理范围保护失败。");
                if (Downloads.HashMatches(file, new string('0', 64))) throw new Exception("哈希校验失败。");
                var defaults = InstallOptions.Defaults();
                defaults.Root = Path.GetPathRoot(temporary);
                try { Installation.Validate(defaults); throw new Exception("磁盘根目录未拒绝。"); } catch (IOException) { }
                ConsoleReport("Self-test passed: Unicode paths, atomic state, SHA256, cleanup scope, root directory protection.");
                return 0;
            }
            finally { Installation.SafeRemove(temporary, Path.GetTempPath()); }
        }
    }
}
