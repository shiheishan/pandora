#!/usr/bin/env bash
# 易支付端到端：自建渠道与套餐 → 注册下单 → 收银台验签 → 防篡改回调 →
# 幂等入账 → 账本与凭据保护。
#
# 口径：渠道（code 由脚本生成，回调走 /webhooks/payments/<code>）、套餐、可服务节点都由本脚本经后台自建，
# 商户号与密钥现生成；在途意图只有 requires_action；库内金额是元（numeric(20,2)）、JSON 是分；
# 账本漂移用 DriftAccounts 口径 SQL。
#
# 仍用 SQL 的步骤及原因：
#   - 在途意图数、payment_events 幂等键、账本配平与漂移、凭据密文不含明文：核对库内事实，无等价只读 API
#   - 可服务节点：e2e job 无节点端，发布套餐需 DeliverableNodeCond
#
# 环境：PUB、ADM、PSQL、ADMIN_EMAIL、ADMIN_PASS（必填）；EPAY_E2E_DISPOSABLE=YES_DELETE_FIXTURES（必填）。
# 可选 SERVE_LOG：CI 指向 serve 日志文件，检查是否泄露渠道密钥。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
SERVE_LOG=${SERVE_LOG:-}

pass=0; fail=0
ok()   { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad()  { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec()  { echo; echo "=== $1 ==="; }

db() { "$PSQL" -X -qAtc "$1" | tr -d '[:space:]'; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code() { curl -sS -o /dev/null -w '%{http_code}' "$@"; }
json() { curl -sS -H 'Content-Type: application/json' "$@"; }

[ "${EPAY_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] EPAY_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$(db 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }
# 注册 IP 限额是后台设置（缺省每 IP 每分钟 10 次），连着注册会撞 429：一次性库上先关掉（见 lib/signup.sh）
. "$(dirname "$0")/lib/signup.sh"
relax_signup_ip_limit

STAMP="$(date +%s)$RANDOM"
EPAY_CODE="epay-${STAMP}"
EPAY_KEY="E2E_KEY_$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
EPAY_PID="10$RANDOM"

AH=""
PLAN_ID=""; PLAN_RV=""
POOL_ID=""; SERVER_ID=""; NODE_ID=""

is_zero_amount() {
  case "$1" in 0|0.00|0.0) return 0 ;; *) return 1 ;; esac
}

admin_login() {
  local body tok
  body=$(json -X POST "$ADM/v1/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}")
  tok=$(echo "$body" | jqr "['access_token']")
  [ -n "$tok" ] && AH="Authorization: Bearer ${tok}"
}

admin_reauth() {
  local body tok
  body=$(json -X POST "$ADM/v1/auth/reauth" -H "$AH" -d "{\"password\":\"$ADMIN_PASS\"}")
  tok=$(echo "$body" | jqr "['access_token']")
  [ -n "$tok" ] && AH="Authorization: Bearer ${tok}"
}

# 易支付 MD5：非空参数按名升序 k=v&，末尾直接接密钥，不做 URL 编码
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

cleanup() {
  local status=$?
  trap - EXIT
  if [ -n "$PLAN_ID" ] && [ -n "$AH" ]; then
    admin_login 2>/dev/null || true
    [ -n "$AH" ] && admin_reauth 2>/dev/null || true
    if [ -n "$AH" ]; then
      gp=$(curl -sS "$ADM/v1/plans/$PLAN_ID" -H "$AH")
      rv=$(echo "$gp" | jqr "['plan']['row_version']" 2>/dev/null || true)
      if [ -n "$rv" ]; then
        c=$(code -X POST "$ADM/v1/plans/${PLAN_ID}/archive" -H "$AH" -H 'Content-Type: application/json' \
          -H "Idempotency-Key: epay-plan-archive-${STAMP}" -d "{\"expected_row_version\":${rv}}")
        [ "$c" = "200" ] || { echo "  [恢复失败] 套餐归档 HTTP ${c}" >&2; status=1; }
      fi
    fi
  fi
  if [ -n "$EPAY_CODE" ] && [ -n "$AH" ]; then
    c=$(code -X POST "$ADM/v1/payment-providers/${EPAY_CODE}/toggle" -H "$AH" -H 'Content-Type: application/json' \
      -d '{"enabled":false,"accepting_new":false}')
    [ "$c" = "200" ] || { echo "  [恢复失败] 渠道 toggle HTTP ${c}" >&2; status=1; }
  fi
  exit "$status"
}
trap cleanup EXIT

#-------------------------------------------------------------------------------
sec "0. 前置：环境、渠道与可服务套餐"
#-------------------------------------------------------------------------------
ready=$(code --max-time 5 "$PUB/readyz")
[ "$ready" = "200" ] && ok "门户 readyz 200" || bad "门户 readyz" "HTTP ${ready}"

admin_login && ok "后台管理员已登录" || { bad "后台登录失败" ""; exit 1; }

schemas=$(curl -sS "$ADM/v1/node-protocol-schemas" -H "$AH")
CIPHER=$(echo "$schemas" | python3 -c '
import sys,json
for s in json.load(sys.stdin).get("schemas",[]):
    if s.get("node_type")=="shadowsocks":
        m=s.get("methods") or []
        print(m[0] if m else "aes-128-gcm"); raise SystemExit
print("aes-128-gcm")
' 2>/dev/null)
ok "shadowsocks cipher=${CIPHER}"

rp=$(json -X POST "$ADM/v1/node-pools" -H "$AH" -d "{\"code\":\"epay-${STAMP}\",\"name\":\"Epay pool ${STAMP}\"}")
POOL_ID=$(echo "$rp" | jqr "['id']")
[ -n "$POOL_ID" ] && ok "节点池已创建" || { bad "建池失败" "$rp"; exit 1; }

rs=$(json -X POST "$ADM/v1/servers" -H "$AH" -d "{\"name\":\"epay-${STAMP}-s1\",\"hostname\":\"epay-${STAMP}.example.test\",\"public_ipv4\":\"203.0.113.32\"}")
SERVER_ID=$(echo "$rs" | jqr "['id']")
[ -n "$SERVER_ID" ] && ok "服务器已创建" || { bad "建服务器失败" "$rs"; exit 1; }

rn=$(curl -sS -X POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: epay-node-${STAMP}" \
  -d "{\"name\":\"epay-${STAMP}-n1\",\"server_id\":\"${SERVER_ID}\",\"pool_id\":\"${POOL_ID}\",\"node_type\":\"shadowsocks\",\"server_host\":\"epay-${STAMP}.example.test\",\"server_port\":22102,\"traffic_rate\":1,\"display_name\":\"Epay Line ${STAMP}\",\"protocol_config\":{\"cipher\":\"${CIPHER}\"}}")
NODE_ID=$(echo "$rn" | jqr "['id']")
[ -n "$NODE_ID" ] && ok "节点已创建" || { bad "建节点失败" "$rn"; exit 1; }

db "UPDATE servers SET status = 'ready', last_heartbeat_at = now() WHERE id = '${SERVER_ID}'" >/dev/null
db "UPDATE nodes SET serving_status = 'active' WHERE id = '${NODE_ID}'" >/dev/null
ok "夹具节点标为可服务（e2e 无节点端，SQL 满足发布条件）"

admin_reauth || true
prov_body=$(python3 -c "import json; print(json.dumps({
  'code':'${EPAY_CODE}','adapter':'epay','display_name':'Epay E2E ${STAMP}',
  'base_url':'https://pay.example.test','methods':['alipay'],'default_method':'alipay',
  'merchant_id':'${EPAY_PID}','key':'${EPAY_KEY}'}))")
cp=$(code -X POST "$ADM/v1/payment-providers" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: epay-prov-create-${STAMP}" -d "$prov_body")
[ "$cp" = "201" ] && ok "易支付渠道已创建 ${EPAY_CODE}" || { bad "建渠道失败" "HTTP ${cp}"; exit 1; }

tg=$(code -X POST "$ADM/v1/payment-providers/${EPAY_CODE}/toggle" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"enabled":true,"accepting_new":true}')
[ "$tg" = "200" ] && ok "渠道 accepting_new=true" || { bad "toggle 失败" "HTTP ${tg}"; exit 1; }

admin_reauth || true
rpc=$(curl -sS -w '\n%{http_code}' -X POST "$ADM/v1/plans/complete" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: epay-plan-${STAMP}" \
  -d "{\"code\":\"epay-plan-${STAMP}\",\"name\":\"Epay Plan ${STAMP}\",\"visibility\":\"public\",\"traffic_gb\":1,\"pool_ids\":[\"${POOL_ID}\"],\"prices\":[{\"billing_interval\":\"month\",\"interval_count\":1,\"unit_amount\":990,\"currency\":\"CNY\",\"trial_days\":0}],\"publish\":true}")
rpc_body=${rpc%$'\n'*}; rpc_code=${rpc##*$'\n'}
PLAN_ID=$(echo "$rpc_body" | jqr "['plan']['id']")
PRICE_ID=$(echo "$rpc_body" | jqr "['price_ids'][0]")
PLAN_RV=$(echo "$rpc_body" | jqr "['plan']['row_version']")
[ "$rpc_code" = "201" ] && [ -n "$PLAN_ID" ] && [ -n "$PRICE_ID" ] \
  && ok "套餐已发布 plan=${PLAN_ID} price=${PRICE_ID}" \
  || { bad "plans/complete 失败" "HTTP ${rpc_code} ${rpc_body}"; exit 1; }

#-------------------------------------------------------------------------------
sec "1. 注册并登录"
#-------------------------------------------------------------------------------
EMAIL="epay_${STAMP}@example.test"
PASS="Epay!E2e2026x"

r1=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"${EMAIL}\"}")
RTOK=$(echo "$r1" | jqr "['registration_token']")
DEV_CODE=$(echo "$r1" | jqr "['dev_code']" 2>/dev/null || true)
[ -n "$RTOK" ] && ok "注册事务已签发" || { bad "注册 start 失败" "$r1"; exit 1; }

r2h=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"${RTOK}\",\"code\":\"${DEV_CODE:-}\",\"password\":\"${PASS}\"}")
r2body=${r2h%$'\n'*}; r2code=${r2h##*$'\n'}
USER_ID=$(echo "$r2body" | jqr "['user_id']")
[ "$r2code" = "201" ] && [ -n "$USER_ID" ] && ok "用户已创建（201）" || bad "注册 complete 失败" "HTTP ${r2code} ${r2body}"

r3=$(json -X POST "$PUB/v1/auth/login" -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASS}\"}")
TOKEN=$(echo "$r3" | jqr "['access_token']")
[ -n "$TOKEN" ] && ok "登录成功" || { bad "登录失败" "$r3"; exit 1; }
AUTH="Authorization: Bearer ${TOKEN}"

#-------------------------------------------------------------------------------
sec "2. 下单"
#-------------------------------------------------------------------------------
r4h=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/orders" -H "$AUTH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: epay-order-${STAMP}" \
  -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0}")
r4=${r4h%$'\n'*}; r4c=${r4h##*$'\n'}
ORDER_ID=$(echo "$r4" | jqr "['order_id']"); ORDER_NO=$(echo "$r4" | jqr "['order_no']")
PAYABLE=$(echo "$r4" | jqr "['payable_amount']"); CURRENCY=$(echo "$r4" | jqr "['currency']")
[ "$r4c" = "201" ] && [ -n "$ORDER_ID" ] && [ "$PAYABLE" = "990" ] \
  && ok "订单已创建 ${ORDER_NO} 应付 ${PAYABLE} ${CURRENCY}（201）" \
  || bad "下单失败" "HTTP ${r4c} ${r4}"

MONEY=$(python3 -c "print(f'{int(${PAYABLE})//100}.{int(${PAYABLE})%100:02d}')")

#-------------------------------------------------------------------------------
sec "3. 支付意图与收银台"
#-------------------------------------------------------------------------------
pay1=$(curl -sS -w '\n%{http_code}' -X POST "$PUB/v1/orders/${ORDER_ID}/pay" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"${EPAY_CODE}\",\"method\":\"alipay\"}")
pay_body=${pay1%$'\n'*}; pay_c=${pay1##*$'\n'}
REDIR=$(echo "$pay_body" | jqr "['redirect_url']")
INTENT=$(echo "$pay_body" | jqr "['intent_id']")
[ "$pay_c" = "201" ] && [ -n "$REDIR" ] && [ -n "$INTENT" ] && ok "支付意图已创建 ${INTENT}（201）" || bad "去支付失败" "HTTP ${pay_c} ${pay_body}"

CHECK=$(python3 - "$REDIR" "$PAYABLE" "$ORDER_NO" "$EPAY_PID" "$EPAY_KEY" <<'PY'
import sys, hashlib
from urllib.parse import urlparse, parse_qs
url, payable, order_no, pid, key = sys.argv[1:6]
q = {k: v[0] for k, v in parse_qs(urlparse(url).query).items()}
problems = []
expect_money = f"{int(payable)//100}.{int(payable)%100:02d}"
if q.get('money') != expect_money:
    problems.append(f"money={q.get('money')} 期望 {expect_money}")
if q.get('out_trade_no') != order_no:
    problems.append(f"out_trade_no 不符")
if q.get('pid') != pid:
    problems.append(f"pid={q.get('pid')} 期望 {pid}")
if q.get('sign_type') != 'MD5':
    problems.append('sign_type 不是 MD5')
params = {k: v for k, v in q.items() if v and k not in ('sign', 'sign_type')}
s = '&'.join(f'{k}={params[k]}' for k in sorted(params)) + key
if hashlib.md5(s.encode()).hexdigest() != q.get('sign', '').lower():
    problems.append('签名与参数不匹配')
print('|'.join(problems) if problems else f"OK money={q.get('money')}")
PY
)
case "$CHECK" in
  OK*) ok "收银台参数与签名正确（$CHECK）" ;;
  *)   bad "收银台参数有误" "$CHECK" ;;
esac

pay2=$(json -X POST "$PUB/v1/orders/${ORDER_ID}/pay" -H "$AUTH" -H 'Content-Type: application/json' \
  -d "{\"provider\":\"${EPAY_CODE}\",\"method\":\"alipay\"}")
INTENT2=$(echo "$pay2" | jqr "['intent_id']")
REUSED=$(echo "$pay2" | jqr "['reused']")
case "$REUSED" in True|true) ru=1 ;; *) ru=0 ;; esac
[ "$INTENT2" = "$INTENT" ] && [ "$ru" = 1 ] && ok "连点两次复用意图（reused=true）" \
  || bad "重复发起未复用" "intent ${INTENT} -> ${INTENT2} reused=${REUSED}"

N_INTENT=$(db "SELECT count(*) FROM payment_intents WHERE order_id='${ORDER_ID}' AND status='requires_action'")
[ "$N_INTENT" = "1" ] && ok "在途意图 requires_action = 1" || bad "在途意图数异常" "count=${N_INTENT}"

#-------------------------------------------------------------------------------
sec "4. 防篡改：坏回调必须被拒"
#-------------------------------------------------------------------------------
TRADE_NO="E2E${STAMP}"
NOTIFY="$PUB/v1/webhooks/payments/${EPAY_CODE}"

R=$(curl -sS "$NOTIFY?pid=${EPAY_PID}&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&money=${MONEY}&trade_status=TRADE_SUCCESS&sign=00000000000000000000000000000000&sign_type=MD5")
[ "$R" = "fail" ] && ok "坏签名回 fail" || bad "坏签名未被拒" "响应=${R}"

BADSIG=$(epay_sign "pid=${EPAY_PID}" "trade_no=${TRADE_NO}" "out_trade_no=${ORDER_NO}" "type=alipay" "money=0.01" "trade_status=TRADE_SUCCESS")
R=$(curl -sS "$NOTIFY?pid=${EPAY_PID}&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&money=0.01&trade_status=TRADE_SUCCESS&sign=${BADSIG}&sign_type=MD5")
[ "$R" = "fail" ] && ok "金额不符回 fail（ErrAmountMismatch）" || bad "金额篡改未被拒" "响应=${R}"

FOREIGN=$(epay_sign "pid=9999" "trade_no=${TRADE_NO}" "out_trade_no=${ORDER_NO}" "type=alipay" "money=${MONEY}" "trade_status=TRADE_SUCCESS")
R=$(curl -sS "$NOTIFY?pid=9999&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&money=${MONEY}&trade_status=TRADE_SUCCESS&sign=${FOREIGN}&sign_type=MD5")
[ "$R" = "fail" ] && ok "外来商户号回 fail" || bad "其他商户号未被拒" "响应=${R}"

ST=$(db "SELECT status FROM orders WHERE id='${ORDER_ID}'")
[ "$ST" = "pending_payment" ] && ok "坏回调后订单仍 pending_payment" || bad "订单被污染" "status=${ST}"

#-------------------------------------------------------------------------------
sec "5. 正常回调 + 幂等（重复 10 次）"
#-------------------------------------------------------------------------------
GOODSIG=$(epay_sign "pid=${EPAY_PID}" "trade_no=${TRADE_NO}" "out_trade_no=${ORDER_NO}" "type=alipay" "name=套餐" "money=${MONEY}" "trade_status=TRADE_SUCCESS")
URL="${NOTIFY}?pid=${EPAY_PID}&trade_no=${TRADE_NO}&out_trade_no=${ORDER_NO}&type=alipay&name=%E5%A5%97%E9%A4%90&money=${MONEY}&trade_status=TRADE_SUCCESS&sign=${GOODSIG}&sign_type=MD5"

RESULTS=""
for i in $(seq 1 10); do
  RESULTS="${RESULTS}$(curl -sS "$URL")"
done
case "$RESULTS" in
  successsuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccess) ok "10 次回调全部 success" ;;
  *) bad "回执不符" "$RESULTS" ;;
esac

N_EV=$(db "SELECT count(*) FROM payment_events WHERE provider_event_id='${TRADE_NO}:TRADE_SUCCESS'")
[ "$N_EV" = "1" ] && ok "payment_events 1 行" || bad "事件重复" "count=${N_EV}"

N_PAY=$(db "SELECT count(*) FROM payments WHERE order_id='${ORDER_ID}'")
[ "$N_PAY" = "1" ] && ok "payments 1 笔" || bad "支付重复" "count=${N_PAY}"

N_SUB=$(db "SELECT count(*) FROM subscriptions WHERE user_id='${USER_ID}'")
[ "$N_SUB" = "1" ] && ok "订阅 1 份" || bad "订阅重复" "count=${N_SUB}"

N_TXN=$(db "SELECT count(*) FROM ledger_transactions WHERE source_type='order' AND source_id='${ORDER_ID}' AND kind='order_paid'")
[ "$N_TXN" = "1" ] && ok "ledger order_paid 1 笔" || bad "账本重复" "count=${N_TXN}"

#-------------------------------------------------------------------------------
sec "6. 金额与账本正确性"
#-------------------------------------------------------------------------------
PAID=$(db "SELECT amount FROM payments WHERE order_id='${ORDER_ID}'")
EXPECT_YUAN=$(python3 -c "c=int(${PAYABLE}); print(f'{c//100}.{c%100:02d}')")
[ "$PAID" = "$EXPECT_YUAN" ] && ok "入账 ${PAID} 元 = JSON ${PAYABLE} 分（回调 ${MONEY}）" \
  || bad "金额换算" "库 ${PAID} 期望 ${EXPECT_YUAN}"

ORDER_ST=$(db "SELECT status FROM orders WHERE id='${ORDER_ID}'")
[ "$ORDER_ST" = "fulfilled" ] && ok "订单 fulfilled" || bad "订单状态" "$ORDER_ST"

SUB_ST=$(db "SELECT status FROM subscriptions WHERE user_id='${USER_ID}'")
[ "$SUB_ST" = "active" ] && ok "订阅 active" || bad "订阅状态" "$SUB_ST"

TXN=$(db "SELECT id FROM ledger_transactions WHERE source_type='order' AND source_id='${ORDER_ID}' AND kind='order_paid' LIMIT 1")
if [ -n "$TXN" ]; then
  IMBAL=$(db "SELECT coalesce(sum(CASE direction WHEN 'debit' THEN amount ELSE -amount END),0) FROM ledger_entries WHERE transaction_id='${TXN}'")
  is_zero_amount "$IMBAL" && ok "本单交易借贷配平" || bad "本单不平" "差额=${IMBAL}"
else
  bad "无 order_paid 交易" ""
fi

DRIFT=$(db "SELECT count(*) FROM ledger_accounts a
  LEFT JOIN (SELECT account_id,
                    coalesce(sum(amount) FILTER (WHERE direction = 'debit'), 0) AS d,
                    coalesce(sum(amount) FILTER (WHERE direction = 'credit'), 0) AS c
               FROM ledger_entries GROUP BY account_id) s ON s.account_id = a.id
 WHERE a.balance <> CASE WHEN a.account_type IN ('channel_cash','platform_fee_expense','platform_promo_expense','suspense')
                         THEN coalesce(s.d,0) - coalesce(s.c,0) ELSE coalesce(s.c,0) - coalesce(s.d,0) END")
[ "$DRIFT" = "0" ] && ok "全库账户无漂移" || bad "账户漂移" "${DRIFT} 个"

INTENT_ST=$(db "SELECT status FROM payment_intents WHERE id='${INTENT}'")
[ "$INTENT_ST" = "succeeded" ] && ok "意图 succeeded" || bad "意图状态" "$INTENT_ST"

N_OPEN=$(db "SELECT count(*) FROM payment_intents WHERE order_id='${ORDER_ID}' AND status='requires_action'")
[ "$N_OPEN" = "0" ] && ok "无在途意图" || bad "仍有在途" "count=${N_OPEN}"

LINKED=$(db "SELECT payment_intent_id FROM payments WHERE order_id='${ORDER_ID}'")
[ "$LINKED" = "$INTENT" ] && ok "payments.payment_intent_id 关联意图" || bad "未关联意图" "${LINKED} vs ${INTENT}"

#-------------------------------------------------------------------------------
sec "7. 凭据保护"
#-------------------------------------------------------------------------------
PLAIN=$(db "SELECT count(*) FROM payment_providers WHERE code='${EPAY_CODE}' AND position(convert_to('${EPAY_KEY}','UTF8') in credentials_sealed) > 0")
[ "$PLAIN" = "0" ] && ok "密钥未明文出现在 credentials_sealed" || bad "密钥明文落库" ""

plist=$(curl -sS "$ADM/v1/payment-providers" -H "$AH")
echo "$plist" | grep -q "$EPAY_KEY" && bad "后台列表响应含密钥" "" || ok "GET payment-providers 不含密钥"

if [ -n "$SERVE_LOG" ] && [ -f "$SERVE_LOG" ]; then
  grep -q "$EPAY_KEY" "$SERVE_LOG" && bad "密钥出现在 SERVE_LOG" "" || ok "SERVE_LOG 未含密钥"
else
  echo "  - 跳过日志检查：SERVE_LOG 未设置或文件不存在"
fi

#-------------------------------------------------------------------------------
echo
echo "==============================================="
echo "  通过 $pass 项，失败 $fail 项"
echo "==============================================="
[ "$fail" -eq 0 ] || exit 1
