using System;
using System.IO;
using System.Net;
using System.Drawing;
using System.Threading.Tasks;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace XghClient
{
    /// <summary>
    /// 独立窗口宿主：WebView2 渲染现有 Web 前端 + 底部状态栏显示隧道状态。
    /// 外壳不加任何业务逻辑——登录、扣分、排班全走现有 Web 界面。
    /// 加载失败/隧道未通时显示内置提示页（自动重试 + 手动重试按钮）。
    /// </summary>
    public class MainForm : Form
    {
        private readonly ClientConfig _cfg;
        private readonly TunnelManager _tunnel;
        private WebView2 _web;
        private ToolStripStatusLabel _status;
        private NotifyIcon _tray;
        private readonly Timer _retryTimer;
        private bool _webReady;
        private bool _tunnelConnected;
        private bool _navigated;
        private bool _showingInfo;
        private FormWindowState _lastNonMin = FormWindowState.Maximized;

        public MainForm(ClientConfig cfg)
        {
            _cfg = cfg;
            _tunnel = new TunnelManager(cfg);
            _tunnel.StatusChanged += OnTunnelStatus;

            Text = "学管会综合管理系统";
            Width = 1280;
            Height = 800;
            StartPosition = FormStartPosition.CenterScreen;
            WindowState = FormWindowState.Maximized;

            _web = new WebView2 { Dock = DockStyle.Fill };

            var strip = new StatusStrip();
            _status = new ToolStripStatusLabel("正在初始化…");
            strip.Items.Add(_status);

            Controls.Add(_web);
            Controls.Add(strip);

            SetupTrayIcon();

            _retryTimer = new Timer { Interval = 5000 };
            _retryTimer.Tick += OnRetryTick;
            _retryTimer.Start();

            Resize += OnResize;
            Load += OnFormLoad;
            FormClosing += OnFormClosing;
        }

        private void SetupTrayIcon()
        {
            _tray = new NotifyIcon
            {
                Icon = SystemIcons.Application,
                Text = "学管会综合管理系统",
                Visible = true
            };
            var menu = new ContextMenuStrip();
            menu.Items.Add("打开", null, delegate { RestoreFromTray(); });
            menu.Items.Add("退出", null, delegate { Close(); });
            _tray.ContextMenuStrip = menu;
            _tray.DoubleClick += delegate { RestoreFromTray(); };
        }

        // 最小化即收进托盘（不占任务栏），从托盘图标恢复
        private void OnResize(object sender, EventArgs e)
        {
            if (WindowState == FormWindowState.Minimized)
                Hide();
            else
                _lastNonMin = WindowState;
        }

        private void RestoreFromTray()
        {
            Show();
            WindowState = _lastNonMin;
            Activate();
        }

        private async void OnFormLoad(object sender, EventArgs e)
        {
            Log.Info("窗口加载，模式: " + (_cfg.DirectMode ? "本地直连（调试）" : "SSH 隧道"));
            if (_cfg.DirectMode)
            {
                // 直连模式视为常通，失败提示页/自动重试照常工作
                _tunnelConnected = true;
                _status.Text = "本地直连模式（仅开发调试，正式分发不启用）";
            }
            else
            {
                _tunnel.Start();
            }

            try
            {
                await InitWebViewAsync();
                _webReady = true;
                WireWebEvents();
                AttemptNavigate();
            }
            catch (Exception ex)
            {
                Log.Error("WebView2 初始化失败: " + ex.Message);
                _status.Text = "WebView2 初始化失败：" + ex.Message;
                MessageBox.Show(
                    "WebView2 运行时初始化失败：" + ex.Message +
                    "\n\n若为 Win7 部署：请确认应用目录下存在 WebView2Runtime/ 固定版运行时文件夹（109.0.1518.78 x86）。" +
                    "\n若为本机开发：请安装 WebView2 Evergreen 运行时。",
                    "学管会客户端",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private async Task InitWebViewAsync()
        {
            // 用户数据目录固定在 LOCALAPPDATA，不写 Program Files（标准用户无写权限）
            var userData = Path.Combine(
                Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                "XghClient", "WebView2");

            // Win7 部署用固定版运行时（应用目录 WebView2Runtime/）；本机开发回退到系统 Evergreen
            var fixedDir = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "WebView2Runtime");
            CoreWebView2Environment env;
            if (Directory.Exists(fixedDir))
                env = await CoreWebView2Environment.CreateAsync(fixedDir, userData);
            else
                env = await CoreWebView2Environment.CreateAsync(null, userData);

            await _web.EnsureCoreWebView2Async(env);
        }

        private void WireWebEvents()
        {
            _web.CoreWebView2.NavigationCompleted += OnNavCompleted;
            _web.CoreWebView2.WebMessageReceived += (s, e) =>
            {
                if (e.TryGetWebMessageAsString() == "xgh-retry")
                {
                    Log.Info("用户点击「立即重试」");
                    AttemptNavigate();
                }
            };
        }

        // TunnelManager 的事件在后台线程触发，统一 Invoke 到 UI 线程
        private void OnTunnelStatus(string message, bool connected)
        {
            if (IsDisposed) return;
            BeginInvoke((MethodInvoker)delegate
            {
                if (IsDisposed) return;
                _status.Text = message;
                _tunnelConnected = connected;
                if (connected)
                {
                    if (!_navigated || _showingInfo)
                        AttemptNavigate();
                    else if (_web.CoreWebView2 != null)
                    {
                        // 重连成功：刷新页面恢复前端请求
                        _web.CoreWebView2.Reload();
                    }
                }
            });
        }

        private void OnRetryTick(object sender, EventArgs e)
        {
            if (_showingInfo && _tunnelConnected)
                AttemptNavigate();
        }

        private string TargetUrl()
        {
            if (_cfg.DirectMode && !string.IsNullOrEmpty(_cfg.DirectUrl))
                return _cfg.DirectUrl;
            return "http://127.0.0.1:" + _cfg.LocalPort;
        }

        private void AttemptNavigate()
        {
            if (!_webReady || _web.CoreWebView2 == null) return;

            if (!_cfg.DirectMode && !_tunnelConnected)
            {
                ShowInfoPage("正在连接服务器", "正在建立加密隧道，连接成功后会自动进入系统…", "");
                return;
            }

            var url = TargetUrl();
            Log.Info("导航 → " + url);
            _showingInfo = false;
            _navigated = true;
            _web.CoreWebView2.Navigate(url);
        }

        private void OnNavCompleted(object sender, CoreWebView2NavigationCompletedEventArgs e)
        {
            if (_showingInfo) return;
            if (e.IsSuccess)
            {
                Log.Info("页面加载完成");
                return;
            }
            Log.Error("页面加载失败: " + e.WebErrorStatus);
            ShowErrorPage("错误代码 " + e.WebErrorStatus);
        }

        private void ShowErrorPage(string detail)
        {
            _showingInfo = true;
            _web.CoreWebView2.NavigateToString(BuildPage(
                "无法连接到服务器",
                "正在自动重试，恢复后本页会自动进入系统。",
                ("目标地址: " + TargetUrl() + (string.IsNullOrEmpty(detail) ? "" : "  ·  " + detail)).Trim()));
            _status.Text = "无法连接到服务器，等待自动重试…";
        }

        private void ShowInfoPage(string title, string msg, string detail)
        {
            _showingInfo = true;
            _web.CoreWebView2.NavigateToString(BuildPage(title, msg, detail));
            _status.Text = title + "…";
        }

        private static string BuildPage(string title, string msg, string detail)
        {
            return PageHtml
                .Replace("__TITLE__", WebUtility.HtmlEncode(title))
                .Replace("__MSG__", WebUtility.HtmlEncode(msg))
                .Replace("__DETAIL__", WebUtility.HtmlEncode(detail));
        }

        // 与后台管理端一致的视觉方向：白底为主、黑色只作边框
        private const string PageHtml = @"<!DOCTYPE html>
<html lang='zh-CN'>
<head>
<meta charset='utf-8'>
<title>学管会客户端</title>
<style>
  html, body { margin:0; height:100%; }
  body { font-family:'Microsoft YaHei',sans-serif; background:#fff; color:#111;
         display:flex; align-items:center; justify-content:center; }
  .card { border:2px solid #111; padding:40px 48px; max-width:440px; text-align:center; }
  h1 { font-size:20px; margin:0 0 14px; }
  p { font-size:14px; color:#555; margin:0 0 10px; line-height:1.7; }
  .detail { font-size:12px; color:#999; margin:14px 0 22px; word-break:break-all; }
  button { font-size:14px; padding:9px 36px; border:2px solid #111; background:#fff; color:#111; cursor:pointer; }
  button:hover { background:#111; color:#fff; }
  .hint { font-size:12px; color:#aaa; margin-top:18px; }
</style>
</head>
<body>
<div class='card'>
  <h1>__TITLE__</h1>
  <p>__MSG__</p>
  <div class='detail'>__DETAIL__</div>
  <button onclick='retry()'>立即重试</button>
  <div class='hint'>若持续无法连接，请联系技术管理员检查服务器状态。</div>
</div>
<script>
  function retry() {
    try { window.chrome.webview.postMessage('xgh-retry'); }
    catch (e) { location.reload(); }
  }
</script>
</body>
</html>";

        private void OnFormClosing(object sender, FormClosingEventArgs e)
        {
            _retryTimer.Stop();
            if (_tray != null)
            {
                _tray.Visible = false;
                _tray.Dispose();
                _tray = null;
            }
            Log.Info("客户端退出");
            _tunnel.Dispose();
        }
    }
}
