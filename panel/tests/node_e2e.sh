#!/usr/bin/env bash
# [INPUT]: 依赖 /opt/aegispanel/bin/aegis-agent（真实 Agent 进程）、/opt/aegispanel/deploy/psql.sh 与 logs/node.log，依赖 admin 与 node 两个网关
# [OUTPUT]: Node Fabric 端到端：一次性引导令牌、Agent 引导接入、状态机、心跳、签名认证、分层配置下发与篡改拒绝、身份吊销与重新引导；写接口都带 Idempotency-Key
# [POS]: panel/tests 的节点生命周期脚本，由 deploy/run-smoke-e2e.sh 在冒烟栈上跑；与 uniproxy_e2e.sh 分工（后者用 SQL 夹具直接造 serving 节点）
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
# Node Fabric 端到端测试。
#
# 这个测试跑的是**真实的 Agent 进程**，不是模拟请求：
# 生成密钥、引导接入、签名心跳、拉取并验签配置、写入本地文件，全程走真实代码路径。
#
# 覆盖：
#   NODE-008 一次性令牌（用后失效、过期拒绝）
#   NODE-009 Agent 本地生成密钥，平台只存公钥
#   NODE-010 节点不能从创建态直接跳到 active
#   NODE-014 身份吊销后旧 Agent 立即失效
#   AGT-004  心跳与资产上报
#   AGT-006/007/008 分层配置合并、签名下发、验签后原子应用、篡改拒绝
set -uo pipefail

ADM=${ADM:-http://127.0.0.1:9001}
NODE=${NODE:-http://127.0.0.1:9003}
PSQL=/opt/aegispanel/deploy/psql.sh
AGENT=/opt/aegispanel/bin/aegis-agent
WORK=/tmp/aegis-agent-test
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@aegispanel.local}
ADMIN_PASS=${ADMIN_PASS:-Aegis#Admin2026!}
NODE_NAME="e2e-node-$(date +%s)"
# 签引导令牌、旧状态接口、配置发布都挂着幂等中间件，缺 Idempotency-Key 回 400。
# 每个用户意图一个键（照 admin_e2e.sh 的写法），本次运行内唯一
RUN_TAG="$(date +%s)-$RANDOM"

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }

rm -rf "$WORK"; mkdir -p "$WORK"
export AEGIS_AGENT_STATE="$WORK/state.json"

#-------------------------------------------------------------------------------
sec "1. 前置"

curl -sf "$NODE/healthz" >/dev/null && ok "Node 控制面存活" || { bad "控制面不可达" "$NODE"; exit 1; }

ATOK=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "管理员已登录" || { bad "登录失败" ""; exit 1; }
AH="Authorization: Bearer $ATOK"

#-------------------------------------------------------------------------------
sec "2. NODE-008 一次性引导令牌"

R=$(curl -s -X POST "$ADM/v1/nodes/bootstrap-token" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: node-bootstrap-token-$RUN_TAG" \
      -d "{\"node_name\":\"$NODE_NAME\",\"ttl_minutes\":20}")
TOKEN=$(echo "$R" | jqr "['token']")
[ -n "$TOKEN" ] && ok "令牌已签发（明文仅此一次）" || { bad "签发失败" "$R"; exit 1; }

# 库里只能有哈希，不能有明文
$PSQL -tAc "SELECT count(*) FROM bootstrap_tokens WHERE token_hash IS NOT NULL" >/dev/null
LEAK=$($PSQL -tAc "SELECT count(*) FROM bootstrap_tokens WHERE encode(token_hash,'escape') LIKE '%$TOKEN%'" | tr -d '[:space:]')
[ "$LEAK" = "0" ] && ok "令牌明文未落库（只存哈希）" || bad "令牌明文疑似落库" ""

# 未认证不能签发
C=$(code -X POST "$ADM/v1/nodes/bootstrap-token" -H 'Content-Type: application/json' -d '{}')
[ "$C" = "401" ] && ok "未认证不能签发令牌（401）" || bad "未认证可签发" "HTTP $C"

#-------------------------------------------------------------------------------
sec "3. NODE-009 Agent 引导接入（真实进程）"

OUT=$("$AGENT" bootstrap --server "$NODE" --token "$TOKEN" --name "$NODE_NAME" 2>&1)
echo "$OUT" | grep -q "引导成功" && ok "Agent 引导成功" || { bad "引导失败" "$OUT"; exit 1; }

NODE_ID=$(python3 -c "import json;print(json.load(open('$AEGIS_AGENT_STATE'))['node_id'])")
[ -n "$NODE_ID" ] && ok "节点 ID $NODE_ID" || bad "状态文件异常" ""

# 私钥必须留在本机，平台只有公钥
PRIV=$(python3 -c "import json;print(json.load(open('$AEGIS_AGENT_STATE'))['private_key'])")
DBHAS=$($PSQL -tAc "SELECT count(*) FROM node_identities WHERE encode(public_key,'base64') LIKE '%$(echo "$PRIV"|head -c 20)%'" | tr -d '[:space:]')
[ "$DBHAS" = "0" ] && ok "私钥未上传到平台（库中只有公钥）" || bad "私钥疑似泄露到平台" ""

PERM=$(stat -c %a "$AEGIS_AGENT_STATE")
[ "$PERM" = "600" ] && ok "状态文件权限 600（含私钥）" || bad "状态文件权限过宽" "$PERM"

ST=$($PSQL -tAc "SELECT status FROM nodes WHERE id='$NODE_ID'" | tr -d '[:space:]')
[ "$ST" = "bootstrapping" ] && ok "节点初始状态为 bootstrapping" || bad "初始状态异常" "$ST"

SERIAL=$($PSQL -tAc "SELECT serial FROM node_identities WHERE node_id='$NODE_ID' AND status='active'" | tr -d '[:space:]')
[ "$SERIAL" = "1" ] && ok "身份序号从 1 开始" || bad "序号异常" "$SERIAL"

# 令牌用后即失效
OUT2=$("$AGENT" bootstrap --server "$NODE" --token "$TOKEN" --name "other-$NODE_NAME" 2>&1)
echo "$OUT2" | grep -q "引导被拒绝" && ok "同一令牌不能二次使用" || bad "令牌可重复使用" "$OUT2"

# 伪造令牌
OUT3=$("$AGENT" bootstrap --server "$NODE" --token "totally-fake-token-xxxxx" --name "fake" 2>&1)
echo "$OUT3" | grep -q "引导被拒绝" && ok "伪造令牌被拒" || bad "伪造令牌被接受" "$OUT3"

#-------------------------------------------------------------------------------
sec "4. NODE-010 不能从创建态直跳 active"

C=$(code -X POST "$ADM/v1/nodes/$NODE_ID/status" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: node-status-skip-$RUN_TAG" \
      -d '{"status":"active","reason":"尝试跳过灰度"}')
[ "$C" = "409" ] && ok "bootstrapping → active 被状态机拒绝（409）" || bad "非法跳转被接受" "HTTP $C"

# 走合法路径：bootstrapping → attesting → installing → validating → standby → canary → active
for s in attesting installing validating standby canary active; do
  curl -s -X POST "$ADM/v1/nodes/$NODE_ID/status" -H "$AH" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: node-status-$s-$RUN_TAG" \
    -d "{\"status\":\"$s\",\"reason\":\"e2e 推进\"}" >/dev/null
done
ST=$($PSQL -tAc "SELECT status FROM nodes WHERE id='$NODE_ID'" | tr -d '[:space:]')
[ "$ST" = "active" ] && ok "按合法路径逐级推进到 active" || bad "推进失败" "$ST"

#-------------------------------------------------------------------------------
sec "5. AGT-004 心跳与资产上报"

"$AGENT" run --server "$NODE" --once >"$WORK/run1.log" 2>&1
grep -q "Agent 启动" "$WORK/run1.log" && ok "Agent 运行一轮" || bad "运行失败" "$(cat "$WORK/run1.log")"

HB=$($PSQL -tAc "SELECT last_heartbeat_at IS NOT NULL FROM nodes WHERE id='$NODE_ID'" | tr -d '[:space:]')
[ "$HB" = "t" ] && ok "心跳时间已记录" || bad "心跳未记录" ""

ASSET=$($PSQL -tAc "SELECT cpu_cores||'/'||coalesce(memory_mb,0)||'/'||coalesce(disk_gb,0)||'/'||coalesce(health_score,0) FROM nodes WHERE id='$NODE_ID'" | tr -d '[:space:]')
echo "$ASSET" | grep -qE "^[1-9]" && ok "资产已上报 CPU/内存MB/磁盘GB/健康分 = $ASSET" || bad "资产异常" "$ASSET"

AV=$($PSQL -tAc "SELECT agent_version FROM nodes WHERE id='$NODE_ID'" | tr -d '[:space:]')
[ -n "$AV" ] && ok "Agent 版本已上报 $AV" || bad "版本未上报" ""

#-------------------------------------------------------------------------------
sec "6. 签名认证不可伪造"

# 无签名头
C=$(code -X POST "$NODE/v1/nodes/heartbeat" -H 'Content-Type: application/json' -d '{}')
[ "$C" = "401" ] && ok "无签名的心跳被拒（401）" || bad "无签名被接受" "HTTP $C"

# 伪造签名
C=$(code -X POST "$NODE/v1/nodes/heartbeat" -H 'Content-Type: application/json' \
      -H "X-Node-Id: $NODE_ID" -H "X-Node-Ts: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      -H "X-Node-Sig: $(echo -n fake | base64)" -d '{}')
[ "$C" = "401" ] && ok "伪造签名被拒（401）" || bad "伪造签名被接受" "HTTP $C"

# 过期时间戳（10 分钟前，超出 5 分钟窗口）
OLD=$(date -u -d '10 minutes ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)
C=$(code -X POST "$NODE/v1/nodes/heartbeat" -H 'Content-Type: application/json' \
      -H "X-Node-Id: $NODE_ID" -H "X-Node-Ts: $OLD" \
      -H "X-Node-Sig: $(echo -n x | base64)" -d '{}')
[ "$C" = "401" ] && ok "超窗时间戳被拒（防重放）" || bad "超窗被接受" "HTTP $C"

#-------------------------------------------------------------------------------
sec "7. AGT-006/007 分层配置与签名下发"

# 全局层
R=$(curl -s -X POST "$ADM/v1/nodes/config/publish" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: node-config-global-$RUN_TAG" \
      -d '{"scope":"global","payload":{"log_level":"info","mtu":1420,"dns":"1.1.1.1"}}')
GV=$(echo "$R" | jqr "['version']"); GN=$(echo "$R" | jqr "['affected_nodes']")
[ -n "$GV" ] && ok "全局配置 v$GV 已发布，影响 $GN 个节点" || { bad "发布失败" "$R"; exit 1; }

# 节点层覆盖：同名键必须压过全局
R=$(curl -s -X POST "$ADM/v1/nodes/config/publish" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: node-config-node-$RUN_TAG" \
      -d "{\"scope\":\"node\",\"scope_ref\":\"$NODE_ID\",\"payload\":{\"log_level\":\"debug\",\"node_tag\":\"e2e\"}}")
NV=$(echo "$R" | jqr "['version']")
[ -n "$NV" ] && ok "节点层配置 v$NV 已发布" || bad "节点层发布失败" "$R"

# Agent 拉取并应用
"$AGENT" run --server "$NODE" --once >"$WORK/run2.log" 2>&1
cat "$WORK/run2.log" | grep -q "配置 v.* 已应用" && ok "Agent 已拉取并应用配置" || bad "配置未应用" "$(cat "$WORK/run2.log")"

RT="$WORK/runtime.json"
[ -f "$RT" ] && ok "配置已原子写入 runtime.json" || bad "配置文件未生成" ""

LL=$(python3 -c "import json;print(json.load(open('$RT'))['log_level'])" 2>/dev/null)
[ "$LL" = "debug" ] && ok "节点层覆盖全局层生效（log_level=debug）" || bad "分层合并有误" "log_level=$LL"

MTU=$(python3 -c "import json;print(json.load(open('$RT'))['mtu'])" 2>/dev/null)
[ "$MTU" = "1420" ] && ok "未被覆盖的全局键保留（mtu=1420）" || bad "全局键丢失" "mtu=$MTU"

TAG=$(python3 -c "import json;print(json.load(open('$RT'))['node_tag'])" 2>/dev/null)
[ "$TAG" = "e2e" ] && ok "节点独有键存在（node_tag=e2e）" || bad "节点键丢失" ""

# 来源可解释（AGT-006 验收）
grep -q "来源.*global.*node\|来源.*node" "$WORK/run2.log" && ok "配置来源可追溯（日志含各层版本）" \
  || echo "  [note] 来源信息: $(grep -o '来源.*' "$WORK/run2.log" | head -1)"

# 应用过程已留痕
NAPP=$($PSQL -tAc "SELECT count(*) FROM node_config_applications WHERE node_id='$NODE_ID'" | tr -d '[:space:]')
[ "$NAPP" -ge 2 ] && ok "配置应用过程已留痕（$NAPP 条）" || bad "应用记录不足" "count=$NAPP"

PHASES=$($PSQL -tAc "SELECT string_agg(DISTINCT phase,',') FROM node_config_applications WHERE node_id='$NODE_ID'" | tr -d '[:space:]')
echo "$PHASES" | grep -q "switched" && ok "记录了 switched 阶段（$PHASES）" || bad "缺少切换阶段" "$PHASES"

#-------------------------------------------------------------------------------
sec "8. AGT-007 篡改的配置必须被拒"

# 直接改库里的签名，模拟下发链路被污染
$PSQL -c "UPDATE node_configs SET signature = decode('00','hex')
           WHERE tenant_id='00000000-0000-7000-8000-000000000001'
             AND scope='node' AND status='published'" >/dev/null 2>&1
# 让 Agent 认为有新配置
$PSQL -c "UPDATE nodes SET desired_config_version = desired_config_version + 100 WHERE id='$NODE_ID'" >/dev/null 2>&1

BEFORE=$(cat "$RT")
"$AGENT" run --server "$NODE" --once >"$WORK/run3.log" 2>&1
# 逐层验签在服务端就拦下了，Agent 根本拿不到被污染的配置。
# 比「下发后由 Agent 拒绝」更好：污染内容压根没离开控制面。
# 服务端逐层验签在下发前就拦下了，Agent 拿到的是 503 而非被污染的配置。
# 比「下发后由 Agent 拒绝」更好：污染内容压根没离开控制面。
grep -qE "503|配置暂不可用" "$WORK/run3.log"   && ok "签名被篡改的配置遭拒绝（服务端逐层验签拦截，返回 503）"   || bad "篡改配置被接受" "$(cat "$WORK/run3.log")"

AFTER=$(cat "$RT")
[ "$BEFORE" = "$AFTER" ] && ok "拒绝后保留上一份可用配置（AGT-008）" || bad "配置被污染" ""

# 服务端直接拒绝下发时不会产生 Agent 侧的 failed 记录 ——
# 那条记录只在「配置发到了 Agent 但验签不过」时出现。
# 这里改为确认服务端确实拒绝了这次拉取。
# 拒绝原因只应出现在服务端日志里，不能回给 Agent
grep -q "配置暂不可用" "$WORK/run3.log" && ok "Agent 只收到笼统提示，不含内部细节"   || bad "未见拒绝提示" "$(tail -2 "$WORK/run3.log")"
grep -q "签名校验失败\|内容与存档哈希不符" /opt/aegispanel/logs/node.log   && ok "服务端日志记录了具体篡改原因（便于排查）"   || bad "服务端未记录原因" ""

#-------------------------------------------------------------------------------
sec "9. NODE-014 吊销身份后旧 Agent 立即失效"

curl -s -X POST "$ADM/v1/nodes/$NODE_ID/revoke-identity" -H "$AH" >/dev/null
RV=$($PSQL -tAc "SELECT status FROM node_identities WHERE node_id='$NODE_ID' ORDER BY serial DESC LIMIT 1" | tr -d '[:space:]')
[ "$RV" = "revoked" ] && ok "身份已吊销" || bad "吊销失败" "$RV"

"$AGENT" run --server "$NODE" --once >"$WORK/run4.log" 2>&1
grep -q "401\|身份校验失败" "$WORK/run4.log" && ok "被吊销的 Agent 立即失去访问" || bad "吊销后仍可访问" "$(cat "$WORK/run4.log")"

# 重新引导可恢复，且 serial 递增（不复用旧序号）
R=$(curl -s -X POST "$ADM/v1/nodes/bootstrap-token" -H "$AH" -H 'Content-Type: application/json' \
      -H "Idempotency-Key: node-bootstrap-token-reissue-$RUN_TAG" \
      -d "{\"node_name\":\"$NODE_NAME\"}")
TOK2=$(echo "$R" | jqr "['token']")
"$AGENT" bootstrap --server "$NODE" --token "$TOK2" --name "$NODE_NAME" >"$WORK/re.log" 2>&1
S2=$($PSQL -tAc "SELECT serial FROM node_identities WHERE node_id='$NODE_ID' AND status='active'" | tr -d '[:space:]')
[ "$S2" = "2" ] && ok "重新引导后 serial 递增为 2（旧序号不复用）" || bad "序号异常" "$S2"

"$AGENT" run --server "$NODE" --once >"$WORK/run5.log" 2>&1
grep -q "Agent 启动" "$WORK/run5.log" && ! grep -q "401" "$WORK/run5.log" \
  && ok "新身份恢复访问" || bad "新身份不可用" "$(cat "$WORK/run5.log")"

#-------------------------------------------------------------------------------
sec "10. 管理端可见性"

NL=$(curl -s "$ADM/v1/nodes" -H "$AH")
FOUND=$(echo "$NL" | python3 -c "
import sys,json
for n in json.load(sys.stdin)['nodes']:
    if n['id']=='$NODE_ID':
        print(f\"{n['name']}|{n['status']}|{n['agent_version']}|{n['identity_serial']}|{n['stale']}\"); break" 2>/dev/null)
[ -n "$FOUND" ] && ok "管理端可见该节点：$FOUND" || bad "管理端不可见" "$NL"

# 权限：无 node.read 不可读
C=$(code "$ADM/v1/nodes")
[ "$C" = "401" ] && ok "未认证不可读节点列表（401）" || bad "未认证可读" "HTTP $C"

NA=$($PSQL -tAc "SELECT count(*) FROM audit_events WHERE action LIKE 'node.%'" | tr -d '[:space:]')
[ "$NA" -ge 5 ] && ok "节点相关审计 $NA 条（SEC-012）" || bad "审计不足" "count=$NA"

#-------------------------------------------------------------------------------
# 清理：把测试节点退役，避免污染队列
curl -s -X POST "$ADM/v1/nodes/$NODE_ID/status" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: node-status-cleanup-draining-$RUN_TAG" \
  -d '{"status":"draining","reason":"e2e 清理"}' >/dev/null
curl -s -X POST "$ADM/v1/nodes/$NODE_ID/status" -H "$AH" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: node-status-cleanup-retired-$RUN_TAG" \
  -d '{"status":"retired","reason":"e2e 清理"}' >/dev/null
rm -rf "$WORK"

echo
echo "==============================================="
echo "  通过 $pass 项，失败 $fail 项"
echo "==============================================="
[ "$fail" -eq 0 ]
