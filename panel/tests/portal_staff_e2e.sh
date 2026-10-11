#!/usr/bin/env bash
# 管理员不能登录门户（w5account，用户 2026-10-07 定）与登录失败审计限频：
#   - 持有后台角色的账号用正确口令登门户：403，提示去管理后台的登录入口，不带任何后台路径；
#   - 口令不对时照旧是「邮箱或密码不正确」（禁登提示不能拿来枚举管理员）；
#   - 同一账号照常能登后台；普通用户照常能登门户；
#   - 登录失败记审计（user.login_failed），同一来源 IP 在窗口内只记一条。
#
# 会留下审计，只许在一次性库上跑：PORTAL_STAFF_E2E_DISPOSABLE=YES_DELETE_FIXTURES，
# 库名带独立的 test / e2e 段。来源 IP 用 X-Real-IP 扮演（冒烟栈直接信它）。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
json(){ curl -s -H 'Content-Type: application/json' "$@"; }
status_of(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }

[ "${PORTAL_STAFF_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] PORTAL_STAFF_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$("$PSQL" -X -qAtc 'SELECT current_database()' | tr -d '[:space:]')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }

# 每次跑用一段新的文档用 IP（TEST-NET-2），免得撞上前一轮窗口里的记录
NET="198.51.100"
IP_STAFF="$NET.$((RANDOM % 50 + 10))"
IP_THROTTLE="$NET.$((RANDOM % 50 + 100))"

#-------------------------------------------------------------------------------
sec "1. 管理员用正确口令登门户被拦"

R=$(json -X POST "$PUB/v1/auth/login" -H "X-Real-IP: $IP_STAFF" \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" -w '\n%{http_code}')
BODY=$(echo "$R" | sed '$d'); C=$(echo "$R" | tail -1)
[ "$C" = 403 ] && ok "门户登录回 403" || bad "管理员竟能登录门户" "HTTP $C"
echo "$BODY" | grep -q '管理后台的登录入口' && ok "提示引导去管理后台的登录入口" || bad "提示文案不对" "$BODY"
echo "$BODY" | grep -q 'access_token' && bad "响应里带了令牌" "" || ok "响应不含令牌"
ADM_PATH=$(python3 -c "import sys,urllib.parse as u;print(u.urlsplit(sys.argv[1]).path.strip('/'))" "$ADM")
if echo "$BODY" | python3 -c "import sys,json;m=json.load(sys.stdin)['error']['message'];sys.exit(0 if ('/' not in m and 'http' not in m) else 1)"; then
  ok "提示里没有任何路径或地址${ADM_PATH:+（后台前缀不外泄）}"
else
  bad "提示里出现了路径或地址" "$BODY"
fi

#-------------------------------------------------------------------------------
sec "2. 口令不对时与普通失败一样"

R=$(json -X POST "$PUB/v1/auth/login" -H "X-Real-IP: $IP_STAFF" \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-password-e2e\"}" -w '\n%{http_code}')
BODY=$(echo "$R" | sed '$d'); C=$(echo "$R" | tail -1)
[ "$C" = 401 ] && echo "$BODY" | grep -q '邮箱或密码不正确' && ! echo "$BODY" | grep -q '管理' \
  && ok "错误口令回 401「邮箱或密码不正确」" || bad "错误口令的回应可被用来枚举管理员" "HTTP $C $BODY"

#-------------------------------------------------------------------------------
sec "3. 后台照常；普通用户照常登门户"

ATOK=$(json -X POST "$ADM/v1/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "同一账号登后台成功" || bad "管理员登不进后台" ""
UE="staffe2e_$(date +%s)_$RANDOM@example.com"
R=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"$UE\"}")
RT=$(echo "$R" | jqr "['registration_token']"); CD=$(echo "$R" | jqr "['dev_code']")
json -X POST "$PUB/v1/auth/register/complete" \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CD\",\"password\":\"Staff!User2026\"}" >/dev/null
C=$(status_of -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$UE\",\"password\":\"Staff!User2026\"}")
[ "$C" = 200 ] && ok "普通用户照常登门户" || bad "普通用户登门户失败" "HTTP $C"

#-------------------------------------------------------------------------------
sec "4. 登录失败审计：被拦与口令错都记，同一 IP 窗口内只记一条"

N=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events a JOIN users u ON u.id = a.resource_id
  WHERE u.email = '$ADMIN_EMAIL' AND a.action = 'user.login_failed' AND a.error_code = 'staff_portal_blocked'
    AND a.occurred_at > now() - interval '10 minutes'" | tr -d '[:space:]')
[ "${N:-0}" -ge 1 ] && ok "门户禁登记了审计（staff_portal_blocked）" || bad "门户禁登没有审计" "count=$N"

before=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events WHERE action = 'user.login_failed'" | tr -d '[:space:]')
for i in 1 2 3 4 5; do
  json -X POST "$PUB/v1/auth/login" -H "X-Real-IP: $IP_THROTTLE" \
    -d "{\"email\":\"nobody${i}_$RANDOM@example.test\",\"password\":\"wrong-password-e2e\"}" >/dev/null
done
after=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events WHERE action = 'user.login_failed'" | tr -d '[:space:]')
[ $((after - before)) -eq 1 ] && ok "同一 IP 撞 5 个账号只记 1 条失败审计" || bad "失败审计没有按 IP 聚合" "delta=$((after - before))"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" -eq 0 ]
