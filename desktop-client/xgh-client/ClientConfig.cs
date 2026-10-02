using System;
using System.IO;
using System.Runtime.Serialization;
using System.Runtime.Serialization.Json;
using System.Text;

namespace XghClient
{
    /// <summary>
    /// config.json 模型。与程序同目录，由安装包部署或手工复制 config.example.json 修改。
    /// </summary>
    [DataContract]
    public class ClientConfig
    {
        [DataMember(Name = "server_host")]
        public string ServerHost { get; set; }

        [DataMember(Name = "ssh_port")]
        public int SshPort { get; set; } = 22;

        [DataMember(Name = "ssh_user")]
        public string SshUser { get; set; } = "xgh-tunnel";

        // MVP 用每机不同的隧道密码（管理员在服务器上为每台机器单独设置）；
        // 配了 key_path 时优先用密钥认证，密码忽略。
        [DataMember(Name = "ssh_password")]
        public string SshPassword { get; set; }

        [DataMember(Name = "key_path")]
        public string KeyPath { get; set; }

        // 服务器公钥指纹（P0 脚本 ssh-keygen -E md5 -lf 的输出，如 MD5:ab:cd:...）。
        // 留空 = 拒绝连接（防中间人，不提供"跳过校验"的口子）。
        [DataMember(Name = "server_fingerprint")]
        public string ServerFingerprint { get; set; }

        [DataMember(Name = "local_port")]
        public int LocalPort { get; set; } = 18080;

        // 转发目标以"服务器视角"为准：sshd 在服务器本机转发到后端 8080
        [DataMember(Name = "remote_host")]
        public string RemoteHost { get; set; } = "127.0.0.1";

        [DataMember(Name = "remote_port")]
        public int RemotePort { get; set; } = 8080;

        // 以下两项仅本地开发调试用：绕过 SSH 隧道直接加载后端地址。
        // 正式分发的 config.json 不启用。
        [DataMember(Name = "direct_mode")]
        public bool DirectMode { get; set; }

        [DataMember(Name = "direct_url")]
        public string DirectUrl { get; set; }

        public static ClientConfig Load(string path)
        {
            if (!File.Exists(path))
                throw new FileNotFoundException("配置文件不存在：" + path);

            using (var fs = File.OpenRead(path))
            {
                var ser = new DataContractJsonSerializer(typeof(ClientConfig));
                var cfg = (ClientConfig)ser.ReadObject(fs);
                if (cfg == null)
                    throw new InvalidDataException("配置文件内容为空或不是有效的 JSON 对象");
                return cfg;
            }
        }
    }
}
