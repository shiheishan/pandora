#!/usr/bin/env bash
# 工单端到端（support 模块）：
#   1. 门户建单、分类推导优先级、校验；后台队列与回复；状态流转（open → pending_user → pending_agent → resolved → 重开 → closed）。
#   2. OPS-001 核心：内部备注对用户不可见（详情、列表 message_count）；客服可见；库 CHECK 拒绝用户写 internal_note。
#   3. 越权隔离（他人工单 404）；SLA 首次响应超时升级（含 worker 可能抢先扫描的幂等）；未结工单上限 5；指派与审计。
#
# 共 9 段（1 账号 → 9 指派与审计）；路由为 $PUB/v1/support/* 与 $ADM/v1/tickets/*。
#
# 断言口径：
#   - 管理员在 users 表（00006_auth.sql），口令来自环境变量。
#   - 分类 6 个。
#   - 建单成功 HTTP 201；未知 JSON 字段（如 priority）httpx.BindJSON DisallowUnknownFields → 400（仍断言拒绝，见 panel/internal/pkg/httpx/bind.go）。
#   - 第 7 段：worker 周期 SLA 扫描可能先于 POST /v1/tickets/escalate 升级，第一次只断言工单终态 escalated 与优先级抬高，不断言 escalated≥1。
#   - 第 9 段审计只数本脚本主工单 TID 与 SLA 工单 T2（resource_id 过滤），阈值按实际动作条数。
#
# 仍用 SQL 的步骤及原因：
#   - 读 status / first_responded_at / resolved_at / 未结数：比多轮 HTTP 更快、与 service 落库语义一致。
#   - SLA 把 sla_first_response_due 拨到过去：无对外「改 SLA」接口，仅 e2e 夹具。
#   - INSERT 违反 ticket_messages_internal_only_from_staff：验证库约束，API 层测不到。
#   - 审计条数、用户 id：登录响应没有 user_id，按邮箱查 users。
#
# 会建用户与工单，只许一次性库：SUPPORT_E2E_DISPOSABLE=YES_DELETE_FIXTURES，库名带独立 test/e2e 段。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
json(){ curl -s -H 'Content-Type: application/json' "$@"; }

[ "${SUPPORT_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] SUPPORT_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$("$PSQL" -X -qAtc 'SELECT current_database()' | tr -d '[:space:]')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }

STAMP="$(date +%s)$RANDOM"
SECRET="内部备注禁止外泄标记$STAMP"
idem(){ printf 'Idempotency-Key: support-e2e-%s-%s' "$STAMP" "$1"; }

# register <email> <password>：两步注册，complete 须 201
register(){
  local email=$1 pw=$2 r rt cd c
  r=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"$email\"}")
  rt=$(echo "$r" | jqr "['registration_token']"); cd=$(echo "$r" | jqr "['dev_code']")
  c=$(code -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
    -d "{\"registration_token\":\"$rt\",\"code\":\"$cd\",\"password\":\"$pw\"}")
  [ "$c" = 201 ] || { bad "注册 complete 非 201" "HTTP $c"; return 1; }
  ok "注册 complete 201 ($email)"
}

#-------------------------------------------------------------------------------
sec "1. 准备账号"

UE="tk_${STAMP}@example.test"; UP="TkUser!2026abc"
register "$UE" "$UP" || exit 1
UTOK=$(json -X POST "$PUB/v1/auth/login" -d "{\"email\":\"$UE\",\"password\":\"$UP\"}" | jqr "['access_token']")
[ -n "$UTOK" ] && ok "用户已登录" || { bad "用户登录失败" ""; exit 1; }
UH="Authorization: Bearer $UTOK"
UID_=$("$PSQL" -X -qAtc "SELECT id FROM users WHERE email='$UE'" | tr -d '[:space:]')
[ -n "$UID_" ] && ok "已解析提单用户 id" || bad "查不到用户 id" ""

ATOK=$(json -X POST "$ADM/v1/auth/login" -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "客服已登录" || { bad "客服登录失败" ""; exit 1; }
AH="Authorization: Bearer $ATOK"

OE="tkother_${STAMP}@example.test"; OP="TkOther!2026ab"
register "$OE" "$OP" || true
OTOK=$(json -X POST "$PUB/v1/auth/login" -d "{\"email\":\"$OE\",\"password\":\"$OP\"}" | jqr "['access_token']")
[ -n "$OTOK" ] && ok "旁观用户已就绪" || bad "旁观用户准备失败" ""

#-------------------------------------------------------------------------------
sec "2. 用户建单"

CATS=$(curl -s "$PUB/v1/support/categories" -H "$UH" | python3 -c "
import sys,json;print(len(json.load(sys.stdin)['categories']))" 2>/dev/null)
[ "$CATS" -ge 6 ] 2>/dev/null && ok "分类列表可读（$CATS 个）" || bad "分类列表异常" "$CATS"

R=$(curl -s -w '\n%{http_code}' -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-main)" -H 'Content-Type: application/json' \
  -d '{"subject":"香港节点连接超时","category":"technical","body":"最近三天香港节点无法连接，其他地区正常，请协助排查。"}')
C=$(echo "$R" | tail -1); BODY=$(echo "$R" | sed '$d')
[ "$C" = 201 ] && ok "建单 HTTP 201" || { bad "建单非 201" "HTTP $C $BODY"; exit 1; }
TID=$(echo "$BODY" | jqr "['id']"); TNO=$(echo "$BODY" | jqr "['ticket_no']"); TPRIO=$(echo "$BODY" | jqr "['priority']")
[ -n "$TID" ] && ok "工单已创建 $TNO 优先级=$TPRIO" || { bad "建单响应缺 id" "$BODY"; exit 1; }

# priority 不在 createRequest DTO；BindJSON DisallowUnknownFields → 400
C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-priority-invalid)" -H 'Content-Type: application/json' \
  -d '{"subject":"尝试自定优先级的工单","category":"general","body":"验证接口是否接受用户指定的优先级字段。","priority":"urgent"}')
[ "$C" = 400 ] && ok "接口拒绝用户自定 priority 字段（400）" || bad "priority 字段被接受" "HTTP $C"

R2=$(curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-billing)" -H 'Content-Type: application/json' \
  -d '{"subject":"账单有疑问需要核对","category":"billing","body":"上月扣费与我预期不符，麻烦帮忙核对一下明细。"}')
P2=$(echo "$R2" | jqr "['priority']")
[ "$P2" = "high" ] && ok "billing 分类自动置为 high（priorityFor）" || bad "优先级推导异常" "$R2"

C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-short)" -H 'Content-Type: application/json' \
  -d '{"subject":"短","category":"technical","body":"太短"}')
[ "$C" = 422 ] && ok "过短的标题与内容被拒（422）" || bad "校验未生效" "HTTP $C"

C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-category-invalid)" -H 'Content-Type: application/json' \
  -d '{"subject":"分类不存在的工单标题","category":"nonexistent","body":"这里的内容长度是足够的，超过十个字。"}')
[ "$C" = 422 ] && ok "非法分类被拒（422）" || bad "分类校验未生效" "HTTP $C"

#-------------------------------------------------------------------------------
sec "3. 客服处理"

Q=$(curl -s "$ADM/v1/tickets?q=$TNO" -H "$AH")
FOUND=$(echo "$Q" | jqr "['tickets'][0]['ticket_no']")
UEMAIL=$(echo "$Q" | jqr "['tickets'][0]['user_email']")
[ "$FOUND" = "$TNO" ] && ok "客服队列可检索到该工单，提单人 $UEMAIL" || bad "队列检索失败" "$Q"

R=$(curl -s -X POST "$ADM/v1/tickets/$TID/reply" -H "$AH" -H "$(idem agent-internal-note)" -H 'Content-Type: application/json' \
  -d "{\"body\":\"$SECRET\",\"internal_note\":true}")
[ "$(echo "$R" | jqr "['ok']")" = "True" ] && ok "客服已写入内部备注（internal_note）" || bad "写内部备注失败" "$R"

ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
FR=$("$PSQL" -X -qAtc "SELECT coalesce(first_responded_at::text,'null') FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "open" ] && ok "内部备注未改变工单状态（仍为 open）" || bad "内部备注改了状态" "$ST"
[ "$FR" = "null" ] && ok "内部备注不计入首次响应" || bad "内部备注被计为首次响应" "$FR"

curl -s -X POST "$ADM/v1/tickets/$TID/reply" -H "$AH" -H "$(idem agent-public-reply)" -H 'Content-Type: application/json' \
  -d '{"body":"您好，已收到反馈，正在排查香港节点，请稍候。","internal_note":false}' >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
FR=$("$PSQL" -X -qAtc "SELECT coalesce(first_responded_at::text,'null') FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "pending_user" ] && ok "对外回复后状态转为 pending_user" || bad "状态未流转" "$ST"
[ "$FR" != "null" ] && ok "首次响应时间已记录" || bad "首次响应未记录" ""

#-------------------------------------------------------------------------------
sec "4. OPS-001 内部备注对用户不可见（核心）"

DETAIL=$(curl -s "$PUB/v1/support/tickets/$TID" -H "$UH")
echo "$DETAIL" | grep -qF "$SECRET" \
  && bad "内部备注泄露给了用户！" "$DETAIL" \
  || ok "用户详情接口不含内部备注内容"

echo "$DETAIL" | grep -q '"internal_note":true' \
  && bad "用户看到了 internal_note=true 的消息" "" \
  || ok "用户消息列表中没有任何内部备注标记"

UCOUNT=$(curl -s "$PUB/v1/support/tickets" -H "$UH" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['tickets']:
    if t['ticket_no']=='$TNO': print(t['message_count']); break" 2>/dev/null)
DBALL=$("$PSQL" -X -qAtc "SELECT count(*) FROM ticket_messages WHERE ticket_id='$TID'" | tr -d '[:space:]')
DBPUB=$("$PSQL" -X -qAtc "SELECT count(*) FROM ticket_messages WHERE ticket_id='$TID' AND internal_note=false" | tr -d '[:space:]')
[ "$UCOUNT" = "$DBPUB" ] && [ "$DBALL" -gt "$DBPUB" ] \
  && ok "用户侧 message_count=$UCOUNT，库内公开 $DBPUB / 全部 $DBALL" \
  || bad "消息计数不符" "用户看到 $UCOUNT，公开 $DBPUB，全部 $DBALL"

ADETAIL=$(curl -s "$ADM/v1/tickets/$TID" -H "$AH")
echo "$ADETAIL" | grep -qF "$SECRET" && ok "客服详情接口可见内部备注" || bad "客服看不到内部备注" ""

CHK=$("$PSQL" -X -qAtc "INSERT INTO ticket_messages (ticket_id, author_id, author_kind, body, internal_note)
  VALUES ('$TID', '$UID_', 'user', '偷写', true)" 2>&1 | grep -c ticket_messages_internal_only_from_staff || true)
[ "${CHK:-0}" -ge 1 ] && ok "数据库 CHECK 拒绝用户身份写 internal_note" || bad "数据库层未拦截" "$CHK"

#-------------------------------------------------------------------------------
sec "5. 越权隔离"

C=$(code "$PUB/v1/support/tickets/$TID" -H "Authorization: Bearer $OTOK")
[ "$C" = 404 ] && ok "他人工单返回 404" || bad "越权可读他人工单" "HTTP $C"

C=$(code -X POST "$PUB/v1/support/tickets/$TID/reply" -H "Authorization: Bearer $OTOK" \
  -H "$(idem outsider-reply)" -H 'Content-Type: application/json' -d '{"body":"插话"}')
[ "$C" = 404 ] && ok "他人工单不可回复（404）" || bad "越权可回复他人工单" "HTTP $C"

C=$(code "$PUB/v1/support/tickets/$TID")
[ "$C" = 401 ] && ok "未认证访问工单返回 401" || bad "未认证可访问" "HTTP $C"

C=$(code "$ADM/v1/tickets" -H "$UH")
[ "$C" = 401 ] || [ "$C" = 403 ] && ok "用户令牌访问管理端工单被拒（$C）" \
  || bad "用户可访问管理端工单" "HTTP $C"

#-------------------------------------------------------------------------------
sec "6. 用户回复与状态流转"

curl -s -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-observe)" -H 'Content-Type: application/json' \
  -d '{"body":"好的，我这边再观察一下，附上测速截图说明。"}' >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "pending_agent" ] && ok "用户回复后 pending_agent" || bad "状态未回转" "$ST"

curl -s -X POST "$ADM/v1/tickets/$TID/status" -H "$AH" -H "$(idem status-resolved)" -H 'Content-Type: application/json' \
  -d '{"status":"resolved","reason":"香港节点已恢复"}' >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
RS=$("$PSQL" -X -qAtc "SELECT resolved_at IS NOT NULL FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "resolved" ] && [ "$RS" = "t" ] && ok "客服标记 resolved，resolved_at 已记录" || bad "解决状态异常" "$ST/$RS"

curl -s -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-reopen)" -H 'Content-Type: application/json' \
  -d '{"body":"今天又出现了同样的问题，麻烦再看一下。"}' >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
RS=$("$PSQL" -X -qAtc "SELECT resolved_at IS NULL FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "pending_agent" ] && [ "$RS" = "t" ] && ok "追问后自动重开，resolved_at 清空" || bad "重开异常" "$ST/$RS"

curl -s -X POST "$PUB/v1/support/tickets/$TID/close" -H "$UH" -H "$(idem user-close)" >/dev/null
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "closed" ] && ok "用户可自行关闭工单" || bad "关单失败" "$ST"

C=$(code -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-after-close)" -H 'Content-Type: application/json' -d '{"body":"再补充一句"}')
[ "$C" = 409 ] && ok "已关闭工单不可再回复（409）" || bad "已关闭仍可回复" "HTTP $C"

#-------------------------------------------------------------------------------
sec "7. SLA 超时自动升级"

T2=$(curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-sla)" -H 'Content-Type: application/json' \
  -d '{"subject":"这是一个用于验证SLA升级的工单","category":"general","body":"用于验证首次响应超时后能否自动升级并提高优先级。"}' | jqr "['id']")
[ -n "$T2" ] && ok "已创建待超时工单" || bad "创建失败" ""

"$PSQL" -X -qAtc "UPDATE tickets SET sla_first_response_due = now() - interval '1 hour' WHERE id='$T2'" >/dev/null
PRIO_BEFORE=$("$PSQL" -X -qAtc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')

R=$(curl -s -X POST "$ADM/v1/tickets/escalate" -H "$AH" -H "$(idem escalate-first)")
ST=$("$PSQL" -X -qAtc "SELECT status FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
PRIO_AFTER=$("$PSQL" -X -qAtc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$ST" = "escalated" ] && ok "超时工单终态 escalated（worker 可能已抢先升级）" || bad "未升级" "$ST"
[ "$PRIO_BEFORE" = "normal" ] && [ "$PRIO_AFTER" = "high" ] \
  && ok "优先级已提升 $PRIO_BEFORE → $PRIO_AFTER（repo EscalateOverdue）" || bad "优先级未提升" "$PRIO_BEFORE → $PRIO_AFTER"

ESC1=$("$PSQL" -X -qAtc "SELECT coalesce(escalated_at::text,'') FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
R=$(curl -s -X POST "$ADM/v1/tickets/escalate" -H "$AH" -H "$(idem escalate-second)")
N2=$(echo "$R" | jqr "['escalated']")
ESC2=$("$PSQL" -X -qAtc "SELECT coalesce(escalated_at::text,'') FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
PRIO2=$("$PSQL" -X -qAtc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$N2" = "0" ] && ok "重复 escalate 返回 escalated=0（幂等）" || bad "重复升级 escalated=$N2" ""
[ "$ESC1" = "$ESC2" ] && [ -n "$ESC1" ] && ok "escalated_at 未被覆盖" || bad "escalated_at 变化" "$ESC1 → $ESC2"
[ "$PRIO2" = "high" ] && ok "优先级未被二次抬高" || bad "优先级重复提升" "$PRIO2"

FIRST=$(curl -s "$ADM/v1/tickets?limit=5" -H "$AH" | jqr "['tickets'][0]['status']")
[ "$FIRST" = "escalated" ] && ok "队列把升级工单排在最前" || echo "  [note] 队首状态为 $FIRST"

NB=$(curl -s "$ADM/v1/tickets?breached=1" -H "$AH" | jqr "['total']")
[ -n "$NB" ] && ok "breached=1 过滤器可用（total=$NB）" || bad "过滤器异常" ""

#-------------------------------------------------------------------------------
sec "8. 未结工单上限（MaxOpenTickets=5）"

for i in 1 2 3; do
  curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-limit-$i)" -H 'Content-Type: application/json' \
    -d "{\"subject\":\"批量测试工单编号 $i 号\",\"category\":\"general\",\"body\":\"用于验证未结工单数量上限的测试工单内容。\"}" >/dev/null
done
OPENN=$("$PSQL" -X -qAtc "SELECT count(*) FROM tickets WHERE user_id='$UID_' AND status NOT IN ('resolved','closed')" | tr -d '[:space:]')
C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-over-limit)" -H 'Content-Type: application/json' \
  -d '{"subject":"这一单应当被上限拦下来","category":"general","body":"未结工单达到上限后不应再允许创建新的工单。"}')
[ "$C" = 409 ] && ok "未结达 5 后拒绝新建（当前未结 $OPENN，409）" || bad "上限未生效" "HTTP $C，未结 $OPENN"

#-------------------------------------------------------------------------------
sec "9. 指派与审计"

OUID=$("$PSQL" -X -qAtc "SELECT id FROM users WHERE email='$OE'" | tr -d '[:space:]')
C=$(code -X POST "$ADM/v1/tickets/$T2/assign" -H "$AH" -H "$(idem assign-invalid)" -H 'Content-Type: application/json' \
  -d "{\"assigned_to\":\"$OUID\"}")
[ "$C" = 422 ] && ok "不能指派给非管理员（422）" || bad "指派校验未生效" "HTTP $C"

AUID=$(curl -s "$ADM/v1/me" -H "$AH" | jqr "['user_id']")
[ -z "$AUID" ] && AUID=$("$PSQL" -X -qAtc "SELECT id FROM users WHERE email='$ADMIN_EMAIL'" | tr -d '[:space:]')
curl -s -X POST "$ADM/v1/tickets/$T2/assign" -H "$AH" -H "$(idem assign-admin)" -H 'Content-Type: application/json' \
  -d "{\"assigned_to\":\"$AUID\"}" >/dev/null
ASG=$("$PSQL" -X -qAtc "SELECT assigned_to FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$ASG" = "$AUID" ] && ok "已指派给管理员" || bad "指派失败" "$ASG"

# TID：create, internal_note, reply×3, status_change, close；T2：create, sla_escalate, assign → 至少 10
NA=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events WHERE action IN (
  'ticket.create','ticket.assign','ticket.status_change','ticket.close',
  'ticket.reply','ticket.internal_note','ticket.sla_escalate'
) AND resource_id IN ('$TID','$T2')" | tr -d '[:space:]')
[ "${NA:-0}" -ge 10 ] && ok "本脚本 TID/T2 审计 $NA 条" || bad "审计不足" "count=$NA（期望≥10）"

#-------------------------------------------------------------------------------
echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" -eq 0 ]
