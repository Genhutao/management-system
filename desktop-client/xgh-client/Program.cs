using System;
using System.IO;
using System.Reflection;
using System.Windows.Forms;

namespace XghClient
{
    static class Program
    {
        [STAThread]
        static void Main()
        {
            Log.Init();

            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            Application.ThreadException += (s, e) =>
                Log.Error("UI 线程异常: " + e.Exception);
            AppDomain.CurrentDomain.UnhandledException += (s, e) =>
                Log.Error("未处理异常: " + e.ExceptionObject);

            var cfgPath = Path.Combine(AppDomain.CurrentDomain.BaseDirectory, "config.json");
            if (!File.Exists(cfgPath))
            {
                Log.Error("缺少配置文件: " + cfgPath);
                MessageBox.Show(
                    "未找到配置文件 config.json。\n\n请确认它与程序在同一目录（可参照 config.example.json 复制一份并填写）。",
                    "学管会客户端",
                    MessageBoxButtons.OK, MessageBoxIcon.Warning);
                return;
            }

            ClientConfig cfg;
            try
            {
                cfg = ClientConfig.Load(cfgPath);
            }
            catch (Exception ex)
            {
                Log.Error("配置文件解析失败: " + ex.Message);
                MessageBox.Show(
                    "配置文件解析失败：" + ex.Message,
                    "学管会客户端",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
                return;
            }

            Log.Info("配置加载完成，启动主窗口 v" + Assembly.GetExecutingAssembly().GetName().Version);
            try
            {
                Application.Run(new MainForm(cfg));
            }
            catch (Exception ex)
            {
                Log.Error("主循环异常退出: " + ex);
                MessageBox.Show(
                    "客户端遇到错误退出：" + ex.Message,
                    "学管会客户端",
                    MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }
    }
}
