using System;
using System.IO;
using System.Text;

namespace XghClient
{
    /// <summary>
    /// 线程安全的本地文件日志：%LOCALAPPDATA%\XghClient\logs\xgh-client.log。
    /// 绝不写入 ssh 密码等敏感信息。
    /// </summary>
    public static class Log
    {
        private static readonly object _lock = new object();
        private static string _path;

        public static void Init()
        {
            try
            {
                var dir = Path.Combine(
                    Environment.GetFolderPath(Environment.SpecialFolder.LocalApplicationData),
                    "XghClient", "logs");
                Directory.CreateDirectory(dir);
                _path = Path.Combine(dir, "xgh-client.log");
                RotateIfNeeded();
                Info("==== 客户端启动 ====");
            }
            catch { _path = null; }
        }

        public static void Info(string msg) { Write("INFO", msg); }

        public static void Error(string msg) { Write("ERRO", msg); }

        private static void Write(string level, string msg)
        {
            var path = _path;
            if (path == null) return;
            try
            {
                lock (_lock)
                {
                    RotateIfNeeded();
                    File.AppendAllText(path,
                        DateTime.Now.ToString("yyyy-MM-dd HH:mm:ss") + " [" + level + "] " + msg + "\r\n",
                        Encoding.UTF8);
                }
            }
            catch { }
        }

        private static void RotateIfNeeded()
        {
            var fi = new FileInfo(_path);
            if (fi.Exists && fi.Length > 5 * 1024 * 1024)
            {
                var old = _path + ".old";
                if (File.Exists(old)) File.Delete(old);
                File.Move(_path, old);
            }
        }
    }
}
