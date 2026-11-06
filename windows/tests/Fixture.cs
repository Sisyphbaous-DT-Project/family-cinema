using System;
using System.Net;
using System.IO;
using System.Reflection;
using System.Text;

internal static class Fixture
{
    private static void Main(string[] args)
    {
        if (Path.GetFileNameWithoutExtension(Assembly.GetExecutingAssembly().Location) == "go")
        {
            int output = Array.IndexOf(args, "-o");
            if (output < 0) throw new ArgumentException("missing output");
            File.Copy(Assembly.GetExecutingAssembly().Location, args[output + 1], true);
            Console.WriteLine("fixture compiled");
            return;
        }
        string port = Environment.GetEnvironmentVariable("PORT") ?? Environment.GetEnvironmentVariable("FIXTURE_MEDIA_PORT");
        using (var listener = new HttpListener())
        {
            listener.Prefixes.Add("http://127.0.0.1:" + port + "/");
            listener.Start();
            Console.WriteLine("fixture started");
            while (true)
            {
                var request = listener.GetContext();
                string json = request.Request.Url.AbsolutePath == "/api/tasks" ? "[]" : "{\"status\":\"ok\",\"version\":\"fixture\"}";
                byte[] body = Encoding.UTF8.GetBytes(json);
                request.Response.ContentType = "application/json";
                request.Response.OutputStream.Write(body, 0, body.Length);
                request.Response.Close();
            }
        }
    }
}
