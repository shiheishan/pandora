#!/usr/bin/env bash
# 性能闸门的本机入口：把机上脚本传到测试机、按提交构建、后台跑一次完整测量、取回结果、对比。
#
# 用法：
#   gate.sh check | seed | snapshot     机上一次性准备（见 README）
#   gate.sh build <提交>                 在测试机上构建这个提交的 pandora（git archive 的 panel/ 与 nodeapi/，不传别的）
#   gate.sh run <label> <提交>           构建 → 测试机后台跑一次（约 30 分钟）→ 等完 → 取回到结果目录
#   gate.sh wait <label> | fetch <label>
#   gate.sh compare <基线> <候选> [--aa <基线第二遍>] [--target <指标前缀>]
#
# 测试机用 ssh 别名指定：环境变量 GATE_HOST，或主仓库 ops-local/netcup-perf/gate.env 里的 GATE_HOST=…（仓库里不写机器）。
# 结果目录缺省是主仓库的 ops-local/netcup-perf/gate/（被 git 忽略）。
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
top=$(git -C "$here" rev-parse --show-toplevel)
main=$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")
env_file=${GATE_ENV:-$main/ops-local/netcup-perf/gate.env}
if [ -z "${GATE_HOST:-}" ] && [ -f "$env_file" ]; then
  GATE_HOST=$(awk -F= '$1 == "GATE_HOST" { sub(/^[^=]*=/, ""); v = $0 } END { print v }' "$env_file")
fi
OUT=${GATE_OUT:-$main/ops-local/netcup-perf/gate}
R=/root/gate

die() { echo "gate.sh: $*" >&2; exit 1; }
# 机上输出里万一带后台路径前缀，先打码再显示
mask() { sed -E 's#/ops_[0-9a-f]+#/ops_<隐去>#g'; }
host() { [ -n "${GATE_HOST:-}" ] || die "没设 GATE_HOST（或 $env_file）"; echo "$GATE_HOST"; }
rsh() { ssh -o BatchMode=yes "$(host)" "$@"; }

push() {
  local h
  h=$(host)
  rsh "mkdir -p $R/tools/k6 $R/tools/sql $R/src $R/runs $R/logs"
  scp -q "$here/remote.sh" "$here/collect.py" "$here/../access_stats.py" "$h:$R/tools/"
  scp -q "$here"/../k6/*.js "$h:$R/tools/k6/"
  scp -q "$here"/../sql/*.sql "$h:$R/tools/sql/"
}

sha12() { git -C "$top" rev-parse --verify "$1^{commit}" | cut -c1-12; }

build() {
  local ref=${1:?用法: build <提交>} sha
  sha=$(sha12 "$ref")
  if rsh "test -x $R/bin/pandora-$sha"; then
    echo "build: 测试机上已有 pandora-$sha"
    return 0
  fi
  git -C "$top" archive --format=tar "$sha" panel nodeapi | rsh "cat > $R/src/$sha.tar"
  rsh "bash $R/tools/remote.sh build $sha" 2>&1 | mask
}

state() { rsh "if [ -e $R/runs/$1/done ]; then echo done; elif [ -e $R/runs/$1/failed ]; then echo failed; else echo running; fi"; }

wait_run() {
  local label=${1:?用法: wait <label>} st last="" line
  while :; do
    st=$(state "$label")
    line=$(rsh "tail -n 1 $R/runs/$label.log 2>/dev/null" | mask || true)
    [ "$line" != "$last" ] && [ -n "$line" ] && echo "$line" && last=$line
    case $st in
      done) return 0 ;;
      failed) rsh "tail -n 20 $R/runs/$label.log" | mask >&2; die "$label 失败" ;;
    esac
    sleep 30
  done
}

fetch() {
  local label=${1:?用法: fetch <label>}
  mkdir -p "$OUT"
  rsh "tar -C $R/runs -czf - $label $label.log" | tar -C "$OUT" -xzf -
  echo "fetch: $OUT/$label"
}

run() {
  local label=${1:?用法: run <label> <提交>} ref=${2:?用法: run <label> <提交>} sha
  [[ $label =~ ^[A-Za-z0-9._-]+$ ]] || die "label 只能用字母、数字、点、下划线、连字符"
  sha=$(sha12 "$ref")
  push
  build "$sha"
  rsh "test ! -e $R/runs/$label" || die "测试机上已有 $label"
  # 跑一次约 30 分钟，放到测试机后台，ssh 断了也不影响
  ssh -n -o BatchMode=yes "$(host)" "setsid -f bash $R/tools/remote.sh run $label $sha > $R/runs/$label.log 2>&1 < /dev/null"
  echo "run: $label（$sha）已在测试机后台开始"
  wait_run "$label"
  fetch "$label"
}

case ${1:-} in
  check | snapshot)
    push
    rsh "bash $R/tools/remote.sh $1" 2>&1 | mask ;;
  seed)
    # 造数要四十分钟上下（接入限速 + 等统计任务），放到测试机后台
    push
    ssh -n -o BatchMode=yes "$(host)" "setsid -f bash $R/tools/remote.sh seed > $R/logs/seed.log 2>&1 < /dev/null"
    last=""
    while rsh "pgrep -f '^bash $R/tools/remote.sh seed' >/dev/null"; do
      line=$(rsh "tail -n 1 $R/logs/seed.log" | mask || true)
      [ "$line" != "$last" ] && echo "$line" && last=$line
      sleep 30
    done
    rsh "tail -n 5 $R/logs/seed.log" | mask ;;
  build) shift; push; build "$@" ;;
  run) shift; run "$@" ;;
  wait) shift; wait_run "$@" ;;
  fetch) shift; fetch "$@" ;;
  compare)
    shift
    python3 -I "$here/compare.py" --root "$OUT" "$@" ;;
  *) sed -n '2,12p' "$0" >&2; exit 2 ;;
esac
