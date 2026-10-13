#!/usr/bin/env bash
# 删人断线端到端（P5 最后一条完成标准）：用户被移出名单后，节点在几秒内断掉他已建立的连接。
#
# 三个真件，全在本机回环上：
#   - 面板新二进制（pandora serve + worker，由调用方起好）；
#   - 真 pdnd（pandora-native，本仓库构建）：enroll --token-stdin 接入，-c 跑守护进程；
#   - 真 sing-box 客户端（钉住的官方发布物，核对 SHA-256）：从订阅里取 shadowsocks 出站，自建 mixed 入站。
#
# 协议选 shadowsocks（aes-128-gcm）：回环上不要证书；NativeCore 的 shadowsocks 适配器认证通过后按用户 ID
# 登记连接（kernel/user_sessions.go），DelUsers 先删用户表、再 sessions.revoke 关掉该用户全部在途连接
# （kernel/shadowsocks.go DelUsers）。节点端名单 diff 见 node/users.go（换凭据是同 ID 先删旧、再加新）。
#
# 步骤：
#   0  守卫（一次性库、二进制、平台）
#   1  后台登录、shadowsocks cipher
#   2  夹具：池、服务器、节点（监听 0.0.0.0:KICK_NODE_PORT）
#   3  接入令牌 → pdnd enroll（令牌只经 0600 文件喂标准输入）→ 写 config.json → 起 pdnd
#   4  节点上线、等 pdnd 监听端口；套餐发布
#   5  用户 A、B 注册并开通
#   6  sing-box：下载核对（或用 SINGBOX），按 A、B 的订阅各起一个客户端
#   7  回环 echo 服务；经 A、B 各探一次直到通（即名单已到节点）
#   8  A、B 各开一条长连接（每 200ms 收发一个字节）
#   9  后台停用 A：A 的长连接 ≤ KICK_MAX_SEC 断开，A 新连接失败，B 不受影响
#  10  B 在门户重置链接：B 的旧长连接 ≤ KICK_MAX_SEC 断开，旧凭据新连接失败，新订阅能连
#  11  收尾：pdnd 收 SIGTERM 正常退出；打印两次断线耗时
#
# 只在用 SQL 读：订阅的 proxy_uuid 与 id（核对订阅正文里的口令；门户没有按用户取 proxy_uuid 的接口）。
#
# 环境变量：
#   PUB、ADM、PSQL、ADMIN_EMAIL、ADMIN_PASS（同 node_smoke_e2e.sh）
#   NODE_KICK_E2E_DISPOSABLE=YES_DELETE_FIXTURES（必填）
#   PANDORA_NATIVE（必填，可执行的 pandora-native）
#   SINGBOX（可选：sing-box 可执行文件；留空则下载下面钉住的版本，仅 linux x86_64）
#   KICK_MAX_SEC（缺省 5：断线时限，秒）、KICK_NODE_PORT（缺省 21901）、READY_WAIT（缺省 60）
# 会留下用户、订单、审计，只许在库名含独立 test/e2e 段的一次性库上跑。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}
PANDORA_NATIVE=${PANDORA_NATIVE:-}
SINGBOX=${SINGBOX:-}
KICK_MAX_SEC=${KICK_MAX_SEC:-5}
KICK_NODE_PORT=${KICK_NODE_PORT:-21901}
READY_WAIT=${READY_WAIT:-60}

# sing-box 官方发布物：版本与 SHA-256 取自 GitHub release 页公布的值
# （https://github.com/SagerNet/sing-box/releases/tag/v1.14.2 ，资产 sing-box-1.14.2-linux-amd64-glibc.tar.gz）。
# 升版本时两处一起改，SHA 用 release 页公布的值，不用自己下载后算出来的。
SB_VERSION=1.14.2
SB_ASSET="sing-box-${SB_VERSION}-linux-amd64-glibc.tar.gz"
SB_SHA256=5c7bc18461827b28d0e5ee7e89d33b276d3ff7c818531104c8e8d26d85b0656e
SUB_UA='sing-box 1.13.0'

TMP=
PDND_PID=
ECHO_PID=
SB_PIDS=()
HOLD_PIDS=()
pass=0
HTTP_CODE=
HTTP_BODY=
AH=

ok() { echo "  [ OK ] $1"; pass=$((pass + 1)); }
die() { echo "  [FAIL] $1" >&2; [ -z "${2:-}" ] || echo "         $2" >&2; exit 1; }
sec() { echo; echo "=== $1 ==="; }
now() { date +%s.%N; }

dump_logs() {
  [ -n "${TMP:-}" ] && [ -d "$TMP" ] || return 0
  for f in "$TMP"/pdnd.log "$TMP"/sb-*.log "$TMP"/hold-*.status "$TMP"/echo.log; do
    [ -f "$f" ] || continue
    echo "--- $(basename "$f")（末尾 40 行）---" >&2
    tail -n 40 "$f" >&2 || true
  done
}

stop_pid() {
  local pid=$1
  [ -n "$pid" ] || return 0
  kill -TERM "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
}

cleanup() {
  local rc=$?
  [ "$rc" -eq 0 ] || dump_logs
  local p
  for p in "${HOLD_PIDS[@]}" "${SB_PIDS[@]}"; do stop_pid "$p"; done
  stop_pid "${ECHO_PID:-}"
  stop_pid "${PDND_PID:-}"
  [ -z "${TMP:-}" ] || rm -rf "$TMP"
  exit "$rc"
}

is_uuid() {
  [[ ${1:-} =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]
}

request() {
  local method=$1 url=$2 body_file
  shift 2
  body_file=$(mktemp)
  HTTP_CODE=$(curl -sS -o "$body_file" -w '%{http_code}' -X "$method" "$url" "$@") \
    || { rm -f "$body_file"; die "request failed: $method $url"; }
  HTTP_BODY=$(cat "$body_file")
  rm -f "$body_file"
}

expect_http() {
  local expected=$1 method=$2 url=$3
  shift 3
  request "$method" "$url" "$@"
  [ "$HTTP_CODE" = "$expected" ] || die "$method $url expected $expected" "status=$HTTP_CODE body=$(printf '%s' "$HTTP_BODY" | head -c 400)"
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

db() { "$PSQL" -X -v ON_ERROR_STOP=1 -qAtc "$1" | tr -d '[:space:]'; }

admin_auth() {
  expect_http 200 POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}"
  local login_tok
  login_tok=$(json_get "$HTTP_BODY" access_token) || die "admin login: no access_token"
  expect_http 200 POST "$ADM/v1/auth/reauth" -H "Authorization: Bearer $login_tok" \
    -H 'Content-Type: application/json' -d "{\"password\":\"$ADMIN_PASS\"}"
  AH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
}

register_user() {
  local email=$1
  expect_http 200 POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$email\"}"
  local rt code
  rt=$(json_get "$HTTP_BODY" registration_token) || die "register/start: no registration_token"
  # 没开邮箱验证（缺省）时没有 dev_code，code 传空串照样能完成注册
  code=$(json_get "$HTTP_BODY" dev_code 2>/dev/null || true)
  expect_http 201 POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"$rt\",\"code\":\"$code\",\"password\":\"$USER_PASS\"}"
  REG_USER_ID=$(json_get "$HTTP_BODY" user_id)
  is_uuid "$REG_USER_ID" || die "registration returned no user id"
  expect_http 200 POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$email\",\"password\":\"$USER_PASS\"}"
  REG_UH="Authorization: Bearer $(json_get "$HTTP_BODY" access_token)"
}

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

# 把订阅链接的路径拉下来（sing-box UA），取我们这条 shadowsocks 出站，写一份最小客户端配置：
# 一个 127.0.0.1 上的 mixed 入站 + 这条出站（server 换成 127.0.0.1：节点的对外主机名是 example.test，回环上解析不了）。
# 同时核对订阅里的口令就是这份订阅的 proxy_uuid、端口就是节点端口。
build_client_config() {
  local link_path=$1 want_uuid=$2 mixed_port=$3 out=$4
  local i err
  # 订阅渲染可能晚一拍才带上刚上线的节点（同 node_smoke / expiry 的重试），最多 10 次、间隔 3 秒
  for i in $(seq 1 10); do
    request GET "$PUB$link_path" -H "User-Agent: $SUB_UA"
    if [ "$HTTP_CODE" = 200 ]; then
      printf '%s' "$HTTP_BODY" > "$TMP/sub.json"
      if err=$(python3 - "$TMP/sub.json" "$TAG" "$want_uuid" "$KICK_NODE_PORT" "$mixed_port" "$out" 2>&1 <<'PY'
import json, os, sys
sub_path, tag, want_uuid, node_port, mixed_port, out = sys.argv[1:7]
with open(sub_path, encoding="utf-8") as f:
    sub = json.load(f)
cands = [o for o in sub.get("outbounds") or [] if o.get("type") == "shadowsocks" and tag in (o.get("tag") or "")]
if len(cands) != 1:
    raise SystemExit(f"want 1 shadowsocks outbound with tag {tag}, got {len(cands)}")
o = cands[0]
if str(o.get("password", "")).lower() != want_uuid.lower():
    raise SystemExit("outbound password is not the subscription proxy_uuid")
if int(o.get("server_port") or 0) != int(node_port):
    raise SystemExit(f"outbound server_port {o.get('server_port')} != node port {node_port}")
cfg = {
    "log": {"level": "warn", "timestamp": True},
    "inbounds": [{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": int(mixed_port)}],
    "outbounds": [{
        "type": "shadowsocks", "tag": "proxy", "server": "127.0.0.1", "server_port": int(node_port),
        "method": o["method"], "password": o["password"],
    }],
    "route": {"final": "proxy"},
}
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as f:
    json.dump(cfg, f)
PY
      ); then
        rm -f "$TMP/sub.json"
        return 0
      fi
    else
      err="HTTP $HTTP_CODE"
    fi
    sleep 3
  done
  rm -f "$TMP/sub.json"
  die "订阅里没有可用的 shadowsocks 出站" "$err"
}

# 起一个 sing-box 客户端，等 mixed 端口可连；PID 记进 SB_PIDS，最近一个放 LAST_SB_PID。
start_singbox() {
  local name=$1 cfg=$2 port=$3
  LD_LIBRARY_PATH="$SB_LD" "$SINGBOX" check -c "$cfg" > "$TMP/sb-$name.check" 2>&1 \
    || { cat "$TMP/sb-$name.check" >&2; die "sing-box check $name"; }
  LD_LIBRARY_PATH="$SB_LD" "$SINGBOX" run -c "$cfg" > "$TMP/sb-$name.log" 2>&1 &
  LAST_SB_PID=$!
  SB_PIDS+=("$LAST_SB_PID")
  local deadline=$((SECONDS + 20))
  until python3 "$TMP/kick.py" tcp "$port"; do
    kill -0 "$LAST_SB_PID" 2>/dev/null || die "sing-box $name 已退出"
    [ "$SECONDS" -lt "$deadline" ] || die "sing-box $name 20s 内没监听 $port"
    sleep 0.2
  done
  ok "sing-box $name 监听 127.0.0.1:$port"
}

# 经 socks 端口探一次 echo（3 次往返，每次 3 秒超时）；通返回 0。
probe() { python3 "$TMP/kick.py" probe "$1" "$ECHO_PORT"; }

probe_until_ok() {
  local port=$1 explain=$2
  local deadline=$((SECONDS + READY_WAIT))
  until probe "$port"; do
    kill -0 "$PDND_PID" 2>/dev/null || die "pdnd 已退出" "$explain"
    [ "$SECONDS" -lt "$deadline" ] || die "$explain 超时（${READY_WAIT}s）"
    sleep 1
  done
  ok "$explain"
}

start_hold() {
  local name=$1 port=$2
  : > "$TMP/hold-$name.status"
  python3 "$TMP/kick.py" hold "$port" "$ECHO_PORT" "$TMP/hold-$name.status" &
  HOLD_PIDS+=("$!")
}

hold_broken() { grep -q '^broken ' "$TMP/hold-$1.status"; }

# 等长连接断开，回显断开时刻（python 的 time.time()，与 date +%s.%N 同一个钟）。
wait_broken() {
  local name=$1 limit=$2
  local deadline=$((SECONDS + limit))
  until hold_broken "$name"; do
    [ "$SECONDS" -lt "$deadline" ] || { echo "timeout"; return 1; }
    sleep 0.1
  done
  awk '$1=="broken"{print $2; exit}' "$TMP/hold-$name.status"
}

# 判定一次断线：t1 不早于发请求的时刻（否则是别的原因断的），t1 − t0 ≤ KICK_MAX_SEC。
judge_kick() {
  local label=$1 t_start=$2 t0=$3 t1=$4
  python3 - "$t_start" "$t0" "$t1" "$KICK_MAX_SEC" <<'PY'
import sys
t_start, t0, t1, limit = map(float, sys.argv[1:5])
if t1 < t_start:
    print(f"broke {t_start - t1:.3f}s before the request was sent", file=sys.stderr)
    raise SystemExit(1)
dt = t1 - t0
print(f"{dt:.3f}")
raise SystemExit(0 if dt <= limit else 1)
PY
}

#-------------------------------------------------------------------------------
sec "0. 守卫"
[ "${NODE_KICK_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || die "NODE_KICK_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required"
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] || die "ADMIN_EMAIL / ADMIN_PASS must be explicit"
[ -n "$PANDORA_NATIVE" ] && [ -x "$PANDORA_NATIVE" ] || die "PANDORA_NATIVE must point to an executable pandora-native binary"
if [ -n "$SINGBOX" ]; then
  [ -x "$SINGBOX" ] || die "SINGBOX is set but not executable" "$SINGBOX"
else
  [ "$(uname -s)-$(uname -m)" = Linux-x86_64 ] || die "SINGBOX 留空时只能在 linux x86_64 上下载官方包" "$(uname -s)-$(uname -m)"
fi
DB_NAME=$(db 'SELECT current_database()')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] || die "database name must contain a standalone test/e2e segment" "$DB_NAME"

TMP=$(mktemp -d)
chmod 700 "$TMP"
trap cleanup EXIT
RUN="$(date +%s)$RANDOM"
USER_PASS='Kick!E2e2026x'
TAG="Kick $RUN Line"

# 客户端小工具：tcp（端口可连）、probe（经 socks5 做 3 次 echo 往返）、hold（长连接每 200ms 收发一字节，
# 断开时写「broken <时刻> <往返数> <原因>」）、echo（回环 echo 服务，端口写进文件）。
cat > "$TMP/kick.py" <<'PY'
import os, socket, socketserver, sys, time

def recvn(s, n):
    buf = b""
    while len(buf) < n:
        chunk = s.recv(n - len(buf))
        if not chunk:
            raise EOFError("eof")
        buf += chunk
    return buf

def socks_connect(socks_port, echo_port, timeout):
    s = socket.create_connection(("127.0.0.1", socks_port), timeout=timeout)
    s.settimeout(timeout)
    s.sendall(b"\x05\x01\x00")
    if recvn(s, 2) != b"\x05\x00":
        raise OSError("socks5 method rejected")
    s.sendall(b"\x05\x01\x00\x01" + socket.inet_aton("127.0.0.1") + echo_port.to_bytes(2, "big"))
    head = recvn(s, 4)
    if head[1] != 0:
        raise OSError(f"socks5 connect rep={head[1]}")
    atyp = head[3]
    if atyp == 1:
        recvn(s, 4 + 2)
    elif atyp == 4:
        recvn(s, 16 + 2)
    elif atyp == 3:
        recvn(s, recvn(s, 1)[0] + 2)
    else:
        raise OSError(f"socks5 atyp={atyp}")
    return s

mode = sys.argv[1]
if mode == "tcp":
    try:
        socket.create_connection(("127.0.0.1", int(sys.argv[2])), timeout=1).close()
    except OSError:
        raise SystemExit(1)
elif mode == "probe":
    try:
        s = socks_connect(int(sys.argv[2]), int(sys.argv[3]), 3)
        for _ in range(3):
            s.sendall(b"p")
            if recvn(s, 1) != b"p":
                raise OSError("echo mismatch")
        s.close()
    except Exception:
        raise SystemExit(1)
elif mode == "hold":
    socks_port, echo_port, status = int(sys.argv[2]), int(sys.argv[3]), sys.argv[4]
    n = 0
    def mark(line):
        with open(status, "a", encoding="utf-8") as f:
            f.write(line + "\n")
    try:
        s = socks_connect(socks_port, echo_port, 3)
        s.settimeout(1.0)
        while True:
            s.sendall(b"h")
            if recvn(s, 1) != b"h":
                raise OSError("echo mismatch")
            n += 1
            if n == 1:
                mark(f"up {time.time():.6f}")
            time.sleep(0.2)
    except Exception as e:
        mark(f"broken {time.time():.6f} {n} {type(e).__name__}")
elif mode == "echo":
    class H(socketserver.BaseRequestHandler):
        def handle(self):
            while True:
                data = self.request.recv(4096)
                if not data:
                    return
                self.request.sendall(data)
    class S(socketserver.ThreadingTCPServer):
        daemon_threads = True
        allow_reuse_address = True
    srv = S(("127.0.0.1", 0), H)
    with open(sys.argv[2] + ".tmp", "w") as f:
        f.write(str(srv.server_address[1]))
    os.rename(sys.argv[2] + ".tmp", sys.argv[2])
    srv.serve_forever()
else:
    raise SystemExit(f"unknown mode {mode}")
PY
ok "守卫通过（库 $DB_NAME）"

#-------------------------------------------------------------------------------
sec "1. 后台登录、shadowsocks cipher"
admin_auth
expect_http 200 GET "$ADM/v1/node-protocol-schemas" -H "$AH"
CIPHER=$(printf '%s' "$HTTP_BODY" | python3 -c '
import json, sys
for s in json.load(sys.stdin).get("schemas", []):
    if s.get("node_type") == "shadowsocks":
        methods = s.get("methods") or []
        if not methods:
            raise SystemExit("no methods")
        print(methods[0])
        raise SystemExit(0)
raise SystemExit("no shadowsocks schema")
') || die "shadowsocks schema / cipher"
case "$CIPHER" in 2022-*) die "schema 的第一个 cipher 是 2022 系，本脚本只按经典 AEAD 写" "$CIPHER" ;; esac
ok "shadowsocks cipher=$CIPHER"

#-------------------------------------------------------------------------------
sec "2. 夹具：池、服务器、节点"
expect_http 200 POST "$ADM/v1/node-pools" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"code\":\"kick-$RUN\",\"name\":\"Kick pool $RUN\"}"
POOL_ID=$(json_get "$HTTP_BODY" id)
is_uuid "$POOL_ID" || die "pool id invalid"

expect_http 201 POST "$ADM/v1/servers" -H "$AH" -H 'Content-Type: application/json' \
  -d "{\"name\":\"kick-$RUN-s1\",\"hostname\":\"kick-$RUN.example.test\",\"public_ipv4\":\"203.0.113.30\"}"
SERVER_ID=$(json_get "$HTTP_BODY" id)
is_uuid "$SERVER_ID" || die "server id invalid"

expect_http 201 POST "$ADM/v1/nodes" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: kick-node-$RUN" \
  -d "{\"name\":\"kick-$RUN-n1\",\"server_id\":\"$SERVER_ID\",\"pool_id\":\"$POOL_ID\",\"node_type\":\"shadowsocks\",\"server_host\":\"kick-$RUN.example.test\",\"server_port\":$KICK_NODE_PORT,\"traffic_rate\":1,\"display_name\":\"$TAG\",\"protocol_config\":{\"cipher\":\"$CIPHER\"}}"
NODE_ID=$(json_get "$HTTP_BODY" id)
NODE_RV=$(json_get "$HTTP_BODY" row_version)
is_uuid "$NODE_ID" || die "node id invalid"
ok "池、服务器、节点（端口 $KICK_NODE_PORT）"

#-------------------------------------------------------------------------------
sec "3. 接入并启动 pdnd"
expect_http 201 POST "$ADM/v1/servers/$SERVER_ID/bootstrap-token" -H "$AH" \
  -H 'Content-Type: application/json' -H "Idempotency-Key: kick-boot-$RUN" -d '{}'
# 响应体含一次性令牌：落 0600 文件，python 把令牌单独写进另一个 0600 文件；令牌不进 shell 变量、命令行与输出
(umask 077; printf '%s' "$HTTP_BODY" > "$TMP/boot.json")
HTTP_BODY=
python3 - "$TMP/boot.json" "$TMP/token" > "$TMP/boot-parse.txt" <<'PY' || { rm -f "$TMP/boot.json"; die "bootstrap-token response could not be parsed"; }
import json, os, re, sys
body_path, token_path = sys.argv[1], sys.argv[2]
with open(body_path, encoding="utf-8") as f:
    body = json.load(f)
token = body["token"]
cmd = body.get("install_command") or ""
m_panel = re.search(r"--panel\s+'([^']+)'", cmd)
m_ca = re.search(r"--ca-sha256\s+'([^']+)'", cmd)
if not token or not m_panel or not m_ca:
    raise SystemExit("install_command parse failed")
fd = os.open(token_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as f:
    f.write(token + "\n")
print(m_panel.group(1))
print(m_ca.group(1))
PY
rm -f "$TMP/boot.json"
PANEL_ADDR=$(sed -n '1p' "$TMP/boot-parse.txt")
CA_SHA=$(sed -n '2p' "$TMP/boot-parse.txt")
rm -f "$TMP/boot-parse.txt"
[ -n "$PANEL_ADDR" ] && [ -n "$CA_SHA" ] || die "install_command has no --panel / --ca-sha256"

PDIR="$TMP/pdnd"
mkdir -p "$PDIR/state"
chmod 700 "$PDIR" "$PDIR/state"
"$PANDORA_NATIVE" enroll --panel "$PANEL_ADDR" --ca-sha256 "$CA_SHA" --token-stdin \
  --identity-dir "$PDIR/identity" < "$TMP/token" > "$TMP/enroll.log" 2>&1
enroll_rc=$?
rm -f "$TMP/token"
[ "$enroll_rc" -eq 0 ] || { cat "$TMP/enroll.log" >&2; die "pdnd enroll 失败" "rc=$enroll_rc"; }
grep -q '^enrolled' "$TMP/enroll.log" || { cat "$TMP/enroll.log" >&2; die "pdnd enroll 没有输出 enrolled"; }
ok "pdnd enroll（--token-stdin，身份目录在临时目录）"

# 配置全部指向临时目录；私网目标放开：echo 服务在 127.0.0.1，缺省会被节点的私网防护拒掉
python3 - "$PDIR/config.json" "$PANEL_ADDR" "$PDIR/state" "$PDIR/identity" <<'PY' || die "write pdnd config"
import json, os, sys
out, addr, state, ident = sys.argv[1:5]
cfg = {
    "log_level": "info",
    "native_only": True,
    "cache_dir": state,
    "panel": {"address": addr, "identity_dir": ident},
    "runtime": {"allow_private_destinations": True},
}
fd = os.open(out, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as f:
    json.dump(cfg, f)
PY
"$PANDORA_NATIVE" -c "$PDIR/config.json" > "$TMP/pdnd.log" 2>&1 &
PDND_PID=$!
sleep 1
kill -0 "$PDND_PID" 2>/dev/null || die "pdnd 启动即退出"
ok "pdnd 已启动（pid $PDND_PID）"

#-------------------------------------------------------------------------------
sec "4. 节点上线、等监听、发布套餐"
admin_auth
expect_http 200 POST "$ADM/v1/nodes/$NODE_ID/activate" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: kick-act-$RUN" -d "{\"row_version\":$NODE_RV}"
[ "$(json_get "$HTTP_BODY" serving_status)" = active ] || die "activate serving_status"
ok "节点 activate"

deadline=$((SECONDS + READY_WAIT))
until python3 "$TMP/kick.py" tcp "$KICK_NODE_PORT"; do
  kill -0 "$PDND_PID" 2>/dev/null || die "pdnd 已退出"
  [ "$SECONDS" -lt "$deadline" ] || die "pdnd ${READY_WAIT}s 内没有监听 $KICK_NODE_PORT"
  sleep 1
done
ok "pdnd 监听 $KICK_NODE_PORT（配置已下发）"

admin_auth
expect_http 201 POST "$ADM/v1/plans/complete" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: kick-plan-$RUN" \
  -d "{\"code\":\"kick-$RUN\",\"name\":\"Kick $RUN\",\"visibility\":\"public\",\"traffic_gb\":10,\"pool_ids\":[\"$POOL_ID\"],\"prices\":[{\"billing_interval\":\"month\",\"interval_count\":1,\"unit_amount\":1000,\"currency\":\"CNY\",\"trial_days\":0}],\"publish\":true}"
PLAN=$(json_get "$HTTP_BODY" plan.id)
PRICE=$(json_get "$HTTP_BODY" price_ids.0)
is_uuid "$PLAN" && is_uuid "$PRICE" || die "plans/complete returned invalid ids"
ok "套餐 kick-$RUN 已发布"

#-------------------------------------------------------------------------------
sec "5. 用户 A、B 开通"
open_user() {
  local who=$1
  register_user "kick_${RUN}_${who}@example.com"
  local uid=$REG_USER_ID
  admin_auth
  expect_http 201 POST "$ADM/v1/orders/manual" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: kick-order-$RUN-$who" \
    -d "{\"user_id\":\"$uid\",\"plan_id\":\"$PLAN\",\"price_id\":\"$PRICE\",\"reason\":\"node kick e2e\",\"settlement\":\"grant\"}"
  local sub
  sub=$(db "SELECT id FROM subscriptions WHERE user_id='$uid' AND plan_id='$PLAN' AND status='active'")
  is_uuid "$sub" || die "用户 $who 没开出订阅"
  expect_http 200 GET "$PUB/v1/me/subscription-links" -H "$REG_UH"
  local url
  url=$(json_get "$HTTP_BODY" links.0.url) || die "用户 $who 没有订阅链接"
  printf -v "${who}_ID" '%s' "$uid"
  printf -v "${who}_UH" '%s' "$REG_UH"
  printf -v "${who}_SUB" '%s' "$sub"
  printf -v "${who}_UUID" '%s' "$(db "SELECT proxy_uuid FROM subscriptions WHERE id='$sub'")"
  printf -v "${who}_LINK" '%s' "$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$url")"
  ok "用户 $who 已开通"
}
open_user A
open_user B
is_uuid "$A_UUID" && is_uuid "$B_UUID" || die "proxy_uuid 缺失"

#-------------------------------------------------------------------------------
sec "6. sing-box 客户端"
if [ -z "$SINGBOX" ]; then
  curl -fsSL --retry 3 --max-time 300 -o "$TMP/$SB_ASSET" \
    "https://github.com/SagerNet/sing-box/releases/download/v${SB_VERSION}/${SB_ASSET}" \
    || die "下载 sing-box $SB_VERSION 失败"
  (cd "$TMP" && echo "$SB_SHA256  $SB_ASSET" | sha256sum -c -) || die "sing-box 包 SHA-256 不符"
  mkdir -p "$TMP/sb"
  tar -xzf "$TMP/$SB_ASSET" -C "$TMP/sb" --strip-components=1 || die "解包 sing-box 失败"
  rm -f "$TMP/$SB_ASSET"
  SINGBOX="$TMP/sb/sing-box"
  ok "sing-box $SB_VERSION 下载并核对 SHA-256"
fi
# glibc 包旁边带 libcronet.so（只有 naive 出站用到），把包目录放进库搜索路径，免得动态加载找不到
SB_LD="$(dirname "$SINGBOX")${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
LD_LIBRARY_PATH="$SB_LD" "$SINGBOX" version | head -n 1 || die "sing-box version 跑不起来"

A_PORT=$(free_port)
B_PORT=$(free_port)
build_client_config "$A_LINK" "$A_UUID" "$A_PORT" "$TMP/sb-a.json"
build_client_config "$B_LINK" "$B_UUID" "$B_PORT" "$TMP/sb-b.json"
ok "订阅里的出站口令 = proxy_uuid，端口 = $KICK_NODE_PORT"
start_singbox a "$TMP/sb-a.json" "$A_PORT"
start_singbox b "$TMP/sb-b.json" "$B_PORT"

#-------------------------------------------------------------------------------
sec "7. echo 服务与名单就绪"
python3 "$TMP/kick.py" echo "$TMP/echo.port" > "$TMP/echo.log" 2>&1 &
ECHO_PID=$!
deadline=$((SECONDS + 10))
until [ -s "$TMP/echo.port" ]; do
  [ "$SECONDS" -lt "$deadline" ] || die "echo 服务没起来"
  sleep 0.1
done
ECHO_PORT=$(cat "$TMP/echo.port")
ok "echo 服务 127.0.0.1:$ECHO_PORT"
# 名单到节点之前认证不过（节点静默排空），所以经客户端真连通即「名单已到」
probe_until_ok "$A_PORT" "A 经 pdnd 连通 echo（名单已到节点）"
probe_until_ok "$B_PORT" "B 经 pdnd 连通 echo"

#-------------------------------------------------------------------------------
sec "8. 长连接"
start_hold a "$A_PORT"
start_hold b "$B_PORT"
deadline=$((SECONDS + 10))
until grep -q '^up ' "$TMP/hold-a.status" && grep -q '^up ' "$TMP/hold-b.status"; do
  [ "$SECONDS" -lt "$deadline" ] || die "长连接没建立"
  sleep 0.1
done
sleep 2
! hold_broken a && ! hold_broken b || die "长连接在动手之前就断了"
ok "A、B 各一条长连接，每 200ms 往返一字节，稳定 2s"

#-------------------------------------------------------------------------------
sec "9. 后台停用 A"
admin_auth
T_START=$(now)
expect_http 200 POST "$ADM/v1/users/$A_ID/status" -H "$AH" -H 'Content-Type: application/json' \
  -d '{"status":"suspended","reason":"node kick e2e"}'
T0=$(now)
T1=$(wait_broken a $((KICK_MAX_SEC * 3 + 5))) || die "A 的长连接在 $((KICK_MAX_SEC * 3 + 5))s 内没断"
DT_A=$(judge_kick A "$T_START" "$T0" "$T1") || die "停用 A 后断线超时或时序不对" "t0=$T0 t1=$T1 limit=${KICK_MAX_SEC}s dt=${DT_A:-?}"
ok "停用 A 后 ${DT_A}s 断开（≤ ${KICK_MAX_SEC}s）"
probe "$A_PORT" && die "A 被停用后新连接仍然连通"
ok "A 的新连接失败"
hold_broken b && die "停用 A 时 B 的长连接也断了" "$(cat "$TMP/hold-b.status")"
grep -q '^up ' "$TMP/hold-b.status" || die "B 的长连接状态异常"
ok "B 的长连接未受影响"

#-------------------------------------------------------------------------------
sec "10. B 重置订阅链接"
# 先让 B 再稳定跑 1s，证明停用 A 之后的名单下发也没碰 B
sleep 1
hold_broken b && die "B 的长连接在重置前断了" "$(cat "$TMP/hold-b.status")"
T_START=$(now)
expect_http 200 POST "$PUB/v1/me/subscriptions/$B_SUB/rotate" -H "$B_UH"
T0=$(now)
NEW_URL=$(json_get "$HTTP_BODY" url) || die "rotate 没有回新链接"
T1=$(wait_broken b $((KICK_MAX_SEC * 3 + 5))) || die "B 的旧长连接在 $((KICK_MAX_SEC * 3 + 5))s 内没断"
DT_B=$(judge_kick B "$T_START" "$T0" "$T1") || die "重置链接后旧连接断线超时或时序不对" "t0=$T0 t1=$T1 limit=${KICK_MAX_SEC}s dt=${DT_B:-?}"
ok "重置链接后 B 的旧连接 ${DT_B}s 断开（≤ ${KICK_MAX_SEC}s）"
probe "$B_PORT" && die "旧凭据在重置后仍能新建连接"
ok "旧凭据的新连接失败"

B_NEW_UUID=$(db "SELECT proxy_uuid FROM subscriptions WHERE id='$B_SUB'")
is_uuid "$B_NEW_UUID" && [ "$B_NEW_UUID" != "$B_UUID" ] || die "重置后 proxy_uuid 没换" "$B_UUID -> $B_NEW_UUID"
B_NEW_LINK=$(python3 -c 'import sys, urllib.parse as u; print(u.urlsplit(sys.argv[1]).path)' "$NEW_URL")
B2_PORT=$(free_port)
build_client_config "$B_NEW_LINK" "$B_NEW_UUID" "$B2_PORT" "$TMP/sb-b2.json"
start_singbox b2 "$TMP/sb-b2.json" "$B2_PORT"
probe_until_ok "$B2_PORT" "新订阅（新 proxy_uuid）能连通"

#-------------------------------------------------------------------------------
sec "11. 收尾"
for p in "${HOLD_PIDS[@]}" "${SB_PIDS[@]}"; do stop_pid "$p"; done
HOLD_PIDS=()
SB_PIDS=()
kill -TERM "$PDND_PID" 2>/dev/null || die "pdnd 已不在"
deadline=$((SECONDS + 30))
while kill -0 "$PDND_PID" 2>/dev/null; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    kill -KILL "$PDND_PID" 2>/dev/null || true
    wait "$PDND_PID" 2>/dev/null || true
    PDND_PID=
    die "pdnd 收到 SIGTERM 后 30s 没退出"
  fi
  sleep 0.2
done
wait "$PDND_PID"
pdnd_rc=$?
PDND_PID=
[ "$pdnd_rc" -eq 0 ] || die "pdnd 退出码 $pdnd_rc"
ok "pdnd 收到 SIGTERM 正常退出"
if grep -q 'panic' "$TMP/pdnd.log"; then
  grep -n 'panic' "$TMP/pdnd.log" | head -n 20 >&2
  die "pdnd 日志里有 panic"
fi
ok "pdnd 日志无 panic"

echo
echo "断线耗时：停用用户 ${DT_A}s，重置链接 ${DT_B}s（阈值 ${KICK_MAX_SEC}s）"
echo "node kick e2e: $pass checks passed"
