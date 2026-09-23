#!/usr/bin/env bash
# 易支付端到端测试。
#
# 覆盖：
#   1. 下单 → 创建支付意图 → 校验收银台参数与签名
#   2. 金额换算：990 分必须提交为 "9.90"，回调 "9.90" 必须还原为 990 分
#   3. PAY-003 幂等：同一回调重复 10 次，账本只有一笔、权益只发一次
#   4. 防篡改：改金额、改商户号、坏签名一律拒绝
#   5. PAY-005 复式记账：回调后全账户借贷配平
#   6. 连点两次「去支付」复用同一个支付意图，不产生两笔待付款
set -uo pipefail

BASE=${BASE:-http://127.0.0.1:9000}
PSQL=/opt/aegispanel/deploy/psql.sh
EPAY_KEY=${EPAY_KEY:-TESTKEY_e2e_20260725}
EPAY_PID=${EPAY_PID:-1001}

pass=0; fail=0
ok()   { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad()  { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
head() { echo; echo "=== $1 ==="; }

jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }

# 按易支付规则计算签名：非空参数按名升序拼 k=v&，末尾直接追加密钥，MD5 小写
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

#-------------------------------------------------------------------------------
head "0. 前置：确认服务与渠道"

TENANT=$($PSQL -tAc "SELECT id FROM tenants ORDER BY created_at LIMIT 1" | tr -d '[:space:]')
[ -n "$TENANT" ] || { echo "找不到租户，先跑 seed"; exit 1; }
echo "  租户: $TENANT"

curl -sf "$BASE/healthz" >/dev/null && ok "Public 网关存活" || { bad "网关不可达" "$BASE"; exit 1; }

PROV=$($PSQL -tAc "SELECT code||':'||enabled FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'" | tr -d '[:space:]')
[ "$PROV" = "epay:true" ] && ok "易支付渠道已启用" || { bad "易支付渠道未配置" "$PROV"; exit 1; }

#-------------------------------------------------------------------------------
head "1. 注册并登录"

EMAIL="epay_$(date +%s)_$RANDOM@example.com"
R=$(curl -s -X POST "$BASE/v1/auth/register/start" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$EMAIL\"}")
RTOK=$(echo "$R" | jqr "['registration_token']")
CODE=$(echo "$R" | jqr "['dev_code']")
[ -n "$RTOK" ] && ok "注册事务已签发" || { bad "注册失败" "$R"; exit 1; }

R=$(curl -s -X POST "$BASE/v1/auth/register/complete" -H 'Content-Type: application/json' \
      -d "{\"registration_token\":\"$RTOK\",\"code\":\"$CODE\",\"password\":\"Aegis!Test2026\"}")
USER_ID=$(echo "$R" | jqr "['user_id']")
[ -n "$USER_ID" ] && ok "用户已创建 $USER_ID" || { bad "注册完成失败" "$R"; exit 1; }

R=$(curl -s -X POST "$BASE/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"$EMAIL\",\"password\":\"Aegis!Test2026\"}")
TOKEN=$(echo "$R" | jqr "['access_token']")
[ -n "$TOKEN" ] && ok "登录成功" || { bad "登录失败" "$R"; exit 1; }
AUTH="Authorization: Bearer $TOKEN"

#-------------------------------------------------------------------------------
head "2. 下单"

PLANS=$(curl -s "$BASE/v1/plans")
PRICE=$(echo "$PLANS" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# 易支付只结算人民币，优先选 CNY 价格
for p in d['plans']:
    for pr in p['prices']:
        if pr['currency']=='CNY':
            print(pr['id']); raise SystemExit
" 2>/dev/null)
PLAN=$(echo "$PLANS" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for p in d['plans']:
    for pr in p['prices']:
        if pr['currency']=='CNY':
            print(p['id']); raise SystemExit
" 2>/dev/null)
[ -n "$PRICE" ] && ok "取到套餐 $PLAN 价格 $PRICE" || { bad "无可售价格" "$PLANS"; exit 1; }

# use_balance 是要抵扣的金额（最小单位整数），不是布尔
R=$(curl -s -X POST "$BASE/v1/orders" -H "$AUTH" -H 'Content-Type: application/json'       -H "Idempotency-Key: e2e-$(date +%s)-$RANDOM"       -d "{\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\",\"use_balance\":0}")
ORDER_ID=$(echo "$R" | jqr "['order_id']")
ORDER_NO=$(echo "$R" | jqr "['order_no']")
PAYABLE=$(echo "$R" | jqr "['payable_amount']")
CURRENCY=$(echo "$R" | jqr "['currency']")
[ -n "$ORDER_ID" ] && ok "订单已创建 $ORDER_NO 应付 $PAYABLE $CURRENCY" || { bad "下单失败" "$R"; exit 1; }

#-------------------------------------------------------------------------------
head "3. 创建支付意图并校验收银台"

R=$(curl -s -X POST "$BASE/v1/orders/$ORDER_ID/pay" -H "$AUTH" -H 'Content-Type: application/json' \
      -d '{"provider":"epay","method":"alipay"}')
REDIR=$(echo "$R" | jqr "['redirect_url']")
INTENT=$(echo "$R" | jqr "['intent_id']")
[ -n "$REDIR" ] && ok "支付意图已创建 $INTENT" || { bad "创建支付意图失败" "$R"; exit 1; }

# 校验跳转地址里的关键参数
CHECK=$(python3 - "$REDIR" "$PAYABLE" "$ORDER_NO" "$EPAY_PID" "$EPAY_KEY" <<'PY'
import sys, hashlib
from urllib.parse import urlparse, parse_qs
url, payable, order_no, pid, key = sys.argv[1:6]
q = {k: v[0] for k, v in parse_qs(urlparse(url).query).items()}

problems = []
# 金额：分 → 元字符串
expect_money = f"{int(payable)//100}.{int(payable)%100:02d}"
if q.get('money') != expect_money:
    problems.append(f"money={q.get('money')} 期望 {expect_money}")
if q.get('out_trade_no') != order_no:
    problems.append(f"out_trade_no={q.get('out_trade_no')} 期望 {order_no}")
if q.get('pid') != pid:
    problems.append(f"pid={q.get('pid')} 期望 {pid}")
if q.get('sign_type') != 'MD5':
    problems.append(f"sign_type={q.get('sign_type')}")
# 反向验签
params = {k: v for k, v in q.items() if v and k not in ('sign', 'sign_type')}
s = '&'.join(f'{k}={params[k]}' for k in sorted(params)) + key
if hashlib.md5(s.encode()).hexdigest() != q.get('sign', '').lower():
    problems.append("签名与参数不匹配")
print('|'.join(problems) if problems else f"OK money={q['money']}")
PY
)
case "$CHECK" in
  OK*) ok "收银台参数与签名正确（$CHECK）" ;;
  *)   bad "收银台参数有误" "$CHECK" ;;
esac

# 连点两次应复用同一意图
R2=$(curl -s -X POST "$BASE/v1/orders/$ORDER_ID/pay" -H "$AUTH" -H 'Content-Type: application/json' \
       -d '{"provider":"epay","method":"alipay"}')
INTENT2=$(echo "$R2" | jqr "['intent_id']")
REUSED=$(echo "$R2" | jqr "['reused']")
[ "$INTENT2" = "$INTENT" ] && ok "重复发起复用同一支付意图（reused=$REUSED）" \
                           || bad "重复发起产生了新意图" "$INTENT -> $INTENT2"

N_INTENT=$($PSQL -tAc "SELECT count(*) FROM payment_intents WHERE order_id='$ORDER_ID' AND status IN ('created','requires_action','processing')" | tr -d '[:space:]')
[ "$N_INTENT" = "1" ] && ok "在途支付意图恒为 1 笔" || bad "在途意图数异常" "count=$N_INTENT"

#-------------------------------------------------------------------------------
head "4. 防篡改：坏回调必须被拒"

TRADE_NO="E2E$(date +%s)$RANDOM"
MONEY=$(python3 -c "print(f'{int($PAYABLE)//100}.{int($PAYABLE)%100:02d}')")
NOTIFY="$BASE/v1/webhooks/payments/epay"

# 4a 坏签名
R=$(curl -s "$NOTIFY?pid=$EPAY_PID&trade_no=$TRADE_NO&out_trade_no=$ORDER_NO&type=alipay&money=$MONEY&trade_status=TRADE_SUCCESS&sign=00000000000000000000000000000000&sign_type=MD5")
[ "$R" = "fail" ] && ok "坏签名被拒（回 fail）" || bad "坏签名未被拒" "响应=$R"

# 4b 篡改金额：用 0.01 的签名去冒充
BADSIG=$(epay_sign "pid=$EPAY_PID" "trade_no=$TRADE_NO" "out_trade_no=$ORDER_NO" "type=alipay" "money=0.01" "trade_status=TRADE_SUCCESS")
R=$(curl -s "$NOTIFY?pid=$EPAY_PID&trade_no=$TRADE_NO&out_trade_no=$ORDER_NO&type=alipay&money=0.01&trade_status=TRADE_SUCCESS&sign=$BADSIG&sign_type=MD5")
[ "$R" = "fail" ] && ok "金额不符被拒（签名有效但金额与订单不符）" || bad "金额篡改未被拒" "响应=$R"

# 4c 其他商户号
FOREIGN=$(epay_sign "pid=9999" "trade_no=$TRADE_NO" "out_trade_no=$ORDER_NO" "type=alipay" "money=$MONEY" "trade_status=TRADE_SUCCESS")
R=$(curl -s "$NOTIFY?pid=9999&trade_no=$TRADE_NO&out_trade_no=$ORDER_NO&type=alipay&money=$MONEY&trade_status=TRADE_SUCCESS&sign=$FOREIGN&sign_type=MD5")
[ "$R" = "fail" ] && ok "其他商户号被拒" || bad "其他商户号未被拒" "响应=$R"

# 确认坏回调没有污染订单
ST=$($PSQL -tAc "SELECT status FROM orders WHERE id='$ORDER_ID'" | tr -d '[:space:]')
[ "$ST" = "pending_payment" ] && ok "坏回调未改变订单状态（仍为 $ST）" || bad "订单被坏回调污染" "status=$ST"

#-------------------------------------------------------------------------------
head "5. 正常回调 + PAY-003 幂等（重复 10 次）"

# 注意：name 用中文原文参与签名，URL 里则是百分号编码。
# 这正是易支付最容易踩的坑：签名串不做 URL 编码，服务端解码后再验。
GOODSIG=$(epay_sign "pid=$EPAY_PID" "trade_no=$TRADE_NO" "out_trade_no=$ORDER_NO" "type=alipay" "name=套餐" "money=$MONEY" "trade_status=TRADE_SUCCESS")
URL="$NOTIFY?pid=$EPAY_PID&trade_no=$TRADE_NO&out_trade_no=$ORDER_NO&type=alipay&name=%E5%A5%97%E9%A4%90&money=$MONEY&trade_status=TRADE_SUCCESS&sign=$GOODSIG&sign_type=MD5"

RESULTS=""
for i in $(seq 1 10); do
  RESULTS="$RESULTS$(curl -s "$URL")"
done
[ "$RESULTS" = "successsuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccesssuccess" ] \
  && ok "10 次回调全部回 success（易支付要求的纯文本回执）" \
  || bad "回执不符" "$RESULTS"

# --- 幂等断言 ---
N_EV=$($PSQL -tAc "SELECT count(*) FROM payment_events WHERE provider_event_id='$TRADE_NO:TRADE_SUCCESS'" | tr -d '[:space:]')
[ "$N_EV" = "1" ] && ok "payment_events 只落 1 行（10 次回调）" || bad "事件重复落库" "count=$N_EV"

N_PAY=$($PSQL -tAc "SELECT count(*) FROM payments WHERE order_id='$ORDER_ID'" | tr -d '[:space:]')
[ "$N_PAY" = "1" ] && ok "payments 只有 1 笔" || bad "支付记录重复" "count=$N_PAY"

N_SUB=$($PSQL -tAc "SELECT count(*) FROM subscriptions WHERE user_id='$USER_ID'" | tr -d '[:space:]')
[ "$N_SUB" = "1" ] && ok "订阅只激活 1 份（权益未重复发放）" || bad "订阅重复" "count=$N_SUB"

N_TXN=$($PSQL -tAc "SELECT count(*) FROM ledger_transactions WHERE source_id='$ORDER_ID' AND kind='order_paid'" | tr -d '[:space:]')
[ "$N_TXN" = "1" ] && ok "账本只记 1 笔 order_paid" || bad "账本重复记账" "count=$N_TXN"

#-------------------------------------------------------------------------------
head "6. 金额与账本正确性"

PAID=$($PSQL -tAc "SELECT amount FROM payments WHERE order_id='$ORDER_ID'" | tr -d '[:space:]')
[ "$PAID" = "$PAYABLE" ] && ok "入账金额精确等于应付（$PAID = $PAYABLE 分，回调传的是 \"$MONEY\" 元）" \
                         || bad "金额换算有误" "入账 $PAID 应付 $PAYABLE"

ORDER_ST=$($PSQL -tAc "SELECT status FROM orders WHERE id='$ORDER_ID'" | tr -d '[:space:]')
[ "$ORDER_ST" = "fulfilled" ] && ok "订单已履约（$ORDER_ST）" || bad "订单状态异常" "$ORDER_ST"

SUB_ST=$($PSQL -tAc "SELECT status FROM subscriptions WHERE user_id='$USER_ID'" | tr -d '[:space:]')
[ "$SUB_ST" = "active" ] && ok "订阅已激活（$SUB_ST）" || bad "订阅状态异常" "$SUB_ST"

# PAY-005：账本必须配平
IMBAL=$($PSQL -tAc "
  SELECT coalesce(sum(signed_amount),0) FROM ledger_entries
   WHERE transaction_id IN (SELECT id FROM ledger_transactions WHERE source_id='$ORDER_ID')" | tr -d '[:space:]')
[ "$IMBAL" = "0" ] && ok "本单账本借贷配平（差额 0）" || bad "账本不平" "差额=$IMBAL"

DRIFT=$($PSQL -tAc "SELECT count(*) FROM app.verify_ledger_all()" | tr -d '[:space:]')
[ "$DRIFT" = "0" ] && ok "全账户缓存余额与分录求和一致（无漂移）" || bad "账户余额漂移" "$DRIFT 个账户"

# 意图应已终结
INTENT_ST=$($PSQL -tAc "SELECT status FROM payment_intents WHERE id='$INTENT'" | tr -d '[:space:]')
[ "$INTENT_ST" = "succeeded" ] && ok "支付意图已终结为 succeeded" || bad "支付意图未终结" "status=$INTENT_ST"

# 意图终结后不应再有在途意图，否则换渠道重付会被误判为重复
N_OPEN=$($PSQL -tAc "SELECT count(*) FROM payment_intents WHERE order_id='$ORDER_ID' AND status IN ('created','requires_action','processing')" | tr -d '[:space:]')
[ "$N_OPEN" = "0" ] && ok "订单已无在途支付意图" || bad "仍有在途意图" "count=$N_OPEN"

# 支付记录应回指其意图，便于对账时串起完整链路
LINKED=$($PSQL -tAc "SELECT payment_intent_id FROM payments WHERE order_id='$ORDER_ID'" | tr -d '[:space:]')
[ "$LINKED" = "$INTENT" ] && ok "支付记录已关联支付意图" || bad "支付未关联意图" "$LINKED vs $INTENT"

#-------------------------------------------------------------------------------
head "7. 凭据保护"

CREDS=$($PSQL -tAc "SELECT encode(credentials_encrypted,'escape') FROM payment_providers WHERE tenant_id='$TENANT' AND code='epay'")
echo "$CREDS" | grep -q "$EPAY_KEY" && bad "商户密钥以明文存在于数据库" "" || ok "商户密钥未明文落库（信封加密 SEC-010）"

if [ -f /opt/aegispanel/logs/public.log ]; then
  grep -q "$EPAY_KEY" /opt/aegispanel/logs/public.log && bad "商户密钥出现在日志中" "" || ok "商户密钥未出现在日志中（SEC-011）"
fi

#-------------------------------------------------------------------------------
echo
echo "==============================================="
echo "  通过 $pass 项，失败 $fail 项"
echo "==============================================="
[ "$fail" -eq 0 ]
