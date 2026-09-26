# Ubuntu 容器化部署方案（学管会综合管理系统后端）

适用：Ubuntu 20.04+ 服务器，Docker + Docker Compose v2，公网部署，前端与 API 同源反代。
本方案涉及的四个文件已入库：`backend/Dockerfile`、`backend/.dockerignore`、`docker-compose.yml`、`.env.example`。

## 一、为什么这样做（与代码事实一一对应）

| 代码事实 | 出处 | 部署对策 |
|---|---|---|
| 纯 Go sqlite `glebarez/sqlite`，无 CGO | `internal/repository/db.go:9` | `CGO_ENABLED=0` 静态编译，builder 与运行镜像都不需要 gcc |
| DB 路径环境变量 `DB_PATH`，默认相对 `xgh_system.db` | `cmd/server/main.go:16`、`internal/repository/db.go:22` | 固定 `DB_PATH=/data/xgh_system.db`，卷持久化 |
| 时段判定/单双周/ISO 周全走 `time.Local` | `internal/model/period_test.go`、`honor_controller.go:99` | 镜像装 `tzdata` + `TZ=Asia/Shanghai`，否则"今天""本周"整体错 8 小时 |
| Casbin 模型找 `rbac_model.conf`（先工作目录） | `cmd/server/main.go:22-25` | COPY 进镜像工作目录 `/app` |
| 静态资源挂 `./static`、上传目录 `./uploads` | `cmd/server/main.go:38-42`、`dorm_controller.go:141` | `static` 进镜像；`uploads` 必须落持久卷——注意代码写死 `./uploads`（`/app/uploads`），见"已知取舍" |
| JWT 密钥：`JWT_SECRET` 环境变量 > `jwt_secret.key` > 随机生成 | `pkg/jwt/jwt.go:19-40` | compose 显式注入 `JWT_SECRET`，不依赖兜底文件 |
| 配置加密密钥：`CRYPTO_SECRET` 环境变量 > `crypto_secret.key` | `pkg/secretbox/secretbox.go:32-55` | 同上注入 `CRYPTO_SECRET`；**定了就不能换**，换了 `enc:v1:` 密文全解不开 |
| CORS 默认仅同源，`ALLOWED_ORIGINS` 白名单 | `internal/middleware/auth.go:23-32` | 前后端同域反代 = 留空即可，绝不填 `*` |
| 监听端口读 `PORT`（默认 8080） | `cmd/server/main.go:261` | compose `PORT=8080`。旧部署文档里的 `ADDR` 变量代码里不存在，勿用 |

## 二、服务器准备（一次性）

```bash
# Docker Engine + Compose 插件（Ubuntu 官方源即可，版本够用）
sudo apt-get update && sudo apt-get install -y docker.io docker-compose-v2
sudo systemctl enable --now docker

# 防火墙：只放 80/443，8080 由反代内部访问
sudo ufw allow 80/tcp && sudo ufw allow 443/tcp && sudo ufw enable
```

## 三、部署步骤

```bash
# 1. 取代码
sudo mkdir -p /opt/xgh && sudo chown $USER /opt/xgh
git clone <你的仓库地址> /opt/xgh && cd /opt/xgh

# 2. 写密钥（两把钥匙生成命令在注释里，务必不同值）
cp .env.example .env
openssl rand -hex 32   # → JWT_SECRET
openssl rand -hex 32   # → CRYPTO_SECRET（换另一个值）
nano .env              # 填进去；chmod 600 .env

# 3. 构建并启动
docker compose build
docker compose up -d

# 4. 验证
docker compose logs -f xgh-server        # 看到 "正在监听: :8080"，无 [Security] 警告
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/api/v1/recruit/info   # 200
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:8080/                     # 200 (index.html)
```

## 四、公网入口：Caddy 反代（推荐，自动签 TLS 证书）

`/etc/caddy/Caddyfile`：

```
xgh.example.edu {
    reverse_proxy 127.0.0.1:8080
}
```

```bash
sudo apt install -y caddy && sudo systemctl reload caddy
```

要点：
- compose 已把 8080 绑在 `127.0.0.1`，公网无法直连容器端口，所有流量过反代拿 HTTPS。
- 前端与 API 同域（反代同时服务 `/` 与 `/api`），`ALLOWED_ORIGINS` 保持空 = 同源策略天然满足，Cookie 不涉及跨站。
- Docker 官方 `ce` 源与 Compose v1 的写法差异较大；若用老版 compose，把 `docker compose` 换成 `docker-compose` 即可，文件无需改。

## 五、数据备份与恢复

全部状态都在 `xgh-data` 卷里（sqlite 库 + uploads + 兜底密钥文件）：

```bash
# 备份（sqlite 在线快照要先停写，最稳是停容器再拷）
docker compose stop xgh-server
sudo tar czf /backup/xgh-$(date +%F).tar.gz -C /var/lib/docker/volumes/xgh-server_xgh-data _data 2>/dev/null \
  || docker run --rm -v xgh-server_xgh-data:/data -v /backup:/backup alpine tar czf /backup/xgh-$(date +%F).tar.gz -C /data .
docker compose start xgh-server

# 恢复：倒序解包回卷，再 up -d
```

备份必须与 `.env` 同批保管：**丢了 `CRYPTO_SECRET`，库里的 AI/福利网关密钥密文无法解密**，需要在后台重新录入密钥（系统不会崩，但配置要重录）。`JWT_SECRET` 丢了只影响已登录会话，重设即可。

## 六、升级发布

```bash
cd /opt/xgh && git pull
docker compose build && docker compose up -d     # 秒级替换，restart: unless-stopped 保证开机自启
docker image prune -f                            # 清旧层
```

sqlite 迁移由 `AutoMigrate` 在启动时自动执行；升级前照例先做一次卷备份。

## 七、已知取舍（诚实清单）

1. **uploads 目录**：上传代码写死 `./uploads`（相对工作目录 `/app`），不在 `/data`。compose 已直接挂了 `xgh-uploads:/app/uploads` 卷保住照片，备份时注意**卷有两个**（`xgh-data` 与 `xgh-uploads`）都要备。
2. **sqlite 单文件**：没有主从/集群，容器只能单实例（compose 未做 scale，也不要做）。公网部署够用，但要接受备份纪律。
3. **无登录限速**：登录接口本身没有失败次数限制，公网暴露后建议在 Caddy 层加 rate limit 插件，或后续在 C 组补做（目前 C 组已完成项不含此项）。
4. **健康检查**用的是公开的招新接口，只证明"进程活着、路由通"，不代表数据库完好；深度巡检看 `docker compose logs` 里的 GIN 访问日志。

## 八、快速自检清单（上线当天逐条打勾）

- [ ] `curl http://127.0.0.1:8080/api/v1/recruit/info` 返回 200
- [ ] `docker compose logs xgh-server | grep -i warn` 无 `[Security] 无法写入` 类警告（即密钥已由环境变量提供，不靠兜底落盘）
- [ ] 浏览器走 `https://域名` 登录成功，Cookie 是 HttpOnly 且不带 SameSite=None 告警
- [ ] 宿管 APK 端登录正常（`/api/v1/auth/dorm-quick-login` 走同一域名）
- [ ] 后台"AI 调度"页配置一个测试密钥 → `docker compose exec xgh-server grep -c enc:v1: /data/xgh_system.db` 或直接查库确认密文落库
- [ ] 时段任务页显示的"当前时段"与北京时间一致（时区自证）
- [ ] `ufw status` 确认 8080 未对公网开放
