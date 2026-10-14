#!/usr/bin/env bash
# 性能闸门的机上部分：在按 install.sh 装好、跳过 nginx 的测试机上以 root 运行；由本机 gate.sh 上传到 /root/gate/tools/ 后调用。
#
# 用法：
#   remote.sh check                  查前置条件：切核、服务、测试端口只听回环
#   remote.sh build <sha12>          用 /root/gate/src/<sha12>.tar（git archive 的 panel/ 与它 replace 的 nodeapi/）构建 /root/gate/bin/pandora-<sha12>
#   remote.sh seed                   一次性造数：infra → nodesim 接入 → activate → users → 放宽拉取上限 → 等统计任务补齐
#   remote.sh snapshot               停服后把当前库做成模板 pandora_gate_base，nodesim 身份目录打包
#   remote.sh run <label> <sha12>    换二进制 → 从模板重建库 → 四个场景 → 结果在 /root/gate/runs/<label>/
#
# 口令只经文件和环境变量（REDISCLI_AUTH、子进程环境）传，不进参数表、不打印；后台直连 127.0.0.1:9001，不经 nginx，
# 输出里不会出现后台路径前缀。测试端口只听回环，不对公网提供任何服务。
set -euo pipefail

G=/root/gate
TOOLS=$G/tools
DATA=$G/data          # perfseed 的输出：tokens、nodesim.env、infra.json、catalog.json、users.csv
SECRETS=$G/secrets    # admin_email、admin_pass、user_pass（0600）
BASE=$G/base          # 模板配套：nodesim 身份目录打包
ETC=/etc/pandora
LOGDIR=/var/log/pandora
TEMPLATE_DB=pandora_gate_base
SERVERS=${GATE_SERVERS:-200}
USERS=${GATE_USERS:-5000}
# 订阅拉取的阶梯（每秒请求数）与每级时长；某一级饱和（见 pull_saturated）就不再往上加
PULL_RATES=${GATE_PULL_RATES:-"20 50 100 200 400 800"}
PULL_SECS=${GATE_PULL_SECS:-60}
GATE_SUB_PREFIX=0123456789ab   # 虚构的订阅路径前缀（12 位十六进制）
IDLE_SECS=${GATE_IDLE_SECS:-300}
SETTLE_SECS=${GATE_SETTLE_SECS:-60}
RECONNECTS=${GATE_RECONNECTS:-3}
# 诊断：GATE_PPROF=1 时 serve 开 pprof（只听回环），每个窗口结束取一份堆剖析；开了 pprof 的运行只用来定位，不拿来比
PPROF=${GATE_PPROF:-0}
PPROF_ADDR=127.0.0.1:6061
PPROF_DROPIN=/run/systemd/system/pandora-serve.service.d/pprof.conf
# 切核：面板侧（system.slice）0-1 号核、4G；压测进程在 user.slice 的其余核（ops-local 的 cpuset.sh 设置）
WANT_SYSTEM_CPUS=${GATE_SYSTEM_CPUS:-0-1}
WANT_USER_CPUS=${GATE_USER_CPUS:-2-7}

die() { echo "remote.sh: $*" >&2; exit 1; }
log() { echo "[$(date -u +%H:%M:%S)] $*"; }

# kv 从 KEY=VALUE 文件读一个键（最后一次出现为准），不 source
kv() { awk -F= -v k="$2" '$1 == k { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"; }

pgport() { kv "$ETC/deploy.env" POSTGRES_PORT; }

# pg 以 postgres 经本机套接字连库（对等认证，不用口令），SQL 从 stdin 来
pg() { (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p "$(pgport)" -d "$1"); }
# pgval 跑一条查询，输出不带表头
pgval() { (cd / && runuser -u postgres -- psql -X -q -At -v ON_ERROR_STOP=1 -p "$(pgport)" -d "$1" -c "$2"); }

# redis_parts 从 stdin 读 Redis URL，输出「主机 端口 库号 口令」；URL 经 stdin 传，口令不进参数表
redis_parts() {
  python3 -I -c 'import sys, urllib.parse as u
p = u.urlparse(sys.stdin.read().strip())
print(p.hostname or "127.0.0.1", p.port or 6379, (p.path or "/0").lstrip("/") or "0", u.unquote(p.password or ""))'
}

# with_redis_auth 在子 shell 里设好 REDIS_HOST/PORT/DB 与 REDISCLI_AUTH 再执行命令
with_redis_auth() {
  (
    read -r REDIS_HOST REDIS_PORT REDIS_DB REDISCLI_AUTH < <(kv "$ETC/secrets.env" PANDORA_REDIS_URL | redis_parts)
    export REDIS_HOST REDIS_PORT REDIS_DB REDISCLI_AUTH
    "$@"
  )
}
rcli() { redis-cli --no-auth-warning -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" "$@"; }

# redis_clear 清空面板用的 Redis 库（FLUSHDB/FLUSHALL 在加固配置里被禁用，只能 SCAN + UNLINK）
redis_clear_inner() {
  rcli --scan --count 1000 | xargs -r -d '\n' -n 200 \
    redis-cli --no-auth-warning -h "$REDIS_HOST" -p "$REDIS_PORT" -n "$REDIS_DB" UNLINK >/dev/null
  local n
  n=$(rcli DBSIZE)
  [ "$n" = 0 ] || die "Redis 没清干净（还剩 $n 个键）"
}
redis_clear() { with_redis_auth redis_clear_inner; }

# export_env_file 把 KEY=VALUE 文件逐行导出（与 install-lib.sh 的 pandora_export_env_file 同法，不 source）
export_env_file() {
  local line k v
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    [[ "$line" =~ ^[A-Z][A-Z0-9_]*= ]] || continue
    k="${line%%=*}"
    v="${line#*=}"
    export "$k=$v"
  done <"$1"
}

# migrate_up 与安装器的 pandora_cli 同法：清空继承的环境，只给 secrets.env 的键与迁移 DSN，以 pandora 用户跑
migrate_up() {
  (
    local name
    while IFS= read -r name; do unset "$name" 2>/dev/null || true; done < <(compgen -e)
    export PATH=/usr/sbin:/usr/bin:/sbin:/bin HOME=/nonexistent LANG=C.UTF-8
    export_env_file "$ETC/secrets.env"
    PANDORA_DATABASE_MIGRATION_URL="$(kv "$ETC/deploy.env" PANDORA_DATABASE_MIGRATION_URL)"
    export PANDORA_DATABASE_MIGRATION_URL
    exec runuser -u pandora -- /opt/pandora/bin/pandora --config "$ETC/config.yaml" migrate up
  )
}

cgroup_of() { systemctl show -p ControlGroup --value "$1"; }

#---- 前置条件

cmd_check() {
  local cpus mem
  cpus=$(systemctl show system.slice -p AllowedCPUs --value)
  mem=$(systemctl show system.slice -p MemoryMax --value)
  [ "$cpus" = "$WANT_SYSTEM_CPUS" ] || die "system.slice 的核是「$cpus」，要「$WANT_SYSTEM_CPUS」：先跑 ops-local 的 cpuset.sh"
  [ "$mem" = 4294967296 ] || die "system.slice 的内存上限是「$mem」，要 4G：先跑 ops-local 的 cpuset.sh"
  cpus=$(systemctl show user.slice -p AllowedCPUs --value)
  [ "$cpus" = "$WANT_USER_CPUS" ] || die "user.slice 的核是「$cpus」，要「$WANT_USER_CPUS」"
  for u in pandora-serve pandora-worker postgresql@18-main redis-server; do
    systemctl cat "$u" >/dev/null 2>&1 || die "没有 $u 单元"
  done
  # 测试端口只许听回环（PG、Redis、门户、后台、节点通道、两个 metrics）
  local bad
  bad=$(ss -ltnH | awk '{print $4}' | grep -E ':(5432|6379|9000|9001|9443|9464|9465)$' | grep -vE '^(127\.0\.0\.1|\[::1\]):' || true)
  [ -z "$bad" ] || die "有测试端口不是只听回环：$bad"
  grep -q '^127\.0\.0\.1 gate\.example\.test' /etc/hosts || die "/etc/hosts 没有 gate.example.test 指向 127.0.0.1"
  for b in k6 perfseed nodesim; do [ -x "$G/bin/$b" ] || die "缺 $G/bin/$b"; done
  echo "check: 通过（system.slice=$WANT_SYSTEM_CPUS/4G，user.slice=$WANT_USER_CPUS，测试端口只听回环）"
}

#---- 构建

cmd_build() {
  local sha=${1:?用法: build <sha12>} src out
  [[ $sha =~ ^[0-9a-f]{12}$ ]] || die "sha 要 12 位十六进制: $sha"
  src=$G/src/$sha
  out=$G/bin/pandora-$sha
  if [ -x "$out" ]; then
    echo "build: 已有 $out"
    return 0
  fi
  [ -s "$G/src/$sha.tar" ] || die "缺 $G/src/$sha.tar"
  rm -rf "$src" && mkdir -p "$src" && tar -C "$src" -xf "$G/src/$sha.tar"
  # 与 build-release.sh 同样的编译参数；前端用仓库里的占位页（闸门只量后端），所有被比较的二进制都这样构建
  (cd "$src/panel" && GOTOOLCHAIN=local GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    /usr/local/go/bin/go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" -o "$out.tmp" ./cmd/pandora) \
    >"$G/logs/build-$sha.log" 2>&1 || die "构建失败，见 $G/logs/build-$sha.log"
  mv "$out.tmp" "$out"
  rm -rf "$src"
  echo "build: $out（sha256 $(sha256sum "$out" | cut -c1-16)）"
}

#---- 服务与 nodesim

stop_nodesim() {
  pkill -TERM -f "^$G/bin/nodesim " 2>/dev/null || true
  local i
  for i in $(seq 1 20); do
    pgrep -f "^$G/bin/nodesim " >/dev/null || return 0
    sleep 0.5
  done
  pkill -KILL -f "^$G/bin/nodesim " 2>/dev/null || true
}

stop_k6() { pkill -TERM -f "^$G/bin/k6 " 2>/dev/null || true; }

wait_ready() {
  local timeout=${1:-90} i c1 c2
  for i in $(seq 1 "$timeout"); do
    c1=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:9000/readyz || true)
    c2=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:9001/readyz || true)
    if [ "$c1" = 200 ] && [ "$c2" = 200 ] && systemctl is-active --quiet pandora-worker; then
      return 0
    fi
    sleep 1
  done
  return 1
}

# start_nodesim <状态文件> <日志>：后台起 nodesim，身份在 $G/nodesim-state
start_nodesim() {
  local status=$1 out=$2 panel ca
  panel=$(kv "$DATA/nodesim.env" PANEL)
  ca=$(kv "$DATA/nodesim.env" CA_SHA256)
  [ -n "$panel" ] && [ -n "$ca" ] || die "$DATA/nodesim.env 缺 PANEL 或 CA_SHA256"
  setsid -f "$G/bin/nodesim" --panel "$panel" --ca-sha256 "$ca" --tokens-file "$DATA/tokens" \
    --state-dir "$G/nodesim-state" --status-file "$status" --report-interval 10s --traffic-users 5 \
    --status-users=false --spread --enroll-attempts 0 --enroll-retry-wait 7s --enroll-concurrency 1 \
    --log-level warn >"$out" 2>&1 </dev/null
}

# rotate_logs 停服时把上一段服务日志压缩挪走，让这次运行的日志从空文件开始
rotate_logs() {
  local f ts
  ts=$(date -u +%Y%m%dT%H%M%SZ)
  mkdir -p "$G/logs/panel"
  for f in "$LOGDIR"/serve*.log* "$LOGDIR"/worker*.log*; do
    [ -e "$f" ] || continue
    case $f in
      *.gz) mv "$f" "$G/logs/panel/" ;;
      *) gzip -c "$f" >"$G/logs/panel/$(basename "$f").$ts.gz" && rm -f "$f" ;;
    esac
  done
}

#---- 造数（一次性）

cmd_seed() {
  cmd_check
  mkdir -p -m 700 "$DATA" "$G/run-seed"
  [ -s "$SECRETS/admin_pass" ] && [ -s "$SECRETS/admin_email" ] || die "缺 $SECRETS/admin_pass 或 admin_email"
  if [ ! -s "$SECRETS/user_pass" ]; then
    # 造数用户共用的口令（只在这台机器上，0600）
    (umask 077 && head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24 >"$SECRETS/user_pass")
  fi
  local email
  email=$(cat "$SECRETS/admin_email")
  wait_ready 60 || die "面板没就绪"

  log "seed: infra（$SERVERS 台服务器）"
  "$G/bin/perfseed" infra --admin-url http://127.0.0.1:9001 --admin-email "$email" \
    --admin-password-file "$SECRETS/admin_pass" --out-dir "$DATA" --servers "$SERVERS"

  log "seed: nodesim 接入（每个来源 IP 每分钟限 10 次，$SERVERS 台约 $((SERVERS / 10 + 1)) 分钟）"
  stop_nodesim
  mkdir -p -m 700 "$G/nodesim-state"
  start_nodesim "$G/run-seed/nodesim-status.json" "$G/run-seed/nodesim.log"
  python3 -I "$TOOLS/collect.py" wait-connected --status "$G/run-seed/nodesim-status.json" \
    --want "$SERVERS" --since 0 --timeout $((SERVERS * 9 + 300)) || die "nodesim 没有全部接入"

  log "seed: activate"
  "$G/bin/perfseed" activate --admin-url http://127.0.0.1:9001 --admin-email "$email" \
    --admin-password-file "$SECRETS/admin_pass" --out-dir "$DATA"

  log "seed: users（$USERS 个）"
  "$G/bin/perfseed" users --dsn-file "$ETC/deploy.env" --secrets-env "$ETC/secrets.env" --out-dir "$DATA" \
    --user-password-file "$SECRETS/user_pass" --users "$USERS" --history-days 30 --expired-percent 10 --seed 1

  log "seed: 放宽每条订阅每小时拉取上限"
  # SQL 经标准输入给：postgres 用户读不了 /root 下的文件
  (cd / && runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p "$(pgport)" -d pandora -v action=up) \
    <"$TOOLS/sql/perf_settings.sql"

  # 行为趋势的汇总表由每 10 分钟的任务补，一次最多补 7 个历史日；等它把近 16 天补齐，模板才像跑了很久的面板
  log "seed: 等统计任务补齐近 16 天的行为汇总（每 10 分钟一轮）"
  local n i
  for i in $(seq 1 60); do
    n=$(pgval pandora "SELECT count(DISTINCT day) FROM activity_daily WHERE day >= current_date - 17")
    [ "${n:-0}" -ge 16 ] && break
    sleep 60
  done
  [ "${n:-0}" -ge 16 ] || die "等了一小时统计汇总只有 $n 天"
  stop_nodesim
  log "seed: 完成；接着跑 snapshot"
}

cmd_snapshot() {
  cmd_check
  [ -d "$G/nodesim-state" ] || die "没有 $G/nodesim-state"
  stop_nodesim
  systemctl stop pandora-serve pandora-worker
  pg postgres <<SQL
DROP DATABASE IF EXISTS $TEMPLATE_DB;
CREATE DATABASE $TEMPLATE_DB TEMPLATE pandora OWNER pandora_owner;
-- 模板只给复制用，不让应用连
ALTER DATABASE $TEMPLATE_DB WITH ALLOW_CONNECTIONS false;
SQL
  mkdir -p -m 700 "$BASE"
  tar -C "$G" -cf "$BASE/nodesim-state.tar" nodesim-state
  chmod 600 "$BASE/nodesim-state.tar"
  systemctl start pandora-serve pandora-worker
  wait_ready 90 || die "面板没就绪"
  echo "snapshot: 模板 $TEMPLATE_DB（$(pgval postgres "SELECT pg_size_pretty(pg_database_size('$TEMPLATE_DB'))")）与身份目录已存"
}

#---- 每次运行：重置

# reset <sha12> <运行目录>：停 nodesim 与服务 → 换二进制 → 从模板重建库 → 迁移 → 收权限 → 清 Redis → 起服务
reset() {
  local sha=$1 run=$2 bin=$G/bin/pandora-$1
  [ -x "$bin" ] || die "缺 $bin，先 build"
  pgval postgres "SELECT 1 FROM pg_database WHERE datname = '$TEMPLATE_DB'" | grep -q 1 || die "没有模板库，先 seed + snapshot"
  stop_k6
  stop_nodesim
  systemctl stop pandora-serve pandora-worker
  rotate_logs
  install -m 0755 -o root -g root "$bin" /opt/pandora/bin/pandora
  pg postgres <<SQL
DROP DATABASE IF EXISTS pandora WITH (FORCE);
CREATE DATABASE pandora TEMPLATE $TEMPLATE_DB OWNER pandora_owner;
SQL
  # 库级属主与 schema 属主（幂等）→ 候选提交可能带新迁移 → 收敛运行角色权限
  bash /opt/pandora/deploy/bootstrap.sh roles >"$run/reset-roles.log" 2>&1 || die "bootstrap roles 失败，见 $run/reset-roles.log"
  migrate_up >"$run/reset-migrate.log" 2>&1 || die "迁移失败，见 $run/reset-migrate.log"
  bash /opt/pandora/deploy/bootstrap.sh grants >"$run/reset-grants.log" 2>&1 || die "bootstrap grants 失败，见 $run/reset-grants.log"
  # 订阅路径前缀是第一次用到时才生成落库的；起服务前钉一个虚构的固定值，每次运行一样，拉取阶梯直接用
  pg pandora <<<"INSERT INTO settings (key, value) VALUES ('subscription.path_prefix', to_jsonb('$GATE_SUB_PREFIX'::text)) ON CONFLICT (key) DO NOTHING;"
  redis_clear
  rm -rf "$G/nodesim-state"
  tar -C "$G" -xf "$BASE/nodesim-state.tar"
  pprof_dropin
  systemctl start pandora-serve pandora-worker
  wait_ready 90 || die "面板没就绪"
  [ "$PPROF" = 1 ] && echo "诊断运行：serve 开了 pprof（$PPROF_ADDR），只用来定位，不拿来比" >>"$run/notes.txt"
  return 0
}

#---- 采集

collect() { with_redis_auth python3 -I "$TOOLS/collect.py" "$@"; }

# window <运行目录> <名字> <命令...>：命令前后各取一次快照，起止时间记进 windows.jsonl
window() {
  local run=$1 name=$2 t0 t1
  shift 2
  collect snap --out "$run/snap/$name.0.json"
  t0=$(date +%s.%N)
  "$@"
  t1=$(date +%s.%N)
  collect snap --out "$run/snap/$name.1.json"
  heap_profile "$run" "$name"
  printf '{"name":"%s","t0":%s,"t1":%s}\n' "$name" "$t0" "$t1" >>"$run/windows.jsonl"
}

# heap_profile <运行目录> <窗口名>：GATE_PPROF=1 时取 serve 的堆剖析（二进制给 go tool pprof）与带 MemStats 的文本版
heap_profile() {
  [ "$PPROF" = 1 ] || return 0
  mkdir -p "$1/pprof"
  curl -fsS -m 30 -o "$1/pprof/$2.heap" "http://$PPROF_ADDR/debug/pprof/heap" || echo "取 $2 堆剖析失败" >>"$1/notes.txt"
  curl -fsS -m 30 -o "$1/pprof/$2.heap.txt" "http://$PPROF_ADDR/debug/pprof/heap?debug=1" || true
}

# pprof_dropin：按 GATE_PPROF 放上或撤掉 serve 的 pprof 运行时 drop-in（/run 下，重启即失效），起服务前调
pprof_dropin() {
  if [ "$PPROF" = 1 ]; then
    mkdir -p "$(dirname "$PPROF_DROPIN")"
    printf '[Service]\nEnvironment=PANDORA_HTTP_PUBLIC_PPROF_ADDR=%s\n' "$PPROF_ADDR" >"$PPROF_DROPIN"
  else
    rm -f "$PPROF_DROPIN"
  fi
  systemctl daemon-reload
}

#---- 场景

scenario_idle() { sleep "$IDLE_SECS"; }

# 行为趋势曲线读汇总表：量之前等统计任务在重置后跑过一轮（*/10），两边比的是同一种状态
wait_activity_refresh() {
  local since=$1 i ok
  for i in $(seq 1 72); do
    ok=$(pgval pandora "SELECT coalesce(max(computed_at) > to_timestamp($since), false) FROM activity_daily")
    [ "$ok" = t ] && return 0
    sleep 10
  done
  return 1
}

scenario_admin() {
  local run=$1
  ADMIN_BASE_URL=http://127.0.0.1:9001 ADMIN_PASSWORD_FILE="$SECRETS/admin_pass" ADMIN_EMAIL="$(cat "$SECRETS/admin_email")" \
    RATE=10 DURATION=70s PRE_VUS=20 MAX_VUS=100 \
    "$G/bin/k6" run --quiet --no-color --summary-export "$run/k6/admin.json" "$TOOLS/k6/admin.js" \
    >"$run/k6/admin.log" 2>&1 || echo "k6 admin 退出非零（阈值没过也会这样），以统计为准" >>"$run/notes.txt"
}

# pull_step <运行目录> <速率>
pull_step() {
  local run=$1 rate=$2 pre max
  pre=$((rate * 2 > 50 ? rate * 2 : 50))
  max=$((rate * 5 > 200 ? rate * 5 : 200))
  BASE_URL=http://127.0.0.1:9000 SUB_PREFIX="$SUB_PREFIX" USERS_CSV="$DATA/users.csv" RATE="$rate" DURATION="${PULL_SECS}s" \
    PRE_VUS="$pre" MAX_VUS="$max" \
    "$G/bin/k6" run --quiet --no-color --summary-export "$run/k6/pull-$rate.json" "$TOOLS/k6/sub_pull.js" \
    >"$run/k6/pull-$rate.log" 2>&1 || echo "k6 pull-$rate 退出非零（阈值没过也会这样），以统计为准" >>"$run/notes.txt"
}

scenario_pull() {
  local run=$1 rate
  SUB_PREFIX=$(pgval pandora "SELECT value #>> '{}' FROM settings WHERE key = 'subscription.path_prefix'")
  [[ $SUB_PREFIX =~ ^[0-9a-f]{12}$ ]] || die "读不到订阅路径前缀"
  for rate in $PULL_RATES; do
    window "$run" "pull-$rate" pull_step "$run" "$rate"
    if python3 -I "$TOOLS/collect.py" pull-saturated --k6 "$run/k6/pull-$rate.json" --rate "$rate" --secs "$PULL_SECS"; then
      echo "pull: $rate/s 已饱和，不再加压" >>"$run/notes.txt"
      break
    fi
    sleep 20
  done
}

# reconnect_trial <运行目录> <序号>：重启 serve，量全部节点重新连上用了多久、建流对齐超时几次
reconnect_trial() {
  local run=$1 k=$2 t0 off
  off=$(stat -c %s "$LOGDIR/serve.log" 2>/dev/null || echo 0)
  python3 -I "$TOOLS/collect.py" nodes --status "$run/nodesim-status.json" >"$run/reconnect-$k.before.json"
  t0=$(date +%s.%N)
  systemctl restart pandora-serve
  wait_ready 120 || die "重启后面板没就绪"
  local t_ready
  t_ready=$(date +%s.%N)
  python3 -I "$TOOLS/collect.py" wait-connected --status "$run/nodesim-status.json" --want "$SERVERS" \
    --since "$t0" --timeout 180 >"$run/reconnect-$k.wait.json" || echo "reconnect-$k: 180 秒内没有全部连回" >>"$run/notes.txt"
  # 名单与配置送达后再数全量、增量
  sleep 20
  python3 -I "$TOOLS/collect.py" nodes --status "$run/nodesim-status.json" >"$run/reconnect-$k.after.json"
  local busy
  busy=$(tail -c +"$((off + 1))" "$LOGDIR/serve.log" | grep -c '建流对齐超时' || true)
  printf '{"t0":%s,"t_ready":%s,"align_busy":%s}\n' "$t0" "$t_ready" "${busy:-0}" >"$run/reconnect-$k.json"
}

scenario_reconnect() {
  local run=$1 k
  for k in $(seq 1 "$RECONNECTS"); do
    window "$run" "reconnect-$k" reconnect_trial "$run" "$k"
    sleep "$SETTLE_SECS"
  done
}

#---- 一次完整运行

# RUN 是这次运行的目录；放全局，退出时的 trap 也要用
RUN=
on_exit() {
  [ -n "$RUN" ] || return 0
  stop_k6
  stop_nodesim
  collect sample-stop --pidfile "$RUN/sampler.pid" || true
  [ -e "$RUN/done" ] || touch "$RUN/failed"
}

cmd_run() {
  local label=${1:?用法: run <label> <sha12>} sha=${2:?用法: run <label> <sha12>} run
  [[ $label =~ ^[A-Za-z0-9._-]+$ ]] || die "label 只能用字母、数字、点、下划线、连字符"
  run=$G/runs/$label
  [ ! -e "$run" ] || die "$run 已存在，换一个 label"
  mkdir -p "$run/snap" "$run/k6"
  RUN=$run
  trap on_exit EXIT
  cmd_check >"$run/check.txt"
  local t_start
  t_start=$(date +%s.%N)
  log "run $label: 重置（$sha）"
  reset "$sha" "$run"
  local t_reset
  t_reset=$(date +%s)
  collect meta --label "$label" --sha "$sha" --binary /opt/pandora/bin/pandora --servers "$SERVERS" --users "$USERS" \
    --started "$t_start" >"$run/meta.json"
  collect sample-start --out "$run/samples.jsonl" --pidfile "$run/sampler.pid"

  log "run $label: nodesim 连入"
  local t_conn
  t_conn=$(date +%s.%N)
  start_nodesim "$run/nodesim-status.json" "$run/nodesim.log"
  python3 -I "$TOOLS/collect.py" wait-connected --status "$run/nodesim-status.json" --want "$SERVERS" \
    --since "$t_conn" --timeout 300 >"$run/connect.json" || die "nodesim 300 秒内没有全部连上"
  sleep "$SETTLE_SECS"

  log "run $label: 静默 ${IDLE_SECS}s"
  window "$run" idle scenario_idle

  log "run $label: 等统计任务跑过一轮"
  wait_activity_refresh "$t_reset" || echo "统计任务 12 分钟内没在重置后跑过" >>"$run/notes.txt"
  log "run $label: 后台读"
  window "$run" admin scenario_admin "$run"

  log "run $label: 订阅拉取阶梯（$PULL_RATES）"
  scenario_pull "$run"

  log "run $label: 重启重连 ×$RECONNECTS"
  sleep "$SETTLE_SECS"
  scenario_reconnect "$run"

  collect sample-stop --pidfile "$run/sampler.pid"
  stop_nodesim
  cp "$LOGDIR/serve.log" "$run/serve.log"
  cp "$LOGDIR/worker.log" "$run/worker.log"
  log "run $label: 汇总"
  python3 -I "$TOOLS/collect.py" report --run "$run" --access-stats "$TOOLS/access_stats.py" --pull-secs "$PULL_SECS" >"$run/result.json"
  touch "$run/done"
  log "run $label: 完成"
}

case ${1:-} in
  check) cmd_check ;;
  build) shift; cmd_build "$@" ;;
  seed) cmd_seed ;;
  snapshot) cmd_snapshot ;;
  run) shift; cmd_run "$@" ;;
  *) sed -n '2,12p' "$0" >&2; exit 2 ;;
esac
