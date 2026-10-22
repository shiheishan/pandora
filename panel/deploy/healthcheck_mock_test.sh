#!/usr/bin/env bash
# healthcheck.sh 的桩测试：不需要 root、nginx、PostgreSQL、Redis 或网络。
# ① 证书一节（check_tls）：以 HEALTHCHECK_LIB=1 source 它只取函数，把「nginx 在回环 443 上下发的证书」换成临时签的证书：
#   主机取对外地址（域名与公网 IPv4 都查）；阈值按寿命缩放（剩余 < 寿命 1/3，最多 14 天）：
#   6 天的 IP 证书刚签、过半都不报，只剩 1 天才报；90 天证书剩 20 天不报、剩 10 天报；过期报；
#   续期 timer 的结论 RESULT=error 报、超过 36 小时没跑报；没走 HTTPS 边缘不查。
# ② 查库（db_query）：直接以 psql 经 127.0.0.1:POSTGRES_PORT 用 postgres 连，口令只经 PGPASSWORD、不进参数；
#   每条查询带 statement_timeout；凭据不全或已判为连不上时不去连。Redis 连接串的解析。
# ③ 主流程整体跑（桩掉 systemctl、curl、df、stat、timeout、psql、redis-cli 与 pandora）：全部正常记 OK；
#   每个告警分支各跑一遍——服务不在、/readyz 不通、Redis 连接串读不出 / 连不上、worker 与调度器没心跳或读不出、
#   数据库连不上（只报一条、后面不再去连）、磁盘、备份目录读不出 / 没备份 / 太旧 / 太小、计数查询超时或答非所问、
#   通知积压与失败、节点集体失联；告警经 Telegram 推送时令牌不进命令行参数。
# ④ 每条告警文案都能在 RUNBOOK.md 的「巡检告警索引」里找到。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'healthcheck: %s\n' "$*" >&2; exit 1; }
MK="$DEPLOY/fixtures/mkcert-test.sh"
utc_days() {
  local n="$1"; [[ "$n" == -* ]] || n="+$n"
  date -u -d "$n days" +%Y%m%d%H%M%SZ 2>/dev/null || date -u -v"${n}d" +%Y%m%d%H%M%SZ
}

export EDGE_CONF="$T/pandora.conf" TLS_STATUS_FILE="$T/status"
HEALTHCHECK_LIB=1 . "$DEPLOY/healthcheck.sh"
set -euo pipefail
declare -F check_tls served_cert >/dev/null || fail 'library mode did not define check_tls'
# 换掉探测：按 SERVED 指向的文件下发证书，并记下用的主机
served_cert() { printf '%s\n' "$1" >"$T/probed"; cat "$SERVED" 2>/dev/null || true; }

ip=203.0.113.10
domain=panel.example.test
touch "$EDGE_CONF"
run() {
  PROBLEMS=(); check_tls; : >"$T/problems"
  [ "${#PROBLEMS[@]}" -eq 0 ] || printf '%s\n' "${PROBLEMS[@]}" >"$T/problems"
}
none() { run; [ ! -s "$T/problems" ] || fail "$1: unexpected problems: $(cat "$T/problems")"; }
some() { run; grep -q "$2" "$T/problems" || fail "$1: want '$2', got: $(cat "$T/problems")"; }
cert() { bash "$MK" "$T/c.pem" "$T/k.pem" "$1" "$2" window "$(utc_days "$3")" "$(utc_days "$4")"; SERVED="$T/c.pem"; }

# IP 部署：主机取自对外地址（以前从 server_name 用域名正则取，IP 时根本不查）
PUBLIC_BASE_URL="https://$ip/"
cert "$ip" "IP:$ip" 0 6;   none 'fresh 6-day IP certificate'
[ "$(cat "$T/probed")" = "$ip" ] || fail "probed host: $(cat "$T/probed")"
cert "$ip" "IP:$ip" -3 3;  none 'IP certificate at half-life (renewal window, not an alarm)'
cert "$ip" "IP:$ip" -5 1;  some 'IP certificate with 1 day left' '小时到期'
cert "$ip" "IP:$ip" -7 -1; some 'expired IP certificate' '已过期'
# 域名：90 天证书按 14 天线
PUBLIC_BASE_URL="https://$domain"
cert "$domain" "DNS:$domain" -70 20; none '90-day certificate with 20 days left'
cert "$domain" "DNS:$domain" -80 10; some '90-day certificate with 10 days left' '小时到期'
[ "$(cat "$T/probed")" = "$domain" ] || fail 'domain host not probed'
# 取不到证书
SERVED="$T/missing.pem"; some 'nothing served on 443' '取不到'

# 续期 timer 的结论
cert "$domain" "DNS:$domain" -10 80
now_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'RESULT=ok\nMESSAGE=证书正常\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; none 'renewal ok'
printf 'RESULT=warn\nMESSAGE=按设置只用自签证书（PANDORA_ACME=0）\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; none 'opt-out warn is not an alarm'
printf 'RESULT=error\nMESSAGE=lego 续期失败\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; some 'renewal error' 'lego 续期失败'
old_iso="$(date -u -d '-2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-2d +%Y-%m-%dT%H:%M:%SZ)"
printf 'RESULT=ok\nMESSAGE=证书正常\nCHECKED_AT=%s\n' "$old_iso" >"$TLS_STATUS_FILE"; some 'stale renewal timer' '36 小时没跑'
rm -f "$TLS_STATUS_FILE"

# 没走 HTTPS 边缘：不查
rm -f "$EDGE_CONF"; SERVED="$T/missing.pem"; none 'no edge config'
touch "$EDGE_CONF"; PUBLIC_BASE_URL=http://127.0.0.1:9000; none 'non-https base URL'

# 主流程调用它，旧的 server_name 域名正则已经删掉
grep -qx 'check_tls' "$DEPLOY/healthcheck.sh" || fail 'main flow does not call check_tls'
if grep -q 'server_name \\K' "$DEPLOY/healthcheck.sh"; then fail 'old server_name regex still present'; fi

# --- ② 查库：直接 psql，口令只经 PGPASSWORD；每条查询带 statement_timeout ---------------------------------
mkdir -p "$T/root/deploy" "$T/bin"
cp "$DEPLOY/healthcheck.sh" "$T/root/deploy/healthcheck.sh"
printf '#!/usr/bin/env bash\nprintf "psql %%s\\n" "$*" >>"%s/calls"\nprintf "PGO=%%s PW=%%s\\n" "${PGOPTIONS:-}" "${PGPASSWORD:-}" >>"%s/calls"\n' "$T" "$T" >"$T/bin/psql"
printf '#!/usr/bin/env bash\nprintf "runuser %%s\\n" "$*" >>"%s/calls"\n' "$T" >"$T/bin/runuser"
chmod +x "$T/bin/psql" "$T/bin/runuser"
: >"$T/calls"
(
  unset HEALTHCHECK_ROOT
  PATH="$T/bin:$PATH"
  HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"
  [ "$ROOT" = "$T/root" ] || { echo "ROOT=$ROOT, want $T/root" >&2; exit 1; }
  PG_PORT=5432 PG_DB=pandora PG_PASSWORD=pw-fixture
  db_query 'SELECT 1'
) || fail 'install root or query path wrong'
grep -qx 'psql -h 127.0.0.1 -p 5432 -U postgres -d pandora -X -tAc SELECT 1' "$T/calls" || fail "db_query arguments: $(cat "$T/calls")"
grep -qx 'PGO=-c statement_timeout=10s PW=pw-fixture' "$T/calls" || fail "db_query does not bound the query time or pass the password by env: $(cat "$T/calls")"
if grep -q 'pw-fixture' <(grep '^psql ' "$T/calls"); then fail 'the database password reached the psql command line'; fi
# 调用方环境里的 PGOPTIONS 不能把查询时限冲掉
: >"$T/calls"
( export PGOPTIONS='-c statement_timeout=0'; PATH="$T/bin:$PATH"; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"
  PG_PORT=5432 PG_DB=pandora PG_PASSWORD=pw-fixture; db_query 'SELECT 3' ) >/dev/null
grep -qx 'PGO=-c statement_timeout=10s PW=pw-fixture' "$T/calls" || fail "a caller PGOPTIONS overrides the query timeout: $(cat "$T/calls")"
# 已判为连不上（DB_DOWN=1）或凭据不全：不去连
: >"$T/calls"
if ( PATH="$T/bin:$PATH"; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"; PG_PORT=5432 PG_DB=pandora PG_PASSWORD=pw; DB_DOWN=1; db_query 'SELECT 4' ) >/dev/null 2>&1; then
  fail 'db_query succeeded after the database was found down'
fi
if ( PATH="$T/bin:$PATH"; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"; PG_PORT=5432 PG_DB=pandora PG_PASSWORD=; db_query 'SELECT 5' ) >/dev/null 2>&1; then
  fail 'db_query succeeded without a password'
fi
[ ! -s "$T/calls" ] || fail "db_query still connects when it should not: $(cat "$T/calls")"
grep -Eq 'note "数据库连不上' "$DEPLOY/healthcheck.sh" || fail 'main flow does not note database unreachable after SELECT 1 fails'
awk '
  /^db_query\(\)/ { in_fn = 1; next }
  in_fn && /^}/ { in_fn = 0; next }
  in_fn { next }
  /if ! db_query .SELECT 1/ { in_if = 1; next }
  in_if {
    if (/^[[:space:]]*DB_DOWN=1[[:space:]]*$/) n++
    if (/^fi$/) {
      if (n != 1) {
        printf "expected exactly one DB_DOWN=1 between SELECT 1 failure if and fi, found %d\n", n > "/dev/stderr"
        exit 1
      }
      in_if = 0; ok = 1
    }
    next
  }
  /^[[:space:]]*DB_DOWN=1[[:space:]]*$/ {
    printf "DB_DOWN=1 outside db_query() must only appear inside the SELECT 1 failure block (line %d)\n", NR > "/dev/stderr"
    exit 1
  }
  END {
    if (!ok) {
      print "main flow missing if ! db_query SELECT 1 block that sets DB_DOWN=1 once" > "/dev/stderr"
      exit 1
    }
  }' "$DEPLOY/healthcheck.sh" || fail 'main flow does not mark the database down only inside the SELECT 1 failure block'
# 备份目录取 pandora backup dir（config 的 backup.dir），不另写一份缺省
grep -Fq 'BK="${HEALTHCHECK_BACKUP_DIR:-$("$PANDORA_BIN" backup dir 2>/dev/null)}"' "$DEPLOY/healthcheck.sh" \
  || fail 'backup directory is not taken from pandora backup dir'

# Redis 连接串解析：口令（含 %XX）、用户、库号、rediss
(
  HEALTHCHECK_LIB=1 . "$DEPLOY/healthcheck.sh"
  parse_redis_url 'redis://:p%40ss@127.0.0.1:6380/2' || exit 1
  [ "$REDIS_HOST:$REDIS_PORT:$REDIS_DB:$REDIS_PASS:$REDIS_USER:$REDIS_TLS" = '127.0.0.1:6380:2:p@ss::0' ] || { echo "parsed $REDIS_HOST:$REDIS_PORT:$REDIS_DB:$REDIS_PASS:$REDIS_USER:$REDIS_TLS" >&2; exit 1; }
  parse_redis_url 'rediss://app:pw@redis.example.test' || exit 1
  [ "$REDIS_HOST:$REDIS_PORT:$REDIS_DB:$REDIS_USER:$REDIS_TLS" = 'redis.example.test:6379:0:app:1' ] || { echo "parsed tls $REDIS_HOST:$REDIS_PORT:$REDIS_USER:$REDIS_TLS" >&2; exit 1; }
  if parse_redis_url 'http://127.0.0.1:6379'; then echo 'accepted a non-redis URL' >&2; exit 1; fi
  if parse_redis_url ''; then echo 'accepted an empty URL' >&2; exit 1; fi
) || fail 'parse_redis_url'

# --- ③ 主流程整体跑：每个告警分支 ---------------------------------------------------------------------
# 桩的开关都在 $MF/k/ 下（每个场景开头清空）：
#   inactive.<单元> 该单元 is-active 答 failed；readyz.<端口> 该端口 /readyz 答文件里的状态码（空 = 无响应）；
#   redis.down PING 不答 PONG；servers / schedulers ZCOUNT 答文件里的值（缺省 1，「err」= 报错退出）；
#   df.use 磁盘已用百分比（缺省 10）；bk.age_s 最新备份距今秒数（缺省 0）；bk.size 字节数（缺省 20000）；
#   bkdir.fail pandora backup dir 失败。psql 桩同旧：fail-pattern 命中的查询按 fail-mode 出错，values 按子串给计数
MF="$T/mf"
mkdir -p "$MF/deploy" "$MF/bin" "$MF/backups" "$MF/etc" "$MF/k"
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
printf 'POSTGRES_PORT=5432\nPOSTGRES_DB=pandora\nPOSTGRES_SUPER_PASSWORD=pg-fixture\n' >"$MF/etc/deploy.env"
printf 'PANDORA_REDIS_URL=redis://:redis-fixture@127.0.0.1:6379/0\n' >"$MF/etc/secrets.env"
: >"$MF/backups/pandora-postgres-20261010T000000Z.dump.age"
cat >"$MF/bin/psql" <<PSQL
#!/usr/bin/env bash
q="\${*: -1}"
printf '%s\n' "\$q" >>"$MF/queries"
pat="\$(cat "$MF/fail-pattern" 2>/dev/null || true)"
if [ -n "\$pat" ] && [[ "\$q" == *"\$pat"* ]]; then
  case "\$(cat "$MF/fail-mode" 2>/dev/null)" in
    empty) exit 0 ;;
    partial) echo 0; exit 1 ;;
    *) echo 'ERROR:  canceling statement due to statement timeout' >&2; exit 1 ;;
  esac
fi
[ "\$q" = 'SELECT 1' ] && { echo 1; exit 0; }
if [ -f "$MF/values" ]; then
  while IFS='|' read -r sub val || [ -n "\$sub" ]; do
    [ -n "\$sub" ] && [[ "\$q" == *"\$sub"* ]] && { echo "\$val"; exit 0; }
  done <"$MF/values"
fi
echo 0
PSQL
cat >"$MF/bin/systemctl" <<STUB
#!/usr/bin/env bash
[ "\$1" = is-active ] || exit 0
[ -f "$MF/k/inactive.\$2" ] && { echo failed; exit 3; }
echo active
STUB
# curl：-K 的是 Telegram 推送（配置从标准输入读），记下参数与标准输入；其余是 /readyz 探测
cat >"$MF/bin/curl" <<STUB
#!/usr/bin/env bash
case " \$* " in
  *" -K "*) printf '%s\n' "\$*" >>"$MF/tg.args"; cat >>"$MF/tg.stdin"; exit 0 ;;
esac
url="\${*: -1}"; port="\${url#http://127.0.0.1:}"; port="\${port%%/*}"
if [ -f "$MF/k/readyz.\$port" ]; then
  code="\$(cat "$MF/k/readyz.\$port")"; [ -n "\$code" ] || exit 7
  printf '%s' "\$code"; exit 0
fi
printf 200
STUB
cat >"$MF/bin/redis-cli" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >>"$MF/redis.args"
printf 'AUTH=%s\n' "\${REDISCLI_AUTH:-}" >>"$MF/redis.env"
cmd=""; for a in "\$@"; do case "\$a" in PING|ZCOUNT) cmd="\$a" ;; esac; done
case "\$cmd" in
  PING) [ -f "$MF/k/redis.down" ] && { echo 'Could not connect to Redis' >&2; exit 1; }; echo PONG ;;
  ZCOUNT)
    key=""; for a in "\$@"; do case "\$a" in asynq:servers) key=servers ;; asynq:schedulers) key=schedulers ;; esac; done
    v="\$(cat "$MF/k/\$key" 2>/dev/null || echo 1)"
    [ "\$v" = err ] && { echo 'ERR wrong type' >&2; exit 1; }
    echo "\$v" ;;
esac
STUB
printf '#!/usr/bin/env bash\nshift\nexec "$@"\n' >"$MF/bin/timeout"
cat >"$MF/bin/df" <<STUB
#!/usr/bin/env bash
u="\$(cat "$MF/k/df.use" 2>/dev/null)"
printf 'Use%%\n %s%%\n' "\${u:-10}"
STUB
# stat：-c %%Y 给「现在 - bk.age_s」，-c %%s 给 bk.size（GNU 与 BSD 的 stat 选项不同，桩掉免得依赖本机）
cat >"$MF/bin/stat" <<STUB
#!/usr/bin/env bash
case "\$2" in
  %Y) echo \$(( \$(date +%s) - \$(cat "$MF/k/bk.age_s" 2>/dev/null || echo 0) )) ;;
  %s) cat "$MF/k/bk.size" 2>/dev/null || echo 20000 ;;
esac
STUB
cat >"$MF/bin/pandora" <<STUB
#!/usr/bin/env bash
[ "\$*" = 'backup dir' ] || exit 2
[ -f "$MF/k/bkdir.fail" ] && { echo 'backup.dir 必须是规范的绝对路径' >&2; exit 1; }
echo "$MF/backups"
STUB
chmod +x "$MF/bin/"*
main_flow() { # <失败子串，空=全部成功> [timeout|empty|partial] → 设置 rc 与 out（health.log 末行加 stderr）
  printf '%s' "$1" >"$MF/fail-pattern"; printf '%s' "${2:-timeout}" >"$MF/fail-mode"; : >"$MF/queries"; rm -f "$MF/logs/health.log"
  [ -n "$1" ] && rm -f "$MF/values"
  set +e
  out="$(PATH="$MF/bin:$PATH" HEALTHCHECK_ROOT="$MF" HEALTHCHECK_LOG_DIR="$MF/logs" HEALTHCHECK_PANDORA_BIN="$MF/bin/pandora" \
    HEALTHCHECK_DEPLOY_ENV="$MF/etc/deploy.env" HEALTHCHECK_SECRETS_ENV="$MF/etc/secrets.env" HEALTHCHECK_CONFIG="$MF/etc/none.yaml" \
    EDGE_CONF="$MF/none.conf" TLS_STATUS_FILE="$MF/none.status" bash "$MF/deploy/healthcheck.sh" 2>&1)"
  rc=$?
  set -e
  out="$out"$'\n'"$(tail -n 1 "$MF/logs/health.log" 2>/dev/null)"
}
knobs() { rm -rf "$MF/k" "$MF/values"; mkdir -p "$MF/k"; }
# 一个场景：<说明> <期望的告警片段>；调用前用 knob 摆好开关
expect_alert() {
  main_flow ''
  [ "$rc" -ne 0 ] && grep -Fq "$2" <<<"$out" && grep -q 'ALERT' <<<"$out" || fail "$1: want alert '$2' (rc=$rc): $out"
  knobs
}
knob() { printf '%s' "${2-}" >"$MF/k/$1"; }

knobs
main_flow ''
[ "$rc" -eq 0 ] && grep -q ' OK$' <<<"$out" || fail "main flow with everything healthy did not log OK (rc=$rc): $out"
[ "$(grep -c . "$MF/queries")" -ge 5 ] || fail "main flow ran only these queries: $(cat "$MF/queries")"
# Redis 口令只经 REDISCLI_AUTH，不进参数；数的是还没过期的心跳
grep -qx 'AUTH=redis-fixture' "$MF/redis.env" || fail "redis-cli did not get the password by env: $(cat "$MF/redis.env")"
if grep -q 'redis-fixture' "$MF/redis.args"; then fail 'the Redis password reached the redis-cli command line'; fi
grep -Eq -- '-h 127\.0\.0\.1 -p 6379 -n 0 ZCOUNT asynq:servers [0-9]+ \+inf$' "$MF/redis.args" || fail "worker heartbeat query: $(cat "$MF/redis.args")"
grep -Eq 'ZCOUNT asynq:schedulers [0-9]+ \+inf$' "$MF/redis.args" || fail "scheduler heartbeat query: $(cat "$MF/redis.args")"

# 服务与 /readyz
knob inactive.pandora-serve;  expect_alert 'serve inactive' '服务 pandora-serve 状态为 failed'
knob inactive.pandora-worker; expect_alert 'worker inactive' '服务 pandora-worker 状态为 failed'
knob readyz.9000 503;         expect_alert 'public /readyz 503' 'public 端口 9000 探活返回 503'
knob readyz.9001 '';          expect_alert 'admin /readyz no answer' 'admin 端口 9001 探活返回 无响应'
# Redis 与 worker 心跳
mv "$MF/etc/secrets.env" "$MF/etc/secrets.env.keep"
expect_alert 'no Redis URL' '读不出 Redis 连接串'
mv "$MF/etc/secrets.env.keep" "$MF/etc/secrets.env"
knob redis.down;              expect_alert 'Redis down' 'Redis 连不上'
knob servers 0;               expect_alert 'no worker heartbeat' 'worker 没有心跳'
knob servers err;             expect_alert 'worker heartbeat unreadable' '读不出 worker 心跳'
knob schedulers 0;            expect_alert 'no scheduler heartbeat' '定时任务调度器没有心跳'
knob schedulers err;          expect_alert 'scheduler heartbeat unreadable' '读不出定时任务调度器心跳'
# 磁盘与备份
knob df.use 91;               expect_alert 'disk full' '/ 已用 91%'
knob bkdir.fail;              expect_alert 'backup dir unreadable' '读不出备份目录'
# 50.5 小时：healthcheck 先取 date 再取 stat，两次跨秒时整 50 小时会被除成 49（10-21 CI 偶发），留半小时余量
knob bk.age_s 181800;         expect_alert 'stale backup' '最新备份已经是 50 小时前的'
knob bk.size 512;             expect_alert 'tiny backup' '最新备份只有 512 字节，疑似空文件'
mv "$MF/backups/pandora-postgres-20261010T000000Z.dump.age" "$MF/backup.keep"
expect_alert 'no backup at all' "备份目录 $MF/backups 里没有任何备份"
mv "$MF/backup.keep" "$MF/backups/pandora-postgres-20261010T000000Z.dump.age"
# 别的名字的备份不算（备份脚本只写 pandora-postgres-*）
mv "$MF/backups/pandora-postgres-20261010T000000Z.dump.age" "$MF/backups/legacy-postgres-20261010T000000Z.dump.age"
expect_alert 'only an old-named backup' '里没有任何备份'
mv "$MF/backups/legacy-postgres-20261010T000000Z.dump.age" "$MF/backups/pandora-postgres-20261010T000000Z.dump.age"

# 计数查询：SELECT 1 过了，后面某条查询超时 → 告警，说出是哪项查不了
while IFS='|' read -r pat want_note; do
  main_flow "$pat"
  [ "$rc" -ne 0 ] && grep -q "$want_note" <<<"$out" && grep -q 'ALERT' <<<"$out" \
    || fail "a later query that timed out ($pat) was taken as no data (rc=$rc): $out"
done <<'PATS'
status='queued'|通知积压查询失败或超时
status='failed'|通知发送失败查询失败或超时
last_heartbeat_at > now() - interval '30 minutes'|节点心跳（最近 30 分钟）查询失败或超时
interval '7 days'|节点心跳（7 天内）查询失败或超时
PATS
# 两条心跳检查项名互换 → 超时断言必须变红
sed -e 's/节点心跳（最近 30 分钟）/节点心跳（__SWAP__）/' \
    -e 's/节点心跳（7 天内）/节点心跳（最近 30 分钟）/' \
    -e 's/节点心跳（__SWAP__）/节点心跳（7 天内）/' \
    "$DEPLOY/healthcheck.sh" >"$MF/deploy/healthcheck.sh"
main_flow "last_heartbeat_at > now() - interval '30 minutes'"
grep -q '节点心跳（最近 30 分钟）查询失败或超时' <<<"$out" \
  && fail "swapped heartbeat check item names did not break the per-item timeout assertion (rc=$rc): $out"
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
# 退出 0 但没有输出、输出了数字却非零退出，都不算查出了数
for mode in empty partial; do
  main_flow "status='failed'" "$mode"
  [ "$rc" -ne 0 ] && grep -q '通知发送失败查询失败或超时' <<<"$out" \
    || fail "a query that answered '$mode' was taken as a count (rc=$rc): $out"
done
# SELECT 1 失败 → 只报「数据库连不上」，后面的查询不再去连（也不重复报查询失败）
main_flow 'SELECT 1'
[ "$rc" -ne 0 ] && grep -q '数据库连不上' <<<"$out" || fail "SELECT 1 failing was not reported (rc=$rc): $out"
[ "$(grep -c . "$MF/queries")" -eq 1 ] || fail "queries still ran after SELECT 1 failed: $(cat "$MF/queries")"
if grep -q '查询失败或超时' <<<"$out"; then fail "a down database was reported once per query: $out"; fi

# 桩按子串返回计数，阈值分支各自告警或记 OK
threshold_flow() {
  printf '%s\n' "$@" >"$MF/values"
  main_flow ''
}
threshold_flow "status='queued'|250"
[ "$rc" -ne 0 ] && grep -q '有 250 条通知排队超过 30 分钟没发出去' <<<"$out" && grep -q 'ALERT' <<<"$out" \
  || fail "queued threshold 250 did not alert (rc=$rc): $out"
threshold_flow "status='failed'|60"
[ "$rc" -ne 0 ] && grep -q '最近六小时有 60 条通知发送失败' <<<"$out" && grep -q 'ALERT' <<<"$out" \
  || fail "failed threshold 60 did not alert (rc=$rc): $out"
threshold_flow "last_heartbeat_at > now() - interval '30 minutes'|0" "interval '7 days'|5"
[ "$rc" -ne 0 ] && grep -q '过去 30 分钟没有任何节点上报心跳（7 天内曾有 5 个在报）' <<<"$out" \
  || fail "heartbeat recent=0 ever=5 did not alert (rc=$rc): $out"
# 只看就绪与排空中、没删的服务器
grep -q "FROM servers WHERE deleted_at IS NULL AND status IN ('ready','draining') AND last_heartbeat_at" "$MF/queries" \
  || fail "heartbeat query does not filter live servers: $(cat "$MF/queries")"
threshold_flow "status='queued'|199" "status='failed'|49" "last_heartbeat_at > now() - interval '30 minutes'|3" "interval '7 days'|5"
[ "$rc" -eq 0 ] && grep -q ' OK$' <<<"$out" \
  || fail "sub-threshold counts should log OK (rc=$rc): $out"
threshold_flow "last_heartbeat_at > now() - interval '30 minutes'|0" "interval '7 days'|0"
[ "$rc" -eq 0 ] || fail "a panel that never had reporting servers should not alert (rc=$rc): $out"
# 回退：阈值比较放宽 → 变红（只改桩目录里的副本）
sed 's/\[ "$q" -lt 200 \]/[ "$q" -lt 300 ]/' "$DEPLOY/healthcheck.sh" >"$MF/deploy/healthcheck.sh"
threshold_flow "status='queued'|250"
if [ "$rc" -ne 0 ] && grep -q '有 250 条通知排队超过 30 分钟没发出去' <<<"$out"; then
  fail "rollback q<300 was expected to suppress alert on queued=250 but still alerted"
fi
sed 's/\[ "$recent" = 0 \]/[ "$recent" = 1 ]/' "$DEPLOY/healthcheck.sh" >"$MF/deploy/healthcheck.sh"
threshold_flow "last_heartbeat_at > now() - interval '30 minutes'|0" "interval '7 days'|5"
if [ "$rc" -ne 0 ] && grep -q '过去 30 分钟没有任何节点上报心跳' <<<"$out"; then
  fail "rollback recent=1 was expected to suppress heartbeat alert on recent=0 ever=5 but still alerted"
fi
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
knobs

# Telegram 推送：令牌经 curl 的标准输入配置（-K -），不进命令行参数
printf 'PANDORA_ALERT_TG_TOKEN=123456:tg-token-fixture\nPANDORA_ALERT_TG_CHAT=42\n' >>"$MF/etc/deploy.env"
rm -f "$MF/tg.args" "$MF/tg.stdin"
knob inactive.pandora-worker; expect_alert 'alert with Telegram' '服务 pandora-worker 状态为 failed'
grep -Fq 'url = "https://api.telegram.org/bot123456:tg-token-fixture/sendMessage"' "$MF/tg.stdin" || fail "Telegram config not passed on stdin: $(cat "$MF/tg.stdin" 2>/dev/null)"
grep -q 'chat_id=42' "$MF/tg.args" && grep -q 'pandora-worker' "$MF/tg.args" || fail "Telegram message: $(cat "$MF/tg.args" 2>/dev/null)"
if grep -q 'tg-token-fixture' "$MF/tg.args"; then fail 'the Telegram token reached the curl command line'; fi
# 正常时不推
rm -f "$MF/tg.args"; main_flow ''
[ "$rc" -eq 0 ] && [ ! -e "$MF/tg.args" ] || fail "a healthy run pushed an alert (rc=$rc)"

# --- ④ note 文案片段必须出现在 RUNBOOK 巡检告警索引里 ---------------------------------------------
_runbook_index_text() {
  awk '/^## 巡检告警索引$/{f=1;next} f && /^## /{exit} f{printf "%s\n", $0}' "$1"
}
_note_literal_fragments() {
  local text="$1" frag chunks
  chunks="$(printf '%s' "$text" | perl -pe 's/\$[0-9]+|\$[A-Za-z_]\w*|\$\{[^}]*\}|\$\(\([^)]*\)\)|\$\([^)]*\)/\n/g')"
  while IFS= read -r frag; do
    frag="${frag#"${frag%%[![:space:]]*}"}"
    frag="${frag%"${frag##*[![:space:]]}"}"
    [ "${#frag}" -ge 2 ] && printf '%s\n' "$frag"
  done <<<"$chunks"
}
alert_index_missing() {
  local hc="$1" rb="$2" index line text missing=0 frag item n_db=0
  index="$(_runbook_index_text "$rb")"
  while IFS= read -r line; do
    text="${line#*note \"}"
    text="${text%\"*}"
    text="${text%%（*}"
    while IFS= read -r frag; do
      [ -z "$frag" ] && continue
      if ! grep -Fq "$frag" <<<"$index"; then
        printf 'missing RUNBOOK index fragment from note: %s\n' "$frag"
        missing=1
      fi
    done < <(_note_literal_fragments "$text")
  done < <(grep -E 'note "' "$hc")
  while IFS= read -r item; do
    [ -z "$item" ] && continue
    n_db=$((n_db + 1))
    if ! grep -Fq "${item}查询失败或超时" <<<"$index"; then
      printf 'missing RUNBOOK index entry for db_count item: %s\n' "$item"
      missing=1
    fi
  done < <(perl -ne 'while (/db_count\s+\w+\s+"([^"]+)"/g) { print "$1\n" }' "$hc")
  [ "$n_db" -ge 4 ] || fail "alert_index_missing: expected at least 4 db_count check items, found $n_db"
  [ "$missing" -eq 1 ] && return 0
  return 1
}
if alert_index_missing "$DEPLOY/healthcheck.sh" "$DEPLOY/RUNBOOK.md"; then
  fail "healthcheck note fragments missing from RUNBOOK alert index (see above)"
fi
cp "$DEPLOY/healthcheck.sh" "$T/hc-index-test.sh"
printf '%s\n' 'note "完全新的告警文案"' >>"$T/hc-index-test.sh"
alert_index_missing "$T/hc-index-test.sh" "$DEPLOY/RUNBOOK.md" >/dev/null || fail "alert_index_missing self-test did not detect a new note"
cp "$DEPLOY/healthcheck.sh" "$T/hc-dbcount-test.sh"
printf '%s\n' 'if db_count o "订单积压" "SELECT 1"; then :; fi' >>"$T/hc-dbcount-test.sh"
alert_index_missing "$T/hc-dbcount-test.sh" "$DEPLOY/RUNBOOK.md" >/dev/null \
  || fail "alert_index_missing self-test did not detect a new db_count item"

printf 'healthcheck mock: PASS\n'
