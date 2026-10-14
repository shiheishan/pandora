#!/usr/bin/env bash
# 到期与续费的端到端（nodesim 节点夹具）：
#
#   开通 → 到期（worker 的 subscription:expire 每分钟扫描）→ 订阅链接只剩提示节点、门户仍列出且不许换链 →
#   续费（同套餐人工单落在原订阅）→ 真实节点回来、链接不变；门户同套餐新购 409。
#   后台开别的套餐 → 原订阅换套餐（链接不变）；重置链接换 proxy_uuid（旧链接 404），10 分钟内再重置 429。
#   每一步都核对 nodesim 收到的名单（proxy_uuid 在或不在）；收尾断言模拟器 errors=0，优雅停 nodesim。
#   夹具（池、服务器、节点、套餐）都经后台 API 建，节点由 nodesim 接入后 activate。
#
# 仍用 SQL 的步骤及原因：
#   - 第 3 节把订阅/配额周期挪到过去：真等一个月不现实；且 SQL 改周期本身不会触发名单下发，
#     配合 wait_roster absent 才能证明是到期扫描触发的下线。
#   - 读 subscriptions.proxy_uuid、subscription_id、plan_id、订单 kind、事件与 quota、fetch_log：
#     断言库内事实，无对应只读门户接口或比 SQL 更稳。
#
# 环境变量：
#   ADM、PUB、PSQL、ADMIN_EMAIL、ADMIN_PASS（必填）
#   EXPIRY_E2E_DISPOSABLE=YES_DELETE_FIXTURES（必填）
#   NODESIM（必填，可执行的 nodesim 二进制）
#   EXPIRY_E2E_SCAN_WAIT（缺省 180，等 subscription:expire 每分钟一轮）
#   NODESIM_REPORT_INTERVAL（缺省 1h：本脚本不断言流量；若用短间隔，模拟器上报会扣订阅流量，干扰第 4 节用量清零断言）
#   ROSTER_WAIT（缺省 60，等名单 present/absent）
#
# 会留下订单、审计与订阅事件，只许在一次性库上跑。
set -euo pipefail

ADM=${ADM:-http://127.0.0.1:9001}
PUB=${PUB:-http://127.0.0.1:9000}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
NODESIM=${NODESIM:-}
SCAN_WAIT=${EXPIRY_E2E_SCAN_WAIT:-180}
NODESIM_REPORT_INTERVAL=${NODESIM_REPORT_INTERVAL:-1h}
ROSTER_WAIT=${ROSTER_WAIT:-60}

TMP=
SIM_PID=
pass=0
HTTP_CODE=
HTTP_BODY=
HTTP_HEADERS=
AH=

ok() { echo "  [ OK ] $1"; pass=$((pass + 1)); }
die() { echo "  [FAIL] $1" >&2; [ -z "${2:-}" ] || echo "         $2" >&2; exit 1; }
sec() { echo; echo "=== $1 ==="; }
assert_eq() { [ "$1" = "$2" ] || die "$3" "got '$1' want '$2'"; ok "$3"; }

cleanup() {
  if [ -n "${SIM_PID:-}" ]; then
    kill -TERM "$SIM_PID" 2>/dev/null || true
    wait "$SIM_PID" 2>/dev/null || true
    SIM_PID=
  fi
  if [ -n "${TMP:-}" ]; then
    rm -rf "$TMP"
  fi
}

is_uuid() {
  [[ ${1:-} =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]
}

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

admin_auth() {
  expect_http 200 POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
  local login_tok
  login_tok=$(json_get "$HTTP_BODY" access_token)
  expect_http 200 POST "$ADM/v1/auth/reauth" -H "Authorization: Bearer $login_tok" \
    -H 'Content-Type: application/json' -d "{\"password\":\"$ADMIN_PASS\"}"
  AH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
}

register_user() {
  local email=$1
  expect_http 200 POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$email\"}"
  local rt code
  rt=$(json_get "$HTTP_BODY" registration_token)
  code=$(json_get "$HTTP_BODY" dev_code 2>/dev/null || true)
  expect_http 201 POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"$rt\",\"code\":\"$code\",\"password\":\"ExpPass2026!x\"}"
  REG_USER_ID=$(json_get "$HTTP_BODY" user_id)
  is_uuid "$REG_USER_ID" || die "registration returned no user id"
  expect_http 200 POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"ExpPass2026!x\"}"
  REG_UH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
}

manual_order() {
  local user_id=$1 plan_id=$2 price_id=$3 idem_suffix=$4
  local target_json=${5:-}
  admin_auth
  local body
  if [ -n "$target_json" ]; then
    body=$(python3 -c 'import json,sys; print(json.dumps({"user_id":sys.argv[1],"plan_id":sys.argv[2],"price_id":sys.argv[3],"reason":"到期续费端到端测试","settlement":"grant","target":json.loads(sys.argv[4])}))' \
      "$user_id" "$plan_id" "$price_id" "$target_json")
  else
    body=$(python3 -c 'import json,sys; print(json.dumps({"user_id":sys.argv[1],"plan_id":sys.argv[2],"price_id":sys.argv[3],"reason":"到期续费端到端测试","settlement":"grant"}))' \
      "$user_id" "$plan_id" "$price_id")
  fi
  expect_http 201 POST "$ADM/v1/orders/manual" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: exp-manual-$idem_suffix-$STAMP" -d "$body"
}

roster_has() {
  local uuid=$1
  python3 - "$TMP/status.json" "$uuid" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
try:
    with open(path, encoding="utf-8") as f:
        snap = json.load(f)
except FileNotFoundError:
    sys.exit(1)
for srv in (snap.get("servers") or {}).values():
    for node in (srv.get("nodes") or {}).values():
        if want in (node.get("users") or []):
            sys.exit(0)
sys.exit(1)
PY
}

roster_timeout_detail() {
  echo "--- status.json (摘要) ---" >&2
  python3 - "$TMP/status.json" <<'PY' 2>/dev/null || true
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        d = json.load(f)
except Exception as e:
    print(e, file=sys.stderr)
    raise SystemExit(0)
print("errors", d.get("errors"))
for name, s in (d.get("servers") or {}).items():
    print(name, "enrolled", s.get("enrolled"), "connected", s.get("connected"), "nodes", len(s.get("nodes") or {}))
    for nid, n in (s.get("nodes") or {}).items():
        print(" ", nid, "users", n.get("users"))
PY
  echo "--- nodesim.log (末尾 30 行) ---" >&2
  tail -n 30 "$TMP/nodesim.log" 2>/dev/null >&2 || true
}

wait_roster() {
  local mode=$1 uuid=$2 explain=$3
  local deadline=$((SECONDS + ROSTER_WAIT))
  while [ "$SECONDS" -lt "$deadline" ]; do
    kill -0 "$SIM_PID" 2>/dev/null || die "nodesim 已退出" "$explain"
    if [ "$mode" = present ]; then
      roster_has "$uuid" && { ok "$explain"; return 0; }
    else
      roster_has "$uuid" || { ok "$explain"; return 0; }
    fi
    sleep 1
  done
  roster_timeout_detail
  die "名单等待超时（${ROSTER_WAIT}s）: $explain" "mode=$mode uuid=$uuid"
}

wait_nodesim_server_ready() {
  local name=$1 explain=$2
  local deadline=$((SECONDS + 60))
  while [ "$SECONDS" -lt "$deadline" ]; do
    kill -0 "$SIM_PID" 2>/dev/null || die "nodesim 已退出" "$explain"
    if python3 - "$TMP/status.json" "$name" <<'PY'
import json, sys
name = sys.argv[2]
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        d = json.load(f)
except Exception:
    raise SystemExit(1)
s = (d.get("servers") or {}).get(name) or {}
raise SystemExit(0 if s.get("enrolled") and s.get("connected") else 1)
PY
    then
      ok "$explain"
      return 0
    fi
    sleep 1
  done
  die "$explain 超时（60s）"
}

wait_nodesim_node_config() {
  local name=$1 explain=$2
  local deadline=$((SECONDS + 60))
  while [ "$SECONDS" -lt "$deadline" ]; do
    kill -0 "$SIM_PID" 2>/dev/null || die "nodesim 已退出" "$explain"
    if python3 - "$TMP/status.json" "$name" <<'PY'
import json, sys
name = sys.argv[2]
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        d = json.load(f)
except Exception:
    raise SystemExit(1)
s = (d.get("servers") or {}).get(name) or {}
nodes = s.get("nodes") or {}
if not nodes:
    raise SystemExit(1)
for n in nodes.values():
    if (n.get("config_sha256") or "") != "":
        raise SystemExit(0)
raise SystemExit(1)
PY
    then
      ok "$explain"
      return 0
    fi
    sleep 1
  done
  die "$explain 超时（60s）"
}

pull_current_link() {
  expect_http 200 GET "$PUB$LINK_PATH" -H 'User-Agent: sing-box 1.13.0'
}

wait_pull_has_display_line() {
  local tag=$1 explain=$2
  local i=0
  while [ "$i" -lt 20 ]; do
    pull_current_link
    if printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
tag = sys.argv[1]
d = json.load(sys.stdin)
# tag 取自节点展示名（可能带去重后缀），按包含判断
raise SystemExit(0 if any(tag in (o.get("tag") or "") for o in d.get("outbounds", [])) else 1)
' "$tag"; then
      ok "$explain"
      return 0
    fi
    i=$((i + 1))
    sleep 3
  done
  die "$explain" "20 次拉取后仍无出站 tag=$tag"
}

#-------------------------------------------------------------------------------
sec "0. 一次性库守卫"
[ "${EXPIRY_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || die "refusing to run outside an explicitly disposable E2E database" "set EXPIRY_E2E_DISPOSABLE=YES_DELETE_FIXTURES"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] || die "ADMIN_EMAIL / ADMIN_PASS must be provided"
[ -n "$NODESIM" ] && [ -x "$NODESIM" ] || die "NODESIM must point to an executable nodesim binary"
DB_NAME=$(db "SELECT current_database()")
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] || die "database name is not recognizably disposable" "$DB_NAME"
# 注册 IP 限额是后台设置（缺省每 IP 每分钟 10 次），连着注册会撞 429：一次性库上先关掉（见 lib/signup.sh）
. "$(dirname "$0")/lib/signup.sh"
relax_signup_ip_limit

TMP=$(mktemp -d)
trap cleanup EXIT

STAMP="$(date +%s)$RANDOM"

#-------------------------------------------------------------------------------
sec "1. 夹具：节点与套餐（后台 API + nodesim）"
admin_auth
expect_http 200 GET "$ADM/v1/node-protocol-schemas" -H "$AH"
CIPHER=$(printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
d = json.load(sys.stdin)
for s in d.get("schemas", []):
    if s.get("node_type") == "shadowsocks":
        methods = s.get("methods") or []
        print(methods[0] if methods else "aes-128-gcm")
        raise SystemExit(0)
raise SystemExit("no shadowsocks schema")
')
ok "shadowsocks cipher from schema: $CIPHER"

POOL_CODE="exp-$STAMP"
expect_http 200 POST "$ADM/v1/node-pools" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"code\":\"$POOL_CODE\",\"name\":\"Expiry pool $STAMP\"}"
POOL_ID=$(json_get "$HTTP_BODY" id)
is_uuid "$POOL_ID" || die "node pool id invalid"

expect_http 201 POST "$ADM/v1/servers" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"exp-$STAMP-s1\",\"hostname\":\"exp-$STAMP.example.test\",\"public_ipv4\":\"203.0.113.20\"}"
SERVER_ID=$(json_get "$HTTP_BODY" id)
is_uuid "$SERVER_ID" || die "server id invalid"

expect_http 201 POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-node-$STAMP" \
  -d "{\"name\":\"exp-$STAMP-n1\",\"server_id\":\"$SERVER_ID\",\"pool_id\":\"$POOL_ID\",\"node_type\":\"shadowsocks\",\"server_host\":\"exp-$STAMP.example.test\",\"server_port\":22001,\"traffic_rate\":1,\"display_name\":\"Expiry Line $STAMP\",\"protocol_config\":{\"cipher\":\"$CIPHER\"}}"
NODE_ID=$(json_get "$HTTP_BODY" id)
NODE_ROW_VERSION=$(json_get "$HTTP_BODY" row_version)
is_uuid "$NODE_ID" || die "node id invalid"

admin_auth
expect_http 201 POST "$ADM/v1/servers/$SERVER_ID/bootstrap-token" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: exp-bootstrap-$STAMP" -d '{}'
TOKENS_FILE="$TMP/tokens"
# 响应体含一次性令牌：落 0600 临时文件，python 直接把「s1 令牌」写进 0600 令牌文件，令牌不进 shell 变量、命令行与输出；
# stdout 只有 panel 与 ca 两行
(umask 077; printf '%s' "$HTTP_BODY" > "$TMP/boot.json")
python3 - "$TMP/boot.json" "$TOKENS_FILE" > "$TMP/boot-parse.txt" <<'PY' || { rm -f "$TMP/boot.json"; die "bootstrap-token response could not be parsed"; }
import json, os, re, sys
body_path, tokens_path = sys.argv[1], sys.argv[2]
with open(body_path, encoding="utf-8") as f:
    body = json.load(f)
token = body["token"]
cmd = body.get("install_command") or ""
m_panel = re.search(r"--panel\s+'([^']+)'", cmd)
m_ca = re.search(r"--ca-sha256\s+'([^']+)'", cmd)
if not token or not m_panel or not m_ca:
    raise SystemExit("install_command parse failed")
fd = os.open(tokens_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as f:
    f.write(f"s1 {token}\n")
os.chmod(tokens_path, 0o600)
print(m_panel.group(1))
print(m_ca.group(1))
PY
rm -f "$TMP/boot.json"
PANEL_ADDR=$(sed -n '1p' "$TMP/boot-parse.txt")
CA_SHA=$(sed -n '2p' "$TMP/boot-parse.txt")
rm -f "$TMP/boot-parse.txt"
[ -n "$PANEL_ADDR" ] && [ -n "$CA_SHA" ] || die "install_command has no --panel / --ca-sha256"
ok "bootstrap token written to tokens file (not logged)"

: >"$TMP/nodesim.log"
"$NODESIM" --panel "$PANEL_ADDR" --ca-sha256 "$CA_SHA" \
  --tokens-file "$TOKENS_FILE" --state-dir "$TMP/state" \
  --report-interval "$NODESIM_REPORT_INTERVAL" --status-file "$TMP/status.json" \
  >>"$TMP/nodesim.log" 2>&1 &
SIM_PID=$!
wait_nodesim_server_ready s1 "nodesim s1 enrolled and connected"

admin_auth
expect_http 200 POST "$ADM/v1/nodes/$NODE_ID/activate" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-activate-$STAMP" -d "{\"row_version\":$NODE_ROW_VERSION}"
wait_nodesim_node_config s1 "nodesim s1 has node with non-empty config_sha256"

admin_auth
expect_http 201 POST "$ADM/v1/plans/complete" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-plan-$STAMP" \
  -d "{\"code\":\"exp-plan-$STAMP\",\"name\":\"Expiry Plan $STAMP\",\"visibility\":\"public\",\"traffic_gb\":1,\"pool_ids\":[\"$POOL_ID\"],\"prices\":[{\"billing_interval\":\"month\",\"interval_count\":1,\"unit_amount\":1000,\"currency\":\"CNY\",\"trial_days\":0}],\"publish\":true}"
PLAN=$(json_get "$HTTP_BODY" plan.id)
PRICE=$(json_get "$HTTP_BODY" price_ids.0)
is_uuid "$PLAN" && is_uuid "$PRICE" || die "plan/complete returned invalid ids"
ok "plan 1 published with pool binding"

#-------------------------------------------------------------------------------
sec "2. 开通，拿到订阅链接"
register_user "exp_${STAMP}@example.com"
USER_ID="$REG_USER_ID"
UH="$REG_UH"
manual_order "$USER_ID" "$PLAN" "$PRICE" open
SUB_ID=$(db "SELECT id FROM subscriptions WHERE user_id='$USER_ID'")
is_uuid "$SUB_ID" || die "manual grant did not open a subscription"
PROXY_UUID=$(db "SELECT proxy_uuid FROM subscriptions WHERE id='$SUB_ID'")
[ -n "$PROXY_UUID" ] || die "subscription has no proxy_uuid"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
LINK=$(json_get "$HTTP_BODY" links.0.url)
LINK_PATH=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$LINK")
[[ "$LINK_PATH" =~ ^/[^/]+/[^/]+$ ]] || die "subscription link has an unexpected shape"
ok "subscription link issued"

wait_pull_has_display_line "Expiry Line $STAMP" "live subscription serves the real node"
[[ "$HTTP_BODY" == *"$PROXY_UUID"* ]] || die "pull body does not contain proxy_uuid"
ok "pull response contains proxy_uuid"
wait_roster present "$PROXY_UUID" "用户在节点名单里"

#-------------------------------------------------------------------------------
sec "3. 到期：到期扫描接手，订阅只剩提示节点"
db "UPDATE subscriptions SET current_period_start=now()-interval '31 days', current_period_end=now()-interval '1 hour' WHERE id='$SUB_ID'" >/dev/null
db "UPDATE quota_balances SET period_start=now()-interval '31 days', period_end=now()-interval '1 hour' WHERE subscription_id='$SUB_ID' AND period_end IS NOT NULL" >/dev/null
deadline=$((SECONDS + SCAN_WAIT))
until [ "$(db "SELECT status FROM subscriptions WHERE id='$SUB_ID'")" = expired ]; do
  [ $SECONDS -lt $deadline ] || die "expiry scan did not mark the subscription expired within ${SCAN_WAIT}s"
  sleep 5
done
ok "到期扫描把订阅改成 expired"
assert_eq "$(db "SELECT count(*) FROM subscription_events WHERE subscription_id='$SUB_ID' AND event_type='expired'")" 1 "expired event written once"
wait_roster absent "$PROXY_UUID" "到期后用户从节点名单里消失"

pull_current_link
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
  -H "Idempotency-Key: exp-same-plan-$STAMP" -d "{\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\"}"
ok "portal same-plan purchase is refused (renew instead)"
manual_order "$USER_ID" "$PLAN" "$PRICE" renew "{\"kind\":\"renew\",\"subscription_id\":\"$SUB_ID\"}"
assert_eq "$(db "SELECT count(*) FROM subscriptions WHERE user_id='$USER_ID'")" 1 "same-plan manual order renewed in place"
assert_eq "$(db "SELECT status FROM subscriptions WHERE id='$SUB_ID'")" active "subscription is active again"
assert_eq "$(db "SELECT from_status FROM subscription_events WHERE subscription_id='$SUB_ID' AND event_type='renewed' ORDER BY id DESC LIMIT 1")" expired \
  "renewal event records the expired starting state"
assert_eq "$(db "SELECT bool_and(consumed = 0 AND period_start > now() - interval '5 minutes') FROM quota_balances WHERE subscription_id='$SUB_ID' AND period_end IS NOT NULL")" t \
  "quotas restarted at the payment moment"
pull_current_link
json_check "$HTTP_BODY" "any('Expiry Line $STAMP' in (o.get('tag') or '') for o in d['outbounds'])" "renewed subscription serves the real node again"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
json_check "$HTTP_BODY" "len(d['links']) == 1 and d['links'][0]['url'] == '$LINK' and d['links'][0]['expired'] is False" \
  "the subscription link did not change"
wait_roster present "$PROXY_UUID" "续费后用户回到节点名单，proxy_uuid 不变"

#-------------------------------------------------------------------------------
sec "5. 后台开不同套餐：原订阅换套餐，链接不变"
admin_auth
expect_http 201 POST "$ADM/v1/plans/complete" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-plan2-$STAMP" \
  -d "{\"code\":\"exp-plan2-$STAMP\",\"name\":\"Expiry Pro Plan $STAMP\",\"visibility\":\"public\",\"traffic_gb\":2,\"pool_ids\":[\"$POOL_ID\"],\"prices\":[{\"billing_interval\":\"month\",\"interval_count\":1,\"unit_amount\":3000,\"currency\":\"CNY\",\"trial_days\":0}],\"publish\":true}"
PLAN2=$(json_get "$HTTP_BODY" plan.id)
PRICE2=$(json_get "$HTTP_BODY" price_ids.0)
is_uuid "$PLAN2" && is_uuid "$PRICE2" || die "second plan invalid"

admin_auth
expect_http 201 POST "$ADM/v1/orders/manual" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: exp-manual-change-$STAMP" \
  -d "{\"user_id\":\"$USER_ID\",\"plan_id\":\"$PLAN2\",\"price_id\":\"$PRICE2\",\"reason\":\"换套餐端到端测试\",\"settlement\":\"grant\",\"target\":{\"kind\":\"change\",\"subscription_id\":\"$SUB_ID\"}}"
json_check "$HTTP_BODY" "d['status'] == 'fulfilled' and 'balance_refund' in d" "manual order for another plan is an in-place plan change"
assert_eq "$(db "SELECT count(*) FROM subscriptions WHERE user_id='$USER_ID'")" 1 "no second subscription was opened"
assert_eq "$(db "SELECT plan_id FROM subscriptions WHERE id='$SUB_ID'")" "$PLAN2" "the original subscription now carries the new plan"
assert_eq "$(db "SELECT kind FROM orders WHERE subscription_id='$SUB_ID' ORDER BY created_at DESC LIMIT 1")" upgrade \
  "the manual order is an upgrade order on the original subscription"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH"
json_check "$HTTP_BODY" "len(d['links']) == 1 and d['links'][0]['url'] == '$LINK'" "the subscription link did not change"
pull_current_link
json_check "$HTTP_BODY" "any('Expiry Line $STAMP' in (o.get('tag') or '') for o in d['outbounds'])" "the same link serves the new plan's nodes"
roster_has "$PROXY_UUID" || die "user not on roster after plan change"
ok "user still on node roster after plan change (same pool)"

#-------------------------------------------------------------------------------
sec "6. 重置订阅链接：节点密码一起换，门户按份限频"
register_user "exp2_${STAMP}@example.com"
USER2="$REG_USER_ID"
UH2="$REG_UH"
manual_order "$USER2" "$PLAN" "$PRICE" rotate-open
SUB2=$(db "SELECT id FROM subscriptions WHERE user_id='$USER2'")
is_uuid "$SUB2" || die "manual grant did not open the second user's subscription"
expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$UH2"
LINK2=$(json_get "$HTTP_BODY" links.0.url)
LINK2_PATH=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$LINK2")
OLD_UUID=$(db "SELECT proxy_uuid FROM subscriptions WHERE id='$SUB2'")
LINK_PATH="$LINK2_PATH"
wait_pull_has_display_line "Expiry Line $STAMP" "second user pull has real node"
[[ "$HTTP_BODY" == *"$OLD_UUID"* ]] || die "pull does not contain old proxy_uuid"
ok "the link serves the current node password"
wait_roster present "$OLD_UUID" "second user on roster before rotate"

expect_http 200 POST "$PUB/v1/me/subscriptions/$SUB2/rotate" -H "$UH2"
NEW_LINK=$(json_get "$HTTP_BODY" url)
NEW_PATH=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$NEW_LINK")
[ "$NEW_PATH" != "$LINK2_PATH" ] || die "rotation returned the same link"
NEW_UUID=$(db "SELECT proxy_uuid FROM subscriptions WHERE id='$SUB2'")
[ -n "$NEW_UUID" ] && [ "$NEW_UUID" != "$OLD_UUID" ] || die "rotation kept the node password" "$OLD_UUID -> $NEW_UUID"
ok "rotation replaced proxy_uuid"
expect_http 404 GET "$PUB$LINK2_PATH" -H 'User-Agent: sing-box 1.13.0'
ok "the old link is gone"
expect_http 200 GET "$PUB$NEW_PATH" -H 'User-Agent: sing-box 1.13.0'
[[ "$HTTP_BODY" == *"$NEW_UUID"* && "$HTTP_BODY" != *"$OLD_UUID"* ]] || die "new link uuid mismatch" "NEW=$NEW_UUID OLD=$OLD_UUID"
ok "the new link serves only the new node password"
wait_roster present "$NEW_UUID" "节点名单换成新的 proxy_uuid"
roster_has "$OLD_UUID" && die "old proxy_uuid still on roster" "$OLD_UUID"
ok "旧的 proxy_uuid 立即从节点名单下线"

expect_http 429 POST "$PUB/v1/me/subscriptions/$SUB2/rotate" -H "$UH2"
json_check "$HTTP_BODY" "'分钟后再试' in d['error']['message']" "a second reset within 10 minutes is refused with the wait time"
expect_http 200 GET "$PUB$NEW_PATH" -H 'User-Agent: sing-box 1.13.0'
ok "the refused reset left the current link working"

#-------------------------------------------------------------------------------
sec "7. 收尾"
ERRORS=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["errors"])' "$TMP/status.json")
assert_eq "$ERRORS" 0 "nodesim reported no errors"
kill -TERM "$SIM_PID"
sim_rc=0
wait "$SIM_PID" || sim_rc=$?
SIM_PID=
[ "$sim_rc" -eq 0 ] || { tail -n 30 "$TMP/nodesim.log" >&2 || true; die "nodesim exited with $sim_rc"; }
ok "nodesim stopped cleanly"

echo
echo "expiry e2e: $pass checks passed"
