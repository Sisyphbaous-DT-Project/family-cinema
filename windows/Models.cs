using System;
using System.Collections.Generic;
using System.IO;
using System.Reflection;
using System.Text;
using System.Web.Script.Serialization;

namespace FamilyCinema.Setup
{
    internal sealed class ReleaseSettings
    {
        public string Version { get; set; }
        public string SourceCommit { get; set; }
        public string SourceSha256 { get; set; }
        public string SourceUrl { get; set; }
        public string ApkUrl { get; set; }
        public string ApkSha256 { get; set; }
        public int ApkVersionCode { get; set; }
        public string ApkVersionName { get; set; }
        public string GoVersion { get; set; }
        public string GoSha256 { get; set; }
        public string PansouSha256 { get; set; }
        public string FFmpegUrl { get; set; }
        public string FFmpegSha256 { get; set; }

        public static ReleaseSettings Load()
        {
            using (var stream = Assembly.GetExecutingAssembly().GetManifestResourceStream("release-settings.json"))
            {
                if (stream == null) throw new InvalidDataException("安装包缺少发布信息。");
                using (var reader = new StreamReader(stream, Encoding.UTF8))
                    return Json.Read<ReleaseSettings>(reader.ReadToEnd());
            }
        }
    }

    internal sealed class InstallOptions
    {
        public string Root { get; set; }
        public string Media { get; set; }
        public bool Pansou { get; set; }
        public bool FFmpeg { get; set; }
        public bool Lan { get; set; }
        public bool AutoStart { get; set; }
        public int Port { get; set; }
        public int PansouPort { get; set; }

        public static InstallOptions Defaults()
        {
            return new InstallOptions {
                Root = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinema"),
                Media = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.MyVideos), "FamilyCinema"),
                Pansou = true, Lan = true, Port = 8088, PansouPort = 18888
            };
        }
    }

    internal sealed class InstallState
    {
        public string Version { get; set; }
        public string ActiveDirectory { get; set; }
        public string ManagerPath { get; set; }
        public InstallOptions Options { get; set; }
        public string[] Warnings { get; set; }
        public static InstallState Load(string root) { return Json.Load<InstallState>(Path.Combine(root, "installation.json")); }
    }

    internal sealed class ProcessRecord
    {
        public int Pid { get; set; }
        public long StartedUtcTicks { get; set; }
        public string Path { get; set; }
    }

    internal static class Json
    {
        public static T Read<T>(string text) { return new JavaScriptSerializer { MaxJsonLength = 16 * 1024 * 1024 }.Deserialize<T>(text); }
        public static string Stringify(object value) { return new JavaScriptSerializer().Serialize(value); }
        public static T Load<T>(string path) where T : class
        {
            if (!File.Exists(path)) return null;
            return Read<T>(File.ReadAllText(path, Encoding.UTF8));
        }
        public static void Save(string path, object value)
        {
            Directory.CreateDirectory(Path.GetDirectoryName(path));
            var temp = path + ".new";
            File.WriteAllText(temp, Stringify(value), new UTF8Encoding(false));
            if (File.Exists(path)) File.Replace(temp, path, null); else File.Move(temp, path);
        }
        public static Dictionary<string, object> Object(string path)
        {
            return Load<Dictionary<string, object>>(path) ?? new Dictionary<string, object>();
        }
    }
}
