#!/usr/bin/env bash
# healthcheck.sh 证书一节（check_tls）的桩测试：不需要 root、nginx 或网络。
# 以 HEALTHCHECK_LIB=1 source 它只取函数，把「nginx 在回环 443 上下发的证书」换成临时签的证书，验证：
#   主机取对外地址（域名与公网 IPv4 都查）；阈值按寿命缩放（剩余 < 寿命 1/3，最多 14 天）：
#   6 天的 IP 证书刚签、过半都不报，只剩 1 天才报；90 天证书剩 20 天不报、剩 10 天报；过期报；
#   续期 timer 的结论 RESULT=error 报、超过 36 小时没跑报；没走 HTTPS 边缘不查。
# 另验查库（db_query）：安装根目录取脚本自己的位置，只经 <根>/deploy/psql.sh 查；psql.sh 不在就算连不上，
# 没有别的退路（不再以 runuser 切到 postgres）。
# 末尾把主流程整体跑一遍（桩掉 systemctl、curl、df、stat 与 psql.sh）：全部查出 0 记 OK；SELECT 1 之后某条计数查询
# 超时、退出 0 却没输出、输出数字却非零退出，都要告警并说出是哪项（G4）；SELECT 1 失败只报一条、后面不再去连（G5）。
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

export EDGE_CONF="$T/aegis.conf" TLS_STATUS_FILE="$T/status"
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
AEGIS_PUBLIC_BASE_URL="https://$ip/"
cert "$ip" "IP:$ip" 0 6;   none 'fresh 6-day IP certificate'
[ "$(cat "$T/probed")" = "$ip" ] || fail "probed host: $(cat "$T/probed")"
cert "$ip" "IP:$ip" -3 3;  none 'IP certificate at half-life (renewal window, not an alarm)'
cert "$ip" "IP:$ip" -5 1;  some 'IP certificate with 1 day left' '小时到期'
cert "$ip" "IP:$ip" -7 -1; some 'expired IP certificate' '已过期'
# 域名：90 天证书按 14 天线
AEGIS_PUBLIC_BASE_URL="https://$domain"
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
touch "$EDGE_CONF"; AEGIS_PUBLIC_BASE_URL=http://127.0.0.1:9000; none 'non-https base URL'

# 主流程调用它，旧的 server_name 域名正则已经删掉
grep -qx 'check_tls' "$DEPLOY/healthcheck.sh" || fail 'main flow does not call check_tls'
if grep -q 'server_name \\K' "$DEPLOY/healthcheck.sh"; then fail 'old server_name regex still present'; fi

# 查库：安装根目录取脚本自己的位置，只经 deploy/psql.sh
mkdir -p "$T/root/deploy" "$T/bin"
cp "$DEPLOY/healthcheck.sh" "$T/root/deploy/healthcheck.sh"
printf '#!/usr/bin/env bash\nprintf "psql.sh %%s\\n" "$*" >>"%s/calls"\nprintf "PGO=%%s\\n" "${PGOPTIONS:-}" >>"%s/calls"\n' "$T" "$T" >"$T/root/deploy/psql.sh"
printf '#!/usr/bin/env bash\nprintf "runuser %%s\\n" "$*" >>"%s/calls"\n' "$T" >"$T/bin/runuser"
chmod +x "$T/root/deploy/psql.sh" "$T/bin/runuser"
: >"$T/calls"
(
  unset HEALTHCHECK_ROOT
  HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"
  [ "$ROOT" = "$T/root" ] || { echo "ROOT=$ROOT, want $T/root" >&2; exit 1; }
  db_query 'SELECT 1'
) || fail 'install root or query path wrong'
grep -qx 'psql.sh -X -tAc SELECT 1' "$T/calls" || fail "db_query did not go through psql.sh: $(cat "$T/calls")"
# I2：连上了但查询卡住（锁等待、IO 挂住）也不能把巡检挂到 systemd 超时：每条查询带 statement_timeout，
# 调用方环境里的 PGOPTIONS 不能把它冲掉
grep -qx 'PGO=-c statement_timeout=10s' "$T/calls" || fail "db_query does not bound the query time: $(cat "$T/calls")"
: >"$T/calls"
( export PGOPTIONS='-c statement_timeout=0'; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"; db_query 'SELECT 3' ) >/dev/null
grep -qx 'PGO=-c statement_timeout=10s' "$T/calls" || fail "a caller PGOPTIONS overrides the query timeout: $(cat "$T/calls")"
# 第一条 SELECT 1 已失败（DB_DOWN=1）：后面的查询不再去连，免得每条都再等一轮超时
: >"$T/calls"
if ( DB_DOWN=1; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"; DB_DOWN=1; db_query 'SELECT 4' ) >/dev/null 2>&1; then
  fail 'db_query succeeded after the database was found down'
fi
[ ! -s "$T/calls" ] || fail "db_query still connects after the database was found down: $(cat "$T/calls")"
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
# psql.sh 不在：查询失败（主流程记成「数据库连不上」），不退回 runuser
rm -f "$T/root/deploy/psql.sh"; : >"$T/calls"
if ( PATH="$T/bin:$PATH"; HEALTHCHECK_LIB=1 . "$T/root/deploy/healthcheck.sh"; db_query 'SELECT 2' ) 2>/dev/null; then
  fail 'db_query succeeded without psql.sh'
fi
[ ! -s "$T/calls" ] || fail "db_query fell back to something else: $(cat "$T/calls")"
grep -Fq 'BK=${AEGIS_BACKUP_DIR:-/var/backups/pandora}' "$DEPLOY/healthcheck.sh" || fail 'backup directory default is not /var/backups/pandora'

# --- 主流程整体跑一遍（G4、G5）：桩掉 systemctl、curl、df、stat 与 psql.sh，按查询内容决定成功、失败或超时 ---
# 「查出 0」与「查询失败 / 超时」必须分开：后者要告警（非零退出、health.log 记 ALERT），不能当成没数据记 OK
MF="$T/mf"
mkdir -p "$MF/deploy" "$MF/bin" "$MF/backups"
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
: >"$MF/deploy/.env"
: >"$MF/backups/aegis-postgres-20261010T000000Z.dump.age"
# psql.sh：$MF/fail-pattern 里的子串命中查询时按 $MF/fail-mode 出错：timeout = 非零退出、不输出（statement_timeout
# 取消就是这样）；empty = 退出 0 但没有输出；partial = 先输出一个数字再非零退出。其余计数查询输出 0
cat >"$MF/deploy/psql.sh" <<PSQL
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
printf '#!/usr/bin/env bash\necho active\n' >"$MF/bin/systemctl"
printf '#!/usr/bin/env bash\nprintf 200\n' >"$MF/bin/curl"
printf '#!/usr/bin/env bash\nprintf "Use%%%%\\n 10%%%%\\n"\n' >"$MF/bin/df"
# stat：-c %%Y 给「现在」，-c %%s 给 20000 字节（GNU 与 BSD 的 stat 选项不同，桩掉免得依赖本机）
printf '#!/usr/bin/env bash\ncase "$2" in %%Y) date +%%s ;; %%s) echo 20000 ;; esac\n' >"$MF/bin/stat"
chmod +x "$MF/deploy/psql.sh" "$MF/bin/"*
main_flow() { # <失败子串，空=全部成功> [timeout|empty|partial] → 设置 rc 与 out（health.log 末行加 stderr）
  printf '%s' "$1" >"$MF/fail-pattern"; printf '%s' "${2:-timeout}" >"$MF/fail-mode"; : >"$MF/queries"; rm -f "$MF/logs/health.log"
  [ -n "$1" ] && rm -f "$MF/values"
  set +e
  out="$(PATH="$MF/bin:$PATH" HEALTHCHECK_ROOT="$MF" AEGIS_BACKUP_DIR="$MF/backups" EDGE_CONF="$MF/none.conf" \
    TLS_STATUS_FILE="$MF/none.status" bash "$MF/deploy/healthcheck.sh" 2>&1)"
  rc=$?
  set -e
  out="$out"$'\n'"$(tail -n 1 "$MF/logs/health.log" 2>/dev/null)"
}
main_flow ''
[ "$rc" -eq 0 ] && grep -q ' OK$' <<<"$out" || fail "main flow with every query answering 0 did not log OK (rc=$rc): $out"
[ "$(grep -c . "$MF/queries")" -ge 5 ] || fail "main flow ran only these queries: $(cat "$MF/queries")"
# G4：SELECT 1 过了，后面某条查询超时 → 告警，说出是哪项查不了
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
# F7：两条心跳检查项名互换 → 超时断言必须变红
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
sed -e 's/节点心跳（最近 30 分钟）/节点心跳（__SWAP__）/' \
    -e 's/节点心跳（7 天内）/节点心跳（最近 30 分钟）/' \
    -e 's/节点心跳（__SWAP__）/节点心跳（7 天内）/' \
    "$MF/deploy/healthcheck.sh" >"$MF/hc-swapped.sh"
mv "$MF/hc-swapped.sh" "$MF/deploy/healthcheck.sh"
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
# G5：SELECT 1 失败 → 只报「数据库连不上」，后面的查询不再去连（也不重复报查询失败）
main_flow 'SELECT 1'
[ "$rc" -ne 0 ] && grep -q '数据库连不上' <<<"$out" || fail "SELECT 1 failing was not reported (rc=$rc): $out"
[ "$(grep -c . "$MF/queries")" -eq 1 ] || fail "queries still ran after SELECT 1 failed: $(cat "$MF/queries")"
if grep -q '查询失败或超时' <<<"$out"; then fail "a down database was reported once per query: $out"; fi

# F7：桩按子串返回计数，阈值分支各自告警或记 OK
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
threshold_flow "status='queued'|199" "status='failed'|49" "last_heartbeat_at > now() - interval '30 minutes'|3" "interval '7 days'|5"
[ "$rc" -eq 0 ] && grep -q ' OK$' <<<"$out" \
  || fail "sub-threshold counts should log OK (rc=$rc): $out"
# F7 回退：阈值比较放宽 → 变红（只改桩目录里的副本）
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
sed 's/\[ "$q" -lt 200 \]/[ "$q" -lt 300 ]/' "$MF/deploy/healthcheck.sh" >"$MF/hc-q.yml"
mv "$MF/hc-q.yml" "$MF/deploy/healthcheck.sh"
threshold_flow "status='queued'|250"
if [ "$rc" -ne 0 ] && grep -q '有 250 条通知排队超过 30 分钟没发出去' <<<"$out"; then
  fail "rollback q<300 was expected to suppress alert on queued=250 but still alerted"
fi
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
sed 's/\[ "$recent" = 0 \]/[ "$recent" = 1 ]/' "$MF/deploy/healthcheck.sh" >"$MF/hc-recent.yml"
mv "$MF/hc-recent.yml" "$MF/deploy/healthcheck.sh"
threshold_flow "last_heartbeat_at > now() - interval '30 minutes'|0" "interval '7 days'|5"
if [ "$rc" -ne 0 ] && grep -q '过去 30 分钟没有任何节点上报心跳' <<<"$out"; then
  fail "rollback recent=1 was expected to suppress heartbeat alert on recent=0 ever=5 but still alerted"
fi
cp "$DEPLOY/healthcheck.sh" "$MF/deploy/healthcheck.sh"
[ -z "$(git diff -- panel/deploy/healthcheck.sh)" ] || fail "healthcheck.sh must be unchanged after threshold rollbacks"

# F4：note 文案片段必须出现在 RUNBOOK 巡检告警索引里
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
  local hc="$1" rb="$2" index line text missing=0 frag
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
  [ "$missing" -eq 1 ] && return 0
  return 1
}
if alert_index_missing "$DEPLOY/healthcheck.sh" "$DEPLOY/RUNBOOK.md"; then
  fail "healthcheck note fragments missing from RUNBOOK alert index (see above)"
fi
cp "$DEPLOY/healthcheck.sh" "$T/hc-index-test.sh"
printf '%s\n' 'note "完全新的告警文案"' >>"$T/hc-index-test.sh"
alert_index_missing "$T/hc-index-test.sh" "$DEPLOY/RUNBOOK.md" || fail "alert_index_missing self-test did not detect a new note"

printf 'healthcheck mock: PASS\n'
