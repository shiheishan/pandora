#!/usr/bin/env bash
# 管理控制台端到端测试。
#
# 重点验证管理面区别于用户面的那些性质：
#   1. EXT-001 令牌不可跨域：Public 域令牌敲 Admin 域必须失败，反之亦然
#   2. IAM-006 无角色账号登录管理域，响应必须与口令错误完全一致
#   3. IAM-009 默认拒绝：权限不足的路由返回 403 而非数据
#   4. SEC-009 高风险动作要求近期重认证
#   5. SEC-012 管理写操作必须留下审计
#   6. NFR-008 essential 降级开关不可关闭
set -euo pipefail

ADM=${ADM:-http://127.0.0.1:9001}
PUB=${PUB:-http://127.0.0.1:9000}
PSQL=${PSQL:-/opt/aegispanel/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
TENANT=${ADMIN_E2E_TENANT_ID:-}
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) \
  || { echo '[FATAL] cannot resolve admin E2E script directory' >&2; exit 1; }
MIDDLEWARE_SOURCE="$SCRIPT_DIR/../internal/middleware/middleware.go"

pass=0; fail=0
E2E_PREFLIGHT_OK=0
MAIN_DONE=0
RESTORE_FAILED=0
REGISTRATION_ORIGINAL=
REGISTRATION_TOUCHED=0
EPAY_ENABLED_ORIGINAL=
EPAY_ACCEPTING_ORIGINAL=
EPAY_TOUCHED=0
AH=
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
# psql 对 boolean 列输出 t/f，而拼接成 text 时输出 true/false。
# 统一归一化，避免断言写成 "true" 却永远匹配不上。
boolq(){ case "$($PSQL -tAc "$1" | tr -d '[:space:]')" in t|true) echo true;; f|false) echo false;; *) echo unknown;; esac; }
# Pin every request to the explicit loopback origin and ignore curlrc/proxies.
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
  [ "$(db_scalar bool "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.registration'")" = "$REGISTRATION_ORIGINAL" ] || return 1
  REGISTRATION_TOUCHED=0
}

restore_epay(){
  [ "$EPAY_TOUCHED" = 1 ] || return 0
  local enabled accepting status
  case "$EPAY_ENABLED_ORIGINAL" in t) enabled=true;; f) enabled=false;; *) return 1;; esac
  case "$EPAY_ACCEPTING_ORIGINAL" in t) accepting=true;; f) accepting=false;; *) return 1;; esac
  status=$(curl --silent --connect-timeout 5 --max-time 20 -o /dev/null -w '%{http_code}' \
    -X POST "$ADM/v1/payment-providers/epay/toggle" -H "$AH" -H 'Content-Type: application/json' \
    -d "{\"enabled\":$enabled,\"accepting_new\":$accepting}") || return 1
  [ "$status" = 200 ] || return 1
  [ "$(db_scalar bool_pair "SELECT enabled::text || '|' || accepting_new::text FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'")" = "$EPAY_ENABLED_ORIGINAL|$EPAY_ACCEPTING_ORIGINAL" ] || return 1
  EPAY_TOUCHED=0
}

cleanup(){
  local status=$?
  trap - EXIT INT TERM
  set +e
  if [ "$E2E_PREFLIGHT_OK" = 1 ] && [ -n "$AH" ]; then
    restore_registration || RESTORE_FAILED=1
    restore_epay || RESTORE_FAILED=1
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
[ -n "$TENANT" ] && is_uuid "$TENANT" \
  || { echo '[FATAL] ADMIN_E2E_TENANT_ID must be an explicit UUID' >&2; exit 1; }
[ -f "$MIDDLEWARE_SOURCE" ] \
  || { echo '[FATAL] API tenant source is unavailable; refusing to guess the runtime tenant' >&2; exit 1; }
API_DEFAULT_TENANT=$(python3 - "$MIDDLEWARE_SOURCE" <<'PY'
import pathlib, re, sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
ids = re.findall(
    r'(?m)^const DefaultTenantID = "([0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12})"\s*$',
    source,
)
injects_default = re.search(
    r'WithTenantID\(r\.Context\(\),\s*DefaultTenantID\)', source
) is not None
if len(ids) != 1 or not injects_default:
    raise SystemExit(1)
sys.stdout.write(ids[0])
PY
) || { echo '[FATAL] API default tenant contract could not be derived exactly' >&2; exit 1; }
[ "$TENANT" = "$API_DEFAULT_TENANT" ] \
  || { echo '[FATAL] ADMIN_E2E_TENANT_ID does not match the API DefaultTenantID' >&2; exit 1; }
DB_NAME=$(db_scalar identifier 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }
[ "${ADMIN_E2E_DATABASE:-}" = "$DB_NAME" ] \
  || { echo '[FATAL] ADMIN_E2E_DATABASE must exactly match current_database()' >&2; exit 1; }
registration_parser_selftest \
  || { echo '[FATAL] registration response parser self-test failed' >&2; exit 1; }
[ "$(db_scalar integer "SELECT count(*) FROM tenants WHERE id='$TENANT'")" = 1 ] \
  || { echo '[FATAL] disposable tenant must exist exactly once' >&2; exit 1; }
REGISTRATION_ORIGINAL=$(db_scalar bool "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.registration'")
EPAY_ORIGINAL=$(db_scalar bool_pair "SELECT enabled::text || '|' || accepting_new::text FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'")
IFS='|' read -r EPAY_ENABLED_ORIGINAL EPAY_ACCEPTING_ORIGINAL <<<"$EPAY_ORIGINAL"
E2E_PREFLIGHT_OK=1

#-------------------------------------------------------------------------------
sec "1. 管理员登录"

curl -sf "$ADM/healthz" >/dev/null && ok "Admin 网关存活" || { bad "网关不可达" "$ADM"; exit 1; }

R=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
ATOK=$(echo "$R" | jqr "['access_token']")
AUID=$(echo "$R" | jqr "['user_id']")
NPERM=$(echo "$R" | python3 -c "import sys,json;print(len(json.load(sys.stdin).get('permissions',[])))" 2>/dev/null)
[ -n "$ATOK" ] && is_uuid "$AUID" \
  && ok "管理员登录成功，持有 $NPERM 项权限" || { bad "登录失败" "认证响应已隐藏"; exit 1; }
AH="Authorization: Bearer $ATOK"
ADMIN_DB_TENANT=$(db_scalar uuid "SELECT tenant_id::text FROM users WHERE id='$AUID'::uuid")
[ "$ADMIN_DB_TENANT" = "$TENANT" ] \
  || { bad "管理员身份不属于已绑定的 API 租户" "身份细节已隐藏"; exit 1; }
ok "管理员返回身份已精确绑定到 API 租户"

# User-domain checks need registration available. If the disposable fixture had
# it disabled, enable it temporarily and let the EXIT trap restore the exact
# original value. Mark touched before the request so a lost response cannot skip
# restoration after a successful server-side mutation.
if [ "$REGISTRATION_ORIGINAL" != true ]; then
  REGISTRATION_TOUCHED=1
  C=$(code -X POST "$ADM/v1/switches/auth.registration" -H "$AH" \
    -H 'Content-Type: application/json' \
    -d '{"enabled":true,"reason":"admin E2E temporary registration enable"}')
  [ "$C" = 200 ] || { bad "temporary registration enable failed" "HTTP $C"; exit 1; }
  [ "$(db_scalar bool "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.registration'")" = true ] \
    || { bad "temporary registration enable was not persisted" ""; exit 1; }
fi

# 错误口令必须被拒
C=$(code -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-password-xxxx\"}")
[ "$C" = "401" ] && ok "错误口令被拒（401）" || bad "错误口令返回 $C" ""

#-------------------------------------------------------------------------------
sec "2. EXT-001 令牌不可跨域"

# 造一个普通用户并拿 Public 域令牌
UE="admtest_$(date +%s)_$RANDOM@example.com"
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

R=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"UserPass!2026ab\"}")
C=$(code -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"UserPass!2026ab\"}")
MSG=$(echo "$R" | jqr "['error']['message']")
WRONG=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$UE\",\"password\":\"totally-wrong-pw\"}" | jqr "['error']['message']")
[ "$C" = "401" ] && ok "普通用户登录管理域被拒（401）" || bad "普通用户登录管理域返回 $C" "API 响应已隐藏"
[ "$MSG" = "$WRONG" ] && ok "「无角色」与「口令错误」响应完全一致（IAM-006）" \
                      || bad "两种失败可区分，存在账号枚举侧信道" "API 响应已隐藏"

#-------------------------------------------------------------------------------
sec "4. 只读接口"

O=$(curl -s "$ADM/v1/overview" -H "$AH")
UT=$(echo "$O" | jqr "['users']['total']")
# revenue 是按币种分组的数组：多币种不能混加，这里逐币种展示
RT2=$(echo "$O" | python3 -c "
import sys,json
print(' '.join(f\"{r['currency']}:{r['total']}\" for r in json.load(sys.stdin)['revenue']) or 'none')" 2>/dev/null)
DRIFT=$(echo "$O" | jqr "['ledger_drift_accounts']")
[ -n "$UT" ] && ok "仪表盘：用户 $UT 人，累计收入 [$RT2]（最小单位）" || bad "仪表盘异常" "API 响应已隐藏"

# 多币种绝不能被合并成一行 —— 那样得到的数字没有任何意义
NCUR=$(echo "$O" | python3 -c "
import sys,json;d=json.load(sys.stdin)['revenue']
cs=[r['currency'] for r in d]
print('dup' if len(cs)!=len(set(cs)) else len(cs))" 2>/dev/null)
[ "$NCUR" != "dup" ] && ok "收入按币种分组，无重复币种（$NCUR 种）"                      || bad "同一币种出现多行" ""
[ "$DRIFT" = "0" ] && ok "账本无漂移账户（PAY-005）" || bad "账本漂移 $DRIFT 个账户" ""

N=$(curl -s "$ADM/v1/users?limit=5" -H "$AH" | jqr "['total']")
[ -n "$N" ] && ok "用户列表可读，共 $N 人" || bad "用户列表异常" ""

NO=$(curl -s "$ADM/v1/orders?limit=5" -H "$AH" | jqr "['total']")
[ -n "$NO" ] && ok "订单列表可读，共 $NO 单" || bad "订单列表异常" ""

NP=$(curl -s "$ADM/v1/plans" -H "$AH" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['plans']))" 2>/dev/null)
[ -n "$NP" ] && ok "套餐列表可读，共 $NP 个" || bad "套餐列表异常" ""

NPR=$(curl -s "$ADM/v1/payment-providers" -H "$AH" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['providers']))" 2>/dev/null)
[ -n "$NPR" ] && ok "支付渠道可读，共 $NPR 个" || bad "渠道列表异常" ""

# 渠道密钥绝不能出现在管理接口响应里
curl -s "$ADM/v1/payment-providers" -H "$AH" | grep -qi "TESTKEY\|credentials_encrypted\|\"key\"" \
  && bad "渠道密钥或密文出现在 API 响应中" "" \
  || ok "渠道响应不含任何密钥字段（SEC-010）"

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

# 刚登录即视为近期重认证，此处应当放行
R=$(curl -s -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"status":"suspended","reason":"端到端测试：验证停用流程与会话吊销"}')
OKF=$(echo "$R" | jqr "['ok']")
[ "$OKF" = "True" ] && ok "刚登录的会话可执行高风险动作" || bad "高风险动作失败" "API 响应已隐藏"

ST=$($PSQL -tAc "SELECT status FROM users WHERE id='$TUID'" | tr -d '[:space:]')
[ "$ST" = "suspended" ] && ok "用户状态已变更为 suspended" || bad "状态未变更" "$ST"

# 停用必须吊销全部会话，否则封号形同虚设
ACTIVE=$($PSQL -tAc "SELECT count(*) FROM sessions WHERE user_id='$TUID' AND revoked_at IS NULL" | tr -d '[:space:]')
[ "$ACTIVE" = "0" ] && ok "该用户全部会话已被吊销" || bad "仍有 $ACTIVE 个会话未吊销" ""

# 被停用的用户旧令牌应立即失效
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $PTOK")
[ "$C" = "401" ] || [ "$C" = "403" ] \
  && ok "被停用用户的旧令牌已失效（$C）" \
  || bad "停用后旧令牌仍可用" "HTTP $C"

# 缺原因必须被拒
C=$(code -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"status":"banned","reason":""}')
[ "$C" = "422" ] || [ "$C" = "400" ] \
  && ok "封禁未填原因被拒（$C）" || bad "无原因的封禁被接受" "HTTP $C"

# 恢复
curl -s -X POST "$ADM/v1/users/$TUID/status" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"status":"active","reason":"测试结束恢复"}' >/dev/null
ST=$($PSQL -tAc "SELECT status FROM users WHERE id='$TUID'" | tr -d '[:space:]')
[ "$ST" = "active" ] && ok "用户已恢复正常" || bad "恢复失败" "$ST"

#-------------------------------------------------------------------------------
sec "7. SEC-012 管理写操作留审计"

N=$($PSQL -tAc "SELECT count(*) FROM audit_events
                 WHERE action='user.status_change' AND api_domain='admin'" | tr -d '[:space:]')
# 本轮产生 2 条：停用 + 恢复。被拒的那次封禁不该留成功审计。
[ "$N" -ge 2 ] && ok "状态变更已写入 $N 条管理域审计" || bad "审计缺失" "count=$N"

# 审计必须记录变更前后与原因
HAS=$($PSQL -tAc "SELECT count(*) FROM audit_events
                   WHERE action='user.status_change'
                     AND before_digest IS NOT NULL AND after_digest IS NOT NULL
                     AND after_digest->>'reason' IS NOT NULL" | tr -d '[:space:]')
[ "$HAS" -ge 1 ] && ok "审计含变更前后摘要与原因" || bad "审计摘要不完整" "count=$HAS"

# 会话吊销数也应留痕，便于事后核对影响面
REV=$($PSQL -tAc "SELECT after_digest->>'sessions_revoked' FROM audit_events
                   WHERE action='user.status_change'
                     AND after_digest->>'status'='suspended'
                   ORDER BY occurred_at DESC LIMIT 1" | tr -d '[:space:]')
[ -n "$REV" ] && ok "审计记录了被吊销的会话数（$REV）" || bad "未记录会话吊销数" ""

#-------------------------------------------------------------------------------
sec "8. 套餐 authoring / publish / archive"

# Never archive a pre-existing production fixture. Build an isolated catalog
# record and drive the current monotonic contract from draft to archived.
CAT_TAG="$(date +%s)-$RANDOM"
CAT_CODE="adm-e2e-${CAT_TAG}"
POOL_ID=${ADMIN_E2E_POOL_ID:-}
if [ -z "$POOL_ID" ]; then
  POOL_ID=$(curl -s "$ADM/v1/node-pools" -H "$AH" | python3 -c "
import sys,json
for p in json.load(sys.stdin).get('pools',[]):
    if p.get('active_nodes',0)>0: print(p['id']); break" 2>/dev/null)
fi
is_uuid "$POOL_ID" && ok "取到可发布节点分组" \
  || { bad "无活跃节点分组，无法验收套餐发布" ""; exit 1; }

CREATE=$(curl -s -X POST "$ADM/v1/plans" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-create-$CAT_TAG" \
  -d "{\"code\":\"$CAT_CODE\",\"name\":\"后台 E2E $CAT_TAG\",\"visibility\":\"public\"}")
PID=$(echo "$CREATE" | jqr "['plan']['id']")
PLAN_RV=$(echo "$CREATE" | jqr "['plan']['row_version']")
[ -n "$PID" ] && [ "$PLAN_RV" = "1" ] \
  && ok "草稿套餐已创建" || bad "创建草稿套餐失败" "API 响应已隐藏"

# A successful mutation must be replay-safe: the same key and body returns the
# original resource instead of creating a second plan.
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
  || bad "绑定节点分组失败" "API 响应已隐藏"

# Replaying the exact dedicated pool request must return the cached row version
# without another binding mutation or another domain audit event.
POOL_BINDINGS_AFTER_FIRST=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE tenant_id='$TENANT' AND plan_version_id='$VID'::uuid")
POOL_AUDIT_AFTER_FIRST=$(db_scalar integer "SELECT count(*) FROM audit_events WHERE tenant_id='$TENANT' AND resource_id='$VID'::uuid AND action='plan_version.pools_changed'")
BIND_REPLAY=$(curl -s -X POST "$ADM/v1/plans/$PID/pools" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: plan-pools-$CAT_TAG" \
  -d "$BIND_BODY")
BIND_REPLAY_RV=$(echo "$BIND_REPLAY" | jqr "['row_version']")
POOL_BINDINGS_AFTER_REPLAY=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE tenant_id='$TENANT' AND plan_version_id='$VID'::uuid")
POOL_AUDIT_AFTER_REPLAY=$(db_scalar integer "SELECT count(*) FROM audit_events WHERE tenant_id='$TENANT' AND resource_id='$VID'::uuid AND action='plan_version.pools_changed'")
[ "$BIND_REPLAY_RV" = "2" ] \
  && [ "$POOL_BINDINGS_AFTER_FIRST" = "1" ] \
  && [ "$POOL_BINDINGS_AFTER_REPLAY" = "$POOL_BINDINGS_AFTER_FIRST" ] \
  && [ "$POOL_AUDIT_AFTER_FIRST" = "1" ] \
  && [ "$POOL_AUDIT_AFTER_REPLAY" = "$POOL_AUDIT_AFTER_FIRST" ] \
  && ok "节点池专用端点幂等重放未重复绑定或审计" \
  || bad "节点池专用端点幂等重放产生副作用" \
    "row_version=$BIND_REPLAY_RV bindings=$POOL_BINDINGS_AFTER_FIRST/$POOL_BINDINGS_AFTER_REPLAY audits=$POOL_AUDIT_AFTER_FIRST/$POOL_AUDIT_AFTER_REPLAY"

# pool_ids on the legacy semantic-update endpoint is an explicit contract
# violation. It must be rejected before any mutation, including an empty list.
POOL_BINDINGS_BEFORE=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE tenant_id='$TENANT' AND plan_version_id='$VID'::uuid")
C=$(code -X PUT "$ADM/v1/plans/$PID/versions/$VID" -H "$AH" \
  -H 'Content-Type: application/json' -d '{"pool_ids":[]}')
POOL_BINDINGS_AFTER=$(db_scalar integer "SELECT count(*) FROM plan_node_pools WHERE tenant_id='$TENANT' AND plan_version_id='$VID'::uuid")
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
[ "$PLAN_RV" = "2" ] && ok "草稿版本已发布" || bad "发布套餐失败" "API 响应已隐藏"

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
ST=$($PSQL -tAc "SELECT status FROM plans WHERE id='$PID'" | tr -d '[:space:]')
[ "$ST" = "archived" ] && [ "$ARCHIVE_RV" = "3" ] \
  && ok "套餐已单向归档" || bad "套餐归档失败" "db_status=$ST row_version=$ARCHIVE_RV"

# Verify durable audit evidence for every catalog boundary exercised above.
# This checks exact tenant/resource/action rows rather than relying on the
# presence of an audit page or a truncated source-text window.
CATALOG_AUDIT_COUNT=$(db_scalar integer "SELECT count(DISTINCT action) FROM audit_events WHERE tenant_id='$TENANT' AND ((resource_id='$PID'::uuid AND action IN ('plan.create','plan.archive')) OR (resource_id='$VID'::uuid AND action IN ('plan_version.create','plan_version.pools_changed','plan_version.publish')) OR (resource_id='$PRICE_ID'::uuid AND action='price.create'))")
[ "$CATALOG_AUDIT_COUNT" = "6" ] \
  && ok "套餐核心写操作均已形成精确审计事件" \
  || bad "套餐核心审计事件不完整" "distinct actions=$CATALOG_AUDIT_COUNT"

GONE=$(curl -s "$PUB/v1/plans" | python3 -c "
import sys,json
print('yes' if not any(p['id']=='$PID' for p in json.load(sys.stdin)['plans']) else 'no')" 2>/dev/null)
[ "$GONE" = "yes" ] && ok "归档套餐已从用户端目录消失" || bad "归档后用户端仍可见" ""

# The removed status endpoint must remain absent; restoring it would bypass
# the immutable versioned authoring contract.
C=$(code -X POST "$ADM/v1/plans/$PID/status" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"status":"active"}')
[ "$C" = "404" ] && ok "陈旧 status 端点仍保持移除" || bad "陈旧 status 端点被重新引入" "HTTP $C"

#-------------------------------------------------------------------------------
sec "9. NFR-008 降级开关"

SW=$(curl -s "$ADM/v1/switches" -H "$AH")
NSW=$(echo "$SW" | python3 -c "import sys,json;print(len(json.load(sys.stdin)['switches']))" 2>/dev/null)
[ -n "$NSW" ] && ok "降级开关可读，共 $NSW 项" || bad "开关列表异常" "API 响应已隐藏"

# essential 项不可关闭，由数据库约束保证
C=$(code -X POST "$ADM/v1/switches/auth.login" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"enabled":false,"reason":"测试：核心开关应当关不掉"}')
[ "$C" = "409" ] && ok "核心开关 auth.login 不可关闭（409）" || bad "核心开关竟被关闭" "HTTP $C"
EN=$(boolq "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.login'")
[ "$EN" = "true" ] && ok "auth.login 仍处于启用状态" || bad "核心开关已被关闭" "$EN"

# 非核心项可关闭，但必须填原因
REGISTRATION_TOUCHED=1
C=$(code -X POST "$ADM/v1/switches/auth.registration" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":false,"reason":"admin E2E temporary registration disable"}')
[ "$C" = 200 ] || { bad "registration switch mutation failed" "HTTP $C"; exit 1; }
EN=$(boolq "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.registration'")
[ "$EN" = "false" ] && ok "非核心开关 auth.registration 已关闭" || bad "关闭失败" "$EN"

restore_registration || { RESTORE_FAILED=1; bad "registration switch exact-state restore failed" ""; exit 1; }
EN=$(db_scalar bool "SELECT enabled FROM feature_switches WHERE tenant_id='$TENANT' AND code='auth.registration'")
[ "$EN" = "$REGISTRATION_ORIGINAL" ] && ok "registration switch restored to its exact original state" \
  || { bad "registration switch original state mismatch" ""; exit 1; }

#-------------------------------------------------------------------------------
sec "10. 支付渠道启停"

EPAY_TOUCHED=1
C=$(code -X POST "$ADM/v1/payment-providers/epay/toggle" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"accepting_new":false}')
[ "$C" = 200 ] || { bad "payment provider mutation failed" "HTTP $C"; exit 1; }
AC=$(boolq "SELECT accepting_new FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'")
[ "$AC" = "false" ] && ok "渠道已停止收单但保持启用（PAY-009）" || bad "停止收单失败" "$AC"

restore_epay || { RESTORE_FAILED=1; bad "payment provider exact-state restore failed" ""; exit 1; }
AC=$(db_scalar bool_pair "SELECT enabled::text || '|' || accepting_new::text FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'")
[ "$AC" = "$EPAY_ENABLED_ORIGINAL|$EPAY_ACCEPTING_ORIGINAL" ] \
  && ok "payment provider restored to its exact original state" \
  || { bad "payment provider original state mismatch" ""; exit 1; }

#-------------------------------------------------------------------------------
sec "11. SEC-006 错误响应不泄露内部信息"

R=$(curl -s "$ADM/v1/nonexistent-admin-endpoint")
echo "$R" | grep -qiE "goroutine|/opt/|\.go:|panic|pgx|postgres" \
  && bad "404 响应泄露了内部细节" "API 响应已隐藏" \
  || ok "404 不含堆栈、路径与依赖信息"
echo "$R" | grep -q "request_id" && ok "错误响应带 request_id 便于追溯" || bad "缺 request_id" "API 响应已隐藏"

# 不存在的用户 ID 与他人的用户 ID 应返回同一种错误
C1=$(code "$ADM/v1/users/00000000-0000-7000-8000-000000000999" -H "$AH")
[ "$C1" = "404" ] && ok "不存在的资源返回 404" || bad "返回 $C1" ""

#-------------------------------------------------------------------------------
[ "$fail" -eq 0 ] || exit 1
MAIN_DONE=1
exit 0
