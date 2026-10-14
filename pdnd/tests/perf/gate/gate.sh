#!/usr/bin/env bash
# 节点端性能闸门的本机入口：交叉编译 pdnd 与工具、传到测试机、后台跑一次完整测量、取回、对比。
#
# 用法：
#   gate.sh check                         检查测试机（单机：NODEGATE_HOST；多机：NODEGATE_NODE/LOAD1/LOAD2）
#   gate.sh run <label> <提交>            本机编译这个提交的 pdnd 与当前工作区的工具 → 传上测试机 → 跑一次（约 30 分钟）→ 取回
#                                         NODEGATE_PARAMS="键=值;键=值" 覆盖场景参数（见 README）
#   gate.sh wait <label> | fetch <label>  （单机模式）等后台运行结束 / 取回结果
#   gate.sh compare <基线> <候选> --aa <基线第二遍> [--target <指标前缀>]…
#
# 测试机用 ssh 别名：环境变量或主仓库 ops-local/nodegate/gate.env（仓库里不写机器）。
# 结果目录缺省是主仓库的 ops-local/nodegate/（被 git 忽略）。
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
top=$(git -C "$here" rev-parse --show-toplevel)
main=$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")
env_file=${NODEGATE_ENV:-$main/ops-local/nodegate/gate.env}
OUT=${NODEGATE_OUT:-$main/ops-local/nodegate}
R=/root/nodegate
TOOLS_DIR=$top/pdnd/tests/perf/gate/tools

die() { echo "gate.sh: $*" >&2; exit 1; }
mask() { sed -E 's#/ops_[0-9a-f]+#/ops_<隐去>#g'; }

go_directive() {
  local mod=$1
  tr -d '\r' < "$mod" | awk '/^go[[:space:]]+/ { print $2; exit }'
}

cfg() {
  local k=$1 v
  eval "v=\${$k:-}"
  if [ -n "$v" ]; then
    echo "$v"
    return 0
  fi
  if [ -f "$env_file" ]; then
    awk -F= -v key="$k" '$1 == key { sub(/^[^=]*=/, ""); val = $0 } END { print val }' "$env_file"
  fi
}

multi_mode() { [ -n "$(cfg NODEGATE_NODE)" ]; }

single_host() {
  local h
  h=$(cfg NODEGATE_HOST)
  [ -n "$h" ] || die "单机模式需要 NODEGATE_HOST（或 $env_file）"
  echo "$h"
}

ssh_host() {
  ssh -o BatchMode=yes "$1" "$2"
}

push_scripts() {
  local h=$1
  ssh_host "$h" "mkdir -p $R/bin $R/tools $R/runs $R/state"
  scp -q "$here/remote.sh" "$here/collect.py" "$here/report.py" "$here/orchestrate.py" "$h:$R/tools/"
  ssh_host "$h" "chmod +x $R/tools/remote.sh"
}

push_pdnd() {
  local h=$1 bin=$2 sha
  sha=$(basename "$bin")
  sha=${sha#pandora-native-}
  if ssh_host "$h" "test -x $R/bin/pandora-native-$sha"; then
    return 0
  fi
  scp -q "$bin" "$h:$R/bin/pandora-native-$sha"
  ssh_host "$h" "chmod +x $R/bin/pandora-native-$sha"
}

push_tools() {
  local h=$1 tid=$2 dir=$3
  if ssh_host "$h" "test -x $R/bin/tools-$tid/loadgen"; then
    return 0
  fi
  scp -q -r "$dir" "$h:$R/bin/tools-$tid"
  ssh_host "$h" "chmod +x $R/bin/tools-$tid/scalepanel $R/bin/tools-$tid/loadgen $R/bin/tools-$tid/target"
}

push_all() {
  local h=$1 sha=$2 tid=$3
  push_scripts "$h"
  push_pdnd "$h" "$OUT/bin/pandora-native-$sha"
  push_tools "$h" "$tid" "$OUT/bin/tools-$tid"
}

sha12() {
  git -C "$top" rev-parse --verify "$1^{commit}" | cut -c1-12
}

tools_id() {
  local id dirty
  id=$(git -C "$top" rev-parse HEAD:pdnd/tests/perf/gate/tools | cut -c1-12)
  dirty=$(git -C "$top" status --porcelain -- pdnd/tests/perf/gate/tools || true)
  if [ -z "${NODEGATE_ALLOW_DIRTY:-}" ] && [ -n "$dirty" ]; then
    die "pdnd/tests/perf/gate/tools 有未提交改动；请提交或设 NODEGATE_ALLOW_DIRTY=1"
  fi
  if [ -n "${NODEGATE_ALLOW_DIRTY:-}" ] && [ -n "$dirty" ]; then
    id="dirty-$(git -C "$top" diff HEAD -- pdnd/tests/perf/gate/tools | shasum | awk '{print $1}' | cut -c1-8)"
  fi
  echo "$id"
}

build_pdnd() {
  local ref=${1:?} sha tmp go_tc out
  sha=$(sha12 "$ref")
  out="$OUT/bin/pandora-native-$sha"
  if [ -x "$out" ]; then
    echo "build: 本机已有 pandora-native-$sha"
    return 0
  fi
  mkdir -p "$OUT/bin"
  tmp=$(mktemp -d)
  git -C "$top" archive "$sha" pdnd nodeapi | tar -C "$tmp" -xf -
  go_tc=$(go_directive "$tmp/pdnd/go.mod")
  (
    cd "$tmp/pdnd"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS= \
      GOTOOLCHAIN="go${go_tc}" \
      go build -mod=readonly -trimpath \
      -ldflags "-s -w -X main.buildVersion=gate-$sha" \
      -o "$out" .
  )
  rm -rf "$tmp"
  echo "build: $out"
}

build_tools() {
  local tid=$1 go_tc dir
  tid=${1:?}
  dir="$OUT/bin/tools-$tid"
  if [ -x "$dir/loadgen" ] && [ -x "$dir/scalepanel" ] && [ -x "$dir/target" ]; then
    echo "build: 本机已有 tools-$tid"
    return 0
  fi
  mkdir -p "$dir"
  go_tc=$(go_directive "$TOOLS_DIR/go.mod")
  (
    cd "$TOOLS_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
      GOTOOLCHAIN="go${go_tc}" \
      go build -mod=readonly -trimpath -ldflags "-s -w" \
      -o "$dir/" ./cmd/scalepanel ./cmd/loadgen ./cmd/target
  )
  echo "build: $dir"
}

parse_params() {
  local raw=${NODEGATE_PARAMS:-} part
  # 正则放变量里：[[ =~ ]] 右边不加引号时，方括号里的空格会把表达式截断
  local re='^[a-z0-9_]+=[A-Za-z0-9_., -]*$'
  PARAM_ARGS=()
  [ -z "$raw" ] && return 0
  set -f
  IFS=';'
  for part in $raw; do
    [ -z "$part" ] && continue
    [[ $part =~ $re ]] || die "NODEGATE_PARAMS 段非法: $part"
    PARAM_ARGS+=("--param" "$part")
  done
  unset IFS
  set +f
}

state_single() {
  local label=$1 h
  h=$(single_host)
  ssh_host "$h" "if [ -e $R/runs/$label/done ]; then echo done; elif [ -e $R/runs/$label/failed ]; then echo failed; else echo running; fi"
}

wait_single() {
  local label=${1:?} st last="" line
  while :; do
    st=$(state_single "$label")
    line=$(ssh_host "$(single_host)" "tail -n 1 $R/runs/$label.log 2>/dev/null" | mask || true)
    [ "$line" != "$last" ] && [ -n "$line" ] && echo "$line" && last=$line
    case $st in
      done) return 0 ;;
      failed)
        ssh_host "$(single_host)" "tail -n 20 $R/runs/$label.log" | mask >&2
        die "$label 失败"
        ;;
    esac
    sleep 30
  done
}

fetch_single() {
  local label=${1:?} h
  h=$(single_host)
  mkdir -p "$OUT"
  # pdnd/ 是机上身份与缓存，profile/ 与 token 是运行时现生成的密钥和令牌，都不取回
  ssh_host "$h" "tar -C $R/runs -czf - --exclude=$label/pdnd --exclude=$label/profile --exclude=$label/token $label $label.log" | tar -C "$OUT" -xzf -
  echo "fetch: $OUT/$label"
}

cmd_check() {
  if multi_mode; then
    local n l1 l2
    n=$(cfg NODEGATE_NODE)
    l1=$(cfg NODEGATE_LOAD1)
    l2=$(cfg NODEGATE_LOAD2)
    [ -n "$n" ] && [ -n "$l1" ] && [ -n "$l2" ] || die "多机需要 NODEGATE_NODE、NODEGATE_LOAD1、NODEGATE_LOAD2"
    push_scripts "$n"
    ssh_host "$n" "bash $R/tools/remote.sh check --mode multi --role node" 2>&1 | mask
    push_scripts "$l1"
    ssh_host "$l1" "bash $R/tools/remote.sh check --mode multi --role load1" 2>&1 | mask
    push_scripts "$l2"
    ssh_host "$l2" "bash $R/tools/remote.sh check --mode multi --role load2" 2>&1 | mask
  else
    local h
    h=$(single_host)
    push_scripts "$h"
    ssh_host "$h" "bash $R/tools/remote.sh check --mode single --role node" 2>&1 | mask
  fi
}

cmd_run() {
  local label=${1:?} ref=${2:?} sha tid h cmd remote i
  [[ $label =~ ^[A-Za-z0-9._-]+$ ]] || die "label 只能用字母、数字、点、下划线、连字符"
  sha=$(sha12 "$ref")
  tid=$(tools_id)
  build_pdnd "$ref"
  build_tools "$tid"
  parse_params

  if multi_mode; then
    local n l1 l2 nip lip cidr
    n=$(cfg NODEGATE_NODE)
    l1=$(cfg NODEGATE_LOAD1)
    l2=$(cfg NODEGATE_LOAD2)
    nip=$(cfg NODEGATE_NODE_IP)
    lip=$(cfg NODEGATE_LOAD1_IP)
    cidr=$(cfg NODEGATE_CIDR)
    [ -n "$n" ] && [ -n "$l1" ] && [ -n "$l2" ] && [ -n "$nip" ] && [ -n "$lip" ] && [ -n "$cidr" ] \
      || die "多机需要 NODEGATE_NODE、LOAD1、LOAD2、NODE_IP、LOAD1_IP、CIDR"
    [ ! -e "$OUT/$label" ] || die "本机结果目录已有 $OUT/$label"
    mkdir -p "$OUT"
    push_all "$n" "$sha" "$tid"
    push_all "$l1" "$sha" "$tid"
    push_all "$l2" "$sha" "$tid"
    cmd=(python3 -I "$here/orchestrate.py" run --mode multi --label "$label" --sha "$sha" --tools-id "$tid" --out "$OUT/$label"
         --node "$n" --load1 "$l1" --load2 "$l2" --node-ip "$nip" --load1-ip "$lip" --cidr "$cidr")
    if [ ${#PARAM_ARGS[@]} -gt 0 ]; then
      cmd+=("${PARAM_ARGS[@]}")
    fi
    echo "run: 多机 $label（$sha，tools-$tid）"
    "${cmd[@]}" 2>&1 | tee "$OUT/$label.log"
    [ -e "$OUT/$label/done" ] || die "$label 未成功（见 $OUT/$label.log）"
    echo "fetch: $OUT/$label"
    return 0
  fi

  h=$(single_host)
  push_all "$h" "$sha" "$tid"
  ssh_host "$h" "test ! -e $R/runs/$label" || die "测试机上已有 $label"
  remote="setsid -f python3 -I $R/tools/orchestrate.py run --mode single --label $(printf '%q' "$label") --sha $(printf '%q' "$sha") --tools-id $(printf '%q' "$tid") --out $(printf '%q' "$R/runs/$label")"
  i=0
  while [ $i -lt ${#PARAM_ARGS[@]} ]; do
    remote+=" $(printf '%q' "${PARAM_ARGS[$i]}") $(printf '%q' "${PARAM_ARGS[$((i + 1))]}")"
    i=$((i + 2))
  done
  remote+=" > $(printf '%q' "$R/runs/$label.log") 2>&1 < /dev/null"
  ssh -n -o BatchMode=yes "$h" "$remote"
  echo "run: $label（$sha）已在测试机后台开始"
  wait_single "$label"
  fetch_single "$label"
}

case ${1:-} in
  check) cmd_check ;;
  run) shift; cmd_run "$@" ;;
  wait) shift; wait_single "$@" ;;
  fetch) shift; fetch_single "$@" ;;
  compare)
    shift
    python3 -I "$here/compare.py" --root "$OUT" "$@" ;;
  *) sed -n '2,9p' "$0" >&2; exit 2 ;;
esac
