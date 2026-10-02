#!/bin/bash
# 自动编译并启动本机验证实例（Git Bash 运行：bash scripts/rebuild_and_start.sh）
#
# 做的事（与 2026-10-01 的手工操作流程一致）：
#   1. 先编译 —— 编译失败立即退出，绝不为一次失败的构建弄停正在跑的服务
#   2. 备齐运行目录六件套：exe + static/ + uploads/ + rbac_model.conf + 两把密钥
#      密钥只在缺失时才从 backend/ 拷贝：覆盖旧密钥 = 全部会话失效 / 加密数据解不开
#   3. 按端口停旧进程、换 exe（旧版留作 xgh_merged_prev.exe 回滚用）、启动
#   4. 健康检查：GET / 返回 200 才算完成
#
# 环境变量可覆盖：PORT（默认 18095）、DB_PATH（默认 backend/.verify/ui_demo.db）

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND="$ROOT/backend"
DEPLOY="$BACKEND/.verify/deploy-merged"

PORT="${PORT:-18095}"
DB_PATH="${DB_PATH:-$BACKEND/.verify/ui_demo.db}"
# Windows 进程读不了 Git Bash 的 /f/... 风格路径，转成 F:/... 再传
DB_PATH_WIN="$(cygpath -m "$DB_PATH")"

GO_BIN="$(command -v go || echo "/c/Program Files/Go/bin/go.exe")"
export PATH="$(dirname "$GO_BIN"):$PATH"
export CGO_ENABLED=0

step() { printf '\n[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# ---- 1. 编译（先于一切停服动作） ----
step "编译 backend/cmd/server"
mkdir -p "$DEPLOY"
if ! ( cd "$BACKEND" && GOPROXY=off go build -o "$DEPLOY/xgh_merged_new.exe" ./cmd/server ); then
  echo "编译失败：旧服务保持不动，退出。"
  exit 1
fi

# ---- 2. 运行目录六件套 ----
step "同步 static/（前端改动随本次启动生效）"
mkdir -p "$DEPLOY/static"
cp -r "$BACKEND/static/." "$DEPLOY/static/"

step "检查 uploads / rbac_model.conf / 两把密钥"
if [ ! -d "$DEPLOY/uploads" ] && [ -d "$BACKEND/uploads" ]; then
  cp -r "$BACKEND/uploads" "$DEPLOY/uploads"
fi
if [ ! -f "$DEPLOY/rbac_model.conf" ] && [ -f "$BACKEND/rbac_model.conf" ]; then
  cp "$BACKEND/rbac_model.conf" "$DEPLOY/rbac_model.conf"
fi
for key in jwt_secret.key crypto_secret.key; do
  if [ ! -f "$DEPLOY/$key" ] && [ -f "$BACKEND/$key" ]; then
    cp "$BACKEND/$key" "$DEPLOY/$key"
    echo "  已从 backend/ 拷贝 $key（仅首次，之后永不覆盖）"
  fi
done
if [ ! -d "$DEPLOY/publicity-cache" ] && [ -d "$BACKEND/publicity-cache" ]; then
  cmd //c "mklink /J \"$(cygpath -w "$DEPLOY")\\publicity-cache\" \"$(cygpath -w "$BACKEND")\\publicity-cache\"" \
    || cp -r "$BACKEND/publicity-cache" "$DEPLOY/publicity-cache"
fi

# ---- 3. 停旧、换 exe、启动 ----
OLD_PID="$( (netstat -ano | grep ":$PORT " | grep LISTENING | head -1 | awk '{print $NF}') 2>/dev/null | tr -d '\r' || true)"
if [ -n "$OLD_PID" ]; then
  step "停旧服务 (PID $OLD_PID，端口 $PORT)"
  taskkill //PID "$OLD_PID" //F >/dev/null 2>&1 || true
  # 端口没真正释放前新进程会绑定失败即退，必须轮询等它空出来
  for _ in $(seq 1 10); do
    sleep 1
    STILL="$( (netstat -ano | grep ":$PORT " | grep LISTENING | head -1) 2>/dev/null || true)"
    if [ -z "$STILL" ]; then break; fi
  done
  if [ -n "$STILL" ]; then
    echo "端口 $PORT 停旧后 10 秒仍未释放，放弃启动。"
    exit 1
  fi
else
  step "端口 $PORT 无运行中的服务，跳过停旧"
fi

step "换 exe（旧版留作 xgh_merged_prev.exe）"
if [ -f "$DEPLOY/xgh_merged.exe" ]; then
  mv -f "$DEPLOY/xgh_merged.exe" "$DEPLOY/xgh_merged_prev.exe"
fi
mv "$DEPLOY/xgh_merged_new.exe" "$DEPLOY/xgh_merged.exe"

step "启动 (PORT=$PORT)"
cd "$DEPLOY"
powershell -NoProfile -Command "\$env:PORT='$PORT'; \$env:DB_PATH='$DB_PATH_WIN'; \$p=Start-Process -FilePath '.\\xgh_merged.exe' -WorkingDirectory (Get-Location) -PassThru -WindowStyle Hidden; \$p.Id | Out-File -Encoding ascii service.pid"

# ---- 4. 健康检查 ----
step "健康检查（最多等 20 秒）"
OK=""
for _ in $(seq 1 20); do
  sleep 1
  CODE="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/" || true)"
  if [ "$CODE" = "200" ]; then OK=yes; break; fi
done

NEW_PID="$(cat service.pid | tr -d '\r\n ')"
if [ -n "$OK" ]; then
  step "完成：服务已就绪 http://127.0.0.1:$PORT/  (PID $NEW_PID)"
else
  echo "健康检查失败：20 秒内 GET / 未返回 200。"
  if tasklist //FI "PID eq $NEW_PID" 2>/dev/null | grep -qi xgh; then
    echo "进程 $NEW_PID 仍在运行但端口未就绪；稍等片刻后重试 curl。"
  else
    echo "进程 $NEW_PID 已退出：常见原因是端口被占或启动即报错，用前台方式跑一次看日志：PORT=$PORT DB_PATH=$DB_PATH ./xgh_merged.exe"
  fi
  exit 1
fi
