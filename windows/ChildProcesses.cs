using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Reflection;
using System.Text;
using System.Threading;

namespace FamilyCinema.Setup
{
    internal static class ChildProcesses
    {
        public static string Quote(string value)
        {
            if (value.IndexOf('"') >= 0) throw new ArgumentException("参数含不支持的引号。");
            return "\"" + value.TrimEnd('\\') + "\"";
        }
        public static void Run(string executable, string arguments, string directory, IDictionary<string, string> environment, Action<string> report, CancellationToken cancel)
        {
            var info = new ProcessStartInfo(executable, arguments) {
                WorkingDirectory = directory, UseShellExecute = false, CreateNoWindow = true,
                RedirectStandardOutput = true, RedirectStandardError = true,
                StandardOutputEncoding = Encoding.UTF8, StandardErrorEncoding = Encoding.UTF8
            };
            if (environment != null) foreach (var item in environment) info.EnvironmentVariables[item.Key] = item.Value;
            using (var process = new Process { StartInfo = info })
            {
                process.OutputDataReceived += delegate(object sender, DataReceivedEventArgs e) { if (e.Data != null) report(e.Data); };
                process.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs e) { if (e.Data != null) report(e.Data); };
                process.Start(); process.BeginOutputReadLine(); process.BeginErrorReadLine();
                using (var job = new ProcessJob(process))
                {
                    try
                    {
                        while (!process.WaitForExit(200)) cancel.ThrowIfCancellationRequested();
                        process.WaitForExit();
                        if (process.ExitCode != 0) throw new IOException(Path.GetFileName(executable) + " 执行失败（" + process.ExitCode + "），请查看日志。");
                    }
                    finally { if (!process.HasExited) { job.Dispose(); process.WaitForExit(); } }
                }
            }
        }
        public static bool IsPortFree(int port)
        {
            TcpListener listener = null;
            try { listener = new TcpListener(IPAddress.Any, port); listener.Start(); return true; }
            catch (SocketException) { return false; }
            finally { if (listener != null) listener.Stop(); }
        }
        public static int AvailablePort(int preferred)
        {
            for (var port = preferred; port < preferred + 30 && port < 65536; port++) if (IsPortFree(port)) return port;
            throw new IOException("没有可用服务端口，请先关闭占用端口的程序。");
        }
        public static bool Healthy(string url)
        {
            try
            {
                var request = (HttpWebRequest)WebRequest.Create(url);
                request.Proxy = null; request.Timeout = 1200; request.ReadWriteTimeout = 1200;
                using (var response = request.GetResponse()) return ((HttpWebResponse)response).StatusCode == HttpStatusCode.OK;
            }
            catch { return false; }
        }
        public static void RequireIdle(InstallState state)
        {
            var request = (HttpWebRequest)WebRequest.Create("http://127.0.0.1:" + state.Options.Port + "/api/tasks");
            request.Proxy = null; request.Timeout = 3000; request.ReadWriteTimeout = 3000;
            Dictionary<string, object>[] tasks;
            try
            {
                using (var response = request.GetResponse())
                using (var reader = new StreamReader(response.GetResponseStream(), Encoding.UTF8))
                {
                    string json = reader.ReadToEnd();
                    tasks = json.Trim() == "null" ? new Dictionary<string, object>[0] : Json.Read<Dictionary<string, object>[]>(json);
                }
            }
            catch (Exception error) { throw new IOException("无法确认现有服务是否空闲，未停止服务。请稍后重试。", error); }
            if (tasks == null) throw new IOException("没有取得有效任务状态，未停止现有服务。");
            foreach (var task in tasks)
            {
                object status;
                if (task.TryGetValue("status", out status) && (Equals(status, "running") || Equals(status, "processing") || Equals(status, "queued")))
                    throw new IOException("还有下载、上传或媒体处理任务，请等任务结束后再更新；现有服务没有停止。");
            }
        }
        public static Process Verified(ProcessRecord record)
        {
            if (record == null) return null;
            try
            {
                var process = Process.GetProcessById(record.Pid);
                if (!process.HasExited && process.StartTime.ToUniversalTime().Ticks == record.StartedUtcTicks &&
                    string.Equals(process.MainModule.FileName, record.Path, StringComparison.OrdinalIgnoreCase))
                    return process;
                process.Dispose();
            }
            catch (ArgumentException) { }
            catch (InvalidOperationException) { }
            catch (System.ComponentModel.Win32Exception) { }
            return null;
        }
        public static void Stop(string root, Action<string> report)
        {
            foreach (var name in new[] { "media-server", "pansou" })
            {
                var path = Path.Combine(root, "run", name + ".json");
                using (var process = Verified(Json.Load<ProcessRecord>(path)))
                {
                    if (process != null)
                    {
                        process.Kill();
                        if (!process.WaitForExit(10000)) throw new IOException(name + " 未能停止。");
                        report(name + " 已停止");
                    }
                }
                if (File.Exists(path)) File.Delete(path);
            }
        }
        public static void Start(InstallState state, Action<string> report, CancellationToken cancel)
        {
            if (state == null) throw new IOException("请先完成安装。");
            string root = state.Options.Root;
            Directory.CreateDirectory(Path.Combine(root, "run"));
            Directory.CreateDirectory(Path.Combine(root, "logs"));
            var mediaRecord = Json.Load<ProcessRecord>(Path.Combine(root, "run", "media-server.json"));
            bool mediaRunning;
            using (var running = Verified(mediaRecord))
                mediaRunning = running != null;
            if (mediaRunning && !Healthy("http://127.0.0.1:" + state.Options.Port + "/api/web-manifest"))
                throw new IOException("家庭影院进程仍在运行但没有响应，请先停止后再启动。");
            if (!mediaRunning && !IsPortFree(state.Options.Port)) throw new IOException("主服务端口 " + state.Options.Port + " 已被占用，未停止其它程序。");
            var started = new List<string>();
            try
            {
                if (state.Options.Pansou)
                {
                    var record = Json.Load<ProcessRecord>(Path.Combine(root, "run", "pansou.json"));
                    using (var existing = Verified(record))
                    {
                        if (existing == null)
                        {
                            if (!IsPortFree(state.Options.PansouPort)) throw new IOException("盘搜端口已被占用，请安装/修复以重新分配端口。");
                            var env = new Dictionary<string, string> {
                                { "PORT", state.Options.PansouPort.ToString() },
                                { "ENABLED_PLUGINS", "lingjisp,pansearch,quark4k,duoduo,dygang,hdmoli,hunhepan,ouge" },
                                { "CACHE_PATH", Path.Combine(root, "data", "pansou-cache") },
                                { "AUTH_ENABLED", "false" }, { "GIN_MODE", "release" }
                            };
                            StartOne(state, "pansou", env, report); started.Add("pansou");
                        }
                    }
                    WaitHealthy("http://127.0.0.1:" + state.Options.PansouPort + "/api/health", cancel);
                }
                if (mediaRunning) { report("家庭影院已经在运行，所选组件已就绪。"); return; }
                File.Copy(Path.Combine(root, "config.json"), Path.Combine(state.ActiveDirectory, "config.json"), true);
                StartOne(state, "media-server", null, report); started.Add("media-server");
                WaitHealthy("http://127.0.0.1:" + state.Options.Port + "/api/web-manifest", cancel);
                if (state.Options.Lan)
                {
                    try
                    {
                    foreach (var adapter in System.Net.NetworkInformation.NetworkInterface.GetAllNetworkInterfaces())
                    {
                        if (adapter.OperationalStatus != System.Net.NetworkInformation.OperationalStatus.Up) continue;
                        foreach (var entry in adapter.GetIPProperties().UnicastAddresses)
                        {
                            if (entry.Address.AddressFamily != AddressFamily.InterNetwork) continue;
                            var bytes = entry.Address.GetAddressBytes();
                            bool local = bytes[0] == 10 || (bytes[0] == 172 && bytes[1] >= 16 && bytes[1] <= 31) || (bytes[0] == 192 && bytes[1] == 168);
                            if (!local) continue;
                            string baseUrl = "http://" + entry.Address + ":" + state.Options.Port;
                            report("局域网地址：" + baseUrl);
                            report("手机浏览器下载 App：" + baseUrl + "/api/app/apk");
                        }
                    }
                    }
                    catch (System.Net.NetworkInformation.NetworkInformationException) { report("局域网地址暂时无法列出，主服务已正常启动。"); }
                }
                report("家庭影院已启动：http://127.0.0.1:" + state.Options.Port);
            }
            catch
            {
                foreach (string name in started)
                {
                    string record = Path.Combine(root, "run", name + ".json");
                    using (var process = Verified(Json.Load<ProcessRecord>(record)))
                        if (process != null) { process.Kill(); process.WaitForExit(10000); }
                    if (File.Exists(record)) File.Delete(record);
                }
                throw;
            }
        }
        private static void WaitHealthy(string url, CancellationToken cancel)
        {
            for (int i = 0; i < 45; i++)
            {
                cancel.ThrowIfCancellationRequested();
                if (Healthy(url)) return;
                cancel.WaitHandle.WaitOne(500);
            }
            throw new IOException("服务未能正常启动，请查看运行日志。");
        }
        private static void StartOne(InstallState state, string name, Dictionary<string, string> environment, Action<string> report)
        {
            string root = state.Options.Root, binary = Path.Combine(state.ActiveDirectory, name + ".exe");
            string recordPath = Path.Combine(root, "run", name + ".json");
            if (File.Exists(recordPath)) File.Delete(recordPath);
            // 日志宿主独立于界面存活，避免关闭安装窗口后丢失后台进程的输出。
            var wrapper = new ProcessStartInfo(File.Exists(state.ManagerPath) ? state.ManagerPath : Assembly.GetExecutingAssembly().Location,
                "--host --root " + Quote(root) + " --component " + name) {
                WorkingDirectory = root, UseShellExecute = false, CreateNoWindow = true
            };
            if (environment != null) foreach (var item in environment) wrapper.EnvironmentVariables[item.Key] = item.Value;
            using (var command = Process.Start(wrapper))
            {
                Process process = null;
                for (int i = 0; i < 40 && process == null; i++)
                {
                    Thread.Sleep(100);
                    process = Verified(Json.Load<ProcessRecord>(recordPath));
                    if (command.HasExited && process == null) break;
                }
                if (process == null) throw new IOException(name + " 启动失败，请查看日志。");
                process.Dispose();
            }
            report(name + " 进程已启动");
        }
        public static void Host(string root, string name)
        {
            if (name != "media-server" && name != "pansou") throw new ArgumentException("未知组件。");
            var state = InstallState.Load(root);
            if (state == null) throw new IOException("安装状态不存在。");
            string binary = Path.Combine(state.ActiveDirectory, name + ".exe");
            string output = Path.Combine(root, "logs", name + ".log"), error = Path.Combine(root, "logs", name + ".error.log");
            Rotate(output); Rotate(error);
            using (var stdout = new StreamWriter(new FileStream(output, FileMode.Append, FileAccess.Write, FileShare.ReadWrite), new UTF8Encoding(false)))
            using (var stderr = new StreamWriter(new FileStream(error, FileMode.Append, FileAccess.Write, FileShare.ReadWrite), new UTF8Encoding(false)))
            using (var process = new Process { StartInfo = new ProcessStartInfo(binary) {
                WorkingDirectory = state.ActiveDirectory, UseShellExecute = false, CreateNoWindow = true,
                RedirectStandardOutput = true, RedirectStandardError = true, StandardOutputEncoding = Encoding.UTF8, StandardErrorEncoding = Encoding.UTF8
            } })
            {
                stdout.AutoFlush = true; stderr.AutoFlush = true;
                process.OutputDataReceived += delegate(object sender, DataReceivedEventArgs e) { if (e.Data != null) lock (stdout) stdout.WriteLine(e.Data); };
                process.ErrorDataReceived += delegate(object sender, DataReceivedEventArgs e) { if (e.Data != null) lock (stderr) stderr.WriteLine(e.Data); };
                process.Start(); process.BeginOutputReadLine(); process.BeginErrorReadLine();
                using (var job = new ProcessJob(process))
                {
                    Json.Save(Path.Combine(root, "run", name + ".json"), new ProcessRecord { Pid = process.Id, StartedUtcTicks = process.StartTime.ToUniversalTime().Ticks, Path = binary });
                    process.WaitForExit();
                }
            }
        }
        private static void Rotate(string path)
        {
            if (!File.Exists(path) || new FileInfo(path).Length < 5 * 1024 * 1024) return;
            if (File.Exists(path + ".old")) File.Delete(path + ".old");
            File.Move(path, path + ".old");
        }
    }
}
