using System;
using System.Threading;
using Renci.SshNet;
using Renci.SshNet.Common;

namespace XghClient
{
    /// <summary>
    /// SSH 加密隧道：连接服务器 → 本地监听 127.0.0.1:LocalPort → 转发到服务器视角的 RemoteHost:RemotePort。
    /// 断线自动重连（退避 5s→30s），公钥固定防中间人。
    /// </summary>
    public class TunnelManager : IDisposable
    {
        private readonly ClientConfig _cfg;
        private SshClient _client;
        private ForwardedPortLocal _port;
        private Thread _loop;
        private volatile bool _stopped;

        // 在后台线程触发；消费方（MainForm）负责 Invoke 到 UI 线程
        public event Action<string, bool> StatusChanged;

        public TunnelManager(ClientConfig cfg)
        {
            _cfg = cfg;
        }

        public void Start()
        {
            _stopped = false;
            _loop = new Thread(RunLoop);
            _loop.IsBackground = true;
            _loop.Start();
        }

        private void RunLoop()
        {
            int backoff = 5;
            while (!_stopped)
            {
                try
                {
                    Log.Info("隧道: 正在连接 " + _cfg.ServerHost + ":" + _cfg.SshPort + "（用户 " + _cfg.SshUser + "）");
                    Emit("正在连接 " + _cfg.ServerHost + ":" + _cfg.SshPort + " …", false);

                    var info = new ConnectionInfo(
                        _cfg.ServerHost, _cfg.SshPort, _cfg.SshUser,
                        new AuthenticationMethod[] { BuildAuth() });
                    _client = new SshClient(info);
                    _client.HostKeyReceived += OnHostKey;
                    _client.ConnectionInfo.Timeout = TimeSpan.FromSeconds(10);
                    _client.KeepAliveInterval = TimeSpan.FromSeconds(15);
                    _client.Connect();
                    if (!_client.IsConnected)
                        throw new Exception("SSH 握手失败");

                    // 转发目标以服务器视角为准：sshd 转发到服务器本机的后端 8080
                    _port = new ForwardedPortLocal(
                        "127.0.0.1", (uint)_cfg.LocalPort,
                        _cfg.RemoteHost, (uint)_cfg.RemotePort);
                    _client.AddForwardedPort(_port);
                    _port.Start();
                    Log.Info("隧道: 已建立 127.0.0.1:" + _cfg.LocalPort + " → " + _cfg.RemoteHost + ":" + _cfg.RemotePort);
                    Emit("已连接（http://127.0.0.1:" + _cfg.LocalPort + "）", true);
                    backoff = 5;

                    // 阻塞等待断开
                    while (!_stopped && _client.IsConnected && _port.IsStarted)
                        Thread.Sleep(1000);
                    if (_stopped) break;
                    throw new Exception("连接断开");
                }
                catch (Exception ex)
                {
                    if (_stopped) break;
                    Log.Error("隧道: " + ex.Message);
                    Emit("连接断开（" + ex.Message + "），" + backoff + " 秒后重试", false);
                    CleanupOnce();
                    Thread.Sleep(backoff * 1000);
                    backoff = Math.Min(backoff * 2, 30);
                }
            }
        }

        private void Emit(string message, bool connected)
        {
            var handler = StatusChanged;
            if (handler != null) handler(message, connected);
        }

        private AuthenticationMethod BuildAuth()
        {
            var keyPath = _cfg.KeyPath;
            if (!string.IsNullOrEmpty(keyPath) && System.IO.File.Exists(keyPath))
            {
                var key = new PrivateKeyFile(keyPath);
                return new PrivateKeyAuthenticationMethod(_cfg.SshUser, key);
            }
            return new PasswordAuthenticationMethod(_cfg.SshUser, _cfg.SshPassword ?? "");
        }

        private void OnHostKey(object sender, HostKeyEventArgs e)
        {
            // 公钥固定：指纹不符即拒绝。未配置指纹也拒绝——不提供"跳过校验"的口子。
            e.CanTrust = false;
            var expected = (_cfg.ServerFingerprint ?? "").Trim();
            if (expected.Length == 0)
            {
                Log.Error("隧道: 未配置 server_fingerprint，拒绝连接（不提供跳过校验）");
                return;
            }

            var fp = FormatFingerprint(e.FingerPrint);
            if (expected.StartsWith("MD5:", StringComparison.OrdinalIgnoreCase))
                expected = expected.Substring(4);
            expected = expected.Replace(":", "").Trim().ToLowerInvariant();

            var trusted = string.Equals(fp, expected, StringComparison.Ordinal);
            if (!trusted)
                Log.Error("隧道: 服务器指纹不符，拒绝连接。期望: " + expected + "  实际: " + fp +
                          "（请与 p0-server-setup.sh 采集的 MD5 指纹核对）");
            e.CanTrust = trusted;
        }

        // e.FingerPrint 的哈希算法以 SSH.NET 2024.0.0 实际实现为准（历史版本为 MD5）。
        // 统一输出为无冒号小写 hex，与 ssh-keygen -E md5 的输出对齐后可比。
        // P1 对真实服务器验证时核对指纹匹配；若算法不符，调整这里而不是放宽校验。
        private static string FormatFingerprint(byte[] fingerprint)
        {
            if (fingerprint == null) return "";
            var sb = new System.Text.StringBuilder(fingerprint.Length * 2);
            foreach (var b in fingerprint)
                sb.Append(b.ToString("x2"));
            return sb.ToString();
        }

        private void CleanupOnce()
        {
            try { if (_port != null) { if (_port.IsStarted) _port.Stop(); _port = null; } }
            catch { }
            try { if (_client != null) { _client.Disconnect(); _client.Dispose(); _client = null; } }
            catch { }
        }

        public void Dispose()
        {
            _stopped = true;
            CleanupOnce();
            if (_loop != null && _loop.IsAlive)
                _loop.Join(3000);
        }
    }
}
