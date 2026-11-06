using System;
using System.Drawing;
using System.IO;
using System.Reflection;
using System.Text;
using System.Text.RegularExpressions;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace FamilyCinema.Setup
{
    internal sealed class BrowserHost : IDisposable
    {
        private readonly WebView2 view;
        private readonly Action<string> receive;
        private bool pendingEmbedded;
        private bool trustedPage;
        private string embeddedNavigation;
        public Control View { get { return view; } }

        public BrowserHost(Action<string> receive)
        {
            this.receive = receive;
            view = new WebView2 { DefaultBackgroundColor = Color.FromArgb(248, 250, 240) };
        }

        public static bool RuntimeAvailable()
        {
            try { return !string.IsNullOrWhiteSpace(CoreWebView2Environment.GetAvailableBrowserVersionString()); }
            catch (WebView2RuntimeNotFoundException) { return false; }
        }

        public async Task Initialize()
        {
            string data = Path.Combine(Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData), "FamilyCinemaSetup", "webview", "profile");
            var environment = await CoreWebView2Environment.CreateAsync(null, data);
            await view.EnsureCoreWebView2Async(environment);
            var core = view.CoreWebView2;
            core.Settings.AreHostObjectsAllowed = false;
            core.Settings.AreDefaultScriptDialogsEnabled = false;
            core.Settings.AreDefaultContextMenusEnabled = false;
            core.Settings.AreDevToolsEnabled = false;
            core.Settings.AreBrowserAcceleratorKeysEnabled = false;
            core.Settings.IsStatusBarEnabled = false;
            core.Settings.IsZoomControlEnabled = false;
            core.Settings.IsGeneralAutofillEnabled = false;
            core.Settings.IsPasswordAutosaveEnabled = false;
            core.Settings.IsWebMessageEnabled = true;
            core.NavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) {
                if (pendingEmbedded && (e.Uri == "about:blank" || e.Uri == embeddedNavigation))
                { pendingEmbedded = false; trustedPage = true; }
                else { e.Cancel = true; }
            };
            core.FrameNavigationStarting += delegate(object sender, CoreWebView2NavigationStartingEventArgs e) { e.Cancel = true; };
            core.NewWindowRequested += delegate(object sender, CoreWebView2NewWindowRequestedEventArgs e) { e.Handled = true; };
            core.PermissionRequested += delegate(object sender, CoreWebView2PermissionRequestedEventArgs e) { e.State = CoreWebView2PermissionState.Deny; };
            core.DownloadStarting += delegate(object sender, CoreWebView2DownloadStartingEventArgs e) { e.Cancel = true; };
            core.WebMessageReceived += delegate(object sender, CoreWebView2WebMessageReceivedEventArgs e) {
                if (trustedPage && IsEmbeddedSource(e.Source) && IsEmbeddedSource(core.Source))
                    receive(e.WebMessageAsJson);
            };
            core.AddWebResourceRequestedFilter("*", CoreWebView2WebResourceContext.All);
            core.WebResourceRequested += delegate(object sender, CoreWebView2WebResourceRequestedEventArgs e) {
                e.Response = environment.CreateWebResourceResponse(new MemoryStream(new byte[0]), 403, "Forbidden", "Content-Type: text/plain");
            };
            string html;
            using (var resource = Assembly.GetExecutingAssembly().GetManifestResourceStream("setup-ui.html"))
            {
                if (resource == null) throw new InvalidDataException("一键程序缺少内嵌安装界面。");
                using (var reader = new StreamReader(resource, Encoding.UTF8)) html = reader.ReadToEnd();
            }
            if (Encoding.UTF8.GetByteCount(html) > 1900 * 1024) throw new InvalidDataException("安装界面超过内嵌大小限制，请重新构建。");
            const string policy = "<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-src 'none'; object-src 'none'\">";
            html = Regex.Replace(html, "<head(?:\\s[^>]*)?>", "$0" + policy, RegexOptions.IgnoreCase);
            // 部分运行时以内部 data 导航承载 NavigateToString，只放行本次内嵌页面的完整内容。
            embeddedNavigation = "data:text/html;charset=utf-8;base64," + Convert.ToBase64String(Encoding.UTF8.GetBytes(html));
            pendingEmbedded = true;
            core.NavigateToString(html);
        }

        private bool IsEmbeddedSource(string source)
        { return source == "about:blank" || source == embeddedNavigation; }

        public void Send(string json)
        {
            if (!view.IsDisposed && view.CoreWebView2 != null && trustedPage) view.CoreWebView2.PostWebMessageAsJson(json);
        }

        public void Dispose()
        {
            trustedPage = false;
            view.Dispose();
        }
    }
}
