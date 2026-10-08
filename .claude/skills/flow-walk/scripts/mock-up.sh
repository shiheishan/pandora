#!/usr/bin/env bash
# 本机起门户与后台的假后端开发服务器，给 flow-walk 点流程用。
# 用法：
#   mock-up.sh up [前端目录]    起 portal(5181) 与 admin(5182)，就绪后打印地址
#   mock-up.sh down             只停自己起的两个进程
#   mock-up.sh status
# 端口固定，不碰 8766（别的任务的原型服务器）；已有进程占着就报错退出，不抢。
set -euo pipefail

PORTAL_PORT=5181
ADMIN_PORT=5182
STATE="${TMPDIR:-/tmp}/pandora-flow-walk"
mkdir -p "$STATE"

listening() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

start() {
  local mode="$1" port="$2" dir="$3"
  if listening "$port"; then
    echo "端口 $port 已被占用，先查是谁（lsof -iTCP:$port），不要直接杀" >&2
    exit 1
  fi
  # strictPort：端口被抢就失败，不让 vite 悄悄换端口
  (cd "$dir" && nohup npm run "dev:$mode" -- --host 127.0.0.1 --port "$port" --strictPort \
    >"$STATE/$mode.log" 2>&1 & echo $! >"$STATE/$mode.pid")
}

case "${1:-}" in
  up)
    dir="${2:-$(git rev-parse --show-toplevel)/panel/frontend}"
    [[ -d "$dir/node_modules" ]] || { echo "$dir 没有 node_modules，先在那里 npm ci（别和 go 命令并发）" >&2; exit 1; }
    start portal "$PORTAL_PORT" "$dir"
    start admin "$ADMIN_PORT" "$dir"
    for _ in $(seq 1 30); do
      if listening "$PORTAL_PORT" && listening "$ADMIN_PORT"; then
        echo "门户 http://127.0.0.1:$PORTAL_PORT/   user@pandora.dev"
        echo "后台 http://127.0.0.1:$ADMIN_PORT/   admin@pandora.dev（只读：viewer@pandora.dev）"
        echo "口令见 dev/mock-api.ts 的 MOCK_ACCOUNTS；日志在 $STATE/*.log"
        exit 0
      fi
      sleep 1
    done
    echo "30 秒内没起来，看 $STATE/*.log" >&2
    exit 1
    ;;
  down)
    for mode in portal admin; do
      if [[ -f "$STATE/$mode.pid" ]]; then
        # npm 是父进程，vite 是子进程：杀整棵
        pkill -P "$(cat "$STATE/$mode.pid")" 2>/dev/null || true
        kill "$(cat "$STATE/$mode.pid")" 2>/dev/null || true
        rm -f "$STATE/$mode.pid"
      fi
    done
    echo "已停"
    ;;
  status)
    for p in "$PORTAL_PORT" "$ADMIN_PORT"; do
      if listening "$p"; then echo "$p 在监听"; else echo "$p 空闲"; fi
    done
    ;;
  *)
    echo "用法: $0 up [前端目录] | down | status" >&2
    exit 2
    ;;
esac
