#!/usr/bin/env bash
# 首次点击测试用：起多个门户假后端实例，一个测试员一个端口。
# 假后端的场景状态是整个 vite 进程共用的，两个测试员连同一个端口会互相改数据。
# 用法：
#   mock-multi.sh up <前端目录> <个数>   从 5191 起依次开门户实例（最多 8 个），就绪后打印地址
#   mock-multi.sh down                   只停自己起的实例
#   mock-multi.sh status
# 端口写死并加 --strictPort，被占就报错、不换端口、不抢。不碰 5181/5182（flow-walk）与 8766（原型）。
# 起完要在内置浏览器里逐个端口登录一次（账号见 dev/mock-api.ts 的 MOCK_ACCOUNTS）。
set -euo pipefail

BASE_PORT=5191
MAX=8
STATE="${TMPDIR:-/tmp}/pandora-ux-review"
mkdir -p "$STATE"

listening() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

case "${1:-}" in
  up)
    dir="${2:?用法: $0 up <前端目录> <个数>}"
    n="${3:?用法: $0 up <前端目录> <个数>}"
    [[ "$n" =~ ^[1-9]$ ]] && (( n <= MAX )) || { echo "个数要在 1–$MAX 之间" >&2; exit 2; }
    [[ -d "$dir/node_modules" ]] || { echo "$dir 没有 node_modules，先在那里 npm ci（别和 go 命令并发）" >&2; exit 1; }
    # 先全部检查再起，免得起到一半才发现端口被占
    for ((i = 0; i < n; i++)); do
      p=$((BASE_PORT + i))
      if listening "$p"; then
        echo "端口 $p 已被占用，先查是谁（lsof -iTCP:$p），不要直接杀" >&2
        exit 1
      fi
    done
    for ((i = 0; i < n; i++)); do
      p=$((BASE_PORT + i))
      (cd "$dir" && nohup npm run dev:portal -- --host 127.0.0.1 --port "$p" --strictPort \
        >"$STATE/portal-$p.log" 2>&1 & echo $! >"$STATE/portal-$p.pid")
    done
    for _ in $(seq 1 40); do
      ready=0
      for ((i = 0; i < n; i++)); do listening $((BASE_PORT + i)) && ready=$((ready + 1)); done
      if (( ready == n )); then
        for ((i = 0; i < n; i++)); do
          p=$((BASE_PORT + i))
          echo "测试员 $((i + 1))  http://127.0.0.1:$p/   原型场景入口 http://127.0.0.1:$p/v1/__mock/proto?s=<proto-场景>"
        done
        echo "前端目录 $dir（$(git -C "$dir" rev-parse --short HEAD 2>/dev/null || echo 非 git)）；日志在 $STATE/*.log"
        echo "下一步：内置浏览器里逐个端口用 MOCK_ACCOUNTS.portal 登录一次；测注册登录的端口不登录"
        exit 0
      fi
      sleep 1
    done
    echo "40 秒内没全部起来，看 $STATE/*.log；已起的用 $0 down 停" >&2
    exit 1
    ;;
  down)
    shopt -s nullglob
    for f in "$STATE"/portal-*.pid; do
      pid="$(cat "$f")"
      # npm 是父进程，vite 是子进程：杀整棵
      pkill -P "$pid" 2>/dev/null || true
      kill "$pid" 2>/dev/null || true
      rm -f "$f"
    done
    echo "已停"
    ;;
  status)
    for ((i = 0; i < MAX; i++)); do
      p=$((BASE_PORT + i))
      if listening "$p"; then
        owner="别人的进程"
        [[ -f "$STATE/portal-$p.pid" ]] && owner="本脚本起的"
        echo "$p 在监听（$owner）"
      fi
    done
    ;;
  *)
    echo "用法: $0 up <前端目录> <个数> | down | status" >&2
    exit 2
    ;;
esac
