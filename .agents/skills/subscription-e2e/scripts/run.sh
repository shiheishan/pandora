#!/usr/bin/env bash
# 订阅渲染端到端矩阵，一条命令跑完：
#   1. go test -overlay 往面板三个包注入临时测试（仓库里不落文件）：
#      node/protocol：表单形状夹具（+额外夹具）过后台校验、经 KernelConfig 翻成内核字段名，写 fixtures_index.json；
#      subscription/render：渲染成 clash / clash-premium / singbox / uri；
#      node/service：用真实的 buildNodeConfigDoc 生成下发给节点的配置文档
#   2. sing-box 离线校验（box.New + Start）
#   3. Clash YAML、URI 静态检查
#   4. 起本地 pdnd NativeCore，用订阅里的 sing-box 出站真连一次；设了 MIHOMO_BIN 时再用官方 mihomo 按 Clash 订阅真连一次
#   5. 拼矩阵（scripts/matrix.py），有 FAIL 退出码为 1
#
# 用法：run.sh [选项]
#   --repo <目录>            被测的仓库根或 worktree（默认：本 skill 所在仓库）
#   --work <目录>            工作目录，放渲染输出、编译产物和结果（默认 $TMPDIR/pandora-subscription-e2e，可复用编译缓存）
#   --extra <夹具.json|none> 额外夹具，格式 [{id,type,port,config}]（默认 fixtures/extra.json）
#   --fixtures-file <文件>   用这个文件替换 node/protocol 包里的 kernel_config_parity_test.go（表单夹具所在），比修前修后时用
#   --formats <列表>         渲染哪些格式，默认 clash,clash-premium,singbox,uri
#   --ua <User-Agent>        渲染 sing-box 时按这个 UA 挑版本写法（缺省空：兼容写法）
#   --only <id,id>           E2E 只跑这些夹具（渲染与静态检查照常全跑）
#   --no-e2e                 跳过第 4 步
#   --no-naive               构建时不带 with_naive_outbound（cronet 链接不了时用，如 CI；矩阵把 naive 行的 sing-box 两列记为 -）
#   --online                 联网取 Go 模块、用当前 PATH 里的 Go（GOTOOLCHAIN=local），只给 CI 用
#   --log <级别>             E2E 里 sing-box 客户端的日志级别，排查时用 debug
#
# 环境变量：
#   MIHOMO_BIN               官方 mihomo 可执行文件，不给就不跑 mihomo E2E
#   MIHOMO_SHA256            可执行文件本身的 sha256，给了 MIHOMO_BIN 就必须给
#
# 默认只用本机 Go 模块缓存，不联网下载（--online 例外）；Go 版本取 go.mod 的（scripts/toolchain.sh）。
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo="$(git -C "$here" rev-parse --show-toplevel)"
work="${TMPDIR:-/tmp}/pandora-subscription-e2e"
extra="$here/fixtures/extra.json"
fixtures_file=""
only=""
formats="clash,clash-premium,singbox,uri"
ua=""
do_e2e=1
no_naive=0
online=0
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
    --ua) ua="$2"; shift 2 ;;
    --no-e2e) do_e2e=0; shift ;;
    --no-naive) no_naive=1; tags="with_quic,with_utls,with_gvisor"; shift ;;
    --online) online=1; shift ;;
    --log) loglevel="$2"; shift 2 ;;
    -h|--help) sed -n '2,29p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done
[ "$extra" = "none" ] && extra=""
if [ -n "$extra" ]; then extra="$(cd "$(dirname "$extra")" && pwd)/$(basename "$extra")"; fi

# mihomo E2E：给了 MIHOMO_BIN 才跑；必须同时给可执行文件本身的 SHA-256，免得拿来历不明的二进制当证据
mihomo_bin="${MIHOMO_BIN:-}"
if [ -n "$mihomo_bin" ]; then
  [ -x "$mihomo_bin" ] || { echo "MIHOMO_BIN 不是可执行文件: $mihomo_bin" >&2; exit 2; }
  [ -n "${MIHOMO_SHA256:-}" ] || { echo "设了 MIHOMO_BIN 就必须设 MIHOMO_SHA256（可执行文件本身的 sha256）" >&2; exit 2; }
fi

[ -d "$repo/panel/internal/module/subscription/render" ] && [ -d "$repo/panel/internal/module/node/protocol" ] && [ -d "$repo/pdnd/kernel" ] || { echo "不是 pandora 仓库: $repo" >&2; exit 2; }

source "$here/scripts/toolchain.sh"
pin_go_toolchain "$repo" "$online"
mkdir -p "$work"
work="$(cd "$work" && pwd)"
out="$work/out"; res="$work/results"
rm -rf "$out" "$res" "$work/e2e"
mkdir -p "$out" "$res" "$work/bin"
echo "仓库: $repo"
echo "工作目录: $work"

# ---- 1. 夹具、渲染与下发配置（overlay 测试，仓库里不落任何文件） ----
proto="$repo/panel/internal/module/node/protocol"
rend="$repo/panel/internal/module/subscription/render"
nsvc="$repo/panel/internal/module/node/service"
python3 - "$work/overlay.json" "$proto" "$rend" "$nsvc" "$here/overlay" "$fixtures_file" <<'PY'
import json, sys
path, proto, rend, nsvc, ov, fx = sys.argv[1:]
rep = {
    f"{proto}/zz_e2e_fixtures_test.go": f"{ov}/zz_e2e_fixtures_test.go",
    f"{rend}/zz_e2e_dump_test.go": f"{ov}/zz_e2e_dump_test.go",
    f"{nsvc}/zz_e2e_nodeconfig_test.go": f"{ov}/zz_e2e_nodeconfig_test.go",
}
if fx:
    rep[f"{proto}/kernel_config_parity_test.go"] = fx
json.dump({"Replace": rep}, open(path, "w"), indent=1)
PY

# overlay_test <测试名> <包路径（相对 panel）> <日志名> <失败说明>：跑一个注入的测试，失败或没跑到就退出
overlay_test() {
  local name="$1" pkg="$2" log="$res/$3.log" what="$4" rc=0
  (cd "$repo/panel" && E2E_OUT="$out" E2E_EXTRA="$extra" E2E_FORMATS="$formats" E2E_UA="$ua" \
    go test -overlay "$work/overlay.json" -count=1 -v -run "^${name}\$" "$pkg") > "$log" 2>&1 || rc=$?
  if [ "$rc" != 0 ]; then tail -30 "$log"; echo "${what}失败（退出 $rc），日志: $log" >&2; exit 1; fi
  grep -q -- "--- PASS: ${name}" "$log" || { tail -30 "$log"; echo "${name} 没有跑（被跳过或没匹配上）" >&2; exit 1; }
}
echo "[1/5] 夹具翻译、渲染四种格式、下发配置（overlay: $work/overlay.json）"
overlay_test TestE2EFixtures ./internal/module/node/protocol/ fixtures "夹具翻译"
overlay_test TestE2EDump ./internal/module/subscription/render/ render "渲染"
overlay_test TestE2ENodeConfig ./internal/module/node/service/ nodeconfig "生成下发配置"
grep -hE 'zz_e2e_.*(夹具|渲染了|下发配置)' "$res/fixtures.log" "$res/render.log" "$res/nodeconfig.log" | sed 's/^.*zz_e2e_/  zz_e2e_/' || true

# ---- 2. 编译检查程序（工作副本里把 pdnd 指向被测仓库） ----
echo "[2/5] 编译 sbcheck / yamlcheck / uricheck / e2e（tags: $tags）"
rm -rf "$work/tools"; mkdir -p "$work/tools"
cp -R "$here/tools/." "$work/tools/"
# pdnd 起默认拒绝回环与内网目标（w4kernel，outbound.SetBlockPrivateDestinations）；E2E 的目标站点就在
# 127.0.0.1，被测仓库有这道闸时在工作副本里放开。老基点没有这个函数，不写这个文件
if [ -f "$repo/pdnd/outbound/private_guard.go" ]; then
  cat > "$work/tools/e2e/zz_allow_private.go" <<'GO'
package main

import "github.com/aegispanel/nodeagent/outbound"

// E2E 的目标站点在 127.0.0.1：放开 pdnd 的私网目标拦截（只在这个测试进程里）
func init() { outbound.SetBlockPrivateDestinations(false) }
GO
fi
(
  cd "$work/tools"
  go mod edit -replace "github.com/aegispanel/nodeagent=$repo/pdnd"
  # pdnd 经 replace 引用仓库里的 nodeapi；replace 不随依赖传递，这里同样指到被测仓库
  [ -d "$repo/nodeapi" ] && go mod edit -replace "github.com/aegispanel/nodeapi=$repo/nodeapi"
  # -mod=mod：pdnd 依赖变了时从本机缓存离线补齐 go.mod，缓存里没有就直接报错（--online 时联网补齐）
  GOFLAGS=-mod=mod go build -tags "$tags" -o "$work/bin/" ./e2e ./sbcheck ./yamlcheck ./uricheck ./kp
) > "$res/build.log" 2>&1 || { grep -v 'duplicate libraries' "$res/build.log" | tail -30; echo "编译失败，日志: $res/build.log" >&2; exit 1; }
if ! diff -q <(grep -v '^replace ' "$here/tools/go.mod") <(grep -v '^replace ' "$work/tools/go.mod") >/dev/null; then
  echo "  提示：pdnd 的依赖与 skill 里的 tools/go.mod 不一致，已在工作副本里离线补齐。"
  echo "        要更新 skill，就把 $work/tools/go.mod 与 go.sum 拷回去，两条 replace 行改回 ../../../../pdnd 与 ../../../../nodeapi。"
fi

# ---- 3. sing-box 离线校验、Clash 与 URI 静态检查 ----
echo "[3/5] sing-box 校验、Clash / URI 静态检查"
# 没渲染的格式没有文件，nullglob 让通配展开成空
(cd "$out" && shopt -s nullglob && f=(*.singbox) && [ ${#f[@]} -eq 0 ] || "$work/bin/sbcheck" -start "${f[@]}") > "$res/singbox.txt" 2>&1 || true
(cd "$out" && shopt -s nullglob && f=(*.clash *.clash-premium) && [ ${#f[@]} -eq 0 ] || "$work/bin/yamlcheck" "${f[@]}") > "$res/clash.txt" 2>&1 || true
(cd "$out" && shopt -s nullglob && f=(*.uri) && [ ${#f[@]} -eq 0 ] || "$work/bin/uricheck" "${f[@]}") > "$res/uri.txt" 2>&1 || true

# ---- 4. 端到端 ----
if [ "$do_e2e" = 1 ]; then
  echo "[4/5] E2E：本地 pdnd NativeCore × sing-box 客户端$([ -n "$mihomo_bin" ] && echo ' / mihomo 客户端')"
  e2e_args=(-out "$out" -work "$work" -log "$loglevel")
  [ -n "$only" ] && e2e_args+=(-only "$only")
  "$work/bin/e2e" "${e2e_args[@]}" > "$res/e2e.txt" 2> "$res/e2e.stderr" || true
  if [ -n "$mihomo_bin" ]; then
    "$work/bin/e2e" -client mihomo "${e2e_args[@]}" > "$res/e2e-mihomo.txt" 2> "$res/e2e-mihomo.stderr" || true
  else
    echo "  没设 MIHOMO_BIN：跳过 mihomo E2E（矩阵里这一列显示 -）"
  fi
else
  echo "[4/5] 跳过 E2E"
fi

# ---- 5. 矩阵 ----
echo "[5/5] 矩阵（同时写入 $res/matrix.md）"
echo
set +e
matrix_args=()
[ "$no_naive" = 1 ] && matrix_args+=(--no-naive)
python3 "$here/scripts/matrix.py" ${matrix_args[@]+"${matrix_args[@]}"} "$out" "$res" | tee "$res/matrix.md"
rc=${PIPESTATUS[0]}
set -e
echo
echo "原始结果: $res/{fixtures.log,render.log,nodeconfig.log,singbox.txt,clash.txt,uri.txt,e2e.txt,e2e-mihomo.txt}；每个用例的客户端配置: $work/e2e/<id>.client.json（sing-box）、<id>.mihomo.yaml 与 <id>.mihomo.log（mihomo）"
exit "$rc"
