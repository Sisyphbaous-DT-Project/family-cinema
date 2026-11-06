using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Reflection;
using System.Runtime.CompilerServices;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading;
using System.Threading.Tasks;
using System.Windows.Forms;
using System.Web.Script.Serialization;

namespace FamilyCinema.Setup
{
    internal sealed class MainForm : Form
    {
        private readonly ReleaseSettings release;
        private readonly object logLock = new object();
        private readonly Panel preparation;
        private readonly Label preparationStatus;
        private readonly Button retry;
        private readonly Button cancelPreparation;
        private readonly ProgressBar preparationProgress;
        private readonly Queue<string> pendingProgress = new Queue<string>();
        private string currentRoot;
        private BrowserHost browser;
        private CancellationTokenSource cancellation;
        private CancellationTokenSource preparing;
        private bool busy;
        private bool initializing;
        private bool pageReady;

        public MainForm(ReleaseSettings release, string initialRoot)
        {
            this.release = release;
            currentRoot = string.IsNullOrWhiteSpace(initialRoot) ? InstallOptions.Defaults().Root : Path.GetFullPath(initialRoot);
            Text = "家庭影院 · Windows 一键部署";
            Font = new Font("Microsoft YaHei UI", 9F);
            BackColor = Color.FromArgb(248, 250, 240);
            MinimumSize = new Size(700, 640);
            Size = new Size(920, 800);
            StartPosition = FormStartPosition.CenterScreen;
            AutoScaleMode = AutoScaleMode.Dpi;
            Icon = Icon.ExtractAssociatedIcon(Assembly.GetExecutingAssembly().Location);

            preparation = new Panel { Dock = DockStyle.Fill, BackColor = BackColor, Padding = new Padding(40) };
            var body = new TableLayoutPanel { Dock = DockStyle.Fill, ColumnCount = 1, RowCount = 6 };
            body.RowStyles.Add(new RowStyle(SizeType.Percent, 40));
            body.RowStyles.Add(new RowStyle(SizeType.Absolute, 50));
            body.RowStyles.Add(new RowStyle(SizeType.Absolute, 64));
            body.RowStyles.Add(new RowStyle(SizeType.Absolute, 14));
            body.RowStyles.Add(new RowStyle(SizeType.Absolute, 64));
            body.RowStyles.Add(new RowStyle(SizeType.Percent, 60));
            body.Controls.Add(new Label { Text = "家庭影院", AutoSize = true, Font = new Font(Font.FontFamily, 25, FontStyle.Bold), ForeColor = Color.FromArgb(25, 28, 32), Anchor = AnchorStyles.Left }, 0, 1);
            preparationStatus = new Label { Text = "正在准备安装界面", Dock = DockStyle.Fill, ForeColor = Color.FromArgb(68, 71, 78), AutoEllipsis = true, Padding = new Padding(0, 12, 0, 0) };
            body.Controls.Add(preparationStatus, 0, 2);
            preparationProgress = new ProgressBar { Dock = DockStyle.Fill, Style = ProgressBarStyle.Marquee, MarqueeAnimationSpeed = 25 };
            body.Controls.Add(preparationProgress, 0, 3);
            var commands = new FlowLayoutPanel { Dock = DockStyle.Fill, Padding = new Padding(0, 14, 0, 0), WrapContents = false };
            retry = new Button { Text = "重试", Visible = false, AutoSize = true, Height = 36, BackColor = Color.FromArgb(0, 105, 85), ForeColor = Color.White, FlatStyle = FlatStyle.Flat, Padding = new Padding(16, 4, 16, 4) };
            cancelPreparation = new Button { Text = "取消", AutoSize = true, Height = 36, FlatStyle = FlatStyle.Flat, Padding = new Padding(16, 4, 16, 4), Margin = new Padding(10, 3, 0, 3) };
            retry.Click += delegate { BeginInitialize(); };
            cancelPreparation.Click += delegate { if (preparing != null) preparing.Cancel(); else Close(); };
            commands.Controls.Add(retry); commands.Controls.Add(cancelPreparation);
            body.Controls.Add(commands, 0, 4);
            preparation.Controls.Add(body);
            Controls.Add(preparation);
            Shown += delegate { BeginInitialize(); };
            FormClosing += OnClosing;
        }

        private async void BeginInitialize()
        {
            if (initializing) return;
            initializing = true;
            retry.Visible = false;
            preparation.Visible = true;
            cancelPreparation.Text = "取消";
            preparationProgress.Style = ProgressBarStyle.Marquee;
            preparing = new CancellationTokenSource();
            try
            {
                SetPreparation("正在准备安装界面");
                await Task.Run((Action)EmbeddedDependencies.Initialize);
                if (!RuntimeAvailable())
                {
                    SetPreparation("首次打开需要准备微软网页界面运行时");
                    await Task.Run(delegate { WebViewRuntime.Install(SetPreparation, preparing.Token); });
                    preparing.Token.ThrowIfCancellationRequested();
                    if (!RuntimeAvailable()) throw new IOException("网页界面运行时尚未就绪，请重试；若系统要求重启，请重启后再打开。");
                }
                preparing.Token.ThrowIfCancellationRequested();
                await CreateBrowser();
                preparation.Visible = false;
                browser.View.BringToFront();
            }
            catch (Exception error)
            {
                if (browser != null) { browser.Dispose(); browser = null; }
                string message = preparing.IsCancellationRequested ? "已取消界面准备，现有安装没有改变。" : "界面准备未完成：" + SafeMessage(error.Message);
                SetPreparation(message);
                retry.Visible = true;
                cancelPreparation.Text = "关闭";
                preparationProgress.Style = ProgressBarStyle.Blocks;
                preparationProgress.Value = 0;
                Report(message);
            }
            finally
            {
                preparing.Dispose();
                preparing = null;
                initializing = false;
            }
        }

        [MethodImpl(MethodImplOptions.NoInlining)]
        private static bool RuntimeAvailable()
        { return BrowserHost.RuntimeAvailable(); }

        [MethodImpl(MethodImplOptions.NoInlining)]
        private async Task CreateBrowser()
        {
            browser = new BrowserHost(ReceiveMessage);
            browser.View.Dock = DockStyle.Fill;
            Controls.Add(browser.View);
            await browser.Initialize();
        }

        private void SetPreparation(string message)
        {
            if (IsDisposed) return;
            if (InvokeRequired) { BeginInvoke((Action<string>)SetPreparation, message); return; }
            preparationStatus.Text = message;
        }

        private async void ReceiveMessage(string json)
        {
            int id = 0;
            try
            {
                if (json == null || json.Length > 64 * 1024) throw new InvalidDataException("界面请求过大。");
                var serializer = new JavaScriptSerializer { MaxJsonLength = 64 * 1024 };
                var request = serializer.Deserialize<Dictionary<string, object>>(json);
                if (request == null || !request.ContainsKey("id") || !(request["id"] is int)) throw new InvalidDataException("界面请求编号无效。");
                id = (int)request["id"];
                if (id < 0) throw new InvalidDataException("界面请求编号无效。");
                string method = request.ContainsKey("method") ? request["method"] as string : null;
                var args = request.ContainsKey("args") ? request["args"] as Dictionary<string, object> : null;
                if (args == null) args = new Dictionary<string, object>();
                object result;
                switch (method)
                {
                    case "init":
                        pageReady = true;
                        result = Snapshot(Argument(args, "root", currentRoot));
                        break;
                    case "browse":
                        RequireIdle();
                        string kind = Argument(args, "kind", "");
                        if (kind != "root" && kind != "media") throw new ArgumentException("未知目录类型。");
                        string path = Argument(args, "path", "");
                        using (var picker = new FolderBrowserDialog { Description = kind == "root" ? "选择安装文件夹" : "选择媒体文件夹", SelectedPath = path, ShowNewFolderButton = true })
                            result = picker.ShowDialog(this) == DialogResult.OK ? picker.SelectedPath : path;
                        break;
                    case "install":
                        RequireIdle();
                        var options = serializer.ConvertToType<InstallOptions>(args);
                        if (options == null) throw new ArgumentException("安装选项为空。");
                        Installation.Validate(options);
                        if (options.FFmpeg && !FFmpegAvailable()) throw new IOException("此发行包未配置声音与字幕工具。");
                        currentRoot = options.Root;
                        result = await RunOperation(delegate(CancellationToken token) {
                            var state = new Installation(release, Report, token).Install(options, true);
                            foreach (string warning in state.Warnings ?? new string[0]) Report(warning);
                        });
                        if (options.Lan) RequestFirewall(options.Root);
                        break;
                    case "start":
                        RequireIdle();
                        currentRoot = ResolveRoot(args);
                        result = await RunOperation(delegate(CancellationToken token) { ChildProcesses.Start(RequiredState(currentRoot), Report, token); });
                        break;
                    case "stop":
                        RequireIdle();
                        currentRoot = ResolveRoot(args);
                        result = await RunOperation(delegate(CancellationToken token) {
                            RequiredState(currentRoot); token.ThrowIfCancellationRequested(); ChildProcesses.Stop(currentRoot, Report);
                        });
                        break;
                    case "cancel":
                        if (cancellation != null) cancellation.Cancel();
                        result = new { cancelled = cancellation != null };
                        break;
                    case "openWeb":
                        string suffix = Argument(args, "path", "");
                        if (suffix != "" && suffix != "/api/app/apk") throw new ArgumentException("不支持的网页地址。");
                        var stateForWeb = RequiredState(ResolveRoot(args));
                        Process.Start(new ProcessStartInfo("http://127.0.0.1:" + stateForWeb.Options.Port + suffix) { UseShellExecute = true });
                        result = new { opened = true };
                        break;
                    case "openLogs":
                        string logsRoot = ResolveRoot(args);
                        bool owned = File.Exists(Path.Combine(logsRoot, ".family-cinema")) &&
                            File.ReadAllText(Path.Combine(logsRoot, ".family-cinema")) == "FamilyCinema Windows installation";
                        string folder = owned ? Path.Combine(logsRoot, "logs") :
                            Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinemaSetup", "logs");
                        Directory.CreateDirectory(folder);
                        Process.Start(new ProcessStartInfo("explorer.exe", ChildProcesses.Quote(folder)) { UseShellExecute = true });
                        result = new { opened = true };
                        break;
                    case "copyText":
                        string text = Argument(args, "text", "");
                        if (text.Length > 60000) throw new ArgumentException("日志过长，请打开日志文件夹查看完整记录。");
                        if (text.Length > 0) Clipboard.SetText(text, TextDataFormat.UnicodeText);
                        result = new { copied = true };
                        break;
                    case "firewall":
                        RequireIdle();
                        result = RequestFirewall(ResolveRoot(args));
                        break;
                    case "close":
                        RequireIdle();
                        Send(new { id = id, result = new { closed = true } });
                        BeginInvoke((Action)Close);
                        return;
                    default: throw new ArgumentException("不支持的界面操作。");
                }
                Send(new { id = id, result = result });
                if (pageReady) FlushProgress();
            }
            catch (Exception error)
            {
                string message = error is OperationCanceledException ? "已取消，本次临时文件已清理。" : SafeMessage(error.Message);
                Report(message);
                Send(new { id = id, error = message });
            }
        }

        private async Task<object> RunOperation(Action<CancellationToken> operation)
        {
            RequireIdle();
            busy = true;
            cancellation = new CancellationTokenSource();
            try
            {
                await Task.Run(delegate { operation(cancellation.Token); });
                return Snapshot(currentRoot);
            }
            finally
            {
                busy = false;
                cancellation.Dispose();
                cancellation = null;
            }
        }

        private object Snapshot(string root)
        {
            string path = Path.GetFullPath(root);
            var defaults = InstallOptions.Defaults();
            defaults.Root = path;
            var warnings = new List<string>();
            var state = InstallState.Load(path);
            if (state != null)
            {
                if (state.Options == null) throw new IOException("安装记录不完整，请查看日志。");
                defaults = state.Options;
                warnings.AddRange(state.Warnings ?? new string[0]);
            }
            bool running = false;
            if (state != null)
            {
                using (var process = ChildProcesses.Verified(Json.Load<ProcessRecord>(Path.Combine(path, "run", "media-server.json"))))
                    running = process != null;
            }
            return new { version = release.Version, options = defaults, installed = state != null, running = running,
                ffmpegAvailable = FFmpegAvailable(), warnings = warnings.ToArray(), address = "http://127.0.0.1:" + defaults.Port };
        }

        private static InstallState RequiredState(string root)
        {
            var state = InstallState.Load(root);
            if (state == null || state.Options == null) throw new IOException("请先完成安装。");
            return state;
        }

        private bool FFmpegAvailable()
        { return !string.IsNullOrWhiteSpace(release.FFmpegUrl) && !string.IsNullOrWhiteSpace(release.FFmpegSha256); }

        private string ResolveRoot(Dictionary<string, object> args)
        { return Path.GetFullPath(Argument(args, "root", currentRoot)); }

        private static string Argument(Dictionary<string, object> args, string name, string fallback)
        {
            object value;
            if (!args.TryGetValue(name, out value) || value == null) return fallback;
            var text = value as string;
            if (text == null) throw new ArgumentException("界面参数类型无效：" + name);
            return string.IsNullOrWhiteSpace(text) && name == "root" ? fallback : text;
        }

        private void RequireIdle()
        { if (busy) throw new IOException("还有操作正在进行，请先等待完成或取消。"); }

        private object RequestFirewall(string root)
        {
            var state = RequiredState(root);
            if (!state.Options.Lan) throw new IOException("请先启用局域网访问并完成安装。");
            try
            {
                using (var process = Process.Start(new ProcessStartInfo(Assembly.GetExecutingAssembly().Location, "--firewall --root " + ChildProcesses.Quote(root)) {
                    Verb = "runas", UseShellExecute = true
                })) { }
                Report("已请求 Windows 添加专用网络的局域网访问规则。");
                return new { requested = true };
            }
            catch (System.ComponentModel.Win32Exception)
            {
                Report("未获得管理员授权，防火墙没有改变。");
                return new { requested = false };
            }
        }

        private void Report(string text)
        {
            text = SafeMessage(text);
            lock (logLock)
            {
                pendingProgress.Enqueue(text);
                while (pendingProgress.Count > 150) pendingProgress.Dequeue();
                try
                {
                    bool owned = File.Exists(Path.Combine(currentRoot, ".family-cinema")) && File.ReadAllText(Path.Combine(currentRoot, ".family-cinema")) == "FamilyCinema Windows installation";
                    string directory = owned ? Path.Combine(currentRoot, "logs") : Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinemaSetup", "logs");
                    Directory.CreateDirectory(directory);
                    string file = Path.Combine(directory, "setup.log");
                    if (File.Exists(file) && new FileInfo(file).Length > 5 * 1024 * 1024)
                    {
                        if (File.Exists(file + ".old")) File.Delete(file + ".old");
                        File.Move(file, file + ".old");
                    }
                    File.AppendAllText(file, DateTime.Now.ToString("s") + " " + text + Environment.NewLine, new UTF8Encoding(false));
                }
                catch { }
            }
            if (IsDisposed || !IsHandleCreated) return;
            try
            {
                if (InvokeRequired) BeginInvoke((Action)FlushProgress); else FlushProgress();
            }
            catch (InvalidOperationException) { }
        }

        private void FlushProgress()
        {
            if (!pageReady || browser == null || IsDisposed) return;
            List<string> messages = new List<string>();
            lock (logLock) while (pendingProgress.Count > 0) messages.Add(pendingProgress.Dequeue());
            foreach (string message in messages) Send(new { type = "progress", message = message });
        }

        private void Send(object value)
        {
            if (browser != null && !IsDisposed) browser.Send(Json.Stringify(value));
        }

        private static string SafeMessage(string text)
        {
            return Regex.Replace(text ?? "", @"https?://[^\s<>""']+", delegate(Match match) {
                Uri uri;
                if (!Uri.TryCreate(match.Value, UriKind.Absolute, out uri)) return "下载源";
                if (uri.IsLoopback && string.IsNullOrEmpty(uri.Query)) return match.Value;
                return uri.GetLeftPart(UriPartial.Authority);
            }, RegexOptions.IgnoreCase);
        }

        private void OnClosing(object sender, FormClosingEventArgs e)
        {
            if (busy || initializing)
            {
                e.Cancel = true;
                if (MessageBox.Show(this, "还有操作正在进行。要取消当前操作吗？", "家庭影院", MessageBoxButtons.YesNo, MessageBoxIcon.Question) == DialogResult.Yes)
                {
                    if (cancellation != null) cancellation.Cancel();
                    if (preparing != null) preparing.Cancel();
                }
            }
        }

        protected override void Dispose(bool disposing)
        {
            if (disposing && browser != null) browser.Dispose();
            base.Dispose(disposing);
        }
    }
}
