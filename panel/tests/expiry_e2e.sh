#!/usr/bin/env bash
# 到期与续费的端到端（w5expiry，用户 2026-10-07 定的规则）：
#
#   开通 → 到期（aegis-admin 的过期扫描把订阅改成 expired）→ 订阅链接只剩一条提示节点、
#   门户照常列出链接且不许换 → 续费（同套餐人工开单落成原订阅上的续费单）→ 真实节点
#   回来、链接不变；门户同套餐新购被拒（只续不新开）。
#
# 夹具用 SQL 建（服务器、节点池、带心跳的节点、绑池的套餐），用户走门户注册，开单走
# 后台人工单，到期用 SQL 把周期挪到过去，等真实的过期扫描循环接手。
# 会留下订单、审计与订阅事件这类不可逆证据，只肯在一次性库上跑：
#   EXPIRY_E2E_DISPOSABLE=YES_DELETE_FIXTURES
#   EXPIRY_E2E_DATABASE=<current_database() 的原值，名字里要有 test / e2e 段>
#   EXPIRY_E2E_TENANT_ID=<一次性租户 UUID>
set -euo pipefail

ADM=${ADM:-http://127.0.0.1:9001}
PUB=${PUB:-http://127.0.0.1:9000}
PSQL=${PSQL:-/opt/aegispanel/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
TENANT=${EXPIRY_E2E_TENANT_ID:-}
# 过期扫描一分钟一轮（首轮随机延迟），给足两轮多的时间
SCAN_WAIT=${EXPIRY_E2E_SCAN_WAIT:-180}

pass=0
HTTP_CODE=
HTTP_BODY=
HTTP_HEADERS=

ok() { echo "  [ OK ] $1"; pass=$((pass + 1)); }
die() { echo "  [FAIL] $1" >&2; [ -z "${2:-}" ] || echo "         $2" >&2; exit 1; }
sec() { echo; echo "=== $1 ==="; }
assert_eq() { [ "$1" = "$2" ] || die "$3" "got '$1' want '$2'"; ok "$3"; }

is_uuid() {
  [[ ${1:-} =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]
}

# request METHOD URL [curl 参数…]：结果放进 HTTP_CODE / HTTP_BODY / HTTP_HEADERS
request() {
  local method=$1 url=$2 body_file header_file
  shift 2
  body_file=$(mktemp); header_file=$(mktemp)
  HTTP_CODE=$(curl -sS -o "$body_file" -D "$header_file" -w '%{http_code}' -X "$method" "$url" "$@") \
    || { rm -f "$body_file" "$header_file"; die "request failed: $method $url"; }
  HTTP_BODY=$(cat "$body_file")
  HTTP_HEADERS=$(tr -d '\r' <"$header_file")
  rm -f "$body_file" "$header_file"
}

expect_http() {
  local expected=$1 method=$2 url=$3
  shift 3
  request "$method" "$url" "$@"
  [ "$HTTP_CODE" = "$expected" ] || die "$method $url expected $expected" "status=$HTTP_CODE body=$(printf '%s' "$HTTP_BODY" | head -c 400)"
}

header() {
  printf '%s\n' "$HTTP_HEADERS" | awk -v name="$1" 'BEGIN{IGNORECASE=1} index(tolower($0), tolower(name)":")==1 {sub(/^[^:]*:[ ]*/, ""); print}' | tail -1
}

json_get() {
  printf '%s' "$1" | python3 -c '
import json, sys
value = json.load(sys.stdin)
for part in sys.argv[1].split("."):
    value = value[int(part)] if isinstance(value, list) else value[part]
if value is None:
    raise SystemExit(2)
print("true" if value is True else "false" if value is False else value)
' "$2"
}

json_check() {
  local body=$1 expression=$2 message=$3
  printf '%s' "$body" | python3 -c '
import json, sys
d = json.load(sys.stdin)
safe = {"__builtins__": {}, "any": any, "all": all, "len": len, "str": str, "int": int}
if not eval(sys.argv[1], safe, {"d": d}):
    raise SystemExit(1)
' "$expression" || die "$message" "JSON assertion failed: $(printf '%s' "$body" | head -c 400)"
  ok "$message"
}

db() {
  "$PSQL" -X -v ON_ERROR_STOP=1 -qAtc "$1" | tr -d '[:space:]'
}

#-------------------------------------------------------------------------------
sec "0. 一次性库守卫"
[ "${EXPIRY_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || die "refusing to run outside an explicitly disposable E2E database" "set EXPIRY_E2E_DISPOSABLE=YES_DELETE_FIXTURES"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] || die "ADMIN_EMAIL / ADMIN_PASS must be provided"
is_uuid "$TENANT" || die "EXPIRY_E2E_TENANT_ID must be an explicit UUID"
DB_NAME=$(db "SELECT current_database()")
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] || die "database name is not recognizably disposable" "$DB_NAME"
[ "${EXPIRY_E2E_DATABASE:-}" = "$DB_NAME" ] || die "EXPIRY_E2E_DATABASE does not match current_database()"
assert_eq "$(db "SELECT count(*) FROM tenants WHERE id='$TENANT'")" 1 "disposable tenant exists"

#-------------------------------------------------------------------------------
sec "1. 夹具：带心跳的节点、绑池的套餐"
STAMP="$(date +%s)$RANDOM"
FIXTURE=$(db "
  WITH server AS (
    INSERT INTO servers (tenant_id,name,status,hostname,public_ipv4,capacity_nodes)
    VALUES ('$TENANT','exp-server-$STAMP','ready','exp-$STAMP.example.test','203.0.113.20',4) RETURNING id
  ), pool AS (
    INSERT INTO node_pools (tenant_id,code,name) VALUES ('$TENANT','exp-pool-$STAMP','Expiry pool $STAMP') RETURNING id
  ), node AS (
    INSERT INTO nodes (tenant_id,name,server_id,pool_id,status,serving_status,node_type,server_host,
                       server_port,kernel,traffic_rate,display_name,protocol_config,
                       protocol_schema_version,config_validated_at,row_version,last_heartbeat_at)
    SELECT '$TENANT','exp-node-$STAMP',s.id,p.id,'draft','active','vless','node.example.com',443,'auto',1.0,
           'Expiry Line $STAMP','{\"network\":\"ws\",\"tls\":false}'::jsonb,1,now(),1,now()
      FROM server s CROSS JOIN pool p RETURNING pool_id
  ), product AS (
    INSERT INTO products (tenant_id,code,name,status) VALUES ('$TENANT','exp-product-$STAMP','Expiry $STAMP','active') RETURNING id
  ), plan AS (
    INSERT INTO plans (tenant_id,product_id,code,name,status,visibility)
    SELECT '$TENANT',id,'exp-plan-$STAMP','Expiry Plan $STAMP','draft','public' FROM product RETURNING id, product_id
  ), version AS (
    INSERT INTO plan_versions (tenant_id,plan_id,version) SELECT '$TENANT',id,1 FROM plan RETURNING id, plan_id
  ), quota AS (
    INSERT INTO quota_definitions (tenant_id,plan_version_id,metric,limit_value,unit,period)
    SELECT '$TENANT',id,'traffic.bytes',1073741824,'bytes','cycle' FROM version RETURNING plan_version_id
  ), bind AS (
    INSERT INTO plan_node_pools (tenant_id,plan_version_id,pool_id)
    SELECT '$TENANT',v.id,n.pool_id FROM version v CROSS JOIN node n RETURNING plan_version_id
  ), price AS (
    INSERT INTO prices (tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,status)
    SELECT '$TENANT',product_id,'CNY',1000,'month',1,'active' FROM plan RETURNING id
  )
  SELECT p.id::text || '|' || v.id::text || '|' || pr.id::text
    FROM plan p, version v, price pr, quota q, bind b")
IFS='|' read -r PLAN VERSION PRICE <<<"$FIXTURE"
is_uuid "$PLAN" && is_uuid "$VERSION" && is_uuid "$PRICE" || die "fixture creation returned '$FIXTURE'"
db "UPDATE plan_versions SET frozen_at=now(), status='published' WHERE id='$VERSION'" >/dev/null
db "UPDATE plans SET current_version_id='$VERSION', status='active' WHERE id='$PLAN'" >/dev/null
ok "plan, price, pool-bound node with heartbeat created"

expect_http 200 POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
AH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
ok "admin authenticated"

UE="exp_${STAMP}@example.com"
expect_http 200 POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$UE\"}"
RT=$(json_get "$HTTP_BODY" registration_token)
CODE=$(json_get "$HTTP_BODY" dev_code 2>/dev/null || true)
expect_http 201 POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CODE\",\"password\":\"ExpPass2026\"}"
USER_ID=$(json_get "$HTTP_BODY" user_id)
is_uuid "$USER_ID" || die "registration returned no user id"
expect_http 200 POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"$UE\",\"password\":\"ExpPass2026\"}"
UH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
ok "portal user registered and logged in"

manual_grant() {
  expect_http 201 POST "$ADM/v1/orders/manual" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: exp-manual-$1-$STAMP" \
    -d "{\"user_id\":\"$USER_ID\",\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\",\"reason\":\"到期续费端到端测试\",\"settlement\":\"grant\"}"
}

#-------------------------------------------------------------------------------
sec "2. 开通，拿到订阅链接"
manual_grant open
SUB_ID=$(db "SELECT id FROM subscriptions WHERE user_id='$USER_ID'")
is_uuid "$SUB_ID" || die "manual grant did not open a subscription"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
LINK=$(json_get "$HTTP_BODY" links.0.url)
LINK_PATH=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$LINK")
[[ "$LINK_PATH" =~ ^/[^/]+/[^/]+$ ]] || die "subscription link has an unexpected shape"
ok "subscription link issued"
pull() { expect_http 200 GET "$PUB$LINK_PATH?target=singbox" -H 'User-Agent: sing-box 1.13'; }
pull
json_check "$HTTP_BODY" "any(o.get('tag') == 'Expiry Line $STAMP' for o in d['outbounds'])" "live subscription serves the real node"

#-------------------------------------------------------------------------------
sec "3. 到期：过期扫描接手，订阅只剩提示节点"
db "UPDATE subscriptions SET current_period_start=now()-interval '31 days', current_period_end=now()-interval '1 hour' WHERE id='$SUB_ID'" >/dev/null
db "UPDATE subscription_credentials SET expires_at=now()-interval '1 hour' WHERE subscription_id='$SUB_ID' AND status='active'" >/dev/null
db "UPDATE quota_balances SET period_start=now()-interval '31 days', period_end=now()-interval '1 hour' WHERE subscription_id='$SUB_ID'" >/dev/null
deadline=$((SECONDS + SCAN_WAIT))
until [ "$(db "SELECT status FROM subscriptions WHERE id='$SUB_ID'")" = expired ]; do
  [ $SECONDS -lt $deadline ] || die "expiry scan did not mark the subscription expired within ${SCAN_WAIT}s"
  sleep 5
done
ok "aegis-admin expiry scan marked the subscription expired"
assert_eq "$(db "SELECT count(*) FROM subscription_events WHERE subscription_id='$SUB_ID' AND event_type='expired'")" 1 "expired event written once"

pull
json_check "$HTTP_BODY" "len(d['outbounds']) == 2 and d['outbounds'][1]['server'] == '127.0.0.1' and d['outbounds'][1]['tag'].startswith('已于') and d['route']['final'] == d['outbounds'][0]['tag']" \
  "expired subscription serves only the renewal notice"
EXPIRE=$(header Subscription-Userinfo | python3 -c 'import sys,re; m=re.search(r"expire=(\d+)", sys.stdin.read()); print(m.group(1) if m else "")')
[ -n "$EXPIRE" ] && [ "$EXPIRE" -lt "$(date +%s)" ] || die "Subscription-Userinfo expire is not in the past" "$EXPIRE"
ok "Subscription-Userinfo reports a past expiry"
[[ "$(header Profile-Web-Page-Url)" == *"#/checkout?renew=$SUB_ID" ]] || die "profile-web-page-url does not point at the renewal page"
ok "profile-web-page-url points at the renewal page"
expect_http 404 GET "$PUB${LINK_PATH%/*}/not-a-real-token-0123456789abcdef"
ok "an invalid token still gets the decoy 404"
assert_eq "$(db "SELECT count(*) FROM subscription_fetch_log WHERE subscription_id='$SUB_ID' AND result='expired'")" 1 \
  "expired pull is logged apart from scanner traffic"

expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
json_check "$HTTP_BODY" "len(d['links']) == 1 and d['links'][0]['expired'] is True and d['links'][0]['url'] == '$LINK'" \
  "portal still lists the same link, marked expired"
expect_http 409 POST "$PUB/v1/me/subscriptions/$SUB_ID/rotate" -H "$UH"
ok "link rotation is refused while expired"
expect_http 200 GET "$PUB/v1/me/subscriptions" -H "$UH"
json_check "$HTTP_BODY" "any(s['id'] == '$SUB_ID' and s['status'] == 'expired' and s['renewable'] is True for s in d['subscriptions'])" \
  "portal shows the expired subscription as renewable"

#-------------------------------------------------------------------------------
sec "4. 续费：原订阅恢复，链接不变"
expect_http 409 POST "$PUB/v1/orders" -H "$UH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-same-plan-$STAMP" -d "{\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\",\"use_balance\":0}"
ok "portal same-plan purchase is refused (renew instead)"
manual_grant renew
assert_eq "$(db "SELECT count(*) FROM subscriptions WHERE user_id='$USER_ID'")" 1 "same-plan manual order renewed in place"
assert_eq "$(db "SELECT status FROM subscriptions WHERE id='$SUB_ID'")" active "subscription is active again"
assert_eq "$(db "SELECT from_status FROM subscription_events WHERE subscription_id='$SUB_ID' AND event_type='renewed' ORDER BY id DESC LIMIT 1")" expired \
  "renewal event records the expired starting state"
assert_eq "$(db "SELECT bool_and(consumed = 0 AND period_start > now() - interval '5 minutes') FROM quota_balances WHERE subscription_id='$SUB_ID'")" t \
  "quotas restarted at the payment moment"
pull
json_check "$HTTP_BODY" "any(o.get('tag') == 'Expiry Line $STAMP' for o in d['outbounds'])" "renewed subscription serves the real node again"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
json_check "$HTTP_BODY" "len(d['links']) == 1 and d['links'][0]['url'] == '$LINK' and d['links'][0]['expired'] is False" \
  "the subscription link did not change"

echo
echo "expiry e2e: $pass checks passed"
