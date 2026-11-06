using System;
using System.IO;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Security.Cryptography;

namespace FamilyCinema.Setup
{
    internal static class EmbeddedDependencies
    {
        internal const string SdkVersion = "1.0.2903.40";
        private static readonly object Gate = new object();
        private static string directory;
        private static IntPtr nativeLoader;

        public static void Initialize()
        {
            lock (Gate)
            {
                if (directory != null) return;
                string target = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinemaSetup", "webview", SdkVersion);
                Directory.CreateDirectory(target);
                foreach (string name in new[] { "Microsoft.Web.WebView2.Core.dll", "Microsoft.Web.WebView2.WinForms.dll", "WebView2Loader.dll" })
                    ExtractVerified(name, target);
                ExtractVerified("WebView2-LICENSE.txt", target, "LICENSE.txt");
                ExtractVerified("WebView2-NOTICE.txt", target, "NOTICE.txt");
                nativeLoader = LoadLibraryEx(Path.Combine(target, "WebView2Loader.dll"), IntPtr.Zero, 0x00000100 | 0x00001000);
                if (nativeLoader == IntPtr.Zero) throw new IOException("无法加载网页界面组件，Windows 错误 " + Marshal.GetLastWin32Error() + "。");
                directory = target;
                AppDomain.CurrentDomain.AssemblyResolve += Resolve;
            }
        }

        private static Assembly Resolve(object sender, ResolveEventArgs args)
        {
            string name = new AssemblyName(args.Name).Name;
            if (name != "Microsoft.Web.WebView2.Core" && name != "Microsoft.Web.WebView2.WinForms") return null;
            return Assembly.LoadFrom(Path.Combine(directory, name + ".dll"));
        }

        private static void ExtractVerified(string name, string target, string resourceName = null)
        {
            byte[] expected;
            byte[] bytes;
            using (var resource = Assembly.GetExecutingAssembly().GetManifestResourceStream("webview." + (resourceName ?? name)))
            {
                if (resource == null) throw new InvalidDataException("一键程序缺少内嵌界面组件：" + name);
                using (var memory = new MemoryStream()) { resource.CopyTo(memory); bytes = memory.ToArray(); }
            }
            using (var hash = SHA256.Create()) expected = hash.ComputeHash(bytes);
            string path = Path.Combine(target, name);
            if (File.Exists(path))
            {
                using (var input = new FileStream(path, FileMode.Open, FileAccess.Read, FileShare.Read))
                using (var hash = SHA256.Create())
                    if (Equal(expected, hash.ComputeHash(input))) return;
            }
            string temporary = path + "." + Guid.NewGuid().ToString("N") + ".tmp";
            try
            {
                File.WriteAllBytes(temporary, bytes);
                if (File.Exists(path)) File.Replace(temporary, path, null); else File.Move(temporary, path);
            }
            finally { if (File.Exists(temporary)) File.Delete(temporary); }
        }

        private static bool Equal(byte[] first, byte[] second)
        {
            if (first.Length != second.Length) return false;
            int different = 0;
            for (int i = 0; i < first.Length; i++) different |= first[i] ^ second[i];
            return different == 0;
        }

        [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
        private static extern IntPtr LoadLibraryEx(string path, IntPtr reserved, uint flags);
    }
}
