#!/usr/bin/env bash
# 管理控制台端到端（11 段）。
#
# 覆盖：跨域令牌、非管理员登后台、只读接口形状、未认证 401、高风险停用用户、审计、套餐 authoring、
#       降级开关、支付渠道启停、错误响应不泄露。
#
# 不测的东西及原因：
#   - 租户隔离：单租户，没有租户守卫。
#   - 权限码：后台权限只分有和没有，登录响应不带权限码列表。
#   - 会话表行数：会话在 Redis；吊销以门户令牌 GET /v1/me → 401/403 验证。
# 支付渠道由本脚本自建易支付渠道（adm-<STAMP>）；审计细节在 audit_events.data（00003_audit.sql）。
#
# SEC-009「超过 15 分钟无近期重认证」本脚本不造过期令牌（成本高）；口令登录算重认证，
# 覆盖由 auth_e2e.sh 与 Go 单测承担。本节只验刚登录可停用、缺原因 422、令牌失效。
#
# 仍用 SQL 的步骤及原因：
#   - feature_switches / users.status / audit_events / plan_node_pools：断言库内事实或比 API 更稳；
#   - 可服务节点夹具末尾 UPDATE servers/nodes：e2e job 无节点端，用 SQL 造发布用节点。
#
# 环境：PUB、ADM、PSQL、ADMIN_EMAIL、ADMIN_PASS；ADMIN_E2E_DISPOSABLE=YES_DELETE_FIXTURES。
set -euo pipefail

ADM=${ADM:-http://127.0.0.1:9001}
PUB=${PUB:-http://127.0.0.1:9000}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
STAMP="$(date +%s)$RANDOM"

pass=0; fail=0
E2E_PREFLIGHT_OK=0
MAIN_DONE=0
RESTORE_FAILED=0
REGISTRATION_ORIGINAL=
REGISTRATION_TOUCHED=0
PAY_CODE="adm-$STAMP"
PAY_KEY="E2E_KEY_$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
PAY_TOUCHED=0
AH=
AUID=

ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
errsig(){ python3 -c "import sys,json;e=json.load(sys.stdin)['error'];print(e['code']+'|'+e['message'])" 2>/dev/null; }
boolq(){ case "$("$PSQL" -X -qAtc "$1" | tr -d '[:space:]')" in t|true) echo true;; f|false) echo false;; *) echo unknown;; esac; }
curl(){ command curl -q --noproxy '*' "$@"; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }

is_uuid(){ [[ ${1:-} =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]; }

parse_registration_start(){
  printf '%s' "$1" | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not isinstance(d, dict): raise SystemExit(1)
token = d.get("registration_token")
verify = d.get("verification_required")
if not isinstance(token, str) or not token or type(verify) is not bool:
    raise SystemExit(1)
if verify:
    code = d.get("dev_code")
    if not isinstance(code, str) or not code: raise SystemExit(1)
else:
    if "dev_code" in d: raise SystemExit(1)
    code = "-"
print(token)
print("true" if verify else "false")
print(code)
'
}

registration_parser_selftest(){
  local got sample
  got=$(parse_registration_start '{"registration_token":"t","verification_required":false}' | tr -d '\r') \
    || return 1
  [[ "$got" == $'t\nfalse\n-' ]] || return 1
  got=$(parse_registration_start '{"registration_token":"t","verification_required":true,"dev_code":"123456"}' | tr -d '\r') \
    || return 1
  [[ "$got" == $'t\ntrue\n123456' ]] || return 1
  for sample in \
    '{"registration_token":"t","verification_required":true}' \
    '{"registration_token":"t","verification_required":true,"dev_code":""}' \
    '{"registration_token":"t"}' \
    '{"registration_token":"t","verification_required":null}' \
    '{"registration_token":"t","verification_required":"false"}' \
    '{"registration_token":"t","verification_required":0}' \
    '{"registration_token":"t","verification_required":false,"dev_code":"123456"}' \
    '{"registration_token":"t","verification_required":true,"dev_code":123456}' \
    '{malformed'; do
    if parse_registration_start "$sample" >/dev/null 2>&1; then
      return 1
    fi
  done
  return 0
}

require_loopback_url(){
  python3 -c '
import sys, urllib.parse
try:
    u = urllib.parse.urlsplit(sys.argv[1])
    ok = (u.scheme in {"http", "https"}
          and u.hostname in {"127.0.0.1", "::1"}
          and u.port is not None
          and u.username is None and u.password is None
          and u.path in {"", "/"} and not u.query and not u.fragment)
except ValueError:
    ok = False
raise SystemExit(0 if ok else 1)
' "$1" >/dev/null 2>&1 || { echo "[FATAL] $2 must be a loopback origin with an explicit port" >&2; exit 1; }
}

db_scalar(){
  local type=$1 query=$2 raw out
  raw=$("$PSQL" -X -v ON_ERROR_STOP=1 -qAtc \
    "COPY ($query) TO STDOUT WITH (FORMAT csv, NULL '\\N')") \
    || { echo '[FATAL] database scalar query failed' >&2; exit 1; }
  out=$(printf '%s' "$raw" | python3 -c '
import csv, io, sys
rows=list(csv.reader(io.StringIO(sys.stdin.read())))
if len(rows)!=1 or len(rows[0])!=1 or rows[0][0] in {"",r"\N"}: raise SystemExit(1)
sys.stdout.write(rows[0][0])
') || { echo '[FATAL] database scalar must be one non-null, non-empty cell' >&2; exit 1; }
  case "$type" in
    bool) case "$out" in t|true) out=true;; f|false) out=false;; *) echo '[FATAL] expected database boolean' >&2; exit 1;; esac ;;
    bool_pair)
      [[ "$out" =~ ^(t|f|true|false)\|(t|f|true|false)$ ]] \
        || { echo '[FATAL] expected database boolean pair' >&2; exit 1; }
      out=${out//true/t}; out=${out//false/f}
      ;;
    identifier) [[ "$out" =~ ^[A-Za-z_][A-Za-z0-9_-]*$ ]] || { echo '[FATAL] expected database identifier' >&2; exit 1; } ;;
    integer) [[ "$out" =~ ^[0-9]+$ ]] || { echo '[FATAL] expected database integer' >&2; exit 1; } ;;
    uuid) is_uuid "$out" || { echo '[FATAL] expected database UUID' >&2; exit 1; } ;;
    *) echo '[FATAL] unknown database scalar type' >&2; exit 1 ;;
  esac
  printf '%s' "$out"
}

restore_registration(){
  [ "$REGISTRATION_TOUCHED" = 1 ] || return 0
  local status
  status=$(curl --silent --connect-timeout 5 --max-time 20 -o /dev/null -w '%{http_code}' \
    -X POST "$ADM/v1/switches/auth.registration" -H "$AH" -H 'Content-Type: application/json' \
    -d "{\"enabled\":$REGISTRATION_ORIGINAL,\"reason\":\"admin E2E exact-state restore\"}") || return 1
  [ "$status" = 200 ] || return 1
  [ "$(db_scalar bool "SELECT enabled FROM feature_switches WHERE code='auth.registration'")" = "$REGISTRATION_ORIGINAL" ] || return 1
  REGISTRATION_TOUCHED=0
}

restore_payment_provider(){
  [ "$PAY_TOUCHED" = 1 ] || return 0
  local status
  status=$(curl --silent --connect-timeout 5 --max-time 20 -o /dev/null -w '%{http_code}' \
    -X POST "$ADM/v1/payment-providers/$PAY_CODE/toggle" -H "$AH" -H 'Content-Type: application/json' \
    -d '{"enabled":false,"accepting_new":false}') || return 1
  [ "$status" = 200 ] || return 1
  PAY_TOUCHED=0
}

cleanup(){
  local status=$?
  trap - EXIT INT TERM
  set +e
  if [ "$E2E_PREFLIGHT_OK" = 1 ] && [ -n "$AH" ]; then
    restore_registration || RESTORE_FAILED=1
    restore_payment_provider || RESTORE_FAILED=1
  fi
  if [ "$RESTORE_FAILED" -ne 0 ]; then
    echo '[FAIL] exact mutable-state restoration failed; discard the disposable database' >&2
    [ "$status" -ne 0 ] || status=1
  elif [ "$MAIN_DONE" = 1 ] && [ "$status" -eq 0 ]; then
    echo
    echo '==============================================='
    echo "  passed $pass checks, failed $fail checks"
    echo '  discard the acknowledged disposable database'
    echo '==============================================='
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

[ "${ADMIN_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] ADMIN_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] || { echo '[FATAL] ADMIN_EMAIL must be explicit' >&2; exit 1; }
[ -n "$ADMIN_PASS" ] || { echo '[FATAL] ADMIN_PASS must be explicit' >&2; exit 1; }
require_loopback_url "$ADM" ADM
require_loopback_url "$PUB" PUB
DB_NAME=$(db_scalar identifier 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }
# 注册 IP 限额是后台设置（缺省每 IP 每分钟 10 次），连着注册会撞 429：一次性库上先关掉（见 lib/signup.sh）
. "$(dirname "$0")/lib/signup.sh"
relax_signup_ip_limit
registration_parser_selftest \
  || { echo '[FATAL] registration response parser self-test failed' >&2; exit 1; }
REGISTRATION_ORIGINAL=$(db_scalar bool "SELECT enabled FROM feature_switches WHERE code='auth.registration'")
E2E_PREFLIGHT_OK=1

#-------------------------------------------------------------------------------
sec "1. 管理员登录"

curl -sf "$ADM/healthz" >/dev/null && ok "Admin 网关存活" || { bad "网关不可达" "$ADM"; exit 1; }

R=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
ATOK=$(echo "$R" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "管理员登录成功" || { bad "登录失败" "认证响应已隐藏"; exit 1; }
AH="Authorization: Bearer $ATOK"

ME=$(curl -s "$ADM/v1/me" -H "$AH")
AUID=$(echo "$ME" | jqr "['user_id']")
is_uuid "$AUID" && ok "GET /v1/me 返回管理员 id" || { bad "/v1/me 无有效 user_id" "API 响应已隐藏"; exit 1; }

if [ "$REGISTRATION_ORIGINAL" != true ]; then
  REGISTRATION_TOUCHED=1
  C=$(code -X POST "$ADM/v1/switches/auth.registration" -H "$AH" \
    -H 'Content-Type: application/json' \
    -d '{"enabled":true,"reason":"admin E2E temporary registration enable"}')
  [ "$C" = 200 ] || { bad "temporary registration enable failed" "HTTP $C"; exit 1; }
  [ "$(db_scalar bool "SELECT enabled FROM feature_switches WHERE code='auth.registration'")" = true ] \
    || { bad "temporary registration enable was not persisted" ""; exit 1; }
fi

C=$(code -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-password-xxxx\"}")
[ "$C" = "401" ] && ok "错误口令被拒（401）" || bad "错误口令返回 $C" ""

#-------------------------------------------------------------------------------
sec "2. EXT-001 令牌不可跨域"

UE="admtest_${STAMP}@example.test"
RR=$(curl -s -X POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' \
       -d "{\"email\":\"$UE\"}")
START_FIELDS=$(parse_registration_start "$RR" 2>/dev/null) \
  || { bad "Public registration start failed" "safe response contract was not satisfied"; exit 1; }
mapfile -t REG_FIELDS <<<"$START_FIELDS"
[ "${#REG_FIELDS[@]}" -eq 3 ] \
  || { bad "Public registration start failed" "safe response contract was not satisfied"; exit 1; }
RT=${REG_FIELDS[0]}; VR=${REG_FIELDS[1]}; CD=${REG_FIELDS[2]}
[ "$CD" = - ] && CD=
C=$(code -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CD\",\"password\":\"UserPass!2026ab\"}")
[ "$C" = 201 ] || { bad "Public registration completion failed" "HTTP $C"; exit 1; }
PTOK=$(curl -s -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$UE\",\"password\":\"UserPass!2026ab\"}" |
  python3 -c "import sys,json; print(json.load(sys.stdin).get('access_token',''))" 2>/dev/null) || PTOK=
[ -n "$PTOK" ] && ok "已备好普通用户与 Public 域令牌" || { bad "备用户失败" ""; exit 1; }

C=$(code "$ADM/v1/overview" -H "Authorization: Bearer $PTOK")
[ "$C" = "401" ] || [ "$C" = "403" ] \
  && ok "Public 域令牌访问 Admin 域被拒（$C）" \
  || bad "Public 令牌竟能访问管理域" "HTTP $C"

C=$(code "$PUB/v1/me" -H "$AH")
[ "$C" = "401" ] || [ "$C" = "403" ] \
  && ok "Admin 域令牌访问 Public 域被拒（$C）" \
  || bad "Admin 令牌竟能访问用户域" "HTTP $C"

#-------------------------------------------------------------------------------
sec "3. IAM-006 无角色账号不能登录管理域"

C=$(code -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"UserPass!2026ab\"}")
MSG=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"UserPass!2026ab\"}" | errsig)
WRONG=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"totally-wrong-pw\"}" | errsig)
[ "$C" = "401" ] && ok "普通用户登录管理域被拒（401）" || bad "普通用户登录管理域返回 $C" "API 响应已隐藏"
[ "$MSG" = "$WRONG" ] && ok "「无角色」与「口令错误」响应完全一致（IAM-006）" \
                      || bad "两种失败可区分，存在账号枚举侧信道" "API 响应已隐藏"

#-------------------------------------------------------------------------------
sec "4. 只读接口"

O=$(curl -s "$ADM/v1/overview" -H "$AH")
UT=$(echo "$O" | jqr "['users']['total']")
RT2=$(echo "$O" | python3 -c "
import sys,json
print(' '.join(f\"{r['currency']}:{r['total']}\" for r in json.load(sys.stdin)['revenue']) or 'none')" 2>/dev/null)
DRIFT=$(echo "$O" | jqr "['ledger_drift_accounts']")
[ -n "$UT" ] && ok "仪表盘：用户 $UT 人，累计收入 [$RT2]（分）" || bad "仪表盘异常" "API 响应已隐藏"

NCUR=$(echo "$O" | python3 -c "
import sys,json;d=json.load(sys.stdin)['revenue']
cs=[r['currency'] for r in d]
print('dup' if len(cs)!=len(set(cs)) else len(cs))" 2>/dev/null)
[ "$NCUR" != "dup" ] && ok "收入按币种分组，无重复币种（$NCUR 种）" || bad "同一币种出现多行" ""
[ "$DRIFT" = "0" ] && ok "账本无漂移账户（PAY-005）" || bad "账本漂移 $DRIFT 个账户" ""

N=$(curl -s "$ADM/v1/users?limit=5" -H "$AH" | jqr "['total']")
[ -n "$N" ] && ok "用户列表可读，共 $N 人" || bad "用户列表异常" ""

NO=$(curl -s "$ADM/v1/orders?limit=5" -H "$AH" | jqr "['total']")
[ -n "$NO" ] && ok "订单列表可读，共 $NO 单" || bad "订单列表异常" ""

NP=$(curl -s "$ADM/v1/plans" -H "$AH" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['plans']))" 2>/dev/null)
[ -n "$NP" ] && ok "套餐列表可读，共 $NP 个" || bad "套餐列表异常" ""

NPR=$(curl -s "$ADM/v1/payment-providers" -H "$AH" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['providers']))" 2>/dev/null)
[ -n "$NPR" ] && ok "支付渠道可读，共 $NPR 个" || bad "渠道列表异常" ""

PP_BODY=$(curl -s "$ADM/v1/payment-providers" -H "$AH")
echo "$PP_BODY" | grep -qi "credentials_sealed\|\"key\"" \
  && bad "渠道响应含密钥字段名" "" \
  || ok "渠道响应不含 credentials_sealed / key 字段名（SEC-010）"

NA=$(curl -s "$ADM/v1/audit?limit=5" -H "$AH" | jqr "['total']")
[ -n "$NA" ] && ok "审计日志可读，共 $NA 条" || bad "审计列表异常" ""

#-------------------------------------------------------------------------------
sec "5. IAM-009 默认拒绝（未认证一律不可读）"

for p in overview users orders plans payment-providers audit switches; do
  C=$(code "$ADM/v1/$p")
  [ "$C" = "401" ] || { bad "未认证访问 /v1/$p 返回 $C，应为 401" ""; continue; }
done
ok "全部管理接口在未认证时返回 401"

#-------------------------------------------------------------------------------
sec "6. SEC-009 高风险动作需近期重认证"

TUID=$(curl -s "$ADM/v1/users?q=$UE" -H "$AH" | jqr "['users'][0]['id']")
[ -n "$TUID" ] && ok "定位到测试用户 ${TUID:0:18}…" || bad "找不到测试用户" ""

R=$(curl -s -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"status":"suspended","reason":"端到端测试：验证停用流程与会话吊销"}')
OKF=$(echo "$R" | jqr "['ok']")
[ "$OKF" = "True" ] && ok "刚登录的会话可执行高风险动作" || bad "高风险动作失败" "API 响应已隐藏"

ST=$("$PSQL" -X -qAtc "SELECT status FROM users WHERE id='$TUID'" | tr -d '[:space:]')
[ "$ST" = "suspended" ] && ok "用户状态已变更为 suspended" || bad "状态未变更" "$ST"

C=$(code "$PUB/v1/me" -H "Authorization: Bearer $PTOK")
[ "$C" = "401" ] || [ "$C" = "403" ] \
  && ok "被停用用户的旧令牌已失效（$C）" \
  || bad "停用后旧令牌仍可用" "HTTP $C"

C=$(code -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"status":"banned","reason":""}')
[ "$C" = "422" ] \
  && ok "封禁未填原因被拒（422）" || bad "无原因的封禁被接受" "HTTP $C"

curl -s -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"status":"active","reason":"测试结束恢复"}' >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM users WHERE id='$TUID'" | tr -d '[:space:]')
[ "$ST" = "active" ] && ok "用户已恢复正常" || bad "恢复失败" "$ST"

#-------------------------------------------------------------------------------
sec "7. SEC-012 管理写操作留审计"

N=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events
                 WHERE action='user.status_change' AND actor_audience='admin' AND resource_id='$TUID'::uuid" | tr -d '[:space:]')
[ "$N" -ge 2 ] && ok "状态变更已写入 $N 条管理域审计" || bad "审计缺失" "count=$N"

HAS=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events
                   WHERE action='user.status_change' AND resource_id='$TUID'::uuid
                     AND data->>'reason' IS NOT NULL AND btrim(data->>'reason') <> ''
                     AND data->>'before' IS NOT NULL" | tr -d '[:space:]')
[ "$HAS" -ge 1 ] && ok "审计含 before 与 reason" || bad "审计 data 不完整" "count=$HAS"

REV=$("$PSQL" -X -qAtc "SELECT data->>'sessions_revoked' FROM audit_events
                   WHERE action='user.status_change' AND resource_id='$TUID'::uuid
                     AND data->>'status'='suspended'
                   ORDER BY occurred_at DESC LIMIT 1" | tr -d '[:space:]')
[ "$REV" = "true" ] && ok "审计记录了 sessions_revoked=true" || bad "未记录会话吊销" "sessions_revoked=$REV"

#-------------------------------------------------------------------------------
sec "8. 套餐 authoring / publish / archive"

CAT_TAG="$STAMP"
CAT_CODE="adm-e2e-${CAT_TAG}"

admin_auth_fixture_pool(){
  local cipher server_id node_id
  cipher=$(curl -s "$ADM/v1/node-protocol-schemas" -H "$AH" | python3 -c '
import sys,json
d=json.load(sys.stdin)
for s in d.get("schemas",[]):
    if s.get("node_type")=="shadowsocks":
        m=s.get("methods") or []
        print(m[0] if m else "aes-128-gcm"); raise SystemExit(0)
raise SystemExit(1)
' 2>/dev/null) || { bad "无法读取 shadowsocks cipher" ""; return 1; }
  POOL_ID=$(curl -s -X POST "$ADM/v1/node-pools" -H "$AH" -H 'Content-Type: application/json' \
    -d "{\"code\":\"adm-$STAMP\",\"name\":\"Admin E2E pool $STAMP\"}" | jqr "['id']")
  is_uuid "$POOL_ID" || { bad "创建节点池失败" ""; return 1; }
  server_id=$(curl -s -X POST "$ADM/v1/servers" -H "$AH" -H 'Content-Type: application/json' \
    -d "{\"name\":\"adm-$STAMP-s1\",\"hostname\":\"adm-$STAMP.example.test\",\"public_ipv4\":\"203.0.113.33\"}" | jqr "['id']")
  is_uuid "$server_id" || { bad "创建服务器失败" ""; return 1; }
  node_id=$(curl -s -X POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: adm-node-$STAMP" \
    -d "{\"name\":\"adm-$STAMP-n1\",\"server_id\":\"$server_id\",\"pool_id\":\"$POOL_ID\",\"node_type\":\"shadowsocks\",\"server_host\":\"adm-$STAMP.example.test\",\"server_port\":22103,\"traffic_rate\":1,\"display_name\":\"Admin E2E $STAMP\",\"protocol_config\":{\"cipher\":\"$cipher\"}}" | jqr "['id']")
  is_uuid "$node_id" || { bad "创建节点失败" ""; return 1; }
  "$PSQL" -X -qAtc "UPDATE servers SET status = 'ready', last_heartbeat_at = now() WHERE id = '$server_id';" >/dev/null
  "$PSQL" -X -qAtc "UPDATE nodes SET serving_status = 'active' WHERE id = '$node_id';" >/dev/null
  ok "可服务节点夹具就绪（池 $POOL_ID）"
  return 0
}

admin_auth_fixture_pool || exit 1

CREATE=$(curl -s -X POST "$ADM/v1/plans" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-create-$CAT_TAG" \
  -d "{\"code\":\"$CAT_CODE\",\"name\":\"后台 E2E $CAT_TAG\",\"visibility\":\"public\"}")
PID=$(echo "$CREATE" | jqr "['plan']['id']")
PLAN_RV=$(echo "$CREATE" | jqr "['plan']['row_version']")
[ -n "$PID" ] && [ "$PLAN_RV" = "1" ] \
  && ok "草稿套餐已创建" || bad "创建草稿套餐失败" "API 响应已隐藏"

CREATE_REPLAY=$(curl -s -X POST "$ADM/v1/plans" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-create-$CAT_TAG" \
  -d "{\"code\":\"$CAT_CODE\",\"name\":\"后台 E2E $CAT_TAG\",\"visibility\":\"public\"}")
REPLAY_PID=$(echo "$CREATE_REPLAY" | jqr "['plan']['id']")
REPLAY_RV=$(echo "$CREATE_REPLAY" | jqr "['plan']['row_version']")
[ "$REPLAY_PID" = "$PID" ] && [ "$REPLAY_RV" = "$PLAN_RV" ] \
  && ok "套餐创建幂等重放返回原资源" || bad "套餐创建幂等重放产生漂移" "API 响应已隐藏"

VERSION=$(curl -s -X POST "$ADM/v1/plans/$PID/versions" -H "$AH" \
  -H "Idempotency-Key: plan-version-$CAT_TAG")
VID=$(echo "$VERSION" | jqr "['version']['id']")
VERSION_RV=$(echo "$VERSION" | jqr "['version']['row_version']")
[ -n "$VID" ] && [ "$VERSION_RV" = "1" ] \
  && ok "草稿版本已创建" || bad "创建草稿版本失败" "API 响应已隐藏"

BIND_BODY="{\"version_id\":\"$VID\",\"expected_version_row_version\":$VERSION_RV,\"pool_ids\":[\"$POOL_ID\"]}"
BIND=$(curl -s -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-$CAT_TAG" \
  -d "$BIND_BODY")
VERSION_RV=$(echo "$BIND" | jqr "['row_version']")
[ "$VERSION_RV" = "2" ] && ok "草稿版本已绑定节点分组" \
  || bad "绑定节点分组失败" "row_version=$VERSION_RV"

POOL_BINDINGS_AFTER_FIRST=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE plan_version_id='$VID'::uuid")
POOL_AUDIT_AFTER_FIRST=$(db_scalar integer "SELECT count(*) FROM audit_events WHERE resource_id='$VID'::uuid AND action='plan_version.pools_changed'")
BIND_REPLAY=$(curl -s -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-$CAT_TAG" \
  -d "$BIND_BODY")
BIND_REPLAY_RV=$(echo "$BIND_REPLAY" | jqr "['row_version']")
POOL_BINDINGS_AFTER_REPLAY=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE plan_version_id='$VID'::uuid")
POOL_AUDIT_AFTER_REPLAY=$(db_scalar integer "SELECT count(*) FROM audit_events WHERE resource_id='$VID'::uuid AND action='plan_version.pools_changed'")
[ "$BIND_REPLAY_RV" = "2" ] \
  && [ "$POOL_BINDINGS_AFTER_FIRST" = "1" ] \
  && [ "$POOL_BINDINGS_AFTER_REPLAY" = "$POOL_BINDINGS_AFTER_FIRST" ] \
  && [ "$POOL_AUDIT_AFTER_FIRST" = "1" ] \
  && [ "$POOL_AUDIT_AFTER_REPLAY" = "$POOL_AUDIT_AFTER_FIRST" ] \
  && ok "节点池专用端点幂等重放未重复绑定或审计" \
  || bad "节点池专用端点幂等重放产生副作用" \
    "row_version=$BIND_REPLAY_RV bindings=$POOL_BINDINGS_AFTER_FIRST/$POOL_BINDINGS_AFTER_REPLAY audits=$POOL_AUDIT_AFTER_FIRST/$POOL_AUDIT_AFTER_REPLAY"

POOL_BINDINGS_BEFORE=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE plan_version_id='$VID'::uuid")
C=$(code -X PUT "$ADM/v1/plans/$PID/versions/$VID" -H "$AH" \
  -H 'Content-Type: application/json' -d '{"pool_ids":[]}')
POOL_BINDINGS_AFTER=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE plan_version_id='$VID'::uuid")
[ "$C" = "422" ] && [ "$POOL_BINDINGS_BEFORE" = "1" ] && [ "$POOL_BINDINGS_AFTER" = "$POOL_BINDINGS_BEFORE" ] \
  && ok "旧版 PUT 拒绝 pool_ids 且保留节点池绑定" \
  || bad "旧版 PUT 仍可改动节点池绑定" "HTTP $C, before=$POOL_BINDINGS_BEFORE after=$POOL_BINDINGS_AFTER"

C=$(code -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-stale-$CAT_TAG" \
  -d "{\"version_id\":\"$VID\",\"expected_version_row_version\":1,\"pool_ids\":[\"$POOL_ID\"]}")
[ "$C" = "409" ] && ok "节点分组乐观锁冲突返回 409" \
  || bad "过期版本号未被拒绝" "HTTP $C"

C=$(code -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-invalid-$CAT_TAG" \
  -d "{\"version_id\":\"$VID\",\"expected_version_row_version\":0,\"pool_ids\":[]}")
[ "$C" = "422" ] && ok "节点分组无效版本号返回 422" \
  || bad "无效版本号未返回 422" "HTTP $C"

PRICE=$(curl -s -X POST "$ADM/v1/plans/$PID/prices" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-price-$CAT_TAG" \
  -d '{"currency":"CNY","unit_amount":100,"billing_interval":"month","interval_count":1}')
PRICE_ID=$(echo "$PRICE" | jqr "['price']['id']")
[ -n "$PRICE_ID" ] && ok "在售价格已创建" || bad "创建价格失败" "API 响应已隐藏"

PUBLISHED_RAW=$(curl -sS -w '\n%{http_code}' -X POST "$ADM/v1/plans/$PID/versions/$VID/publish" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-publish-$CAT_TAG" \
  -d "{\"expected_plan_row_version\":$PLAN_RV,\"expected_version_row_version\":$VERSION_RV}")
PUBLISHED_CODE=${PUBLISHED_RAW##*$'\n'}
PUBLISHED=${PUBLISHED_RAW%$'\n'*}
if [ "$PUBLISHED_CODE" != 200 ]; then
  PUBLISH_ERROR=$(printf '%s' "$PUBLISHED" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error',{}).get('code','non_json'))" 2>/dev/null) || PUBLISH_ERROR=non_json
  bad "发布套餐失败" "HTTP $PUBLISHED_CODE error=$PUBLISH_ERROR"
  exit 1
fi
PLAN_RV=$(echo "$PUBLISHED" | jqr "['plan_row_version']")
PUBLISHED_VERSION_RV=$(echo "$PUBLISHED" | jqr "['version_row_version']")
# 发布：套餐 1→2；版本在绑池后是 2，发布再 +1 成 3（catalog PublishVersion：verNext = expected + 1）
[ "$PLAN_RV" = "2" ] && [ "$PUBLISHED_VERSION_RV" = "3" ] && ok "草稿版本已发布" || bad "发布套餐失败" "plan_rv=$PLAN_RV ver_rv=$PUBLISHED_VERSION_RV"

POOLS_VIEW=$(curl -s "$ADM/v1/plans/$PID/pools" -H "$AH")
EDITABLE=$(echo "$POOLS_VIEW" | jqr "['editable']")
VIEW_STATUS=$(echo "$POOLS_VIEW" | jqr "['version_status']")
[ "$EDITABLE" = "False" ] && [ "$VIEW_STATUS" = "published" ] \
  && ok "已发布版本在分组接口中明确只读" \
  || bad "已发布版本未标记为只读" "API 响应已隐藏"

C=$(code -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-published-$CAT_TAG" \
  -d "{\"version_id\":\"$VID\",\"expected_version_row_version\":$PUBLISHED_VERSION_RV,\"pool_ids\":[\"$POOL_ID\"]}")
[ "$C" = "409" ] && ok "已发布版本的分组写入返回 409" \
  || bad "已发布版本仍可修改分组" "HTTP $C"

VISIBLE=$(curl -s "$PUB/v1/plans" | python3 -c "
import sys,json
print('yes' if any(p['id']=='$PID' for p in json.load(sys.stdin)['plans']) else 'no')" 2>/dev/null)
[ "$VISIBLE" = "yes" ] && ok "新套餐在用户端可见" || bad "发布后用户端不可见" ""

ARCHIVED=$(curl -s -X POST "$ADM/v1/plans/$PID/archive" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-archive-$CAT_TAG" \
  -d "{\"expected_row_version\":$PLAN_RV}")
ARCHIVE_RV=$(echo "$ARCHIVED" | jqr "['row_version']")
ST=$("$PSQL" -X -qAtc "SELECT status FROM plans WHERE id='$PID'" | tr -d '[:space:]')
[ "$ST" = "archived" ] && [ "$ARCHIVE_RV" = "3" ] \
  && ok "套餐已单向归档" || bad "套餐归档失败" "db_status=$ST row_version=$ARCHIVE_RV"

CATALOG_AUDIT_COUNT=$(db_scalar integer "SELECT count(DISTINCT action) FROM audit_events WHERE ((resource_id='$PID'::uuid AND action IN ('plan.create','plan.archive')) OR (resource_id='$VID'::uuid AND action IN ('plan_version.create','plan_version.pools_changed','plan_version.publish')) OR (resource_id='$PRICE_ID'::uuid AND action='price.create'))")
[ "$CATALOG_AUDIT_COUNT" = "6" ] \
  && ok "套餐核心写操作均已形成精确审计事件" \
  || bad "套餐核心审计事件不完整" "distinct actions=$CATALOG_AUDIT_COUNT"

GONE=$(curl -s "$PUB/v1/plans" | python3 -c "
import sys,json
print('yes' if not any(p['id']=='$PID' for p in json.load(sys.stdin)['plans']) else 'no')" 2>/dev/null)
[ "$GONE" = "yes" ] && ok "归档套餐已从用户端目录消失" || bad "归档后用户端仍可见" ""

C=$(code -X POST "$ADM/v1/plans/$PID/status" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"status":"active"}')
[ "$C" = "404" ] && ok "陈旧 status 端点仍保持移除" || bad "陈旧 status 端点被重新引入" "HTTP $C"

#-------------------------------------------------------------------------------
sec "9. NFR-008 降级开关"

SW=$(curl -s "$ADM/v1/switches" -H "$AH")
NSW=$(echo "$SW" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['switches']))" 2>/dev/null)
[ -n "$NSW" ] && ok "降级开关可读，共 $NSW 项" || bad "开关列表异常" "API 响应已隐藏"

C=$(code -X POST "$ADM/v1/switches/auth.login" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"enabled":false,"reason":"测试：核心开关应当关不掉"}')
[ "$C" = "409" ] && ok "核心开关 auth.login 不可关闭（409）" || bad "核心开关竟被关闭" "HTTP $C"
EN=$(boolq "SELECT enabled FROM feature_switches WHERE code='auth.login'")
[ "$EN" = "true" ] && ok "auth.login 仍处于启用状态" || bad "核心开关已被关闭" "$EN"

REGISTRATION_TOUCHED=1
C=$(code -X POST "$ADM/v1/switches/auth.registration" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":false,"reason":"admin E2E temporary registration disable"}')
[ "$C" = 200 ] || { bad "registration switch mutation failed" "HTTP $C"; exit 1; }
EN=$(boolq "SELECT enabled FROM feature_switches WHERE code='auth.registration'")
[ "$EN" = "false" ] && ok "非核心开关 auth.registration 已关闭" || bad "关闭失败" "$EN"

restore_registration || { RESTORE_FAILED=1; bad "registration switch exact-state restore failed" ""; exit 1; }
EN=$(db_scalar bool "SELECT enabled FROM feature_switches WHERE code='auth.registration'")
[ "$EN" = "$REGISTRATION_ORIGINAL" ] && ok "registration switch restored to its exact original state" \
  || { bad "registration switch original state mismatch" ""; exit 1; }

#-------------------------------------------------------------------------------
sec "10. 支付渠道启停"

CREATE_PAY=$(curl -s -X POST "$ADM/v1/payment-providers" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: adm-pay-create-$STAMP" \
  -d "{\"code\":\"$PAY_CODE\",\"adapter\":\"epay\",\"display_name\":\"Admin E2E $STAMP\",\"base_url\":\"https://pay.example.test\",\"methods\":[\"alipay\"],\"default_method\":\"alipay\",\"merchant_id\":\"e2e$STAMP\",\"key\":\"$PAY_KEY\"}")
PAY_ID=$(echo "$CREATE_PAY" | jqr "['id']")
is_uuid "$PAY_ID" && ok "自建易支付渠道 $PAY_CODE" || { bad "创建支付渠道失败" "API 响应已隐藏"; exit 1; }
echo "$CREATE_PAY" | grep -q "$PAY_KEY" && bad "建渠道响应泄露密钥原文" "" || ok "建渠道响应不含密钥原文"
PP_AFTER=$(curl -s "$ADM/v1/payment-providers" -H "$AH")
echo "$PP_AFTER" | grep -q "$PAY_KEY" && bad "渠道列表泄露本脚本密钥原文" "" || ok "渠道列表不含本脚本密钥原文"
C=$(code -X POST "$ADM/v1/payment-providers/$PAY_CODE/toggle" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"accepting_new":true}')
[ "$C" = 200 ] || { bad "启用渠道收单失败" "HTTP $C"; exit 1; }

PAY_TOUCHED=1
C=$(code -X POST "$ADM/v1/payment-providers/$PAY_CODE/toggle" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"accepting_new":false}')
[ "$C" = 200 ] || { bad "payment provider mutation failed" "HTTP $C"; exit 1; }
AC=$(boolq "SELECT accepting_new FROM payment_providers WHERE code='$PAY_CODE'")
[ "$AC" = "false" ] && ok "渠道已停止收单但保持启用（PAY-009）" || bad "停止收单失败" "$AC"

restore_payment_provider || { RESTORE_FAILED=1; bad "payment provider restore failed" ""; exit 1; }
AC=$(boolq "SELECT enabled FROM payment_providers WHERE code='$PAY_CODE'")
[ "$AC" = "false" ] && ok "trap 已将自建渠道 toggle 为全关" || bad "渠道未恢复为全关" "$AC"
PAY_TOUCHED=0

#-------------------------------------------------------------------------------
sec "11. SEC-006 错误响应不泄露内部信息"

R=$(curl -s "$ADM/v1/nonexistent-admin-endpoint")
echo "$R" | grep -qiE "goroutine|/opt/|\.go:|panic|pgx|postgres" \
  && bad "404 响应泄露了内部细节" "API 响应已隐藏" \
  || ok "404 不含堆栈、路径与依赖信息"
echo "$R" | grep -q "request_id" && ok "错误响应带 request_id 便于追溯" || bad "缺 request_id" "API 响应已隐藏"

C1=$(code "$ADM/v1/users/00000000-0000-7000-8000-000000000999" -H "$AH")
[ "$C1" = "404" ] && ok "不存在的资源返回 404" || bad "返回 $C1" ""

#-------------------------------------------------------------------------------
if [ "$fail" -ne 0 ]; then
  MAIN_DONE=1
  exit 1
fi
MAIN_DONE=1
exit 0
