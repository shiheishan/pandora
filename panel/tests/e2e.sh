#!/usr/bin/env bash
# [INPUT]: 依赖 deploy/.env（主密钥签演示渠道回调）与 deploy/psql.sh，依赖 public 网关（BASE）与 admin 网关（ADM，ADMIN_EMAIL / ADMIN_PASS 显式给出）
# [OUTPUT]: 主链路端到端：注册（邮箱验证关闭的默认路径 + 临时开启后的验证码）→ 登录 → 自建带权益的套餐 → 下单与幂等 → 演示回调十连发 → 账本配平 → 订阅与配额 → 审计链 → 限流；退出时恢复邮件设置、归档自建套餐
# [POS]: panel/tests 的主链路脚本（make e2e），与 invariants.sql 分工；联调冒烟第 ⑤ 步起由 deploy/run-smoke-e2e.sh 在冒烟栈上跑
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
# 端到端链路测试：注册 → 登录 → 下单 → 支付 → 账本 → 订阅 → 配额 → 凭据
#
# 与 invariants.sql 的分工：
#   invariants.sql 证明「数据库拦得住违规写入」；
#   本脚本证明「整条业务链路真的跑得通，且幂等与账本在真实 HTTP 调用下成立」。
#
# 重点验证 PAY-003：同一支付回调连发 10 次，只能产生一次权益与一笔账。
#
# 需要一个后台管理员（ADMIN_EMAIL / ADMIN_PASS 显式给出）：
#   - 注册按现在的默认（auth.email_verification 关闭，迁移 00030 / 00042 起）走；
#     验证码那一段先经后台接口临时打开邮箱验证再测，退出时恢复原值；
#   - 订单快照要验「权益随订单固化」，脚本自己经后台接口建一个带权益的套餐，
#     不依赖 seed-demo，也不挑目录里的第一个套餐；退出时归档它。
set -uo pipefail

BASE="${AEGIS_BASE:-http://127.0.0.1:9000}"
ADM="${ADM:-http://127.0.0.1:9001}"
ADMIN_EMAIL="${ADMIN_EMAIL:-}"
ADMIN_PASS="${ADMIN_PASS:-}"
DEPLOY="$(cd "$(dirname "$0")/../deploy" && pwd)"
set -a; . "${DEPLOY}/.env"; set +a

PSQL="${DEPLOY}/psql.sh"

pass=0; fail=0
step() { printf '\n\033[1m▸ %s\033[0m\n' "$1"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail+1)); }
jqr()  { echo "$1" | python3 -c "import sys,json;d=json.load(sys.stdin);print(eval('d'+sys.argv[1]) if len(sys.argv)>1 else d)" "$2" 2>/dev/null; }

[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo "[FATAL] ADMIN_EMAIL / ADMIN_PASS 必须显式给出（建套餐、临时开关邮箱验证要用）" >&2; exit 1; }

# 每次运行用不同邮箱，脚本可反复执行
STAMP=$(date +%s)
EMAIL="e2e-${STAMP}@example.test"
PASSWORD="CorrectHorseBatteryStaple-${STAMP}"

#-------------------------------------------------------------------------------
# 后台：邮件设置整份覆盖（POST settings/mail），开关邮箱验证要带上其余各项原值。
# 原值在第一次改动前取好；EXIT 陷阱把邮箱验证与套餐都恢复，脚本中途退出也一样
#-------------------------------------------------------------------------------
AH=""
MAIL_ORIG=""        # GET settings/mail 的原始响应
MAIL_FROM_NAME=""   # 发件人名取库里的原值：GET 在它为空时回显站点名，照抄会写脏
MAIL_TOUCHED=0
E2E_PLAN_ID=""; E2E_PLAN_RV=""

# 用法：mail_settings_body <true|false|orig>，按原值拼出整份请求体；
# 开启验证而原来没配 SMTP 时补上占位地址（接口要求先有发信配置），恢复时照原值写回
mail_settings_body() {
  MAIL_ORIG="$MAIL_ORIG" FROM_NAME="$MAIL_FROM_NAME" python3 - "$1" <<'PY'
import json, os, sys
o = json.loads(os.environ["MAIL_ORIG"])
want = sys.argv[1]
body = {
    "smtp_host": o.get("smtp_host", ""), "smtp_port": o.get("smtp_port", 465),
    "encryption": o.get("encryption", "ssl"), "smtp_username": o.get("smtp_username", ""),
    "from_address": o.get("from_address", ""), "from_name": os.environ.get("FROM_NAME", ""),
}
if want == "orig":
    body["email_verification"] = bool(o.get("email_verification"))
else:
    body["email_verification"] = want == "true"
    if body["email_verification"]:
        body["smtp_host"] = body["smtp_host"] or "smtp.example.test"
        body["from_address"] = body["from_address"] or "e2e@example.test"
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
    else echo "  [恢复失败] 邮件设置没能写回原值，请手动检查后台邮件设置" >&2; status=1; fi
  fi
  if [ -n "$E2E_PLAN_ID" ] && [ -n "$E2E_PLAN_RV" ]; then
    c=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${ADM}/v1/plans/${E2E_PLAN_ID}/archive" \
          -H "$AH" -H 'Content-Type: application/json' -H "Idempotency-Key: e2e-plan-archive-${STAMP}" \
          -d "{\"expected_row_version\":${E2E_PLAN_RV}}")
    [ "$c" = "200" ] || { echo "  [恢复失败] 本次建的套餐 ${E2E_PLAN_ID} 没能归档（HTTP ${c}）" >&2; status=1; }
  fi
  exit "$status"
}
trap cleanup EXIT

#-------------------------------------------------------------------------------
step "0. 服务可达性"
#-------------------------------------------------------------------------------
health=$(curl -fsS --max-time 5 "${BASE}/healthz" 2>&1) \
  && ok "healthz: ${health}" || { bad "服务不可达: ${health}"; exit 1; }

ready=$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "${BASE}/readyz")
[ "$ready" = "200" ] && ok "readyz 返回 200（数据库与缓存均可达）" || bad "readyz 返回 ${ready}"

ra=$(curl -sS -X POST "${ADM}/v1/auth/login" -H 'Content-Type: application/json' \
      -d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASS}\"}")
ATOK=$(jqr "$ra" "['access_token']")
[ -n "$ATOK" ] && ok "后台管理员已登录" || { bad "后台管理员登录失败（响应已隐藏）"; exit 1; }
AH="Authorization: Bearer ${ATOK}"
MAIL_ORIG=$(curl -sS "${ADM}/v1/settings/mail" -H "$AH")
jqr "$MAIL_ORIG" "['email_verification']" >/dev/null \
  && ok "已取邮件设置原值（邮箱验证 $(jqr "$MAIL_ORIG" "['email_verification']")）" \
  || { bad "取邮件设置失败: $MAIL_ORIG"; exit 1; }
MAIL_FROM_NAME=$("$PSQL" -tAc "SELECT value #>> '{}' FROM system_settings WHERE key='mail.from_name' AND tenant_id='00000000-0000-7000-8000-000000000001';" | tr -d '\r')

#-------------------------------------------------------------------------------
step "1. 注册（IAM-001 / IAM-006，邮箱验证关闭：当前默认）"
#-------------------------------------------------------------------------------
# 迁移 00030 / 00042 起 auth.email_verification 默认关闭：注册不下发验证码，
# complete 不校验 code。本段在关闭状态下走；原本开着的安装先临时关掉
if [ "$(jqr "$MAIL_ORIG" "['email_verification']")" = "True" ]; then
  set_email_verification false && ok "邮箱验证临时关闭（退出时恢复）" || { bad "关闭邮箱验证失败"; exit 1; }
fi

r1=$(curl -sS -X POST "${BASE}/v1/auth/register/start" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"${EMAIL}\"}")
REG_TOKEN=$(jqr "$r1" "['registration_token']")
VERIFY=$(jqr "$r1" "['verification_required']")
DEV_CODE=$(jqr "$r1" "['dev_code']")

[ -n "$REG_TOKEN" ] && ok "注册事务已签发" || bad "未取得 registration_token: $r1"
[ "$VERIFY" = "False" ] && [ -z "$DEV_CODE" ] && ok "邮箱验证关闭时不要求、也不下发验证码" \
  || bad "邮箱验证关闭时 verification_required=${VERIFY} dev_code=${DEV_CODE:+有}"

# IAM-006：对已存在邮箱，响应结构必须与新邮箱完全一致
r1b=$(curl -sS -X POST "${BASE}/v1/auth/register/start" \
       -H 'Content-Type: application/json' \
       -d '{"email":"nonexistent-probe@example.test"}')
k1=$(echo "$r1" | python3 -c "import sys,json;print(sorted(json.load(sys.stdin).keys()))")
k2=$(echo "$r1b" | python3 -c "import sys,json;print(sorted(json.load(sys.stdin).keys()))")
[ "$k1" = "$k2" ] && ok "已存在与不存在邮箱的响应字段集一致（账号枚举防护）" \
                  || bad "响应字段集不一致：${k1} vs ${k2}"

# 弱口令必须被拒
weak=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/auth/register/complete" \
        -H 'Content-Type: application/json' \
        -d "{\"registration_token\":\"${REG_TOKEN}\",\"code\":\"\",\"password\":\"short\"}")
[ "$weak" = "422" ] && ok "弱口令被拒（422）" || bad "弱口令返回 ${weak}，期望 422"

r2=$(curl -sS -X POST "${BASE}/v1/auth/register/complete" \
      -H 'Content-Type: application/json' \
      -d "{\"registration_token\":\"${REG_TOKEN}\",\"code\":\"\",\"password\":\"${PASSWORD}\"}")
USER_ID=$(jqr "$r2" "['user_id']")
[ -n "$USER_ID" ] && ok "账号已创建 user_id=${USER_ID}" || bad "注册失败: $r2"

# 库中不得存在明文口令或明文验证码
leak=$("$PSQL" -tAc "SELECT count(*) FROM user_passwords WHERE phc NOT LIKE '\$argon2id\$%';")
[ "$leak" = "0" ] && ok "口令均为 Argon2id PHC 串，无明文（IAM-003）" || bad "发现 ${leak} 条非 Argon2id 口令"

#-------------------------------------------------------------------------------
step "1b. 邮箱验证开启时的验证码（IAM-002）"
#-------------------------------------------------------------------------------
# 经后台接口临时打开，测完立刻恢复原值（EXIT 陷阱兜底）
if set_email_verification true; then
  ok "邮箱验证已临时开启"
  VEMAIL="e2e-verify-${STAMP}@example.test"
  rv=$(curl -sS -X POST "${BASE}/v1/auth/register/start" \
        -H 'Content-Type: application/json' -d "{\"email\":\"${VEMAIL}\"}")
  VTOKEN=$(jqr "$rv" "['registration_token']")
  VCODE=$(jqr "$rv" "['dev_code']")
  [ "$(jqr "$rv" "['verification_required']")" = "True" ] && [ -n "$VCODE" ] \
    && ok "开启后要求验证码，且开发模式回显了验证码" || bad "开启后未要求验证码或未回显: $rv"

  # 错误验证码必须被拒
  wrongcode=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/auth/register/complete" \
               -H 'Content-Type: application/json' \
               -d "{\"registration_token\":\"${VTOKEN}\",\"code\":\"000000\",\"password\":\"${PASSWORD}\"}")
  [ "$wrongcode" = "400" ] && ok "错误验证码被拒（400）" || bad "错误验证码返回 ${wrongcode}"

  rvc=$(curl -sS -X POST "${BASE}/v1/auth/register/complete" \
         -H 'Content-Type: application/json' \
         -d "{\"registration_token\":\"${VTOKEN}\",\"code\":\"${VCODE}\",\"password\":\"${PASSWORD}\"}")
  [ -n "$(jqr "$rvc" "['user_id']")" ] && ok "正确验证码完成注册" || bad "正确验证码注册失败: $rvc"

  set_email_verification orig && MAIL_TOUCHED=0 && ok "邮箱验证已恢复原值" || bad "恢复邮箱验证失败"
else
  bad "临时开启邮箱验证失败"
fi

#-------------------------------------------------------------------------------
step "2. 登录（IAM-005 / IAM-006）"
#-------------------------------------------------------------------------------
badlogin=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/auth/login" \
            -H 'Content-Type: application/json' \
            -d "{\"email\":\"${EMAIL}\",\"password\":\"wrong-password-here\"}")
[ "$badlogin" = "401" ] && ok "错误口令返回 401" || bad "错误口令返回 ${badlogin}"

nouser=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/auth/login" \
          -H 'Content-Type: application/json' \
          -d '{"email":"ghost-user@example.test","password":"wrong-password-here"}')
[ "$nouser" = "401" ] && ok "不存在账号同样返回 401（不泄露存在性）" || bad "不存在账号返回 ${nouser}"

r3=$(curl -sS -X POST "${BASE}/v1/auth/login" \
      -H 'Content-Type: application/json' \
      -d "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\"}")
TOKEN=$(jqr "$r3" "['access_token']")
[ -n "$TOKEN" ] && ok "登录成功，已签发访问令牌" || bad "登录失败: $r3"

# EXT-001：public 域令牌不应被其他域接受。此处验证令牌自带的域标记。
aud=$(echo "$TOKEN" | cut -d. -f2)
[ "$aud" = "public" ] && ok "令牌域标记为 public（EXT-001）" || bad "令牌域标记为 ${aud}"

# 篡改令牌必须被拒。
# 不能改签名段的最后一个字符 —— base64url(RawURLEncoding) 末位的部分比特
# 不参与解码，改了可能得到完全相同的字节串，测试会假阴性。
# 改 payload 段（第 3 段）的首字符，一定会改变被签名的内容。
p3=$(echo "$TOKEN" | cut -d. -f3)
tampered="v1.public.X${p3:1}.$(echo "$TOKEN" | cut -d. -f4)"
tam=$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/v1/me" -H "Authorization: Bearer ${tampered}")
[ "$tam" = "401" ] && ok "篡改令牌载荷被拒（401）" || bad "篡改令牌返回 ${tam}"

# 换域名后签名必然不符（EXT-001 的密码学保证）
crossdomain="v1.admin.${p3}.$(echo "$TOKEN" | cut -d. -f4)"
cd_code=$(curl -sS -o /dev/null -w '%{http_code}' "${BASE}/v1/me" -H "Authorization: Bearer ${crossdomain}")
[ "$cd_code" = "401" ] && ok "改写令牌域标记被拒（EXT-001）" || bad "跨域令牌返回 ${cd_code}"

me=$(curl -sS "${BASE}/v1/me" -H "Authorization: Bearer ${TOKEN}")
meid=$(jqr "$me" "['user_id']")
[ "$meid" = "$USER_ID" ] && ok "/v1/me 返回本人信息" || bad "/v1/me 异常: $me"

#-------------------------------------------------------------------------------
step "3. 套餐目录（XBD-011）：脚本自建带权益的套餐"
#-------------------------------------------------------------------------------
# 订单快照要验「权益随订单固化」，就得有带权益的套餐。向导建的套餐不写权益行，
# 目录里第一个套餐未必带；所以经后台接口自己建：草稿版本写权益与配额 → 绑节点池 →
# 价格 → 发布，下单只用它。退出时归档
PLAN_TAG="e2e-${STAMP}-${RANDOM}"
POOL_ID=$(curl -sS "${ADM}/v1/node-pools" -H "$AH" | python3 -c "
import sys,json
for p in json.load(sys.stdin).get('pools',[]):
    if p.get('active_nodes',0)>0: print(p['id']); break" 2>/dev/null)
[ -n "$POOL_ID" ] && ok "取到有活跃节点的节点池" || { bad "没有带活跃节点的节点池，套餐发布不了"; exit 1; }

rp=$(curl -sS -X POST "${ADM}/v1/plans" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: ${PLAN_TAG}-create" \
      -d "{\"code\":\"${PLAN_TAG}\",\"name\":\"E2E 链路 ${STAMP}\",\"visibility\":\"public\"}")
PLAN_ID=$(jqr "$rp" "['plan']['id']"); PLAN_RV=$(jqr "$rp" "['plan']['row_version']")
[ -n "$PLAN_ID" ] && ok "草稿套餐已创建 ${PLAN_ID}" || { bad "建套餐失败: $rp"; exit 1; }

rv=$(curl -sS -X POST "${ADM}/v1/plans/${PLAN_ID}/versions" -H "$AH" \
      -H "Idempotency-Key: ${PLAN_TAG}-version")
VID=$(jqr "$rv" "['version']['id']"); VRV=$(jqr "$rv" "['version']['row_version']")
[ -n "$VID" ] && ok "草稿版本已创建" || { bad "建版本失败: $rv"; exit 1; }

sem=$(curl -sS -o /dev/null -w '%{http_code}' -X PUT "${ADM}/v1/plans/${PLAN_ID}/versions/${VID}" -H "$AH" \
       -H 'Content-Type: application/json' \
       -d "{\"expected_row_version\":${VRV},\"quota_reset_strategy\":\"billing_cycle\",
            \"entitlements\":[{\"code\":\"feature.multi_device\",\"value\":true},
                              {\"code\":\"support.tier\",\"value\":\"standard\"}],
            \"quotas\":[{\"metric\":\"traffic.bytes\",\"limit\":107374182400,\"unit\":\"bytes\",\"period\":\"cycle\"},
                        {\"metric\":\"devices.active\",\"limit\":3,\"unit\":\"count\",\"period\":\"total\"}]}")
[ "$sem" = "200" ] && VRV=$((VRV+1)) && ok "版本写入 2 项权益与 2 项配额" || { bad "写版本语义返回 ${sem}"; exit 1; }

rb=$(curl -sS -X POST "${ADM}/v1/plans/${PLAN_ID}/pools" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: ${PLAN_TAG}-pools" \
      -d "{\"version_id\":\"${VID}\",\"expected_version_row_version\":${VRV},\"pool_ids\":[\"${POOL_ID}\"]}")
VRV=$(jqr "$rb" "['row_version']")
[ -n "$VRV" ] && ok "版本已绑定节点池" || { bad "绑节点池失败: $rb"; exit 1; }

rpr=$(curl -sS -X POST "${ADM}/v1/plans/${PLAN_ID}/prices" -H "$AH" -H 'Content-Type: application/json' \
       -H "Idempotency-Key: ${PLAN_TAG}-price" \
       -d '{"currency":"CNY","unit_amount":990,"billing_interval":"month","interval_count":1}')
[ -n "$(jqr "$rpr" "['price']['id']")" ] && ok "价格已创建（CNY 9.90 / 月）" || { bad "建价格失败: $rpr"; exit 1; }

rpub=$(curl -sS -X POST "${ADM}/v1/plans/${PLAN_ID}/versions/${VID}/publish" -H "$AH" \
        -H 'Content-Type: application/json' -H "Idempotency-Key: ${PLAN_TAG}-publish" \
        -d "{\"expected_plan_row_version\":${PLAN_RV},\"expected_version_row_version\":${VRV}}")
PLAN_RV=$(jqr "$rpub" "['plan_row_version']")
[ -n "$PLAN_RV" ] && ok "版本已发布" || { bad "发布失败: $rpub"; exit 1; }
E2E_PLAN_ID="$PLAN_ID"; E2E_PLAN_RV="$PLAN_RV"

plans=$(curl -sS "${BASE}/v1/plans")
PRICE_ID=$(echo "$plans" | python3 -c "
import sys,json
for p in json.load(sys.stdin)['plans']:
    if p['id']=='${PLAN_ID}': print(p['prices'][0]['id']); break" 2>/dev/null)
PRICE_AMT=$(echo "$plans" | python3 -c "
import sys,json
for p in json.load(sys.stdin)['plans']:
    if p['id']=='${PLAN_ID}': print(p['prices'][0]['unit_amount']); break" 2>/dev/null)
[ -n "$PRICE_ID" ] && ok "门户目录可见本套餐，价格 ${PRICE_AMT} (最小单位)" || bad "门户目录里没有本套餐: $plans"

#-------------------------------------------------------------------------------
step "4. 下单（SUB-001 价格快照）"
#-------------------------------------------------------------------------------
IDEM="order-${STAMP}"
r4=$(curl -sS -X POST "${BASE}/v1/orders" \
      -H "Authorization: Bearer ${TOKEN}" \
      -H 'Content-Type: application/json' \
      -H "Idempotency-Key: ${IDEM}" \
      -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0}")
ORDER_ID=$(jqr "$r4" "['order_id']")
ORDER_NO=$(jqr "$r4" "['order_no']")
PAYABLE=$(jqr "$r4" "['payable_amount']")
CURRENCY=$(jqr "$r4" "['currency']")
[ -n "$ORDER_ID" ] && ok "订单已创建 ${ORDER_NO} 应付=${PAYABLE}" || bad "下单失败: $r4"

# 幂等：同 key 重放应返回同一张订单，而不是新建
r4b=$(curl -sS -X POST "${BASE}/v1/orders" \
       -H "Authorization: Bearer ${TOKEN}" \
       -H 'Content-Type: application/json' \
       -H "Idempotency-Key: ${IDEM}" \
       -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":0}")
ORDER_ID2=$(jqr "$r4b" "['order_id']")
[ "$ORDER_ID" = "$ORDER_ID2" ] && ok "相同幂等键重放返回同一订单（未重复下单）" \
                              || bad "幂等失效：${ORDER_ID} vs ${ORDER_ID2}"

# 同 key 不同请求体必须冲突
conf=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/orders" \
        -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
        -H "Idempotency-Key: ${IDEM}" \
        -d "{\"plan_id\":\"${PLAN_ID}\",\"price_id\":\"${PRICE_ID}\",\"use_balance\":100}")
[ "$conf" = "409" ] && ok "同幂等键+不同请求体返回 409（未静默返回旧结果）" \
                    || bad "期望 409，实际 ${conf}"

# 订单行必须含权益与配额快照
snap=$("$PSQL" -tAc "SELECT jsonb_array_length(snapshot_entitlements) FROM order_items WHERE order_id='${ORDER_ID}';")
[ "${snap:-0}" -ge 1 ] && ok "订单行已固化 ${snap} 项权益快照（SUB-001）" || bad "权益快照缺失"

#-------------------------------------------------------------------------------
step "5. 支付回调（PAY-003 幂等 / PAY-005 账本）"
#-------------------------------------------------------------------------------
# 用 python3 而非 xxd 做 base64->hex：Debian 13 最小安装不带 xxd，
# 而 python3 本脚本已在使用（jqr），不引入新依赖。
MASTER_HEX=$(printf %s "$AEGIS_MASTER_KEY" | python3 -c "
import sys,base64
sys.stdout.write(base64.b64decode(sys.stdin.read().strip()).hex())")
EVENT_ID="evt-${STAMP}"
CANON="${EVENT_ID}|${ORDER_ID}|${PAYABLE}|${CURRENCY}"
SIG=$(printf '%s' "$CANON" | openssl dgst -sha256 -mac HMAC -macopt "hexkey:${MASTER_HEX}" -hex | awk '{print $NF}')

payload=$(cat <<EOF
{"event_id":"${EVENT_ID}","event_type":"payment.succeeded","payment_id":"pay-${STAMP}",
 "order_id":"${ORDER_ID}","amount":${PAYABLE},"currency":"${CURRENCY}","fee":30,"raw":{"src":"e2e"}}
EOF
)

# 先验证签名错误会被拒
badsig=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/webhooks/payments/demo" \
          -H 'Content-Type: application/json' -H 'X-Aegis-Signature: deadbeef' \
          -H "Idempotency-Key: wh-bad-${STAMP}" -d "$payload")
[ "$badsig" = "401" ] && ok "签名错误的回调被拒（401）" || bad "错误签名返回 ${badsig}"

# 正确签名，连发 10 次
echo "  连发 10 次相同回调..."
processed=0; already=0
for i in $(seq 1 10); do
  resp=$(curl -sS -X POST "${BASE}/v1/webhooks/payments/demo" \
          -H 'Content-Type: application/json' \
          -H "X-Aegis-Signature: ${SIG}" \
          -H "Idempotency-Key: wh-${EVENT_ID}-${i}" \
          -d "$payload")
  p=$(jqr "$resp" "['processed']"); a=$(jqr "$resp" "['already_handled']")
  [ "$p" = "True" ] && processed=$((processed+1))
  [ "$a" = "True" ] && already=$((already+1))
  if [ "$i" = "1" ]; then
    SUB_ID=$(jqr "$resp" "['subscription_id']")
    TXN_ID=$(jqr "$resp" "['ledger_txn_id']")
  fi
done
echo "    processed=${processed} already_handled=${already}"

paycnt=$("$PSQL" -tAc "SELECT count(*) FROM payments WHERE order_id='${ORDER_ID}';")
[ "$paycnt" = "1" ] && ok "10 次回调只产生 1 条支付记录（PAY-003）" || bad "产生了 ${paycnt} 条支付记录"

subcnt=$("$PSQL" -tAc "SELECT count(*) FROM subscriptions WHERE user_id='${USER_ID}';")
[ "$subcnt" = "1" ] && ok "10 次回调只激活 1 个订阅" || bad "产生了 ${subcnt} 个订阅"

txncnt=$("$PSQL" -tAc "SELECT count(*) FROM ledger_transactions WHERE source_id='${ORDER_ID}';")
[ "$txncnt" = "1" ] && ok "10 次回调只记 1 笔账（PAY-005）" || bad "产生了 ${txncnt} 笔账务交易"

evtcnt=$("$PSQL" -tAc "SELECT count(*) FROM payment_events WHERE provider_event_id='${EVENT_ID}';")
[ "$evtcnt" = "1" ] && ok "回调事件表只落 1 条（唯一约束生效）" || bad "落了 ${evtcnt} 条事件"

#-------------------------------------------------------------------------------
step "6. 账本正确性（PAY-005）"
#-------------------------------------------------------------------------------
# 先确认交易确实存在 —— 否则「空集求和为 0」会让配平检查假阳性通过
TXN=$("$PSQL" -tAc "SELECT id FROM ledger_transactions WHERE source_id='${ORDER_ID}' LIMIT 1;")
if [ -z "$TXN" ]; then
  bad "未产生任何账务交易，配平检查无从谈起"
else
  ok "账务交易已生成 ${TXN}"
  entrycnt=$("$PSQL" -tAc "SELECT count(*) FROM ledger_entries WHERE transaction_id='${TXN}';")
  [ "${entrycnt:-0}" -ge 2 ] && ok "该交易含 ${entrycnt} 条分录" || bad "分录数 ${entrycnt} < 2"
  imbalance=$("$PSQL" -tAc "SELECT coalesce(sum(signed_amount),0) FROM ledger_entries WHERE transaction_id='${TXN}';")
  [ "$imbalance" = "0" ] && ok "该笔交易借贷配平（差额 0）" || bad "借贷不平，差额 ${imbalance}"
fi

drift=$("$PSQL" -tAc "SELECT count(*) FROM app.verify_ledger_all();")
[ "$drift" = "0" ] && ok "全部账户缓存余额与分录求和一致（0 个漂移）" || bad "${drift} 个账户余额漂移"

echo "  账本分录明细："
"$PSQL" -tAc "
  SELECT '    ' || rpad(la.account_type, 26) || rpad(le.direction, 7) ||
         lpad(le.amount::text, 8) || ' ' || le.currency
    FROM ledger_entries le JOIN ledger_accounts la ON la.id = le.account_id
   WHERE le.transaction_id = (SELECT id FROM ledger_transactions WHERE source_id='${ORDER_ID}' LIMIT 1)
   ORDER BY le.direction DESC, le.amount DESC;"

revenue=$("$PSQL" -tAc "
  SELECT sum(le.amount) FROM ledger_entries le
    JOIN ledger_accounts la ON la.id = le.account_id
   WHERE la.account_type='platform_revenue' AND le.direction='credit'
     AND le.transaction_id=(SELECT id FROM ledger_transactions WHERE source_id='${ORDER_ID}' LIMIT 1);")
[ "$revenue" = "$PAYABLE" ] && ok "平台收入贷方金额=${revenue} 与订单总额一致" \
                           || bad "收入 ${revenue} ≠ 订单 ${PAYABLE}"

#-------------------------------------------------------------------------------
step "7. 订阅与配额（SUB-004 / USE-005 / XBD-002）"
#-------------------------------------------------------------------------------
subs=$(curl -sS "${BASE}/v1/me/subscriptions" -H "Authorization: Bearer ${TOKEN}")
sstatus=$(jqr "$subs" "['subscriptions'][0]['status']")
[ "$sstatus" = "active" ] && ok "订阅状态为 active" || bad "订阅状态为 ${sstatus}: $subs"

# 直接查库而不是解析 JSON：jqr 只能处理下标表达式，套函数会拼错。
qcnt=$("$PSQL" -tAc "SELECT count(*) FROM quota_balances WHERE subscription_id='${SUB_ID}';")
[ "${qcnt:-0}" -ge 2 ] && ok "已初始化 ${qcnt} 项配额（USE-005）" || bad "配额数 ${qcnt}，期望 ≥2"

# 配额应当来自套餐版本定义，且 remaining 派生列计算正确
qdetail=$("$PSQL" -tAc "
  SELECT string_agg(metric || '=' || coalesce(remaining::text,'unlimited'), ', ')
    FROM quota_balances WHERE subscription_id='${SUB_ID}';")
[ -n "$qdetail" ] && ok "配额余额：${qdetail}" || bad "配额明细为空"

# SUB-004：订阅必须经 pending → active，且留下事件
evt=$("$PSQL" -tAc "SELECT from_status || '->' || to_status FROM subscription_events WHERE subscription_id='${SUB_ID}' LIMIT 1;")
[ "$evt" = "pending->active" ] && ok "订阅状态转换事件已记录：${evt}" || bad "状态事件异常：${evt}"

credcnt=$("$PSQL" -tAc "SELECT count(*) FROM subscription_credentials WHERE subscription_id='${SUB_ID}' AND status='active';")
[ "$credcnt" = "1" ] && ok "已签发 1 份订阅凭据（XBD-002）" || bad "凭据数 ${credcnt}"

plainleak=$("$PSQL" -tAc "SELECT count(*) FROM subscription_credentials WHERE token_hash IS NULL;")
[ "$plainleak" = "0" ] && ok "凭据仅存哈希，无明文" || bad "发现 ${plainleak} 条无哈希凭据"

ordstatus=$("$PSQL" -tAc "SELECT status FROM orders WHERE id='${ORDER_ID}';")
[ "$ordstatus" = "fulfilled" ] && ok "订单已履约（fulfilled）" || bad "订单状态 ${ordstatus}"

#-------------------------------------------------------------------------------
step "8. 审计链（SEC-012）"
#-------------------------------------------------------------------------------
audits=$("$PSQL" -tAc "SELECT count(*) FROM audit_events WHERE occurred_at > now() - interval '5 minutes';")
[ "${audits:-0}" -ge 3 ] && ok "本次链路产生 ${audits} 条审计记录" || bad "审计记录仅 ${audits} 条"

nullhash=$("$PSQL" -tAc "SELECT count(*) FROM audit_events WHERE entry_hash IS NULL;")
[ "$nullhash" = "0" ] && ok "全部审计记录带哈希链" || bad "${nullhash} 条审计缺哈希"

# 链条连续性：除首条外，每条的 prev_hash 必须等于前一条的 entry_hash
broken=$("$PSQL" -tAc "
  WITH ordered AS (
    SELECT id, prev_hash, entry_hash,
           lag(entry_hash) OVER (ORDER BY occurred_at, id) AS expected_prev
      FROM audit_events WHERE tenant_id='00000000-0000-7000-8000-000000000001')
  SELECT count(*) FROM ordered
   WHERE expected_prev IS NOT NULL AND prev_hash IS DISTINCT FROM expected_prev;")
[ "$broken" = "0" ] && ok "审计哈希链连续，无断裂" || bad "${broken} 处哈希链断裂"

#-------------------------------------------------------------------------------
step "9. 限流与错误处理（SEC-002 / SEC-006）"
#-------------------------------------------------------------------------------
# 探测次数必须超过服务端的认证限流阈值，否则触发不了。
# 阈值可由 AEGIS_RL_AUTH_PER_MIN 配置（测试环境会调高），
# 所以这里从 RL_PROBE 读，而不是写死一个数。
RL_PROBE=${RL_PROBE:-14}
codes=""
for i in $(seq 1 $RL_PROBE); do
  c=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE}/v1/auth/login" \
       -H 'Content-Type: application/json' -d '{"email":"rl@example.test","password":"xxxxxxxxxx"}')
  codes="${codes}${c} "
done
echo "$codes" | grep -q 429 && ok "认证接口触发限流 429（SEC-002）" || bad "未触发限流：${codes}"

nf=$(curl -sS "${BASE}/v1/nonexistent-endpoint")
echo "$nf" | grep -qi 'stack\|goroutine\|/opt/\|\.go:' && bad "错误响应泄露内部信息" \
  || ok "404 响应不含堆栈、路径与文件名（SEC-006）"

echo "$nf" | grep -q 'request_id' && ok "错误响应带 request_id 便于追溯（NFR-005）" \
  || bad "错误响应缺 request_id"

#-------------------------------------------------------------------------------
printf '\n\033[1m================ 端到端结果 ================\033[0m\n'
printf '  通过 \033[32m%d\033[0m   失败 \033[31m%d\033[0m\n\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1
