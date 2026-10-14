#!/usr/bin/env bash
# 节点端性能闸门的机上部分：在测试机上以 root 运行；由 gate.sh 上传到 /root/nodegate/tools/ 后调用。
#
# 用法（命令与 §6 一致）：
#   remote.sh check --mode M --role R
#   remote.sh prepare --label L --mode M --role R
#   remote.sh cleanup --label L
#   remote.sh pdnd-config --label L --panel HOST:PORT
#   remote.sh enroll --label L --bin PATH --panel HOST:PORT --ca-sha256 FP --token-file F
#   remote.sh pdnd-start --label L --bin PATH
#   remote.sh pdnd-stop --label L
#   remote.sh bg-start --label L --name N -- CMD...
#   remote.sh bg-stop --label L --name N [--wait 30]
#   remote.sh fg --label L -- CMD...
#   remote.sh panel-ctl --label L --method GET|POST --path P [--port 9180]
#   remote.sh snap --label L
#   remote.sh sample-start --label L
#   remote.sh sample-stop --label L
#   remote.sh rmem-set [--ttl 14400]
#   remote.sh rmem-restore
#   remote.sh fw-guard --label L --cidr C --ports A:B --ttl S
#   remote.sh fw-unguard --label L
#   remote.sh cat --label L --path P
#   remote.sh untar --label L --dir D   # 从 stdin 读 tar
#   remote.sh tar --label L --dir D     # tar 打到 stdout
#
# 常量：R=/root/nodegate，网络命名空间 NS=nodegate，临时 pdnd 单元 UNIT=nodegate-pdnd。
set -euo pipefail

R=/root/nodegate
NS=nodegate
UNIT=nodegate-pdnd
COLLECT=$R/tools/collect.py

CMD=""
LABEL=""
MODE=""
ROLE=""
RUN=""
REST=()
CMDV=()  # 「--」之后的命令（bg-start、fg）

usage() {
  sed -n '4,28p' "$0" | sed 's/^# //'
}

die() { echo "remote.sh: $*" >&2; exit 1; }
log() { echo "[$(date -u +%H:%M:%S)] $*"; }

# kv 从 KEY=VALUE 文件读一个键（最后一次出现为准）
kv() { awk -F= -v k="$2" '$1 == k { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"; }

valid_label() {
  [[ ${1:-} =~ ^[A-Za-z0-9._-]+$ ]] || die "label 非法: ${1:-空}"
}

valid_name() {
  [[ ${1:-} =~ ^[A-Za-z0-9._-]+$ ]] || die "name 非法: ${1:-空}"
}

safe_relpath() {
  local p=$1
  [[ "$p" != *".."* ]] || die "path 不许含 ..: $p"
  [[ "$p" != /* ]] || die "path 不许以 / 开头: $p"
}

load_run_ctx() {
  valid_label "$LABEL"
  RUN=$R/runs/$LABEL
  if [[ -f "$RUN/.mode" ]]; then
    MODE=$(<"$RUN/.mode")
  fi
  if [[ -f "$RUN/.role" ]]; then
    ROLE=$(<"$RUN/.role")
  fi
}

nsexec() {
  if [[ "${MODE:-}" == single ]]; then
    ip netns exec "$NS" "$@"
  else
    "$@"
  fi
}

need_root() {
  [[ "$(id -u)" -eq 0 ]] || die "需要 root"
}

need_bin_dir() {
  [[ -d "$R/bin" ]] || die "缺 $R/bin"
}

lock_clear_stale() {
  [[ -f "$R/lock" ]] || return 0
  local locked_label locked_at now age
  locked_label=$(head -n1 "$R/lock" 2>/dev/null || true)
  locked_at=$(sed -n '2p' "$R/lock" 2>/dev/null || echo 0)
  now=$(date +%s)
  age=$((now - locked_at))
  if [[ -n "$locked_label" && ( -f "$R/runs/$locked_label/done" || -f "$R/runs/$locked_label/failed" ) ]]; then
    rm -f "$R/lock"
    return 0
  fi
  if [[ "$age" -ge 21600 ]]; then
    rm -f "$R/lock"
    return 0
  fi
  die "已有 nodegate 运行（锁 label=$locked_label），或先 cleanup"
}

lock_take() {
  valid_label "$LABEL"
  lock_clear_stale
  mkdir -p "$R/runs"
  printf '%s\n%s\n' "$LABEL" "$(date +%s)" >"$R/lock"
}

lock_release() {
  rm -f "$R/lock" || true
}

# timer_spawn PIDFILE SECS FUNC ARGS...：到点在后台调用 FUNC（函数体随命令带进子 shell，
# 不依赖 remote.sh 届时还在）；pid 写进 PIDFILE，timer_kill 用它撤掉定时器。
timer_spawn() {
  local pidf=$1 secs=$2 fn=$3
  shift 3
  rm -f "$pidf"
  setsid -f bash -c "$(declare -f "$fn"); "'echo $$ > "$1"; sleep "$2"; shift 2; '"$fn"' "$@"' \
    _ "$pidf" "$secs" "$@" </dev/null >/dev/null 2>&1
  local w=0
  while [[ ! -s "$pidf" && $w -lt 50 ]]; do sleep 0.1; w=$((w+1)); done
}

timer_kill() {
  local pidf=$1 pid
  [[ -f "$pidf" ]] || return 0
  pid=$(<"$pidf")
  [[ "$pid" =~ ^[0-9]+$ ]] && kill -TERM "$pid" 2>/dev/null || true
  rm -f "$pidf"
}

# rmem_write R W ORIGFILE：写回原值并删记录（定时器与 rmem-restore 共用）
rmem_write() {
  sysctl -q -w "net.core.rmem_max=$1" "net.core.wmem_max=$2" 2>/dev/null || true
  rm -f "$3"
}

rmem_restore() {
  local orig=$R/state/rmem.orig
  timer_kill "$R/state/rmem.timer"
  [[ -f "$orig" ]] || return 0
  rmem_write "$(kv "$orig" rmem_max)" "$(kv "$orig" wmem_max)" "$orig"
}

parse_args() {
  [[ $# -ge 1 ]] || { usage >&2; exit 2; }
  CMD=$1
  shift
  REST=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --label) LABEL=$2; shift 2 ;;
      --mode) MODE=$2; shift 2 ;;
      --role) ROLE=$2; shift 2 ;;
      --)
        shift
        CMDV=("$@")
        return
        ;;
      *) REST+=("$1"); shift ;;
    esac
  done
}

#---- check / prepare / cleanup

cmd_check() {
  need_root
  need_bin_dir
  command -v python3 >/dev/null || die "缺 python3"
  command -v curl >/dev/null || die "缺 curl"
  command -v systemd-run >/dev/null || die "缺 systemd-run"
  local m=${MODE:-} r=${ROLE:-}
  [[ -n "$m" ]] || die "check 需要 --mode"
  [[ -n "$r" ]] || die "check 需要 --role"

  if [[ -f "$R/state/rmem.orig" ]]; then
    rmem_restore
  fi
  lock_clear_stale

  local detail="mode=$m role=$r"
  if [[ "$m" == single ]]; then
    command -v ip >/dev/null || die "缺 ip（netns）"
    ip netns list >/dev/null 2>&1 || die "ip netns 不可用"
    local cpus
    cpus=$(systemctl show system.slice -p AllowedCPUs --value 2>/dev/null || true)
    # 空表示不限；否则展开「0-1,4」这类列表，必须含 0 和 1（pdnd 绑在 0-1 上）
    if [[ -n "$cpus" ]] && ! python3 -I -c '
import sys
s = set()
for part in sys.argv[1].replace(" ", ",").split(","):
    if not part:
        continue
    a, _, b = part.partition("-")
    s.update(range(int(a), int(b or a) + 1))
sys.exit(0 if {0, 1} <= s else 1)' "$cpus"; then
      die "system.slice AllowedCPUs=$cpus，需要包含 0-1 或为空（不限）"
    fi
    if pgrep -f '^bash /root/gate/tools/remote.sh run' >/dev/null 2>&1; then
      die "面板闸门 remote.sh run 正在跑"
    fi
    detail+=", netns=ok, panel-gate=idle"
  fi
  if [[ "$m" == multi && "$r" == node ]]; then
    command -v iptables >/dev/null || die "缺 iptables"
    detail+=", iptables=ok"
  fi
  echo "check: 通过（$detail）"
}

cmd_prepare() {
  need_root
  [[ -n "$LABEL" ]] || die "prepare 需要 --label"
  [[ -n "$MODE" ]] || die "prepare 需要 --mode"
  [[ -n "$ROLE" ]] || die "prepare 需要 --role"
  lock_take
  RUN=$R/runs/$LABEL
  mkdir -p "$RUN/bg" "$RUN/logs" "$RUN/pdnd" "$RUN/profile"
  chmod 700 "$RUN" "$RUN/bg" "$RUN/logs" "$RUN/pdnd" "$RUN/profile" 2>/dev/null || true
  printf '%s\n' "$MODE" >"$RUN/.mode"
  printf '%s\n' "$ROLE" >"$RUN/.role"
  if [[ "$MODE" == single ]]; then
    ip netns del "$NS" 2>/dev/null || true
    ip netns add "$NS"
    ip -n "$NS" link set lo up
    mkdir -p "$R/state"
    : >"$R/state/stopped-units"
    for u in pandora-serve pandora-worker; do
      if systemctl is-active --quiet "$u" 2>/dev/null; then
        systemctl stop "$u" || true
        echo "$u" >>"$R/state/stopped-units"
      fi
    done
  fi
}

cmd_cleanup() {
  [[ -n "$LABEL" ]] || die "cleanup 需要 --label"
  valid_label "$LABEL"
  RUN=$R/runs/$LABEL
  if [[ -f "$RUN/.mode" ]]; then MODE=$(<"$RUN/.mode"); fi

  systemctl stop "$UNIT" 2>/dev/null || true
  systemctl reset-failed "$UNIT" 2>/dev/null || true

  if [[ -d "$RUN/bg" ]]; then
    local pf pid alive w=0
    local -a pids=()
    for pf in "$RUN/bg"/*.pid; do
      [[ -f "$pf" ]] || continue
      pid=$(<"$pf" 2>/dev/null || true)
      [[ "$pid" =~ ^[0-9]+$ ]] || continue
      pids+=("$pid")
      kill -TERM "$pid" 2>/dev/null || true
    done
    # 最多等 10 秒，全退了就不再等
    while [[ ${#pids[@]} -gt 0 && $w -lt 50 ]]; do
      alive=0
      for pid in "${pids[@]}"; do
        kill -0 "$pid" 2>/dev/null && alive=1
      done
      [[ $alive -eq 1 ]] || break
      sleep 0.2
      w=$((w+1))
    done
    for pid in "${pids[@]}"; do
      kill -KILL "$pid" 2>/dev/null || true
    done
    rm -f "$RUN/bg"/*.pid 2>/dev/null || true
  fi

  fw_unguard_impl || true
  rmem_restore || true

  if [[ "${MODE:-}" == single ]]; then
    ip netns del "$NS" 2>/dev/null || true
  fi

  if [[ -f "$R/state/stopped-units" ]]; then
    while IFS= read -r u; do
      [[ -n "$u" ]] && systemctl start "$u" 2>/dev/null || true
    done <"$R/state/stopped-units"
    rm -f "$R/state/stopped-units" || true
  fi

  lock_release || true
}

#---- pdnd / enroll / bg / fg / panel / collect / rmem / fw / cat / tar

rest_get() {
  local k=$1 _i=0
  while [[ $_i -lt ${#REST[@]} ]]; do
    if [[ "${REST[$_i]}" == "$k" ]]; then
      [[ $((_i+1)) -lt ${#REST[@]} ]] || die "$k 缺值"
      echo "${REST[$((_i+1))]}"
      return 0
    fi
    _i=$((_i+1))
  done
  return 1
}

cmd_pdnd_config() {
  load_run_ctx
  local panel
  panel=$(rest_get --panel) || die "pdnd-config 需要 --panel HOST:PORT"
  mkdir -m 700 -p "$RUN/pdnd"
  printf '%s\n' "{\"log_level\":\"warn\",\"cache_dir\":\"$RUN/pdnd/state\",\"panel\":{\"address\":\"$panel\",\"identity_dir\":\"$RUN/pdnd/identity\"},\"runtime\":{\"allow_private_destinations\":true}}" >"$RUN/pdnd/config.json"
  chmod 600 "$RUN/pdnd/config.json"
}

cmd_enroll() {
  load_run_ctx
  local bin panel fp token
  bin=$(rest_get --bin) || die "enroll 需要 --bin"
  panel=$(rest_get --panel) || die "enroll 需要 --panel"
  fp=$(rest_get --ca-sha256) || die "enroll 需要 --ca-sha256"
  token=$(rest_get --token-file) || die "enroll 需要 --token-file"
  mkdir -m 700 -p "$RUN/pdnd"
  nsexec "$bin" enroll --panel "$panel" --ca-sha256 "$fp" --token-file "$token" --identity-dir "$RUN/pdnd/identity"
}

cmd_pdnd_start() {
  load_run_ctx
  local bin
  bin=$(rest_get --bin) || die "pdnd-start 需要 --bin"
  systemctl reset-failed "$UNIT" 2>/dev/null || true
  local t
  t=$(date +%s.%N)
  local -a run_args=(systemd-run --unit="$UNIT" --collect --quiet -p LimitNOFILE=1048576
    -p "StandardOutput=append:$RUN/logs/pdnd.log" -p "StandardError=append:$RUN/logs/pdnd.log")
  if [[ "$MODE" == single ]]; then
    run_args+=(-p AllowedCPUs=0-1 -p MemoryMax=2G -p "NetworkNamespacePath=/run/netns/$NS")
  fi
  run_args+=("$bin" -c "$RUN/pdnd/config.json")
  "${run_args[@]}"
  local i=0
  while [[ $i -lt 100 ]]; do
    if systemctl is-active --quiet "$UNIT" 2>/dev/null; then
      echo "$t"
      return 0
    fi
    sleep 0.1
    i=$((i+1))
  done
  echo "remote.sh: pdnd 未在 10 秒内 active" >&2
  tail -n 20 "$RUN/logs/pdnd.log" 2>/dev/null >&2 || true
  die "pdnd-start 失败"
}

cmd_pdnd_stop() {
  load_run_ctx
  systemctl stop "$UNIT" 2>/dev/null || true
  systemctl reset-failed "$UNIT" 2>/dev/null || true
  date +%s.%N
}

cmd_bg_start() {
  load_run_ctx
  local name
  name=$(rest_get --name) || die "bg-start 需要 --name"
  valid_name "$name"
  [[ ${#CMDV[@]} -gt 0 ]] || die "bg-start 需要 -- CMD..."
  local -a cmd=("${CMDV[@]}")
  local pidfile="$RUN/bg/$name.pid"
  local out="$RUN/bg/$name.out" err="$RUN/bg/$name.err"
  if [[ "$MODE" == single ]]; then
    setsid -f bash -c 'echo $$ > "$1"; shift; exec ip netns exec '"$NS"' "$@"' _ "$pidfile" "${cmd[@]}" >"$out" 2>"$err" < /dev/null
  else
    setsid -f bash -c 'echo $$ > "$1"; shift; exec "$@"' _ "$pidfile" "${cmd[@]}" >"$out" 2>"$err" < /dev/null
  fi
  local w=0
  while [[ $w -lt 50 ]]; do
    [[ -f "$pidfile" ]] && return 0
    sleep 0.1
    w=$((w+1))
  done
  die "bg-start: pid 文件未出现"
}

cmd_bg_stop() {
  load_run_ctx
  local name wait_sec=30
  name=$(rest_get --name) || die "bg-stop 需要 --name"
  valid_name "$name"
  local w
  w=$(rest_get --wait) && wait_sec=$w || true
  local pidfile="$RUN/bg/$name.pid"
  [[ -f "$pidfile" ]] || return 0
  local pid
  pid=$(<"$pidfile" 2>/dev/null || true)
  [[ "$pid" =~ ^[0-9]+$ ]] || return 0
  kill -TERM "$pid" 2>/dev/null || true
  local elapsed=0
  local max=$((wait_sec * 5))
  while [[ $elapsed -lt $max ]]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$pidfile" || true
      return 0
    fi
    sleep 0.2
    elapsed=$((elapsed+1))
  done
  kill -KILL "$pid" 2>/dev/null || true
  rm -f "$pidfile" || true
}

cmd_fg() {
  load_run_ctx
  [[ ${#CMDV[@]} -gt 0 ]] || die "fg 需要 -- CMD..."
  nsexec "${CMDV[@]}"
}

cmd_panel_ctl() {
  load_run_ctx
  local method path port=9180
  method=$(rest_get --method) || die "panel-ctl 需要 --method"
  path=$(rest_get --path) || die "panel-ctl 需要 --path"
  port=$(rest_get --port) || port=9180
  path=${path#/}
  local url="http://127.0.0.1:${port}/${path}"
  nsexec curl -s --max-time 10 -X "$method" "$url" || die "panel-ctl curl 失败"
}

cmd_snap() {
  load_run_ctx
  python3 -I "$COLLECT" snap --unit "$UNIT"
}

cmd_sample_start() {
  load_run_ctx
  setsid -f python3 -I "$COLLECT" sample-start --unit "$UNIT" --out "$RUN/samples.jsonl" --pidfile "$RUN/bg/sampler.pid" < /dev/null > /dev/null 2>&1
}

cmd_sample_stop() {
  load_run_ctx
  python3 -I "$COLLECT" sample-stop --pidfile "$RUN/bg/sampler.pid" || true
}

# rmem-set [--ttl S]：只在显式场景参数下调用；原值记进 $R/state/rmem.orig，
# cleanup / check / 到点定时器（缺省 4 小时）三处都会写回，保证不留下改过的 sysctl。
cmd_rmem_set() {
  mkdir -p "$R/state"
  local orig=$R/state/rmem.orig ttl=14400
  ttl=$(rest_get --ttl) || ttl=14400
  [[ "$ttl" =~ ^[0-9]+$ && "$ttl" -gt 0 ]] || die "ttl 非法"
  [[ -f "$orig" ]] && return 0
  local r w
  r=$(sysctl -n net.core.rmem_max)
  w=$(sysctl -n net.core.wmem_max)
  printf 'rmem_max=%s\nwmem_max=%s\n' "$r" "$w" >"$orig"
  timer_spawn "$R/state/rmem.timer" "$ttl" rmem_write "$r" "$w" "$orig"
  sysctl -q -w net.core.rmem_max=8388608 net.core.wmem_max=8388608
}

cmd_rmem_restore() {
  rmem_restore
}

# fw_apply -I|-D PORTS CIDR：插入或删除守卫规则（tcp、udp 各一条；有 ip6tables 时 IPv6 各一条不带 -s）
fw_apply() {
  local op=$1 ports=$2 cidr=$3 l4 rc=0
  for l4 in tcp udp; do
    iptables "$op" INPUT -p "$l4" --dport "$ports" ! -i lo ! -s "$cidr" -j DROP -m comment --comment nodegate || rc=1
    if command -v ip6tables >/dev/null 2>&1; then
      ip6tables "$op" INPUT -p "$l4" --dport "$ports" ! -i lo -j DROP -m comment --comment nodegate 2>/dev/null || true
    fi
  done
  return $rc
}

fw_unguard_impl() {
  local conf=$R/state/fw.conf
  timer_kill "$R/state/fw.timer"
  [[ -f "$conf" ]] || return 0
  fw_apply -D "$(kv "$conf" ports)" "$(kv "$conf" cidr)" 2>/dev/null || true
  rm -f "$conf"
}

cmd_fw_guard() {
  load_run_ctx
  local cidr ports ttl
  cidr=$(rest_get --cidr) || die "fw-guard 需要 --cidr"
  ports=$(rest_get --ports) || die "fw-guard 需要 --ports"
  ttl=$(rest_get --ttl) || die "fw-guard 需要 --ttl"
  [[ "$cidr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$ ]] || die "cidr 非法"
  [[ "$ports" =~ ^[0-9]+:[0-9]+$ ]] || die "ports 非法"
  [[ "$ttl" =~ ^[0-9]+$ && "$ttl" -gt 0 ]] || die "ttl 非法"
  mkdir -p "$R/state"
  fw_unguard_impl
  printf 'ports=%s\ncidr=%s\n' "$ports" "$cidr" >"$R/state/fw.conf"
  # 先挂好到点自撤，再插规则：插到一半失败也撤得掉
  timer_spawn "$R/state/fw.timer" "$ttl" fw_apply -D "$ports" "$cidr"
  fw_apply -I "$ports" "$cidr" || { fw_unguard_impl; die "iptables 插入失败"; }
}

cmd_fw_unguard() {
  load_run_ctx
  fw_unguard_impl
}

cmd_cat() {
  load_run_ctx
  local path
  path=$(rest_get --path) || die "cat 需要 --path"
  safe_relpath "$path"
  cat "$RUN/$path"
}

cmd_untar() {
  load_run_ctx
  local dir
  dir=$(rest_get --dir) || die "untar 需要 --dir"
  safe_relpath "$dir"
  mkdir -p "$RUN/$dir"
  tar -C "$RUN/$dir" -xf -
}

cmd_tar() {
  load_run_ctx
  local dir
  dir=$(rest_get --dir) || die "tar 需要 --dir"
  safe_relpath "$dir"
  tar -C "$RUN/$dir" -cf - .
}

main() {
  parse_args "$@"
  case "$CMD" in
    check) cmd_check ;;
    prepare) cmd_prepare ;;
    cleanup) cmd_cleanup ;;
    pdnd-config) cmd_pdnd_config ;;
    enroll) cmd_enroll ;;
    pdnd-start) cmd_pdnd_start ;;
    pdnd-stop) cmd_pdnd_stop ;;
    bg-start) cmd_bg_start ;;
    bg-stop) cmd_bg_stop ;;
    fg) cmd_fg ;;
    panel-ctl) cmd_panel_ctl ;;
    snap) cmd_snap ;;
    sample-start) cmd_sample_start ;;
    sample-stop) cmd_sample_stop ;;
    rmem-set) cmd_rmem_set ;;
    rmem-restore) cmd_rmem_restore ;;
    fw-guard) cmd_fw_guard ;;
    fw-unguard) cmd_fw_unguard ;;
    cat) cmd_cat ;;
    untar) cmd_untar ;;
    tar) cmd_tar ;;
    *) usage >&2; exit 2 ;;
  esac
}

main "$@"
