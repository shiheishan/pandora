#!/usr/bin/env bash
# 节点冒烟端到端（P5）：200 用户、5 台服务器各 1 节点、约 60 秒订阅/门户负载，客户端与服务端零 5xx。
#
# 步骤概要：
#   0  一次性库与环境变量守卫
#   1  后台登录 + 重认证（RecentReauth 路由用新令牌）
#   2  读 shadowsocks 协议 schema，取 cipher
#   3  后台建池、服务器、节点、套餐（全走 API）
#   4  并发注册并登录门户用户（并发 4，避免口令哈希排队 503）
#   5  后台人工开单 grant（并发 4）+ SQL 核对 active 订阅数
#   6  各服务器签接入令牌，写入 0600 令牌文件（名 s1..sN，供 nodesim）
#   7  启动 nodesim，等全部 enrolled + connected
#   8  逐个节点 activate（须服务器已接入）；SQL 核对服务器 ready
#   9  等名单：每服 1 节点、config_sha256 非空、user_count = 用户数
#  10  用户 1 验证订阅含 5 条线路 + proxy_uuid；并发取全用户订阅链接
#  11  多轮负载（默认 6 轮 × 10s）：拉订阅 + 门户读；模拟器持续上报流量
#  12  等待最后一轮流量上报被确认
#  13  判定：客户端状态码、serve 日志 5xx/ERROR、模拟器、流量入账 SQL、门户用量
#  14  停模拟器
#  15  汇总通过/失败项
#
# 顺序说明：上线要求服务器已 gRPC 接入（先 nodesim 再 activate）；名单只下发给 active 节点（先上线再等名单）。
#
# SQL 仅用于核对（不写库）：
#   - 开通后 active 订阅数 = 用户数
#   - activate 后服务器 status = ready
#   - 用户 1 的 proxy_uuid（校验订阅正文）
#   - node_traffic_reports 行数、字节合计、invalid/unlisted 为 0
#   - quota_balances 已有 consumed > 0；配合门户 usage 接口
#
# 服务端 5xx 口径：负载开始后对 SERVE_LOG 增量，去掉 ANSI 后，access 日志行
# （msg 为 access，字段 "status":5xx）计数须为 0。来源：panel/internal/pkg/httpx/middleware.go AccessLog。
#
# 环境变量：PUB、ADM、PSQL、ADMIN_EMAIL、ADMIN_PASS（同 auth_e2e.sh）；
#   NODE_SMOKE_E2E_DISPOSABLE=YES_DELETE_FIXTURES；NODESIM、SERVE_LOG（必填）；
#   可调 SMOKE_USERS(200)、SMOKE_SERVERS(5)、SMOKE_SECONDS(60)、SMOKE_ROUNDS(6)、
#   SMOKE_PARALLEL(16)、NODESIM_REPORT_INTERVAL(5s)。
# 只许在库名含独立 test/e2e 段的一次性库上跑。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

SMOKE_USERS=${SMOKE_USERS:-200}
SMOKE_SERVERS=${SMOKE_SERVERS:-5}
SMOKE_SECONDS=${SMOKE_SECONDS:-60}
SMOKE_ROUNDS=${SMOKE_ROUNDS:-6}
SMOKE_PARALLEL=${SMOKE_PARALLEL:-16}
NODESIM_REPORT_INTERVAL=${NODESIM_REPORT_INTERVAL:-5s}

pass=0
fail=0
HTTP_CODE=
HTTP_BODY=
AH=
SIM_PID=

ok()  { echo "  [ OK ] $1"; pass=$((pass + 1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail + 1)); }
sec() { echo; echo "=== $1 ==="; }
fatal() { echo "[FATAL] $1" >&2; [ -z "${2:-}" ] || echo "         $2" >&2; exit 1; }

db() { "$PSQL" -X -v ON_ERROR_STOP=1 -qAtc "$1" | tr -d '[:space:]'; }

api() {
  local method=$1 url=$2 body_file header_file
  shift 2
  body_file=$(mktemp)
  header_file=$(mktemp)
  HTTP_CODE=$(curl -sS -o "$body_file" -D "$header_file" -w '%{http_code}' -X "$method" "$url" "$@") || HTTP_CODE=000
  HTTP_BODY=$(cat "$body_file")
  rm -f "$body_file" "$header_file"
}

jget() {
  local file=$1 path=$2
  python3 -c '
import json, sys
path = sys.argv[1]
with open(sys.argv[2], "r", encoding="utf-8") as f:
    d = json.load(f)
for part in path.split("."):
    if part.isdigit():
        d = d[int(part)]
    else:
        d = d[part]
if d is None:
    raise SystemExit(2)
print(d)
' "$path" "$file" 2>/dev/null
}

jget_body() {
  local path=$1
  printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
path = sys.argv[1]
d = json.load(sys.stdin)
for part in path.split("."):
    if part.isdigit():
        d = d[int(part)]
    else:
        d = d[part]
if d is None:
    raise SystemExit(2)
print(d)
' "$path" 2>/dev/null
}

admin_auth() {
  api POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
  [ "$HTTP_CODE" = 200 ] || fatal "admin login failed" "HTTP $HTTP_CODE"
  local tok
  tok=$(jget_body access_token) || fatal "admin login: no access_token"
  api POST "$ADM/v1/auth/reauth" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    -d "{\"password\":\"$ADMIN_PASS\"}"
  [ "$HTTP_CODE" = 200 ] || fatal "admin reauth failed" "HTTP $HTTP_CODE"
  tok=$(jget_body access_token) || fatal "admin reauth: no access_token"
  AH="Authorization: Bearer $tok"
}

status_ok() {
  python3 -c '
import json, sys
expr = sys.argv[1]
path = sys.argv[2]
try:
    with open(path, "r", encoding="utf-8") as f:
        d = json.load(f)
except Exception:
    sys.exit(1)
# d 必须放进 globals：生成器表达式是嵌套作用域，看不到 eval 的 locals
env = {"__builtins__": {}, "d": d, "all": all, "any": any, "len": len, "int": int, "str": str, "range": range}
try:
    ok = eval(expr, env)
except Exception:
    sys.exit(1)
if not ok:
    sys.exit(1)
' "$1" "$TMP/status.json"
}

wait_status() {
  local deadline=$((SECONDS + $1)) desc=$2 expr=$3
  while [ "$SECONDS" -lt "$deadline" ]; do
    if [[ -n "${SIM_PID:-}" ]] && ! kill -0 "$SIM_PID" 2>/dev/null; then
      echo "--- nodesim.log (tail 50) ---" >&2
      tail -n 50 "$TMP/nodesim.log" 2>/dev/null >&2 || true
      fatal "nodesim 进程已退出" "$desc"
    fi
    if status_ok "$expr"; then
      return 0
    fi
    sleep 1
  done
  echo "--- nodesim.log (tail 50) ---" >&2
  tail -n 50 "$TMP/nodesim.log" 2>/dev/null >&2 || true
  echo "--- status.json ---" >&2
  cat "$TMP/status.json" 2>/dev/null >&2 || true
  fatal "wait_status 超时" "$desc"
}

cleanup() {
  if [[ -n "${SIM_PID:-}" ]] && kill -0 "$SIM_PID" 2>/dev/null; then
    kill -TERM "$SIM_PID" 2>/dev/null || true
    wait "$SIM_PID" 2>/dev/null || true
  fi
  rm -rf "${TMP:-}"
}

#-------------------------------------------------------------------------------
sec "0. 守卫"

[ "${NODE_SMOKE_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || fatal "NODE_SMOKE_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || fatal "ADMIN_EMAIL / ADMIN_PASS must be explicit"
[ -n "${NODESIM:-}" ] && [ -x "$NODESIM" ] || fatal "NODESIM must point to an executable nodesim binary"
[ -n "${SERVE_LOG:-}" ] && [ -r "$SERVE_LOG" ] || fatal "SERVE_LOG must be a readable pandora serve log file"

DB_NAME=$(db 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || fatal "database name must contain a standalone test/e2e segment" "$DB_NAME"

TMP=$(mktemp -d)
trap cleanup EXIT
RUN="$(date +%s)$RANDOM"
mkdir -p "$TMP/users" "$TMP/links" "$TMP/results" "$TMP/fixture"

USER_PASS='Smoke!E2e2026x'
TAG_PREFIX="Smoke $RUN Line"

#-------------------------------------------------------------------------------
sec "1. 后台登录 + 重认证"
admin_auth
ok "admin_auth（login + reauth）"

#-------------------------------------------------------------------------------
sec "2. 协议 schema（shadowsocks）"

api GET "$ADM/v1/node-protocol-schemas" -H "$AH"
[ "$HTTP_CODE" = 200 ] || fatal "node-protocol-schemas" "HTTP $HTTP_CODE"
printf '%s' "$HTTP_BODY" > "$TMP/schemas.json"
CIPHER=$(python3 -c '
import json, sys
with open(sys.argv[1]) as f:
    schemas = json.load(f)["schemas"]
for s in schemas:
    if s.get("node_type") == "shadowsocks":
        req = s.get("required") or []
        if req != ["cipher"]:
            raise SystemExit("shadowsocks required is not [cipher]")
        methods = s.get("methods") or []
        if not methods:
            raise SystemExit("no methods")
        print(methods[0])
        raise SystemExit(0)
raise SystemExit("shadowsocks schema not found")
' "$TMP/schemas.json") || fatal "shadowsocks schema / cipher" "$CIPHER"
ok "shadowsocks cipher=$CIPHER"

#-------------------------------------------------------------------------------
sec "3. 夹具（池、服务器、节点、套餐）"

POOL_CODE="smk-$RUN"
api POST "$ADM/v1/node-pools" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"code\":\"$POOL_CODE\",\"name\":\"Smoke pool $RUN\"}"
[ "$HTTP_CODE" = 200 ] || fatal "create pool" "HTTP $HTTP_CODE"
POOL_ID=$(jget_body id) || fatal "pool id missing"
ok "节点池 $POOL_CODE"

for i in $(seq 1 "$SMOKE_SERVERS"); do
  sname="smoke-$RUN-s$i"
  host="s$i.smoke.example.test"
  ipv4="203.0.113.$((100 + i))"
  api POST "$ADM/v1/servers" -H "$AH" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$sname\",\"hostname\":\"$host\",\"public_ipv4\":\"$ipv4\"}"
  [ "$HTTP_CODE" = 201 ] || fatal "create server $i" "HTTP $HTTP_CODE"
  sid=$(jget_body id) || fatal "server $i id"
  echo "$i $sid" >> "$TMP/fixture/servers"

  port=$((21000 + i))
  disp="$TAG_PREFIX $i"
  api POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: smoke-node-$RUN-$i" \
    -d "{\"name\":\"smoke-$RUN-n$i\",\"server_id\":\"$sid\",\"pool_id\":\"$POOL_ID\",\"node_type\":\"shadowsocks\",\"server_host\":\"$host\",\"server_port\":$port,\"traffic_rate\":1,\"display_name\":\"$disp\",\"protocol_config\":{\"cipher\":\"$CIPHER\"}}"
  [ "$HTTP_CODE" = 201 ] || fatal "create node $i" "HTTP $HTTP_CODE"
  nid=$(jget_body id) || fatal "node $i id"
  nrv=$(jget_body row_version) || fatal "node $i row_version"
  echo "$i $nid $nrv $sid" >> "$TMP/fixture/nodes"
  ok "服务器/节点 $i"
done

api POST "$ADM/v1/plans/complete" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: smoke-plan-$RUN" \
  -d "{\"code\":\"smoke-$RUN\",\"name\":\"Smoke $RUN\",\"visibility\":\"public\",\"traffic_gb\":100,\"pool_ids\":[\"$POOL_ID\"],\"prices\":[{\"billing_interval\":\"month\",\"interval_count\":1,\"unit_amount\":1000,\"currency\":\"CNY\",\"trial_days\":0}],\"publish\":true}"
[ "$HTTP_CODE" = 201 ] || fatal "plans/complete" "HTTP $HTTP_CODE"
PLAN=$(jget_body plan.id) || fatal "plan id"
PRICE=$(jget_body price_ids.0) || fatal "price id"
ok "套餐 smoke-$RUN"

SERVER_ID_LIST=$(awk '{print $2}' "$TMP/fixture/servers" | paste -sd, -)

#-------------------------------------------------------------------------------
sec "4. 门户用户（并发 4）"

register_one() {
  local k=$1
  local email="smoke_${RUN}_${k}@example.com"
  api POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$email\"}"
  [ "$HTTP_CODE" = 200 ] || return 1
  local rt dc
  rt=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["registration_token"])') || return 1
  dc=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["dev_code"])') || return 1
  api POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"$rt\",\"code\":\"$dc\",\"password\":\"$USER_PASS\"}"
  [ "$HTTP_CODE" = 201 ] || return 1
  local uid
  uid=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["user_id"])') || return 1
  api POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$USER_PASS\"}"
  [ "$HTTP_CODE" = 200 ] || return 1
  local tok
  tok=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])') || return 1
  printf '%s %s\n' "$uid" "$tok" > "$TMP/users/$k"
}
export -f register_one api
export PUB TMP RUN USER_PASS HTTP_CODE HTTP_BODY

reg_fail=0
seq 1 "$SMOKE_USERS" | xargs -P 4 -I {} bash -c 'register_one "$1" || exit 1' _ {} || reg_fail=1
if [ "$reg_fail" -ne 0 ]; then
  n_ok=$(find "$TMP/users" -type f 2>/dev/null | wc -l | tr -d ' ')
  fatal "用户注册/登录失败" "成功 $n_ok / $SMOKE_USERS"
fi
ok "$SMOKE_USERS 个门户用户"

#-------------------------------------------------------------------------------
sec "5. 后台开通（grant，并发 4）"

admin_auth

grant_one() {
  local k=$1
  local uid
  uid=$(awk '{print $1}' "$TMP/users/$k") || return 1
  api POST "$ADM/v1/orders/manual" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: smoke-order-$RUN-$k" \
    -d "{\"user_id\":\"$uid\",\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\",\"reason\":\"node smoke e2e\",\"settlement\":\"grant\"}"
  [ "$HTTP_CODE" = 201 ] || return 1
}
export -f grant_one
export ADM AH PLAN PRICE RUN TMP

grant_fail=0
seq 1 "$SMOKE_USERS" | xargs -P 4 -I {} bash -c 'grant_one "$1" || exit 1' _ {} || grant_fail=1
if [ "$grant_fail" -ne 0 ]; then
  fatal "人工开单 grant 失败" "见上方 HTTP 码"
fi
ok "$SMOKE_USERS 笔 grant 订单"

sub_cnt=$(db "SELECT count(*) FROM subscriptions WHERE plan_id='$PLAN' AND status='active'")
[ "$sub_cnt" = "$SMOKE_USERS" ] && ok "SQL：active 订阅数 = $SMOKE_USERS" \
  || bad "SQL：active 订阅数" "got $sub_cnt want $SMOKE_USERS"

#-------------------------------------------------------------------------------
sec "6. 接入令牌"

admin_auth
umask 077
: > "$TMP/tokens"
chmod 600 "$TMP/tokens"

PANEL=
CA=
for i in $(seq 1 "$SMOKE_SERVERS"); do
  sid=$(awk -v i="$i" '$1==i{print $2}' "$TMP/fixture/servers") || fatal "server id $i"
  api POST "$ADM/v1/servers/$sid/bootstrap-token" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: smoke-boot-$RUN-$i" -d '{}'
  [ "$HTTP_CODE" = 201 ] || fatal "bootstrap-token server $i" "HTTP $HTTP_CODE"
  # 响应体含令牌：落 0600 临时文件（上面 umask 077），python 读完即删
  printf '%s' "$HTTP_BODY" > "$TMP/boot.json"
  # 令牌由 python 直接追加进令牌文件，不经 shell 变量、不打印；stdout 只有 panel 与 ca 两行
  python3 - "$TMP/boot.json" "$TMP/tokens" "s$i" > "$TMP/boot-parse.txt" <<'PY' || { rm -f "$TMP/boot.json"; fatal "parse install_command $i"; }
import json, re, sys
body_path, tokens_path, name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(body_path, encoding="utf-8") as f:
    body = json.load(f)
token = body["token"]
cmd = body.get("install_command") or ""
m_panel = re.search(r"--panel\s+'([^']+)'", cmd)
m_ca = re.search(r"--ca-sha256\s+'([^']+)'", cmd)
if not m_panel or not m_ca or not token:
    raise SystemExit("install_command parse failed")
with open(tokens_path, "a", encoding="utf-8") as f:
    f.write(f"{name} {token}\n")
print(m_panel.group(1))
print(m_ca.group(1))
PY
  rm -f "$TMP/boot.json"
  pnl=$(sed -n '1p' "$TMP/boot-parse.txt")
  ca=$(sed -n '2p' "$TMP/boot-parse.txt")
  rm -f "$TMP/boot-parse.txt"
  if [ -z "$PANEL" ]; then
    PANEL=$pnl
    CA=$ca
  else
    [ "$PANEL" = "$pnl" ] && [ "$CA" = "$ca" ] || fatal "各台 panel/ca 不一致" "server $i"
  fi
done
echo "nodesim --panel '$PANEL' --ca-sha256 '$CA'"
ok "$SMOKE_SERVERS 枚 bootstrap 令牌已写入令牌文件"

#-------------------------------------------------------------------------------
sec "7. 启动 nodesim"

mkdir -p "$TMP/state"
"$NODESIM" --panel "$PANEL" --ca-sha256 "$CA" --tokens-file "$TMP/tokens" \
  --state-dir "$TMP/state" --status-file "$TMP/status.json" \
  --report-interval "$NODESIM_REPORT_INTERVAL" > "$TMP/nodesim.log" 2>&1 &
SIM_PID=$!

enroll_expr="len(d.get('servers', {})) == $SMOKE_SERVERS and all(
  d['servers'].get(f's{i}', {}).get('enrolled') and d['servers'].get(f's{i}', {}).get('connected')
  for i in range(1, $SMOKE_SERVERS + 1)
)"
wait_status 60 "nodesim 接入并连接" "$enroll_expr"
ok "nodesim：$SMOKE_SERVERS 台 enrolled + connected"

#-------------------------------------------------------------------------------
sec "8. 节点上线（activate）"

admin_auth
for i in $(seq 1 "$SMOKE_SERVERS"); do
  read -r _ nid nrv _ < <(awk -v i="$i" '$1==i{print}' "$TMP/fixture/nodes")
  api POST "$ADM/v1/nodes/$nid/activate" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: smoke-act-$RUN-$i" -d "{\"row_version\":$nrv}"
  [ "$HTTP_CODE" = 200 ] || fatal "activate node $i" "HTTP $HTTP_CODE"
  ss=$(jget_body serving_status) || fatal "activate serving_status"
  [ "$ss" = active ] || fatal "activate node $i serving_status" "$ss"
  ok "节点 $i activate"
done

ready_cnt=$(db "SELECT count(*) FROM servers WHERE id IN ('$(echo "$SERVER_ID_LIST" | sed "s/,/','/g")') AND status='ready'")
[ "$ready_cnt" = "$SMOKE_SERVERS" ] && ok "SQL：服务器均为 ready" \
  || bad "SQL：ready 服务器数" "got $ready_cnt want $SMOKE_SERVERS"

#-------------------------------------------------------------------------------
sec "9. 等名单下发"

roster_expr=$(python3 <<PY
import json
n = $SMOKE_SERVERS
u = $SMOKE_USERS
expected = {}
with open("$TMP/fixture/nodes") as f:
    for line in f:
        parts = line.split()
        si, nid = parts[0], parts[1]
        expected[f"s{si}"] = nid
conds = []
for name, nid in expected.items():
    conds.append(
        f"(len(d['servers'].get('{name}', {{}}).get('nodes') or {{}}) == 1 "
        f"and '{nid}' in (d['servers'].get('{name}', {{}}).get('nodes') or {{}}) "
        f"and (d['servers']['{name}']['nodes']['{nid}'].get('config_sha256') or '') != '' "
        f"and d['servers']['{name}']['nodes']['{nid}'].get('user_count') == {u})"
    )
print(" and ".join(conds) if conds else "False")
PY
)
wait_status 90 "名单与 config_sha256、user_count" "$roster_expr"
ok "名单：每服 1 节点、user_count=$SMOKE_USERS"

#-------------------------------------------------------------------------------
sec "10. 订阅线路与全用户链接"

uid1=$(awk '{print $1}' "$TMP/users/1")
tok1=$(awk '{print $2}' "$TMP/users/1")
UH1="Authorization: Bearer $tok1"

api GET "$PUB/v1/me/subscription-links" -H "$UH1"
[ "$HTTP_CODE" = 200 ] || fatal "subscription-links user 1" "HTTP $HTTP_CODE"
link_url=$(jget_body links.0.url) || fatal "link url"
LINK_PATH=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$link_url")
PROXY_UUID=$(db "SELECT proxy_uuid FROM subscriptions WHERE user_id='$uid1' AND plan_id='$PLAN' LIMIT 1")
[ -n "$PROXY_UUID" ] || fatal "proxy_uuid for user 1"

found=0
for attempt in $(seq 1 20); do
  api GET "$PUB$LINK_PATH" -H 'User-Agent: sing-box 1.13.0'
  if [ "$HTTP_CODE" = 200 ]; then
    cnt=$(printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
TAG_PREFIX, SERVERS, UUID = sys.argv[1], int(sys.argv[2]), sys.argv[3]
d = json.load(sys.stdin)
n = 0
for o in d.get("outbounds") or []:
    if o.get("type") == "shadowsocks" and TAG_PREFIX in (o.get("tag") or ""):
        n += 1
print(n)
' "$TAG_PREFIX" "$SMOKE_SERVERS" "$PROXY_UUID" 2>/dev/null) || cnt=0
    if [ "${cnt:-0}" = "$SMOKE_SERVERS" ] && printf '%s' "$HTTP_BODY" | grep -qF "$PROXY_UUID"; then
      found=1
      break
    fi
  fi
  sleep 3
done
[ "$found" -eq 1 ] && ok "用户 1：$SMOKE_SERVERS 条 shadowsocks 出站且含 proxy_uuid" \
  || bad "用户 1 订阅线路" "outbounds 匹配数或 uuid 未满足"

fetch_link_one() {
  local k=$1
  local tok uid
  read -r uid tok < "$TMP/users/$k"
  api GET "$PUB/v1/me/subscription-links" -H "Authorization: Bearer $tok"
  [ "$HTTP_CODE" = 200 ] || return 1
  local url sub
  url=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["links"][0]["url"])') || return 1
  sub=$(printf '%s' "$HTTP_BODY" | python3 -c 'import json,sys; print(json.load(sys.stdin)["links"][0]["subscription_id"])') || return 1
  local path
  path=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$url")
  printf '%s %s\n' "$path" "$sub" > "$TMP/links/$k"
}
export -f fetch_link_one
export PUB TMP HTTP_CODE HTTP_BODY

link_fail=0
seq 1 "$SMOKE_USERS" | xargs -P "$SMOKE_PARALLEL" -I {} bash -c 'fetch_link_one "$1" || exit 1' _ {} || link_fail=1
if [ "$link_fail" -ne 0 ]; then
  n=$(find "$TMP/links" -type f 2>/dev/null | wc -l | tr -d ' ')
  fatal "取订阅链接失败" "成功 $n / $SMOKE_USERS"
fi
ok "$SMOKE_USERS 个用户订阅链接"

#-------------------------------------------------------------------------------
sec "11. 负载（${SMOKE_SECONDS}s / ${SMOKE_ROUNDS} 轮）"

OFFSET=$(wc -c < "$SERVE_LOG" | tr -d ' ')
ROUND_SEC=$((SMOKE_SECONDS / SMOKE_ROUNDS))
[ "$ROUND_SEC" -ge 1 ] || fatal "SMOKE_SECONDS / SMOKE_ROUNDS 须 >= 1"

load_one() {
  local k=$1 round=$2
  local tok path sub ua uidx
  read -r path sub < "$TMP/links/$k"
  read -r _ tok < "$TMP/users/$k"
  uidx=$(( (k + round) % 7 ))
  case "$uidx" in
    0) ua='sing-box 1.13.0' ;;
    1) ua='mihomo/1.19.0' ;;
    2) ua='clash-verge/v2.0.0' ;;
    3) ua='ClashforWindows/0.20.39' ;;
    4) ua='Shadowrocket/2070 CFNetwork/1410.0.3 Darwin/22.6.0' ;;
    5) ua='v2rayN/7.0' ;;
    *) ua='Hiddify/2.5.7' ;;
  esac
  api GET "$PUB$path" -H "User-Agent: $ua"
  local bytes=${#HTTP_BODY}
  echo "pull $HTTP_CODE $bytes" >> "$TMP/results/$k"
  if [ $((round % 2)) -eq 1 ]; then
    api GET "$PUB/v1/me/subscriptions" -H "Authorization: Bearer $tok"
  else
    api GET "$PUB/v1/me/subscriptions/$sub/usage" -H "Authorization: Bearer $tok"
  fi
  echo "portal $HTTP_CODE" >> "$TMP/results/$k"
}
export -f load_one api
export PUB TMP HTTP_CODE HTTP_BODY

for round in $(seq 1 "$SMOKE_ROUNDS"); do
  t0=$SECONDS
  seq 1 "$SMOKE_USERS" | xargs -P "$SMOKE_PARALLEL" -I {} bash -c 'load_one "$1" "$2"' _ {} "$round"
  elapsed=$((SECONDS - t0))
  if [ "$elapsed" -lt "$ROUND_SEC" ]; then
    sleep $((ROUND_SEC - elapsed))
  fi
done
ok "负载 ${SMOKE_ROUNDS} 轮完成"

#-------------------------------------------------------------------------------
sec "12. 等待流量上报确认"

WAIT_SEC=$(python3 -c '
import re, sys
s = sys.argv[1].strip().lower()
m = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(s|m|h)?", s)
if not m:
    raise SystemExit(2)
v = float(m.group(1))
u = m.group(2) or "s"
if u == "s": sec = v
elif u == "m": sec = v * 60
else: sec = v * 3600
print(int(sec * 2 + 2))
' "$NODESIM_REPORT_INTERVAL") || WAIT_SEC=12
sleep "$WAIT_SEC"
ok "已等待 ${WAIT_SEC}s（2×上报间隔 + 2）"

#-------------------------------------------------------------------------------
sec "13. 判定"

if python3 - "$TMP/results" "$SMOKE_USERS" <<'PY'
import os, sys, collections
res_dir, users = sys.argv[1], int(sys.argv[2])
codes_pull = []
codes_portal = []
zero_bytes = 0
for k in range(1, users + 1):
    p = os.path.join(res_dir, str(k))
    if not os.path.isfile(p):
        print("missing results", k, file=sys.stderr)
        sys.exit(1)
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        parts = line.split()
        if parts[0] == "pull":
            c = int(parts[1])
            codes_pull.append(c)
            b = int(parts[2])
            if b <= 0:
                zero_bytes += 1
        elif parts[0] == "portal":
            codes_portal.append(int(parts[1]))
nc5 = sum(1 for c in codes_pull + codes_portal if 500 <= c <= 599)
non200 = sum(1 for c in codes_pull + codes_portal if c != 200)
if nc5 != 0:
    print("5xx count", nc5, file=sys.stderr)
    sys.exit(1)
if non200 != 0:
    cnt = collections.Counter(codes_pull + codes_portal)
    print("non-200", dict(cnt), file=sys.stderr)
    sys.exit(1)
if zero_bytes != 0:
    print("zero pull bytes", zero_bytes, file=sys.stderr)
    sys.exit(1)
PY
then
  ok "客户端：零 5xx、非 200 为 0、pull 字节 > 0"
else
  bad "客户端结果汇总" "见 stderr"
fi

LOG_SLICE=$(mktemp)
ESC=$(printf '\033')
tail -c +$((OFFSET + 1)) "$SERVE_LOG" | sed "s/${ESC}\[[0-9;]*m//g" > "$LOG_SLICE"
acc5=$(grep -Ec 'access.*"status": ?5[0-9]{2}' "$LOG_SLICE" || true)
[ "${acc5:-0}" -eq 0 ] && ok "serve 日志 access 5xx = 0" \
  || bad "serve 日志 access 5xx" "count=$acc5"

# console 编码：时间<TAB>级别<TAB>caller<TAB>msg<TAB>{字段}；JSON 编码："level":"error"
err_lines=$(grep -E '"level"[[:space:]]*:[[:space:]]*"error"|^[^[:space:]]+[[:space:]]+error[[:space:]]' "$LOG_SLICE" || true)
err_cnt=$(printf '%s\n' "$err_lines" | sed '/^$/d' | wc -l | tr -d ' ')
[ "${err_cnt:-0}" -eq 0 ] && ok "serve 日志 ERROR 行 = 0" \
  || { bad "serve 日志 ERROR 行" "count=$err_cnt"; printf '%s\n' "$err_lines" | head -n 20; }
rm -f "$LOG_SLICE"

sim_err=$(jget "$TMP/status.json" errors 2>/dev/null || echo 999)
[ "$sim_err" = 0 ] && ok "模拟器 errors=0" || bad "模拟器顶层 errors" "$sim_err"

if python3 - "$TMP/status.json" "$SMOKE_SERVERS" <<'PY'
import json, sys
n = int(sys.argv[2])
with open(sys.argv[1]) as f:
    d = json.load(f)
for i in range(1, n + 1):
    s = d.get("servers", {}).get(f"s{i}")
    if not s:
        print(f"missing s{i}", file=sys.stderr)
        sys.exit(1)
    if not s.get("connected"):
        print(f"s{i} not connected", file=sys.stderr)
        sys.exit(1)
    if (s.get("heartbeats_sent") or 0) < 1:
        print(f"s{i} heartbeats", file=sys.stderr)
        sys.exit(1)
    if (s.get("traffic_reports_acked") or 0) < 1:
        print(f"s{i} traffic acks", s.get("traffic_reports_acked"), file=sys.stderr)
        sys.exit(1)
    if s.get("errors"):
        print(f"s{i} errors={s.get('errors')} last={s.get('last_error')}", file=sys.stderr)
        sys.exit(1)
PY
then
  ok "模拟器：connected、心跳与流量回执"
else
  bad "模拟器各服务器状态" "见 stderr"
fi

sql_in=$(echo "$SERVER_ID_LIST" | sed "s/,/','/g")
rep_rows=$(db "SELECT count(*) FROM node_traffic_reports WHERE server_id IN ('$sql_in')")
[ "${rep_rows:-0}" -gt 0 ] && ok "SQL：node_traffic_reports 有行" \
  || bad "SQL：node_traffic_reports" "count=$rep_rows"

acked_sum=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(sum((s.get("traffic_bytes_acked") or 0) for s in d.get("servers",{}).values()))' "$TMP/status.json")
[ "${acked_sum:-0}" -gt 0 ] || bad "status traffic_bytes_acked 合计" "$acked_sum"
sql_bytes=$(db "SELECT coalesce(sum(upload_bytes+download_bytes),0) FROM node_traffic_reports WHERE server_id IN ('$sql_in')")
[ "${sql_bytes:-0}" -ge "${acked_sum:-0}" ] && ok "SQL 流量字节 >= 模拟器 ack 合计" \
  || bad "SQL 流量字节" "sql=$sql_bytes ack=$acked_sum"
inv=$(db "SELECT coalesce(sum(invalid_count+unlisted_count),0) FROM node_traffic_reports WHERE server_id IN ('$sql_in')")
[ "$inv" = 0 ] && ok "SQL：invalid+unlisted = 0" || bad "SQL invalid/unlisted" "$inv"

quota_users=$(db "SELECT count(DISTINCT subscription_id) FROM quota_balances qb JOIN subscriptions s ON s.id=qb.subscription_id WHERE s.plan_id='$PLAN' AND qb.metric='traffic.bytes' AND qb.consumed > 0")
[ "${quota_users:-0}" -ge 1 ] && ok "SQL：quota consumed > 0" \
  || bad "SQL quota consumed" "count=$quota_users"

usage_uid=$(db "SELECT s.user_id FROM quota_balances qb JOIN subscriptions s ON s.id=qb.subscription_id WHERE s.plan_id='$PLAN' AND qb.metric='traffic.bytes' AND qb.consumed > 0 LIMIT 1")
usage_tok=
for uf in "$TMP"/users/*; do
  [ -f "$uf" ] || continue
  t=$(awk -v u="$usage_uid" '$1==u{print $2; exit}' "$uf")
  [ -n "${t:-}" ] && usage_tok=$t && break
done
usage_sub=$(db "SELECT id FROM subscriptions WHERE user_id='$usage_uid' AND plan_id='$PLAN' LIMIT 1")
api GET "$PUB/v1/me/subscriptions/$usage_sub/usage" -H "Authorization: Bearer $usage_tok"
if [ "$HTTP_CODE" = 200 ]; then
  usage_ok=$(printf '%s' "$HTTP_BODY" | python3 -c '
import json,sys
d=json.load(sys.stdin)
today=int(d.get("today_bytes") or 0)
days=sum(int(x.get("bytes") or 0) for x in (d.get("days") or []))
print(1 if today+days>0 else 0)
')
  [ "$usage_ok" = 1 ] && ok "门户 usage 有流量" || bad "门户 usage 无字节" "body truncated"
else
  bad "门户 usage HTTP" "$HTTP_CODE"
fi

#-------------------------------------------------------------------------------
sec "14. 停止 nodesim"

kill -TERM "$SIM_PID" 2>/dev/null || true
if wait "$SIM_PID"; then
  ok "nodesim 正常退出"
else
  bad "nodesim 退出码非 0" "见 nodesim.log 末尾"
  tail -n 30 "$TMP/nodesim.log" >&2 || true
fi
SIM_PID=

#-------------------------------------------------------------------------------
sec "15. 汇总"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" -eq 0 ]
