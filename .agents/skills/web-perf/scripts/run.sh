#!/usr/bin/env bash
# 面板网页打开性能：静态分析、本机 N 节点基准、线上实测。全部在工作目录（会话 scratchpad）里做，
# 不改仓库里的 panel/frontend（不装依赖、不改 package.json、不打补丁）。
#
# 用法：run.sh <子命令> [选项]
#   prep        [--repo R] [--work W]   拷一份前端到 W/fe、给假后端打性能补丁、npm ci；测量工具装到 W/tools
#   static      [--work W]              构建两个入口（另出一份带 sourcemap 的）→ 产物大小 / 路由分包 / 依赖构成
#   bench       [--work W] [--nodes 1000] [--scenarios "quiet:none:0 refetch2s:none:2000 storm:old:0"]
#               [--throttle "1 4 6"] [--observe 20]
#                                       vite preview + 假后端造 N 个节点，nodes-bench 跑「场景 × CPU 降速」矩阵
#                                       场景写成 名字:PERF_TRIGGER:PERF_NODES_EVENT_MS（含义见 patch-mock.py）
#   idle        [--work W] [--nodes 1000] [--route /nodes/nodes]
#                                       常驻动画吃多少主线程：同一页面动画开 / 关各量 10 秒
#   online      <站点根 URL> [--admin <后台前缀>]
#                                       curl 验线上：协议（h2）、gzip/br、缓存头、304、各资源耗时与传输量
#   lighthouse  <URL> [--app portal|admin] [--runs 5] [--presets "desktop mobile"] [--caches "cold warm"] [--work W]
#                                       Lighthouse 冷热缓存 × 桌面移动，每格跑 N 次取中位数；令牌从环境变量 TOKEN 读
# 工作目录默认 $TMPDIR/pandora-web-perf；结果都写在 W/results/ 下。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cmd="${1:-}"; [ -n "$cmd" ] && shift || { sed -n '2,22p' "$0"; exit 2; }
repo="$(git -C "$here" rev-parse --show-toplevel)"
work="${TMPDIR:-/tmp}/pandora-web-perf"
nodes=1000
scenarios="quiet:none:0 refetch2s:none:2000 storm:old:0"
throttle="1 4 6"
observe=20
route="/nodes/nodes"
admin_prefix=""
app="portal"
runs=5
presets="desktop mobile"
caches="cold warm"
target=""

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$(cd "$2" && pwd)"; shift 2 ;;
    --work) work="$2"; shift 2 ;;
    --nodes) nodes="$2"; shift 2 ;;
    --scenarios) scenarios="$2"; shift 2 ;;
    --throttle) throttle="$2"; shift 2 ;;
    --observe) observe="$2"; shift 2 ;;
    --route) route="$2"; shift 2 ;;
    --admin) admin_prefix="$2"; shift 2 ;;
    --app) app="$2"; shift 2 ;;
    --runs) runs="$2"; shift 2 ;;
    --presets) presets="$2"; shift 2 ;;
    --caches) caches="$2"; shift 2 ;;
    -*) echo "未知参数: $1" >&2; exit 2 ;;
    *) target="$1"; shift ;;
  esac
done
mkdir -p "$work"
work="$(cd "$work" && pwd)"
fe="$work/fe"; tools="$work/tools"; res="$work/results"
mkdir -p "$res"

need_prep() {
  [ -d "$fe/node_modules" ] && [ -d "$tools/node_modules" ] || { echo "先跑 run.sh prep --work $work" >&2; exit 1; }
}

# lock 文件没变就不重装（npm ci 每次都会删掉 node_modules 重来）
npm_ci_if_changed() {
  local dir="$1" stamp sum
  stamp="$dir/node_modules/.pandora-lock-sha"
  sum="$(shasum -a 256 "$dir/package-lock.json" | cut -d' ' -f1)"
  if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$sum" ]; then
    echo "  $dir：依赖没变，跳过 npm ci"
    return
  fi
  (cd "$dir" && npm ci --no-audit --no-fund --loglevel=error)
  echo "$sum" > "$stamp"
}

# 起 vite preview（admin 入口 + 假后端），等它能应答；PID 记进 previews
previews=()
cleanup() { for p in ${previews[@]+"${previews[@]}"}; do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT
start_preview() {
  local port="$1" trigger="$2" event_ms="$3" log="$res/preview-$1.log"
  (cd "$fe" && PERF_NODES="$nodes" PERF_TRIGGER="$trigger" PERF_NODES_EVENT_MS="$event_ms" \
    exec ./node_modules/.bin/vite preview --mode admin --port "$port" --strictPort) > "$log" 2>&1 &
  previews+=("$!")
  for _ in $(seq 1 60); do
    curl -fsS -o /dev/null "http://localhost:$port/" 2>/dev/null && return 0
    sleep 0.5
  done
  echo "vite preview 没起来（端口 $port），日志: $log" >&2; cat "$log" >&2; exit 1
}

case "$cmd" in
prep)
  [ -f "$repo/panel/frontend/package.json" ] || { echo "不是 pandora 仓库: $repo" >&2; exit 2; }
  echo "拷贝前端: $repo/panel/frontend → $fe"
  mkdir -p "$fe"
  rsync -a --delete --exclude node_modules --exclude 'dist*' "$repo/panel/frontend/" "$fe/"
  python3 "$here/scripts/patch-mock.py" "$fe"
  echo "前端依赖（不要和 go build / go test 同时跑）"
  npm_ci_if_changed "$fe"
  echo "测量工具 → $tools"
  mkdir -p "$tools"
  cp "$here/package.json" "$here/package-lock.json" "$here/scripts/"*.mjs "$tools/"
  npm_ci_if_changed "$tools"
  echo "完成。Chrome: ${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
  ;;

static)
  need_prep
  out="$res/static"; mkdir -p "$out"
  echo "构建（与产线一致的 npm run build，另出一份带 sourcemap 的给依赖分析）"
  (cd "$fe" && npm run build --silent) > "$out/build.log" 2>&1 || { tail -30 "$out/build.log"; exit 1; }
  for a in admin portal; do
    (cd "$fe" && ./node_modules/.bin/vite build --mode "$a" --sourcemap --outDir "$fe/dist-map/$a" --emptyOutDir) >> "$out/build.log" 2>&1 \
      || { tail -30 "$out/build.log"; exit 1; }
  done
  for a in admin portal; do
    echo; echo "=================== $a ==================="
    echo "--- 产物大小（KiB：原始 / gzip-9 / brotli-11）"
    node "$here/scripts/sizes.mjs" "$fe/dist/$a" | tee "$out/sizes-$a.txt"
    echo "--- 路由分包（含 mapDeps 带出的依赖，KiB：原始 / gzip / br）"
    node "$here/scripts/routes.mjs" "$fe/dist/$a" | tee "$out/routes-$a.txt"
    echo "--- 依赖构成（KiB，source-map-explorer）"
    "$tools/node_modules/.bin/source-map-explorer" "$fe/dist-map/$a/assets/"*.js --json --no-border-checks > "$out/sme-$a.json" 2> "$out/sme-$a.err" \
      || { echo "source-map-explorer 失败，见 $out/sme-$a.err"; continue; }
    node "$here/scripts/deps.mjs" "$out/sme-$a.json" | tee "$out/deps-$a.txt"
  done
  echo; echo "结果在 $out/"
  ;;

bench)
  need_prep
  [ -d "$fe/dist/admin" ] || { echo "先跑 run.sh static 构建产物" >&2; exit 1; }
  out="$res/bench"; mkdir -p "$out"
  port=4710
  files=()
  for sc in $scenarios; do
    IFS=: read -r name trigger event_ms <<<"$sc"
    port=$((port + 1))
    echo "场景 $name：PERF_TRIGGER=$trigger PERF_NODES_EVENT_MS=$event_ms，$nodes 个节点，端口 $port"
    start_preview "$port" "$trigger" "$event_ms"
    for t in $throttle; do
      f="$out/bench-$name-x$t.json"
      (cd "$tools" && node nodes-bench.mjs "$port" "$t" "$observe") > "$f" 2>&1 || echo "  x$t 失败，见 $f"
      files+=("$f")
    done
  done
  echo
  python3 "$here/scripts/summarize.py" bench "${files[@]}" | tee "$out/summary.md"
  ;;

idle)
  need_prep
  [ -d "$fe/dist/admin" ] || { echo "先跑 run.sh static 构建产物" >&2; exit 1; }
  start_preview 4720 none 0
  for k in 0 1; do (cd "$tools" && node idle-cpu.mjs 4720 "$k" "$route"); done | tee "$res/idle.jsonl"
  ;;

online)
  [ -n "$target" ] || { echo "用法: run.sh online <站点根 URL> [--admin <后台前缀>]" >&2; exit 2; }
  bash "$here/scripts/online.sh" "$target" "$admin_prefix" | tee "$res/online.md"
  ;;

lighthouse)
  [ -n "$target" ] || { echo "用法: run.sh lighthouse <URL> [--app portal|admin] [--runs 5]" >&2; exit 2; }
  [ -d "$tools/node_modules/lighthouse" ] || { echo "先跑 run.sh prep --work $work" >&2; exit 1; }
  key="pandora-portal-token"; [ "$app" = "admin" ] && key="pandora-admin-token"
  [ -n "${TOKEN:-}" ] || echo "提示：没有设 TOKEN，按未登录访问（登录页可以这样测）"
  out="$res/lighthouse"; mkdir -p "$out"
  jsonl="$out/lh-$(date +%Y%m%d-%H%M%S).jsonl"
  for p in $presets; do
    for c in $caches; do
      for i in $(seq 1 "$runs"); do
        if (cd "$tools" && TOKEN_KEY="$key" node lh.mjs "$target" "$p" "$c") >> "$jsonl" 2> "$out/last.err"; then
          echo "  $p $c #$i 完成"
        else
          echo "  $p $c #$i 失败：$(grep -m1 -E 'Error|error' "$out/last.err" || tail -1 "$out/last.err")"
        fi
      done
    done
  done
  echo
  python3 "$here/scripts/summarize.py" lh "$jsonl" | tee "${jsonl%.jsonl}.md"
  ;;

*)
  sed -n '2,22p' "$0"; exit 2 ;;
esac
