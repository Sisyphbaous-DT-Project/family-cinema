using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Linq;
using System.Reflection;
using System.Threading;
using Microsoft.Win32;

namespace FamilyCinema.Setup
{
    internal sealed class Installation
    {
        private const string PansouCommit = "221665fb2c9279f22af4684ea2d8c0c331eac1cc";
        private readonly ReleaseSettings release;
        private readonly Action<string> report;
        private readonly CancellationToken cancel;
        private readonly Downloads downloads;
        private string moduleProxy;
        public Installation(ReleaseSettings release, Action<string> report, CancellationToken cancel)
        { this.release = release; this.report = report; this.cancel = cancel; downloads = new Downloads(report, cancel); }

        public InstallState Install(InstallOptions options, bool start)
        {
            Validate(options);
            Directory.CreateDirectory(options.Root);
            File.WriteAllText(Path.Combine(options.Root, ".family-cinema"), "FamilyCinema Windows installation");
            using (var installLock = new FileStream(Path.Combine(options.Root, ".install.lock"), FileMode.OpenOrCreate, FileAccess.ReadWrite, FileShare.None))
                return InstallLocked(options, start);
        }
        private InstallState InstallLocked(InstallOptions options, bool start)
        {
            string root = options.Root, cache = Path.Combine(root, "cache"), work = Path.Combine(root, "work-" + Guid.NewGuid().ToString("N"));
            string next = Path.Combine(root, "versions", release.Version + "-" + DateTime.UtcNow.ToString("yyyyMMddHHmmss") + "-" + Guid.NewGuid().ToString("N").Substring(0, 6));
            var previous = InstallState.Load(root);
            string previousConfig = File.Exists(Path.Combine(root, "config.json")) ? File.ReadAllText(Path.Combine(root, "config.json")) : null;
            bool switched = false, wasRunning = false;
            var warnings = new List<string>();
            if (previous != null)
            {
                using (var running = ChildProcesses.Verified(Json.Load<ProcessRecord>(Path.Combine(root, "run", "media-server.json"))))
                    wasRunning = running != null;
            }
            if (wasRunning) ChildProcesses.RequireIdle(previous);
            try
            {
                Directory.CreateDirectory(work);
                string go = PrepareGo(root, cache, work);
                report("获取家庭影院固定版本源码");
                string sourceZip = downloads.Get(Downloads.Github(release.SourceUrl), release.SourceSha256, cache, "家庭影院源码");
                string sourceRoot = Path.Combine(work, "source");
                downloads.Extract(sourceZip, sourceRoot);
                string source = SingleDirectory(sourceRoot);
                string stage = Path.Combine(work, "publish");
                Directory.CreateDirectory(stage);
                Build(go, Path.Combine(source, "media-server"), Path.Combine(stage, "media-server.exe"), root);
                string license = Path.Combine(source, "LICENSE");
                if (File.Exists(license)) File.Copy(license, Path.Combine(stage, "LICENSE.txt"), true);
                if (options.Pansou)
                {
                    try
                    {
                        string pansouZip = downloads.Get(Downloads.Github("https://github.com/fish2018/pansou/archive/" + PansouCommit + ".zip"), release.PansouSha256, cache, "盘搜源码");
                        var pansouSource = Path.Combine(work, "pansou-source");
                        downloads.Extract(pansouZip, pansouSource);
                        var pansou = SingleDirectory(pansouSource);
                        string pansouMain = Path.Combine(pansou, "main.go");
                        string original = File.ReadAllText(pansouMain);
                        string changed = original.Replace("Addr:         \":\" + port,", "Addr:         \"127.0.0.1:\" + port,");
                        if (changed == original) throw new InvalidDataException("盘搜版本与本地监听补丁不匹配。");
                        File.WriteAllText(pansouMain, changed, new System.Text.UTF8Encoding(false));
                        Build(go, pansou, Path.Combine(stage, "pansou.exe"), root);
                        if (File.Exists(Path.Combine(pansou, "LICENSE"))) File.Copy(Path.Combine(pansou, "LICENSE"), Path.Combine(stage, "PanSou-LICENSE.txt"), true);
                    }
                    catch (OperationCanceledException) { throw; }
                    catch (Exception error)
                    {
                        options.Pansou = PreviousComponent(previous, stage, new[] { "pansou.exe" });
                        string warning = "盘搜更新失败：" + error.Message + (options.Pansou ? "；继续使用原有盘搜。" : "；主程序继续安装，盘搜可稍后重试补装。");
                        warnings.Add(warning); report(warning);
                    }
                }
                if (options.FFmpeg)
                {
                    try { PrepareFFmpeg(stage, cache, work); }
                    catch (OperationCanceledException) { throw; }
                    catch (Exception error)
                    {
                        options.FFmpeg = PreviousComponent(previous, stage, new[] { "ffmpeg.exe", "ffprobe.exe" });
                        string warning = "转码工具更新失败：" + error.Message + (options.FFmpeg ? "；继续使用原有工具。" : "；主程序继续安装，工具可稍后重试补装。");
                        warnings.Add(warning); report(warning);
                    }
                }
                report("获取同版本安卓 App");
                string apk = downloads.Get(Downloads.Github(release.ApkUrl), release.ApkSha256, cache, "安卓 App");
                Directory.CreateDirectory(Path.Combine(stage, "app"));
                File.Copy(apk, Path.Combine(stage, "app", "app.apk"));
                Json.Save(Path.Combine(stage, "app", "version.json"), new { versionCode = release.ApkVersionCode, versionName = release.ApkVersionName });
                cancel.ThrowIfCancellationRequested();
                if (previous == null || !wasRunning) options.Port = ChildProcesses.AvailablePort(options.Port);
                if (options.Pansou && (previous == null || !wasRunning || !previous.Options.Pansou))
                    options.PansouPort = ChildProcesses.AvailablePort(options.PansouPort);
                var config = Configuration(options, stage);
                Json.Save(Path.Combine(stage, "config.json"), config);
                report("编译与文件校验完成，正在发布");
                cancel.ThrowIfCancellationRequested();
                if (wasRunning) ChildProcesses.RequireIdle(previous);
                ChildProcesses.Stop(root, report);
                switched = true;
                Directory.CreateDirectory(Path.GetDirectoryName(next));
                Directory.Move(stage, next);
                // 发布后配置里的工具路径必须指向最终目录。
                config = Configuration(options, next);
                Json.Save(Path.Combine(root, "config.json"), config);
                Json.Save(Path.Combine(next, "config.json"), config);
                string self = Assembly.GetExecutingAssembly().Location;
                string manager = Path.Combine(root, "FamilyCinema-" + release.Version + "-" + Downloads.Hash(self).Substring(0, 8) + ".exe");
                if (!string.Equals(self, manager, StringComparison.OrdinalIgnoreCase) && !File.Exists(manager)) File.Copy(self, manager);
                var state = new InstallState { Version = release.Version, ActiveDirectory = next, ManagerPath = manager, Options = options, Warnings = warnings.ToArray() };
                Json.Save(Path.Combine(root, "installation.json"), state);
                if (start) ChildProcesses.Start(state, report, cancel);
                ConfigureAutoStart(state);
                SaveLastRoot(root);
                CreateShortcut(state);
                CleanupVersions(root, next, previous == null ? null : previous.ActiveDirectory);
                report("安装完成。配置、观看记录和媒体目录已保留。");
                return state;
            }
            catch
            {
                if (switched)
                {
                    report("安装未完成，正在恢复上一版");
                    ChildProcesses.Stop(root, report);
                    if (previous != null)
                    {
                        if (previousConfig != null) File.WriteAllText(Path.Combine(root, "config.json"), previousConfig, new System.Text.UTF8Encoding(false));
                        Json.Save(Path.Combine(root, "installation.json"), previous);
                        if (wasRunning) ChildProcesses.Start(previous, report, CancellationToken.None);
                    }
                    else
                    {
                        DeleteFile(Path.Combine(root, "installation.json"));
                        DeleteFile(Path.Combine(root, "config.json"));
                    }
                    SafeRemove(next, Path.Combine(root, "versions"));
                }
                throw;
            }
            finally { SafeRemove(work, root); }
        }
        private string PrepareGo(string root, string cache, string work)
        {
            string tools = Path.Combine(root, "tools", "go-" + release.GoVersion);
            string binary = Path.Combine(tools, "go", "bin", "go.exe");
            string marker = Path.Combine(tools, ".verified");
            if (File.Exists(binary) && File.Exists(marker) && File.ReadAllText(marker) == release.GoSha256) return binary;
            string filename = "go" + release.GoVersion + ".windows-amd64.zip";
            var urls = new[] {
                "https://mirrors.aliyun.com/golang/" + filename,
                "https://mirrors.ustc.edu.cn/golang/" + filename,
                "https://go.dev/dl/" + filename
            };
            string archive = downloads.Get(urls, release.GoSha256, cache, "Go 编译工具");
            var stage = Path.Combine(work, "go-tools");
            downloads.Extract(archive, stage);
            if (!File.Exists(Path.Combine(stage, "go", "bin", "go.exe"))) throw new InvalidDataException("Go 工具包不完整。");
            Directory.CreateDirectory(Path.GetDirectoryName(tools));
            SafeRemove(tools, Path.Combine(root, "tools"));
            Directory.Move(stage, tools);
            File.WriteAllText(marker, release.GoSha256);
            return binary;
        }
        private void Build(string go, string source, string output, string root)
        {
            report("编译 " + Path.GetFileName(output) + "，首次需要下载依赖，请耐心等待");
            if (moduleProxy == null)
            {
                const string moduleSample = "/golang.org/x/sys/@v/v0.40.0.zip";
                var moduleSources = MirrorSelection.Rank(new[] {
                    "https://goproxy.cn" + moduleSample,
                    "https://goproxy.io" + moduleSample
                }, Path.Combine(root, "cache"), report, cancel);
                moduleProxy = string.Join("|", moduleSources.Select(url => new Uri(url).GetLeftPart(UriPartial.Authority)));
            }
            var env = new Dictionary<string, string> {
                { "GOTOOLCHAIN", "local" }, { "CGO_ENABLED", "0" },
                { "GOOS", "windows" }, { "GOARCH", "amd64" },
                { "GOPROXY", moduleProxy },
                { "NO_PROXY", DownloadRouting.GoNoProxy() }, { "no_proxy", DownloadRouting.GoNoProxy() },
                { "GOSUMDB", "sum.golang.org" },
                { "GOMODCACHE", Path.Combine(root, "cache", "go-modules") },
                { "GOCACHE", Path.Combine(root, "cache", "go-build") },
                { "GOFLAGS", "-mod=readonly -buildvcs=false" }, { "GOENV", "off" },
                { "GOROOT", Path.GetDirectoryName(Path.GetDirectoryName(go)) }
            };
            ChildProcesses.Run(go, "build -trimpath -ldflags \"-s -w\" -o " + ChildProcesses.Quote(output) + " .", source, env, report, cancel);
        }
        private void PrepareFFmpeg(string stage, string cache, string work)
        {
            string archive = downloads.Get(Downloads.Github(release.FFmpegUrl ?? ""), release.FFmpegSha256, cache, "声音与字幕工具");
            string directory = Path.Combine(work, "ffmpeg");
            downloads.Extract(archive, directory);
            foreach (string library in Directory.GetFiles(directory, "*.dll", SearchOption.AllDirectories))
                File.Copy(library, Path.Combine(stage, Path.GetFileName(library)), true);
            foreach (string name in new[] { "ffmpeg.exe", "ffprobe.exe" })
            {
                var binary = Directory.GetFiles(directory, name, SearchOption.AllDirectories).FirstOrDefault();
                if (binary == null) throw new InvalidDataException("转码工具包缺少 " + name);
                File.Copy(binary, Path.Combine(stage, name));
                ChildProcesses.Run(Path.Combine(stage, name), "-version", stage, null, report, cancel);
            }
            string license = Directory.GetFiles(directory, "*LICENSE*", SearchOption.AllDirectories).FirstOrDefault();
            if (license != null) File.Copy(license, Path.Combine(stage, "FFmpeg-LICENSE.txt"));
            string readme = Directory.GetFiles(directory, "README*", SearchOption.AllDirectories).FirstOrDefault();
            if (readme != null) File.Copy(readme, Path.Combine(stage, "FFmpeg-README.txt"));
        }
        private static bool PreviousComponent(InstallState previous, string stage, string[] files)
        {
            if (previous == null || files.Any(name => !File.Exists(Path.Combine(previous.ActiveDirectory, name)))) return false;
            foreach (string name in files) File.Copy(Path.Combine(previous.ActiveDirectory, name), Path.Combine(stage, name), true);
            if (files.Contains("ffmpeg.exe"))
                foreach (string library in Directory.GetFiles(previous.ActiveDirectory, "*.dll"))
                    File.Copy(library, Path.Combine(stage, Path.GetFileName(library)), true);
            foreach (string name in new[] { "PanSou-LICENSE.txt", "FFmpeg-LICENSE.txt", "FFmpeg-README.txt" })
                if (File.Exists(Path.Combine(previous.ActiveDirectory, name))) File.Copy(Path.Combine(previous.ActiveDirectory, name), Path.Combine(stage, name), true);
            return true;
        }
        private Dictionary<string, object> Configuration(InstallOptions options, string directory)
        {
            var config = Json.Object(Path.Combine(options.Root, "config.json"));
            var media = config.ContainsKey("media") ? config["media"] as Dictionary<string, object> : null;
            if (media == null) media = new Dictionary<string, object>();
            foreach (var pair in new[] { new[] { "movieRoot", "Movies" }, new[] { "seriesRoot", "Series" }, new[] { "downloadDir", "Downloads" }, new[] { "cacheDir", ".torrent-cache" } })
            {
                string path = Path.Combine(options.Media, pair[1]);
                Directory.CreateDirectory(path); config[pair[0]] = path;
            }
            config["port"] = options.Port.ToString();
            config["listenAddr"] = options.Lan ? "" : "127.0.0.1";
            config["btUPnP"] = false;
            config["pansou"] = new { enabled = options.Pansou, url = "http://127.0.0.1:" + options.PansouPort };
            if (!config.ContainsKey("sources")) config["sources"] = new { dytt8899 = true, dygangs = true, mp4z6 = true, mikan = false };
            if (!config.ContainsKey("proxy")) config["proxy"] = new { @base = "", token = "" };
            var anime = config.ContainsKey("anime") ? config["anime"] as Dictionary<string, object> : null;
            if (anime == null) anime = new Dictionary<string, object>();
            if (!anime.ContainsKey("enabled")) anime["enabled"] = true;
            if (!anime.ContainsKey("rulesDir")) anime["rulesDir"] = Path.Combine(options.Root, "data", "rules");
            if (!anime.ContainsKey("cacheDir")) anime["cacheDir"] = Path.Combine(options.Root, "data", "anime-cache");
            if (!anime.ContainsKey("ruleSync")) anime["ruleSync"] = true;
            config["anime"] = anime;
            media["enabled"] = options.FFmpeg;
            media["ffmpegPath"] = options.FFmpeg ? Path.Combine(directory, "ffmpeg.exe") : "";
            media["ffprobePath"] = options.FFmpeg ? Path.Combine(directory, "ffprobe.exe") : "";
            media["stateDir"] = Path.Combine(options.Root, "data", "media-proc");
            config["media"] = media;
            return config;
        }
        public static void Validate(InstallOptions options)
        {
            options.Root = Path.GetFullPath(options.Root.Trim());
            options.Media = Path.GetFullPath(options.Media.Trim());
            foreach (var path in new[] { options.Root, options.Media })
                if (path.TrimEnd('\\') == Path.GetPathRoot(path).TrimEnd('\\') || path.IndexOf('"') >= 0)
                    throw new IOException("请选择专用文件夹，不能直接选择磁盘根目录。");
            if (Inside(options.Root, options.Media) || Inside(options.Media, options.Root))
                throw new IOException("安装文件夹和媒体文件夹需要分开放置，不能互相包含。");
            var state = InstallState.Load(options.Root);
            bool owned = File.Exists(Path.Combine(options.Root, ".family-cinema")) &&
                File.ReadAllText(Path.Combine(options.Root, ".family-cinema")) == "FamilyCinema Windows installation";
            if (state == null && !owned && Directory.Exists(options.Root) && Directory.EnumerateFileSystemEntries(options.Root).Any(p => Path.GetFileName(p) != ".install.lock"))
                throw new IOException("所选安装目录不是本程序管理的目录，请选择一个新文件夹。");
            if (state != null && !string.Equals(state.Options.Media, options.Media, StringComparison.OrdinalIgnoreCase))
                throw new IOException("更新时请保留原媒体目录；迁移媒体请先停止服务并手动处理。");
            if (options.Port < 1024 || options.Port > 65535 || options.PansouPort < 1024 || options.PansouPort > 65505)
                throw new IOException("服务端口必须在 1024 到 65505 之间。");
        }
        private static bool Inside(string path, string parent)
        { return string.Equals(path, parent, StringComparison.OrdinalIgnoreCase) || path.StartsWith(parent.TrimEnd('\\') + "\\", StringComparison.OrdinalIgnoreCase); }
        public static void SafeRemove(string path, string parent)
        {
            string absolute = Path.GetFullPath(path), scope = Path.GetFullPath(parent).TrimEnd('\\') + "\\";
            if (!absolute.StartsWith(scope, StringComparison.OrdinalIgnoreCase)) throw new IOException("拒绝清理安装目录之外的文件。");
            if (Directory.Exists(absolute)) Directory.Delete(absolute, true);
        }
        private static string SingleDirectory(string path)
        {
            var directories = Directory.GetDirectories(path);
            if (directories.Length != 1) throw new InvalidDataException("源码包结构与发布信息不符。");
            return directories[0];
        }
        private static void DeleteFile(string path) { if (File.Exists(path)) File.Delete(path); }
        private void CleanupVersions(string root, string current, string previous)
        {
            foreach (var directory in Directory.GetDirectories(Path.Combine(root, "versions")))
            {
                if (string.Equals(directory, current, StringComparison.OrdinalIgnoreCase) || string.Equals(directory, previous, StringComparison.OrdinalIgnoreCase)) continue;
                try { SafeRemove(directory, Path.Combine(root, "versions")); }
                catch (IOException) { report("旧版本仍被占用，暂时保留：" + Path.GetFileName(directory)); }
                catch (UnauthorizedAccessException) { report("旧版本无法清理，暂时保留：" + Path.GetFileName(directory)); }
            }
        }
        private static void ConfigureAutoStart(InstallState state)
        {
            using (var key = Registry.CurrentUser.CreateSubKey(@"Software\Microsoft\Windows\CurrentVersion\Run"))
            {
                if (state.Options.AutoStart) key.SetValue("FamilyCinema", ChildProcesses.Quote(state.ManagerPath) + " --start --root " + ChildProcesses.Quote(state.Options.Root));
                else key.DeleteValue("FamilyCinema", false);
            }
        }
        public static void SaveLastRoot(string root)
        {
            using (var key = Registry.CurrentUser.CreateSubKey(@"Software\FamilyCinema")) key.SetValue("InstallRoot", root);
        }
        public static string LastRoot()
        {
            using (var key = Registry.CurrentUser.OpenSubKey(@"Software\FamilyCinema"))
                return key == null ? null : key.GetValue("InstallRoot") as string;
        }
        private static void CreateShortcut(InstallState state)
        {
            try
            {
                var type = Type.GetTypeFromProgID("WScript.Shell");
                dynamic shell = Activator.CreateInstance(type);
                dynamic shortcut = shell.CreateShortcut(Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.DesktopDirectory), "家庭影院.lnk"));
                shortcut.TargetPath = state.ManagerPath;
                shortcut.Arguments = "--root " + ChildProcesses.Quote(state.Options.Root);
                shortcut.WorkingDirectory = state.Options.Root;
                shortcut.Save();
            }
            catch { }
        }
    }
}
