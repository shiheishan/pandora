#!/usr/bin/env bash
# 订阅渲染端到端矩阵，一条命令跑完：
#   1. go test -overlay 往 subscription 包注入临时测试，把表单形状夹具（+额外夹具）渲染成
#      clash / clash-premium / singbox / uri；再往 nodefabric 包注入一个，用真实 BuildNodeConfig 生成下发配置
#   2. sing-box 离线校验（box.New + Start）
#   3. Clash YAML、URI 静态检查
#   4. 起本地 pdnd NativeCore，用订阅里的 sing-box 出站真连一次
#   5. 拼矩阵（scripts/matrix.py），有 FAIL 退出码为 1
#
# 用法：run.sh [选项]
#   --repo <目录>            被测的仓库根或 worktree（默认：本 skill 所在仓库）
#   --work <目录>            工作目录，放渲染输出、编译产物和结果（默认 $TMPDIR/pandora-subscription-e2e，可复用编译缓存）
#   --extra <夹具.json|none> 额外夹具，格式 [{id,type,port,config}]（默认 fixtures/extra.json）
#   --fixtures-file <文件>   用这个文件替换（或补进）包里的 render_fixtures_test.go，比修前修后时用
#   --formats <列表>         渲染哪些格式，默认 clash,clash-premium,singbox,uri；修前基点没有 premium 时去掉它
#   --only <id,id>           E2E 只跑这些夹具（渲染与静态检查照常全跑）
#   --no-e2e                 跳过第 4 步
#   --no-naive               构建时不带 with_naive_outbound（naive 行会变 FAIL，仅在 cronet 链接不了时用）
#   --log <级别>             E2E 里 sing-box 客户端的日志级别，排查时用 debug
#
# 全程只用本机 Go 模块缓存：GOPROXY=off、GOTOOLCHAIN=local，不联网下载。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$(git -C "$here" rev-parse --show-toplevel)"
work="${TMPDIR:-/tmp}/pandora-subscription-e2e"
extra="$here/fixtures/extra.json"
fixtures_file=""
only=""
formats="clash,clash-premium,singbox,uri"
do_e2e=1
tags="with_quic,with_utls,with_gvisor,with_naive_outbound"
loglevel="panic"

while [ $# -gt 0 ]; do
  case "$1" in
    --repo) repo="$(cd "$2" && pwd)"; shift 2 ;;
    --work) work="$2"; shift 2 ;;
    --extra) extra="$2"; shift 2 ;;
    --fixtures-file) fixtures_file="$(cd "$(dirname "$2")" && pwd)/$(basename "$2")"; shift 2 ;;
    --only) only="$2"; shift 2 ;;
    --formats) formats="$2"; shift 2 ;;
    --no-e2e) do_e2e=0; shift ;;
    --no-naive) tags="with_quic,with_utls,with_gvisor"; shift ;;
    --log) loglevel="$2"; shift 2 ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done
[ "$extra" = "none" ] && extra=""
if [ -n "$extra" ]; then extra="$(cd "$(dirname "$extra")" && pwd)/$(basename "$extra")"; fi
[ -d "$repo/panel/internal/domain/subscription" ] && [ -d "$repo/pdnd/kernel" ] || { echo "不是 pandora 仓库: $repo" >&2; exit 2; }

export GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local
mkdir -p "$work"
work="$(cd "$work" && pwd)"
out="$work/out"; res="$work/results"
rm -rf "$out" "$res" "$work/e2e"
mkdir -p "$out" "$res" "$work/bin"
echo "仓库: $repo"
echo "工作目录: $work"

# ---- 1. 渲染与下发配置（overlay 测试，仓库里不落任何文件） ----
sub="$repo/panel/internal/domain/subscription"
nf="$repo/panel/internal/domain/nodefabric"
python3 - "$work/overlay.json" "$sub" "$nf" "$here/overlay" "$fixtures_file" <<'PY'
import json, sys
path, sub, nf, ov, fx = sys.argv[1:]
rep = {
    f"{sub}/zz_e2e_dump_test.go": f"{ov}/zz_e2e_dump_test.go",
    f"{nf}/zz_e2e_nodeconfig_test.go": f"{ov}/zz_e2e_nodeconfig_test.go",
}
if fx:
    rep[f"{sub}/render_fixtures_test.go"] = fx
json.dump({"Replace": rep}, open(path, "w"), indent=1)
PY
echo "[1/5] 渲染四种格式（overlay: $work/overlay.json）"
if ! (cd "$repo/panel" && E2E_OUT="$out" E2E_EXTRA="$extra" E2E_FORMATS="$formats" go test -overlay "$work/overlay.json" -count=1 -v -run '^TestE2EDump$' ./internal/domain/subscription/) > "$res/render.log" 2>&1; then
  tail -30 "$res/render.log"; echo "渲染失败，日志: $res/render.log" >&2; exit 1
fi
grep -q -- '--- PASS: TestE2EDump' "$res/render.log" || { tail -30 "$res/render.log"; echo "TestE2EDump 没有跑（被跳过或没匹配上）" >&2; exit 1; }
if ! (cd "$repo/panel" && E2E_OUT="$out" go test -overlay "$work/overlay.json" -count=1 -v -run '^TestE2ENodeConfig$' ./internal/domain/nodefabric/) > "$res/nodeconfig.log" 2>&1; then
  tail -30 "$res/nodeconfig.log"; echo "生成下发配置失败，日志: $res/nodeconfig.log" >&2; exit 1
fi
grep -E 'zz_e2e_.*(渲染了|下发配置)' "$res/render.log" "$res/nodeconfig.log" | sed 's/^.*zz_e2e_/  zz_e2e_/' || true

# ---- 2. 编译检查程序（工作副本里把 pdnd 指向被测仓库） ----
echo "[2/5] 编译 sbcheck / yamlcheck / uricheck / e2e（tags: $tags）"
rm -rf "$work/tools"; mkdir -p "$work/tools"
cp -R "$here/tools/." "$work/tools/"
(
  cd "$work/tools"
  go mod edit -replace "github.com/aegispanel/nodeagent=$repo/pdnd"
  # -mod=mod：pdnd 依赖变了时从本机缓存离线补齐 go.mod，缓存里没有就直接报错
  GOFLAGS=-mod=mod go build -tags "$tags" -o "$work/bin/" ./e2e ./sbcheck ./yamlcheck ./uricheck ./kp
) > "$res/build.log" 2>&1 || { grep -v 'duplicate libraries' "$res/build.log" | tail -30; echo "编译失败，日志: $res/build.log" >&2; exit 1; }
if ! diff -q <(grep -v '^replace ' "$here/tools/go.mod") <(grep -v '^replace ' "$work/tools/go.mod") >/dev/null; then
  echo "  提示：pdnd 的依赖与 skill 里的 tools/go.mod 不一致，已在工作副本里离线补齐。"
  echo "        要更新 skill，就把 $work/tools/go.mod 与 go.sum 拷回去，replace 行改回 ../../../../pdnd。"
fi

# ---- 3. sing-box 离线校验、Clash 与 URI 静态检查 ----
echo "[3/5] sing-box 校验、Clash / URI 静态检查"
# 没渲染的格式没有文件，nullglob 让通配展开成空
(cd "$out" && shopt -s nullglob && f=(*.singbox) && [ ${#f[@]} -eq 0 ] || "$work/bin/sbcheck" -start "${f[@]}") > "$res/singbox.txt" 2>&1 || true
(cd "$out" && shopt -s nullglob && f=(*.clash *.clash-premium) && [ ${#f[@]} -eq 0 ] || "$work/bin/yamlcheck" "${f[@]}") > "$res/clash.txt" 2>&1 || true
(cd "$out" && shopt -s nullglob && f=(*.uri) && [ ${#f[@]} -eq 0 ] || "$work/bin/uricheck" "${f[@]}") > "$res/uri.txt" 2>&1 || true

# ---- 4. 端到端 ----
if [ "$do_e2e" = 1 ]; then
  echo "[4/5] E2E：本地 pdnd NativeCore × sing-box 客户端"
  e2e_args=(-out "$out" -work "$work" -log "$loglevel")
  [ -n "$only" ] && e2e_args+=(-only "$only")
  "$work/bin/e2e" "${e2e_args[@]}" > "$res/e2e.txt" 2> "$res/e2e.stderr" || true
else
  echo "[4/5] 跳过 E2E"
fi

# ---- 5. 矩阵 ----
echo "[5/5] 矩阵（同时写入 $res/matrix.md）"
echo
set +e
python3 "$here/scripts/matrix.py" "$out" "$res" | tee "$res/matrix.md"
rc=${PIPESTATUS[0]}
set -e
echo
echo "原始结果: $res/{render.log,nodeconfig.log,singbox.txt,clash.txt,uri.txt,e2e.txt}；每个用例的客户端配置: $work/e2e/<id>.client.json"
exit "$rc"
