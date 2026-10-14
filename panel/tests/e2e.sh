#!/usr/bin/env bash
# 端到端链路：注册 → 登录 → 套餐 → 下单 → 易支付回调幂等 → 账本 → 订阅与配额 → 审计 → 限流与错误处理。
#
# 与 invariants / 其它 e2e 的分工：本脚本证明整条 HTTP 业务链路在真实网关与一次性库上跑得通；
# PAY-003 同一易支付回调连发 10 次只产生一次权益与一笔账。
#
# 不测的东西及原因：
#   - 审计哈希链：审计没有哈希链（见 p7e2e-common 产品决定），只数条数并核对 audit_events_append_only 触发器
#   - 租户隔离：单租户，没有租户列与 RLS
#
# 仍用 SQL 的步骤及原因：
#   - 口令 PHC、验证码哈希、订单快照、支付/账本/订阅/凭据/事件：无更稳的只读 HTTP，或要核对库内唯一约束
#   - 账本漂移、触发器存在性：与 service 同口径或 DDL 守卫
#
# 环境：PUB、ADM、PSQL、ADMIN_EMAIL、ADMIN_PASS（必填）；E2E_DISPOSABLE=YES_DELETE_FIXTURES（必填）。
# 会留下订单、审计、账本，只许在一次性库上跑；本脚本应排在 smoke 序列最后（限流打满登录额度）。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

pass=0; fail=0
step() { printf '\n\033[1m▸ %s\033[0m\n' "$1"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail+1)); }
sec()  { step "$1"; }

db() { "$PSQL" -X -qAtc "$1" | tr -d '[:space:]'; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code() { curl -sS -o /dev/null -w '%{http_code}' "$@"; }
json() { curl -sS -H 'Content-Type: application/json' "$@"; }

[ "${E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$(db 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }

STAMP="$(date +%s)$RANDOM"
# 审计计数的起点：取库的时钟（UTC ISO 串，不含空格，db() 会删空白）
SCRIPT_START=$(db "SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')")
EMAIL="e2e-${STAMP}@example.test"
PASSWORD="Xxxx!E2e2026x"

AH=""
MAIL_ORIG=""
MAIL_TOUCHED=0
E2E_PLAN_ID=""; E2E_PLAN_RV=""
EPAY_CODE="e2epay${STAMP}"
EPAY_KEY="E2E_KEY_$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
EPAY_PID="m${STAMP}"

mail_settings_body() {
  MAIL_ORIG="$MAIL_ORIG" python3 - "$1" <<'PY'
import json, os, sys
o = json.loads(os.environ["MAIL_ORIG"])
want = sys.argv[1]
body = {
    "smtp_host": o.get("smtp_host", ""), "smtp_port": o.get("smtp_port") or 465,
    "encryption": o.get("encryption", "ssl") or "ssl", "smtp_username": o.get("smtp_username", ""),
    "from_address": o.get("from_address", ""), "from_name": o.get("from_name", ""),
    "smtp_password": "",
}
if want == "orig":
    body["email_verification"] = bool(o.get("email_verification"))
else:
    body["email_verification"] = want == "true"
    if body["email_verification"]:
        body["smtp_host"] = body["smtp_host"] or "smtp.example.test"
        body["from_address"] = body["from_address"] or "e2e@example.test"
        body["encryption"] = body["encryption"] or "ssl"
print(json.dumps(body))
PY
}

set_email_verification() {
  local body c
  body=$(mail_settings_body "$1") || return 1
  MAIL_TOUCHED=1
  c=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${ADM}/v1/settings/mail" \
        -H "$AH" -H 'Content-Type: application/json' -d "$body")
  [ "$c" = "200" ]
}

cleanup() {
  local status=$?
  trap - EXIT
  if [ "$MAIL_TOUCHED" = 1 ]; then
    if set_email_verification orig; then MAIL_TOUCHED=0
    else echo '  [恢复失败] 邮件设置没能写回原值' >&2; status=1; fi
  fi
  if [ -n "$E2E_PLAN_ID" ] && [ -n "$E2E_PLAN_RV" ]; then
    c=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${ADM}/v1/plans/${E2E_PLAN_ID}/archive" \
          -H "$AH" -H 'Content-Type: application/json' -H "Idempotency-Key: e2e-plan-archive-${STAMP}" \
          -d "{\"expected_row_version\":${E2E_PLAN_RV}}")
    [ "$c" = "200" ] || { echo "  [恢复失败] 套餐 ${E2E_PLAN_ID} 没能归档（HTTP ${c}）" >&2; status=1; }
  fi
  exit "$status"
}
trap cleanup EXIT

# 易支付 MD5 签名（参数 k=v，末尾直接接密钥，不做 URL 编码）
epay_sign() {
  python3 - "$EPAY_KEY" "$@" <<'PY'
import sys, hashlib
key = sys.argv[1]
params = {}
for kv in sys.argv[2:]:
    k, _, v = kv.partition('=')
    if v and k not in ('sign', 'sign_type'):
        params[k] = v
s = '&'.join(f'{k}={params[k]}' for k in sorted(params)) + key
print(hashlib.md5(s.encode()).hexdigest())
PY
}

is_zero_amount() {
  case "$1" in 0|0.00|0.0) return 0 ;; *) return 1 ;; esac
}

admin_login() {
  local ra atok
  ra=$(json -X POST "$ADM/v1/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
  atok=$(echo "$ra" | jqr "['access_token']")
  [ -n "$atok" ] || return 1
  AH="Authorization: Bearer ${atok}"
}

admin_reauth() {
  local body tok
  body=$(json -X POST "$ADM/v1/auth/reauth" -H "$AH" -d "{\"password\":\"$ADMIN_PASS\"}")
  tok=$(echo "$body" | jqr "['access_token']")
  [ -n "$tok" ] && AH="Authorization: Bearer ${tok}"
}

#-------------------------------------------------------------------------------
sec "0. 服务可达性与后台登录"
#-------------------------------------------------------------------------------
ready=$(code --max-time 5 "$PUB/readyz")
[ "$ready" = "200" ] && ok "门户 readyz 200" || bad "门户 readyz 返回 ${ready}"
ready=$(code --max-time 5 "$ADM/readyz")
[ "$ready" = "200" ] && ok "后台 readyz 200" || bad "后台 readyz 返回 ${ready}"

admin_login && ok "后台管理员已登录" || { bad "后台管理员登录失败"; exit 1; }
MAIL_ORIG=$(curl -sS "${ADM}/v1/settings/mail" -H "$AH")
echo "$MAIL_ORIG" | jqr "['email_verification']" >/dev/null \
  && ok "已取邮件设置原值（邮箱验证 $(echo "$MAIL_ORIG" | jqr "['email_verification']")）" \
  || { bad "取邮件设置失败"; exit 1; }

#-------------------------------------------------------------------------------
sec "1. 注册（邮箱验证关闭）"
#-------------------------------------------------------------------------------
if [ "$(echo "$MAIL_ORIG" | jqr "['email_verification']")" = "True" ]; then
  set_email_verification false && ok "邮箱验证临时关闭（退出时恢复）" || { bad "关闭邮箱验证失败"; exit 1; }
fi

r1=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"${EMAIL}\"}")
REG_TOKEN=$(echo "$r1" | jqr "['registration_token']")
VERIFY=$(echo "$r1" | jqr "['verification_required']")
DEV_CODE=$(echo "$r1" | jqr "['dev_code']")
[ -n "$REG_TOKEN" ] && ok "注册事务已签发" || bad "未取得 registration_token"
[ "$VERIFY" = "False" ] && [ -z "$DEV_CODE" ] && ok "邮箱验证关闭时不要求、也不下发验证码" \
  || bad "邮箱验证关闭时 verification_required=${VERIFY} dev_code=${DEV_CODE:+有}"

r1b=$(json -X POST "$PUB/v1/auth/register/start" -d '{"email":"nonexistent-probe@example.test"}')
k1=$(echo "$r1" | python3 -c "import sys,json;print(sorted(json.load(sys.stdin).keys()))")
k2=$(echo "$r1b" | python3 -c "import sys,json;print(sorted(json.load(sys.stdin).keys()))")
[ "$k1" = "$k2" ] && ok "已存在与不存在邮箱的响应字段集一致" || bad "响应字段集不一致"

weak=$(code -X POST "$PUB/v1/auth/register/complete" \
  -d "{\"registration_token\":\"${REG_TOKEN}\",\"code\":\"\",\"password\":\"short\"}")
[ "$weak" = "422" ] && ok "弱口令被拒（422）" || bad "弱口令返回 ${weak}，期望 422"

r2h=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/auth/register/complete" \
  -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"${REG_TOKEN}\",\"code\":\"\",\"password\":\"${PASSWORD}\"}")
r2body=${r2h%$'\n'*}
r2code=${r2h##*$'\n'}
USER_ID=$(echo "$r2body" | jqr "['user_id']")
[ "$r2code" = "201" ] && [ -n "$USER_ID" ] && ok "账号已创建 user_id=${USER_ID}（201）" || bad "注册失败 HTTP ${r2code}: ${r2body}"

leak=$(db "SELECT count(*) FROM user_passwords WHERE phc NOT LIKE '\$argon2id\$%'")
[ "$leak" = "0" ] && ok "口令均为 Argon2id PHC" || bad "发现 ${leak} 条非 Argon2id 口令"
plain_pw=$(db "SELECT count(*) FROM user_passwords WHERE phc = '${PASSWORD}'")
[ "$plain_pw" = "0" ] && ok "库中无明文口令" || bad "发现明文口令"
vc_plain=$(db "SELECT count(*) FROM verification_codes WHERE octet_length(code_hash) <> 32")
[ "$vc_plain" = "0" ] && ok "验证码只存哈希" || bad "验证码列异常"

#-------------------------------------------------------------------------------
sec "1b. 邮箱验证开启时的验证码"
#-------------------------------------------------------------------------------
if set_email_verification true; then
  ok "邮箱验证已临时开启"
  VEMAIL="e2e-verify-${STAMP}@example.test"
  rv=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"${VEMAIL}\"}")
  VTOKEN=$(echo "$rv" | jqr "['registration_token']")
  VCODE=$(echo "$rv" | jqr "['dev_code']")
  [ "$(echo "$rv" | jqr "['verification_required']")" = "True" ] && [ -n "$VCODE" ] \
    && ok "开启后要求验证码且 dev_code 回显" || bad "开启后未要求验证码或未回显: $rv"

  rw=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/auth/register/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"${VTOKEN}\",\"code\":\"000000\",\"password\":\"${PASSWORD}\"}")
  wrongcode=${rw##*$'\n'}
  rwbody=${rw%$'\n'*}
  errcode=$(echo "$rwbody" | jqr "['error']['code']" 2>/dev/null || true)
  [ "$wrongcode" = "403" ] && [ "$errcode" = "forbidden" ] \
    && ok "错误验证码被拒（403 forbidden）" || bad "错误验证码返回 ${wrongcode}: ${rwbody}"

  rvc=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/auth/register/complete" \
    -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"${VTOKEN}\",\"code\":\"${VCODE}\",\"password\":\"${PASSWORD}\"}")
  rvccode=${rvc##*$'\n'}
  rvcbody=${rvc%$'\n'*}
  [ "$rvccode" = "201" ] && [ -n "$(echo "$rvcbody" | jqr "['user_id']")" ] && ok "正确验证码完成注册（201）" || bad "正确验证码注册失败: ${rvcbody}"

  set_email_verification orig && MAIL_TOUCHED=0 && ok "邮箱验证已恢复原值" || bad "恢复邮箱验证失败"
else
  bad "临时开启邮箱验证失败"
fi

#-------------------------------------------------------------------------------
sec "2. 登录与 JWT 域"
#-------------------------------------------------------------------------------
badlogin=$(code -X POST "$PUB/v1/auth/login" -d "{\"email\":\"${EMAIL}\",\"password\":\"wrong-password-here\"}")
[ "$badlogin" = "401" ] && ok "错误口令返回 401" || bad "错误口令返回 ${badlogin}"

nouser=$(code -X POST "$PUB/v1/auth/login" -d '{"email":"ghost-user@example.test","password":"wrong-password-here"}')
[ "$nouser" = "401" ] && ok "不存在账号同样返回 401" || bad "不存在账号返回 ${nouser}"

r3=$(json -X POST "$PUB/v1/auth/login" -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\"}")
TOKEN=$(echo "$r3" | jqr "['access_token']")
[ -n "$TOKEN" ] && ok "登录成功，已签发访问令牌" || bad "登录失败: $r3"

jwt_aud=$(python3 - "$TOKEN" <<'PY'
import sys, json, base64
parts = sys.argv[1].split(".")
if len(parts) != 3:
    raise SystemExit("not-jwt")
p = parts[1] + "=" * (-len(parts[1]) % 4)
pl = json.loads(base64.urlsafe_b64decode(p))
aud = pl.get("aud")
if isinstance(aud, list):
    aud = aud[0] if aud else ""
print(aud)
PY
)
[ "$jwt_aud" = "public" ] && ok "JWT aud 为门户受众 public" || bad "JWT aud=${jwt_aud}，期望 public"

p2=$(echo "$TOKEN" | cut -d. -f2)
tampered=$(echo "$TOKEN" | cut -d. -f1)."X${p2:1}."$(echo "$TOKEN" | cut -d. -f3)
tam=$(code "$PUB/v1/me" -H "Authorization: Bearer ${tampered}")
[ "$tam" = "401" ] && ok "篡改 payload 被拒（401）" || bad "篡改令牌返回 ${tam}"

crossdomain=$(python3 - "$TOKEN" <<'PY'
import sys, json, base64
parts = sys.argv[1].split(".")
p = parts[1] + "=" * (-len(parts[1]) % 4)
pl = json.loads(base64.urlsafe_b64decode(p))
pl["aud"] = "admin"
enc = base64.urlsafe_b64encode(json.dumps(pl, separators=(",",":")).encode()).decode().rstrip("=")
print(f"{parts[0]}.{enc}.{parts[2]}")
PY
)
cd_code=$(code "$PUB/v1/me" -H "Authorization: Bearer ${crossdomain}")
[ "$cd_code" = "401" ] && ok "aud 改为 admin 后门户 401" || bad "跨域 aud 返回 ${cd_code}"

adm_me=$(code "$ADM/v1/me" -H "Authorization: Bearer ${TOKEN}")
[ "$adm_me" = "401" ] && ok "门户令牌敲后台 401" || bad "门户令牌进后台 HTTP ${adm_me}"

me=$(curl -sS "$PUB/v1/me" -H "Authorization: Bearer ${TOKEN}")
meid=$(echo "$me" | jqr "['user_id']")
[ "$meid" = "$USER_ID" ] && ok "/v1/me 返回本人信息" || bad "/v1/me 异常: $me"

#-------------------------------------------------------------------------------
sec "3. 套餐目录（自建可服务节点 + authoring）"
#-------------------------------------------------------------------------------
# e2e job 无节点端：API 建池/服务器/节点后 SQL 标 ready 与 serving active（见 p7e2e-common）。
PLAN_TAG="e2e-${STAMP}"
admin_reauth || true

schemas=$(curl -sS "$ADM/v1/node-protocol-schemas" -H "$AH")
CIPHER=$(echo "$schemas" | python3 -c '
import sys,json
d=json.load(sys.stdin)
for s in d.get("schemas",[]):
    if s.get("node_type")=="shadowsocks":
        m=s.get("methods") or []
        print(m[0] if m else "aes-128-gcm"); raise SystemExit
raise SystemExit("no ss")
' 2>/dev/null) || CIPHER="aes-128-gcm"
[ -n "$CIPHER" ] && ok "shadowsocks cipher=${CIPHER}" || bad "无协议 schema"

rp=$(curl -sS -X POST "$ADM/v1/node-pools" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"code\":\"e2e-${STAMP}\",\"name\":\"E2E pool ${STAMP}\"}")
POOL_ID=$(echo "$rp" | jqr "['id']")
[ -n "$POOL_ID" ] && ok "节点池已创建" || { bad "建池失败: $rp"; exit 1; }

rs=$(curl -sS -X POST "$ADM/v1/servers" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"e2e-${STAMP}-s1\",\"hostname\":\"e2e-${STAMP}.example.test\",\"public_ipv4\":\"203.0.113.31\"}")
SERVER_ID=$(echo "$rs" | jqr "['id']")
[ -n "$SERVER_ID" ] && ok "服务器已创建" || { bad "建服务器失败: $rs"; exit 1; }

rn=$(curl -sS -X POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: e2e-node-${STAMP}" \
  -d "{\"name\":\"e2e-${STAMP}-n1\",\"server_id\":\"${SERVER_ID}\",\"pool_id\":\"${POOL_ID}\",\"node_type\":\"shadowsocks\",\"server_host\":\"e2e-${STAMP}.example.test\",\"server_port\":22101,\"traffic_rate\":1,\"display_name\":\"E2E Line ${STAMP}\",\"protocol_config\":{\"cipher\":\"${CIPHER}\"}}")
NODE_ID=$(echo "$rn" | jqr "['id']")
[ -n "$NODE_ID" ] && ok "节点已创建" || { bad "建节点失败: $rn"; exit 1; }

db "UPDATE servers SET status = 'ready', last_heartbeat_at = now() WHERE id = '${SERVER_ID}'" >/dev/null
db "UPDATE nodes SET serving_status = 'active' WHERE id = '${NODE_ID}'" >/dev/null
ok "夹具节点标为可服务（SQL：无节点端）"

admin_reauth || true
rp=$(curl -sS -X POST "$ADM/v1/plans" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${PLAN_TAG}-create" \
  -d "{\"code\":\"${PLAN_TAG}\",\"name\":\"E2E 链路 ${STAMP}\",\"visibility\":\"public\"}")
PLAN_ID=$(echo "$rp" | jqr "['plan']['id']"); PLAN_RV=$(echo "$rp" | jqr "['plan']['row_version']")
[ -n "$PLAN_ID" ] && ok "草稿套餐已创建 ${PLAN_ID}" || { bad "建套餐失败: $rp"; exit 1; }

rv=$(curl -sS -X POST "$ADM/v1/plans/${PLAN_ID}/versions" -H "$AH" \
  -H "Idempotency-Key: ${PLAN_TAG}-version")
VID=$(echo "$rv" | jqr "['version']['id']"); VRV=$(echo "$rv" | jqr "['version']['row_version']")
[ -n "$VID" ] && ok "草稿版本已创建" || { bad "建版本失败: $rv"; exit 1; }

sem=$(code -X PUT "$ADM/v1/plans/${PLAN_ID}/versions/${VID}" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"expected_row_version\":${VRV},\"quota_reset_strategy\":\"billing_cycle\",
       \"entitlements\":[{\"code\":\"feature.multi_device\",\"value\":true},{\"code\":\"support.tier\",\"value\":\"standard\"}],
       \"quotas\":[{\"metric\":\"traffic.bytes\",\"limit\":107374182400,\"unit\":\"bytes\",\"period\":\"cycle\"},
                     {\"metric\":\"devices.active\",\"limit\":3,\"unit\":\"count\",\"period\":\"total\"}]}")
[ "$sem" = "200" ] && VRV=$((VRV+1)) && ok "版本写入 2 项权益与 2 项配额" || { bad "写版本语义返回 ${sem}"; exit 1; }

rb=$(curl -sS -X POST "$ADM/v1/plans/${PLAN_ID}/pools" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${PLAN_TAG}-pools" \
  -d "{\"version_id\":\"${VID}\",\"expected_version_row_version\":${VRV},\"pool_ids\":[\"${POOL_ID}\"]}")
VRV=$(echo "$rb" | jqr "['row_version']")
[ -n "$VRV" ] && ok "版本已绑定节点池" || { bad "绑节点池失败: $rb"; exit 1; }

admin_reauth || true
rpr=$(curl -sS -X POST "$ADM/v1/plans/${PLAN_ID}/prices" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${PLAN_TAG}-price" \
  -d '{"currency":"CNY","unit_amount":990,"billing_interval":"month","interval_count":1}')
[ -n "$(echo "$rpr" | jqr "['price']['id']")" ] && ok "价格已创建（CNY 9.90 / 月）" || { bad "建价格失败: $rpr"; exit 1; }

admin_reauth || true
rpub=$(curl -sS -X POST "$ADM/v1/plans/${PLAN_ID}/versions/${VID}/publish" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: ${PLAN_TAG}-publish" \
  -d "{\"expected_plan_row_version\":${PLAN_RV},\"expected_version_row_version\":${VRV}}")
PLAN_RV=$(echo "$rpub" | jqr "['plan_row_version']")
[ -n "$PLAN_RV" ] && ok "版本已发布" || { bad "发布失败: $rpub"; exit 1; }
E2E_PLAN_ID="$PLAN_ID"; E2E_PLAN_RV="$PLAN_RV"

plans=$(curl -sS "$PUB/v1/plans")
PRICE_ID=$(echo "$plans" | python3 -c "
import sys,json
for p in json.load(sys.stdin).get('plans',[]):
    if p['id']=='${PLAN_ID}':
        for pr in p.get('prices',[]): print(pr['id']); raise SystemExit
" 2>/dev/null)
PRICE_AMT=$(echo "$plans" | python3 -c "
import sys,json
for p in json.load(sys.stdin).get('plans',[]):
    if p['id']=='${PLAN_ID}':
        for pr in p.get('prices',[]): print(pr['unit_amount']); raise SystemExit
" 2>/dev/null)
[ -n "$PRICE_ID" ] && [ "$PRICE_AMT" = "990" ] && ok "门户目录可见本套餐，价格 ${PRICE_AMT} 分" || bad "门户目录里没有本套餐或价格不对"

#-------------------------------------------------------------------------------
sec "4. 下单（价格快照与幂等）"
#-------------------------------------------------------------------------------
IDEM="order-${STAMP}"
r4h=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/orders" \
  -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${IDEM}" \
  -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0}")
r4=${r4h%$'\n'*}; r4c=${r4h##*$'\n'}
ORDER_ID=$(echo "$r4" | jqr "['order_id']"); ORDER_NO=$(echo "$r4" | jqr "['order_no']")
PAYABLE=$(echo "$r4" | jqr "['payable_amount']"); CURRENCY=$(echo "$r4" | jqr "['currency']")
[ "$r4c" = "201" ] && [ -n "$ORDER_ID" ] && [ "$PAYABLE" = "990" ] \
  && ok "订单已创建 ${ORDER_NO} 应付=${PAYABLE}（201）" || bad "下单失败 HTTP ${r4c}: ${r4}"

r4b=$(json -X POST "$PUB/v1/orders" -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${IDEM}" \
  -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0}")
ORDER_ID2=$(echo "$r4b" | jqr "['order_id']")
[ "$ORDER_ID" = "$ORDER_ID2" ] && ok "相同幂等键重放返回同一订单" || bad "幂等失效"

conf=$(code -X POST "$PUB/v1/orders" -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: ${IDEM}" \
  -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0,\"label\":\"dup\"}")
[ "$conf" = "409" ] && ok "同幂等键+不同请求体返回 409" || bad "期望 409，实际 ${conf}"

snap=$(db "SELECT coalesce(jsonb_array_length(snapshot_entitlements),0) FROM order_items WHERE order_id='${ORDER_ID}'")
[ "${snap:-0}" -ge 1 ] && ok "订单行已固化 ${snap} 项权益快照" || bad "权益快照缺失"

#-------------------------------------------------------------------------------
sec "5. 易支付渠道与回调幂等"
#-------------------------------------------------------------------------------
admin_reauth || true
E2E_KEY="$EPAY_KEY"
prov_body=$(python3 -c "import json; print(json.dumps({
  'code':'${EPAY_CODE}','adapter':'epay','display_name':'E2E Epay ${STAMP}',
  'base_url':'https://pay.example.test','methods':['alipay'],'default_method':'alipay',
  'merchant_id':'${EPAY_PID}','key':'${E2E_KEY}'}))")
cp=$(code -X POST "$ADM/v1/payment-providers" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: epay-prov-${STAMP}" -d "$prov_body")
[ "$cp" = "201" ] && ok "易支付渠道已创建 ${EPAY_CODE}" || bad "建渠道 HTTP ${cp}"

tg=$(code -X POST "$ADM/v1/payment-providers/${EPAY_CODE}/toggle" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"accepting_new":true}')
[ "$tg" = "200" ] && ok "渠道已启用且接受新单" || bad "toggle HTTP ${tg}"

payh=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/orders/${ORDER_ID}/pay" \
  -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"${EPAY_CODE}\",\"method\":\"alipay\"}")
payc=${payh##*$'\n'}
[ "$payc" = "201" ] && ok "支付意图已创建（201）" || bad "去支付 HTTP ${payc}"

MONEY="9.90"
TRADE_NO="E2E${STAMP}"
NOTIFY="$PUB/v1/webhooks/payments/${EPAY_CODE}"

badsig=$(curl -sS "$NOTIFY?pid=${EPAY_PID}&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&money=${MONEY}&trade_status=TRADE_SUCCESS&sign=deadbeef&sign_type=MD5")
[ "$badsig" = "fail" ] && ok "坏签名回调回 fail" || bad "坏签名未被拒: ${badsig}"

GOODSIG=$(epay_sign "pid=${EPAY_PID}" "trade_no=${TRADE_NO}" "out_trade_no=${ORDER_NO}" "type=alipay" "money=${MONEY}" "trade_status=TRADE_SUCCESS")
URL="${NOTIFY}?pid=${EPAY_PID}&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&money=${MONEY}&trade_status=TRADE_SUCCESS&sign=${GOODSIG}&sign_type=MD5"

RESULTS=""
for i in $(seq 1 10); do
  RESULTS="${RESULTS}$(curl -sS "$URL")"
done
case "$RESULTS" in
  successsuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccess) ok "10 次回调全部 success" ;;
  *) bad "回执不符: ${RESULTS}" ;;
esac

paycnt=$(db "SELECT count(*) FROM payments WHERE order_id='${ORDER_ID}'")
[ "$paycnt" = "1" ] && ok "10 次回调只产生 1 条支付记录" || bad "支付记录 ${paycnt} 条"

subcnt=$(db "SELECT count(*) FROM subscriptions WHERE user_id='${USER_ID}'")
[ "$subcnt" = "1" ] && ok "10 次回调只激活 1 个订阅" || bad "订阅 ${subcnt} 个"

SUB_ID=$(db "SELECT id FROM subscriptions WHERE source_order_id='${ORDER_ID}' LIMIT 1")
[ -n "$SUB_ID" ] && ok "SUB_ID=${SUB_ID}" || bad "未找到订阅"

txncnt=$(db "SELECT count(*) FROM ledger_transactions WHERE source_type='order' AND source_id='${ORDER_ID}' AND kind='order_paid'")
[ "$txncnt" = "1" ] && ok "order_paid 账本交易 1 笔" || bad "账务交易 ${txncnt} 笔"

evtcnt=$(db "SELECT count(*) FROM payment_events WHERE provider_event_id='${TRADE_NO}:TRADE_SUCCESS'")
[ "$evtcnt" = "1" ] && ok "payment_events 幂等键 1 行" || bad "事件 ${evtcnt} 条"

#-------------------------------------------------------------------------------
sec "6. 账本正确性"
#-------------------------------------------------------------------------------
TXN=$(db "SELECT id FROM ledger_transactions WHERE source_type='order' AND source_id='${ORDER_ID}' AND kind='order_paid' LIMIT 1")
if [ -z "$TXN" ]; then
  bad "未产生 order_paid 交易"
else
  ok "账务交易 ${TXN}"
  entrycnt=$(db "SELECT count(*) FROM ledger_entries WHERE transaction_id='${TXN}'")
  [ "${entrycnt:-0}" -ge 2 ] && ok "该交易含 ${entrycnt} 条分录" || bad "分录数 ${entrycnt} < 2"
  imbalance=$(db "SELECT coalesce(sum(CASE direction WHEN 'debit' THEN amount ELSE -amount END),0) FROM ledger_entries WHERE transaction_id='${TXN}'")
  is_zero_amount "$imbalance" && ok "该笔交易借贷配平" || bad "借贷不平: ${imbalance}"
fi

drift=$(db "SELECT count(*) FROM ledger_accounts a
  LEFT JOIN (SELECT account_id,
                    coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
                    coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
               FROM ledger_entries GROUP BY account_id) s ON s.account_id = a.id
 WHERE a.balance <> CASE WHEN a.account_type IN ('channel_cash','platform_fee_expense','platform_promo_expense','suspense')
                         THEN coalesce(s.d,0) - coalesce(s.c,0) ELSE coalesce(s.c,0) - coalesce(s.d,0) END")
[ "$drift" = "0" ] && ok "全部账户无漂移" || bad "${drift} 个账户漂移"

revenue=$(db "SELECT coalesce(sum(le.amount),0) FROM ledger_entries le
  JOIN ledger_accounts la ON la.id = le.account_id
 WHERE la.account_type='platform_revenue' AND le.direction='credit'
   AND le.transaction_id='${TXN}'")
[ "$revenue" = "9.90" ] && ok "平台收入贷方 9.90 元与订单一致" || bad "收入 ${revenue} ≠ 9.90"

#-------------------------------------------------------------------------------
sec "7. 订阅与配额"
#-------------------------------------------------------------------------------
subs=$(curl -sS "$PUB/v1/me/subscriptions" -H "Authorization: Bearer ${TOKEN}")
sstatus=$(echo "$subs" | jqr "['subscriptions'][0]['status']")
[ "$sstatus" = "active" ] && ok "订阅状态为 active" || bad "订阅状态 ${sstatus}"

qcnt=$(db "SELECT count(*) FROM quota_balances WHERE subscription_id='${SUB_ID}'")
[ "${qcnt:-0}" -ge 1 ] && ok "已初始化 ${qcnt} 项配额" || bad "配额数 ${qcnt}"
has_traffic=$(db "SELECT count(*) FROM quota_balances WHERE subscription_id='${SUB_ID}' AND metric='traffic.bytes'")
[ "$has_traffic" -ge 1 ] && ok "含 traffic.bytes 配额行" || bad "缺少 traffic.bytes"

evt=$(db "SELECT count(*) FROM subscription_events WHERE subscription_id='${SUB_ID}' AND event_type='activated' AND from_status IS NULL AND to_status='active'")
[ "$evt" = "1" ] && ok "activated 事件（无 pending）" || bad "状态事件异常 count=${evt}"

credcnt=$(db "SELECT count(*) FROM subscription_credentials WHERE subscription_id='${SUB_ID}' AND status='active'")
[ "$credcnt" = "1" ] && ok "1 份 active 凭据" || bad "凭据数 ${credcnt}"

th=$(db "SELECT octet_length(token_hash) FROM subscription_credentials WHERE subscription_id='${SUB_ID}' AND status='active' LIMIT 1")
[ "$th" = "32" ] && ok "token_hash 为 32 字节" || bad "token_hash 长度 ${th}"

ordstatus=$(db "SELECT status FROM orders WHERE id='${ORDER_ID}'")
[ "$ordstatus" = "fulfilled" ] && ok "订单 fulfilled" || bad "订单状态 ${ordstatus}"

#-------------------------------------------------------------------------------
sec "8. 审计"
#-------------------------------------------------------------------------------
audits=$(db "SELECT count(*) FROM audit_events WHERE occurred_at > '${SCRIPT_START}'::timestamptz")
[ "${audits:-0}" -ge 3 ] && ok "脚本开始后审计 ≥3 条（${audits}）" || bad "审计仅 ${audits} 条"

trigger_cnt=$(db "SELECT count(*) FROM pg_trigger WHERE tgrelid = 'audit_events'::regclass AND tgname = 'audit_events_append_only' AND NOT tgisinternal")
[ "$trigger_cnt" = "1" ] && ok "audit_events_append_only 触发器存在" || bad "触发器 count=${trigger_cnt}"

#-------------------------------------------------------------------------------
sec "9. 限流与错误处理"
#-------------------------------------------------------------------------------
RL_EMAIL="rl-${STAMP}@example.test"
RL_PROBE=14
codes=""
for i in $(seq 1 $RL_PROBE); do
  c=$(code -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"${RL_EMAIL}\",\"password\":\"xxxxxxxxxx\"}")
  codes="${codes}${c} "
done
echo "$codes" | grep -q 429 && ok "认证接口触发限流 429" || bad "未触发限流：${codes}"

# 用三段路径：两段的 /<前缀>/<令牌> 会落到订阅拉取路由，那里故意回不带信封的裸 404
nf=$(curl -sS "$PUB/v1/nonexistent/endpoint")
echo "$nf" | grep -qi 'stack\|goroutine\|/opt/\|\.go:' && bad "错误响应泄露内部信息" \
  || ok "404 不含堆栈与源码路径"
echo "$nf" | grep -q 'request_id' && ok "错误响应带 request_id" || bad "错误响应缺 request_id"

#-------------------------------------------------------------------------------
printf '\n\033[1m================ 端到端结果 ================\033[0m\n'
printf '  通过 \033[32m%d\033[0m   失败 \033[31m%d\033[0m\n\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
