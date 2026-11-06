using System;
using System.Diagnostics;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Reflection;
using System.Text;
using System.Threading;
using FamilyCinema.Setup;

internal static class UpdateIdleTest
{
    private static int Main(string[] args)
    {
        if (args.Length == 1 && args[0] == "--sentinel")
        {
            Thread.Sleep(120000);
            return 0;
        }
        string temporary = Path.Combine(Path.GetTempPath(), "FamilyCinema-idle-test-" + Guid.NewGuid().ToString("N"));
        Process sentinel = null;
        int passed = 0;
        try
        {
            Directory.CreateDirectory(Path.Combine(temporary, "run"));
            sentinel = Process.Start(new ProcessStartInfo(Assembly.GetExecutingAssembly().Location, "--sentinel") {
                UseShellExecute = false, CreateNoWindow = true
            });
            Thread.Sleep(150);
            string recordPath = Path.Combine(temporary, "run", "media-server.json");
            Json.Save(recordPath, new ProcessRecord { Pid = sentinel.Id, StartedUtcTicks = sentinel.StartTime.ToUniversalTime().Ticks, Path = Assembly.GetExecutingAssembly().Location });
            string initialRecord = File.ReadAllText(recordPath);
            var state = new InstallState { Options = new InstallOptions { Root = temporary } };

            Check("empty array", "[]", 200, true, state, sentinel, recordPath, initialRecord); passed++;
            Check("Go empty slice null", " null \n", 200, true, state, sentinel, recordPath, initialRecord); passed++;
            foreach (string status in new[] { "running", "processing", "queued" })
            {
                Check(status + " task", "[{\"status\":\"" + status + "\"}]", 200, false, state, sentinel, recordPath, initialRecord); passed++;
            }
            foreach (string status in new[] { "done", "error", "stopped" })
            {
                Check(status + " task", "[{\"status\":\"" + status + "\"}]", 200, true, state, sentinel, recordPath, initialRecord); passed++;
            }
            Check("mixed terminal tasks", "[{\"status\":\"done\"},{\"status\":\"error\"},{\"status\":\"stopped\"}]", 200, true, state, sentinel, recordPath, initialRecord); passed++;
            Check("active among terminal tasks", "[{\"status\":\"done\"},{\"status\":\"queued\"}]", 200, false, state, sentinel, recordPath, initialRecord); passed++;
            Check("HTTP 500", "{\"error\":\"temporary failure\"}", 500, false, state, sentinel, recordPath, initialRecord); passed++;
            Check("invalid JSON", "this is not JSON", 200, false, state, sentinel, recordPath, initialRecord); passed++;

            var refused = new TcpListener(IPAddress.Loopback, 0);
            refused.Start();
            state.Options.Port = ((IPEndPoint)refused.LocalEndpoint).Port;
            refused.Stop();
            AssertOutcome("connection refused", false, state, sentinel, recordPath, initialRecord); passed++;
            Console.WriteLine("PASS: " + passed + " actual RequireIdle HTTP cases; registered sentinel and PID record remained unchanged.");
            return 0;
        }
        catch (Exception error)
        {
            Console.Error.WriteLine("FAIL: " + error);
            return 1;
        }
        finally
        {
            if (sentinel != null)
            {
                if (!sentinel.HasExited) { sentinel.Kill(); sentinel.WaitForExit(5000); }
                sentinel.Dispose();
            }
            string scope = Path.GetFullPath(Path.GetTempPath()).TrimEnd(Path.DirectorySeparatorChar) + Path.DirectorySeparatorChar + "FamilyCinema-idle-test-";
            if (!Path.GetFullPath(temporary).StartsWith(scope, StringComparison.OrdinalIgnoreCase)) throw new IOException("测试清理范围不符。");
            if (Directory.Exists(temporary)) Directory.Delete(temporary, true);
        }
    }

    private static void Check(string name, string body, int status, bool allowed, InstallState state, Process sentinel, string recordPath, string initialRecord)
    {
        using (var server = new FakeTasksServer(body, status))
        {
            state.Options.Port = server.Port;
            AssertOutcome(name, allowed, state, sentinel, recordPath, initialRecord);
            server.WaitForRequest();
            if (server.RequestPath != "/api/tasks") throw new Exception("未请求真实任务接口：" + server.RequestPath);
        }
    }

    private static void AssertOutcome(string name, bool allowed, InstallState state, Process sentinel, string recordPath, string initialRecord)
    {
        IOException rejection = null;
        try { ChildProcesses.RequireIdle(state); }
        catch (IOException error) { rejection = error; }
        if (allowed && rejection != null) throw new Exception(name + " 被错误拒绝。", rejection);
        if (!allowed && rejection == null) throw new Exception(name + " 未拒绝更新。");
        if (rejection != null && !rejection.Message.Contains("未停止") && !rejection.Message.Contains("没有停止"))
            throw new Exception(name + " 没有明确说明现有服务未停止。", rejection);
        if (sentinel.HasExited) throw new Exception(name + " 影响了登记的现有进程。");
        if (!File.Exists(recordPath) || File.ReadAllText(recordPath) != initialRecord)
            throw new Exception(name + " 改动了现有 PID 记录。");
        using (var verified = ChildProcesses.Verified(Json.Load<ProcessRecord>(recordPath)))
            if (verified == null || verified.Id != sentinel.Id) throw new Exception(name + " 现有进程身份不再有效。");
        Console.WriteLine("PASS " + name + ": " + (allowed ? "allowed" : "rejected without stopping existing process"));
    }

    private sealed class FakeTasksServer : IDisposable
    {
        private readonly TcpListener listener;
        private readonly Thread worker;
        private readonly string body;
        private readonly int status;
        private Exception failure;
        public int Port { get; private set; }
        public string RequestPath { get; private set; }

        public FakeTasksServer(string body, int status)
        {
            this.body = body; this.status = status;
            listener = new TcpListener(IPAddress.Loopback, 0);
            listener.Start(); Port = ((IPEndPoint)listener.LocalEndpoint).Port;
            worker = new Thread(Serve) { IsBackground = true }; worker.Start();
        }
        private void Serve()
        {
            try
            {
                using (var client = listener.AcceptTcpClient())
                using (var stream = client.GetStream())
                {
                    stream.ReadTimeout = 5000; stream.WriteTimeout = 5000;
                    var request = new StringBuilder();
                    while (!request.ToString().EndsWith("\r\n\r\n", StringComparison.Ordinal))
                    {
                        int value = stream.ReadByte();
                        if (value < 0 || request.Length > 8192) throw new IOException("测试 HTTP 请求不完整。");
                        request.Append((char)value);
                    }
                    RequestPath = request.ToString().Split(' ')[1];
                    byte[] payload = Encoding.UTF8.GetBytes(body);
                    byte[] header = Encoding.ASCII.GetBytes("HTTP/1.1 " + status + (status == 200 ? " OK" : " Internal Server Error") +
                        "\r\nContent-Type: application/json; charset=utf-8\r\nContent-Length: " + payload.Length + "\r\nConnection: close\r\n\r\n");
                    stream.Write(header, 0, header.Length); stream.Write(payload, 0, payload.Length);
                }
            }
            catch (Exception error) { failure = error; }
        }
        public void WaitForRequest()
        {
            if (!worker.Join(5000)) throw new Exception("测试任务接口未完成。");
            if (failure != null) throw new Exception("测试任务接口异常。", failure);
        }
        public void Dispose() { listener.Stop(); worker.Join(1000); }
    }
}
