#!/usr/bin/env bash
# =============================================================================
# 学管会系统 · P0 服务端准备（在校园服务器上以 root/sudo 执行）
# 配套文档：仓库根 学管会系统_Win7客户端开发计划.md §3 P0
# =============================================================================
# 本脚本只做四件事：
#   1. 确认 sshd 在跑
#   2. 建专用隧道账号 xgh-tunnel（shell=nologin，不能登录服务器）
#   3. 输出服务器公钥指纹（写进每台客户端的 config.json）
#   4. 打印破坏性测试清单（管理员从另一台机器执行，验证三道闸门）
#
# 认证方式说明：
#   - MVP：每台机器一个不同的密码（本脚本第 2 步手动 passwd 设置）
#   - 推荐加强：每台机器一对密钥，公钥登记进 authorized_keys 的限制行
#     （见下方"密钥方式"段）。限制行是端口白名单的真正闸门——
#     密码方式无法限制转发目标，只有密钥方式的 permitopen 能锁死 8080。
# =============================================================================

set -euo pipefail

# ---- 1. 确认 sshd 在跑 ----
echo "== 1. 检查 sshd =="
if ! ss -tlnp 2>/dev/null | grep -q ':22 '; then
  echo "警告：22 端口未在监听。若未安装：sudo apt install openssh-server && sudo systemctl enable --now ssh"
  echo "（学校防火墙若封 22，需网管放行——这是隧道唯一需要可达的端口）"
  exit 1
fi
ss -tlnp 2>/dev/null | grep ':22 ' || true

# ---- 2. 建专用隧道账号（shell=nologin，不能登录 shell） ----
echo ""
echo "== 2. 创建隧道账号 xgh-tunnel =="
if id xgh-tunnel >/dev/null 2>&1; then
  echo "账号已存在，跳过创建"
else
  useradd -m -s /usr/sbin/nologin xgh-tunnel
  echo "已创建：shell=/usr/sbin/nologin（手动 ssh 登录会被拒绝，端口转发不受影响）"
fi

echo ""
echo "现在为这台客户端设置隧道密码（每台机器必须不同，记下来填进该机器的 config.json）："
read -r -p "输入回车继续 passwd xgh-tunnel ..." _dummy
passwd xgh-tunnel

# ---- 3. 输出服务器指纹（写进每台客户端 config.json 的 server_fingerprint） ----
echo ""
echo "== 3. 服务器公钥指纹 =="
echo "把下面这一行（ED25519 的 MD5 指纹）写进每台客户端 config.json 的 server_fingerprint："
ssh-keygen -E md5 -lf /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null \
  || ssh-keygen -E md5 -lf /etc/ssh/ssh_host_rsa_key.pub 2>/dev/null \
  || echo "错误：找不到服务器主机密钥，请检查 /etc/ssh/ssh_host_*_key.pub"

# ---- 4. 密钥方式（推荐加强，每台机器一对密钥） ----
echo ""
echo "== 4. 密钥方式（推荐）=="
echo "在有 ssh-keygen 的机器上为每台客户端生成一对密钥（私钥交客户端，公钥登记到服务器）："
echo "  ssh-keygen -t ed25519 -f xgh-client-01.key -N '' -C xgh-client-01"
echo "然后把下面的限制行追加到 /home/xgh-tunnel/.ssh/authorized_keys"
echo "（把 AAAA... 换成 xgh-client-01.key.pub 的内容；每台机器一行）："
echo ""
echo '  restrict,port-forwarding,permitopen="127.0.0.1:8080" ssh-ed25519 AAAA... xgh-client-01'
echo ""
echo "restrict          = 关闭 pty/shell/agent/X11 转发"
echo "port-forwarding   = 单独放回转发能力"
echo 'permitopen="..."  = 转发目标锁死在本机 8080（这是端口白名单的真正闸门）'
echo ""
echo "登记命令（管理员执行，每台机器一行）："
echo '  mkdir -p /home/xgh-tunnel/.ssh'
echo '  echo '"'"'restrict,port-forwarding,permitopen="127.0.0.1:8080" ssh-ed25519 AAAA... xgh-client-01'"'"' >> /home/xgh-tunnel/.ssh/authorized_keys'
echo '  chown -R xgh-tunnel:xgh-tunnel /home/xgh-tunnel/.ssh'
echo '  chmod 700 /home/xgh-tunnel/.ssh && chmod 600 /home/xgh-tunnel/.ssh/authorized_keys'

# ---- 5. 破坏性测试清单（从另一台机器执行，验证三道闸门） ----
echo ""
echo "== 5. 破坏性测试清单（把 SERVER 换成服务器地址，PASSPHRASE 换成刚设的密码）=="
echo ""
echo "闸门 1：手动登录被拒绝（nologin 生效）——期望'This account is not available'，拿不到 shell："
echo '  ssh xgh-tunnel@SERVER   # 输入密码'
echo ""
echo "闸门 2：密码方式转发到非 8080 端口——期望：能连上（密码方式无法限制端口，这是已知限制，"
echo "        正式部署请改用第 4 步的密钥方式让 permitopen 锁死）："
echo '  ssh -N -L 19999:127.0.0.1:22 xgh-tunnel@SERVER'
echo ""
echo "闸门 3：转发到 8080 并访问后端——期望：curl 返回 200 且是系统首页 HTML："
echo '  ssh -N -L 18080:127.0.0.1:8080 xgh-tunnel@SERVER &'
echo '  sleep 3 && curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18080/ && echo " <- 期望 200"'
echo ""
echo "密钥方式追加验证（闸门 4）：permitopen 生效——转发到 22 应被拒绝："
echo '  ssh -N -L 19999:127.0.0.1:22 -i xgh-client-01.key xgh-tunnel@SERVER'
echo '  # 期望：channel ...: connect failed: open failed (administratively prohibited)'

echo ""
echo "== 完成。下一步：把指纹与密码/密钥填进客户端 config.json，启动 xgh-client.exe =="
