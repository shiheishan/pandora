#!/usr/bin/env bash
# 工单端到端测试（OPS-001）。
#
# 最要紧的一条：内部备注绝不能被用户看到。
# 数据库的 CHECK 只保证「用户写不了内部备注」，拦不住「用户读到客服写的」——
# 那是查询层的责任，所以这里从用户视角逐字节确认备注内容不出现在响应里。
#
# 其余覆盖：SLA 超时自动升级且幂等、越权隔离、状态流转、未结工单上限。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=/opt/aegispanel/deploy/psql.sh
ADMIN_EMAIL=${ADMIN_EMAIL:-admin@aegispanel.local}
ADMIN_PASS=${ADMIN_PASS:-Aegis#Admin2026!}
SECRET="内部备注禁止外泄标记$RANDOM"
RUN_ID="$(date +%s)-$$-$RANDOM"
idem(){ printf 'Idempotency-Key: support-e2e-%s-%s' "$RUN_ID" "$1"; }

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }

#-------------------------------------------------------------------------------
sec "1. 准备账号"

UE="tk_$(date +%s)_$RANDOM@example.com"
R=$(curl -s -X POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$UE\"}")
RT=$(echo "$R" | jqr "['registration_token']"); CD=$(echo "$R" | jqr "['dev_code']")
curl -s -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CD\",\"password\":\"TkUser!2026abc\"}" >/dev/null
UTOK=$(curl -s -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$UE\",\"password\":\"TkUser!2026abc\"}" | jqr "['access_token']")
[ -n "$UTOK" ] && ok "用户已就绪 $UE" || { bad "用户准备失败" ""; exit 1; }
UH="Authorization: Bearer $UTOK"

ATOK=$(curl -s -X POST "$ADM/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASS\"}" | jqr "['access_token']")
[ -n "$ATOK" ] && ok "客服已登录" || { bad "客服登录失败" ""; exit 1; }
AH="Authorization: Bearer $ATOK"

# 另造一个用户，用于验证越权隔离
OE="tkother_$(date +%s)_$RANDOM@example.com"
R=$(curl -s -X POST "$PUB/v1/auth/register/start" -H 'Content-Type: application/json' -d "{\"email\":\"$OE\"}")
RT=$(echo "$R" | jqr "['registration_token']"); CD=$(echo "$R" | jqr "['dev_code']")
curl -s -X POST "$PUB/v1/auth/register/complete" -H 'Content-Type: application/json' \
  -d "{\"registration_token\":\"$RT\",\"code\":\"$CD\",\"password\":\"TkOther!2026ab\"}" >/dev/null
OTOK=$(curl -s -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$OE\",\"password\":\"TkOther!2026ab\"}" | jqr "['access_token']")
[ -n "$OTOK" ] && ok "旁观用户已就绪" || bad "旁观用户准备失败" ""

#-------------------------------------------------------------------------------
sec "2. 用户建单"

CATS=$(curl -s "$PUB/v1/support/categories" -H "$UH" | python3 -c "
import sys,json;print(len(json.load(sys.stdin)['categories']))" 2>/dev/null)
[ "$CATS" -ge 5 ] 2>/dev/null && ok "分类列表可读（$CATS 个）" || bad "分类列表异常" "$CATS"

R=$(curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-main)" -H 'Content-Type: application/json' \
      -d '{"subject":"香港节点连接超时","category":"technical","body":"最近三天香港节点无法连接，其他地区正常，请协助排查。"}')
TID=$(echo "$R" | jqr "['id']"); TNO=$(echo "$R" | jqr "['ticket_no']")
TPRIO=$(echo "$R" | jqr "['priority']")
[ -n "$TID" ] && ok "工单已创建 $TNO 优先级=$TPRIO" || { bad "建单失败" "$R"; exit 1; }

# 优先级由分类推导，接口根本不接受 priority 字段。
# DecodeJSON 启用了 DisallowUnknownFields，传了会直接 400 ——
# 比静默忽略更好：调用方能立刻发现这个字段不受支持。
C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-priority-invalid)" -H 'Content-Type: application/json'       -d '{"subject":"尝试自定优先级的工单","category":"general","body":"验证接口是否接受用户指定的优先级字段。","priority":"urgent"}')
[ "$C" = "400" ] && ok "接口拒绝用户自定优先级字段（400）" || bad "priority 字段被接受了" "HTTP $C"

# billing 属于资金问题，分类自动按 high 处理
R2=$(curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-billing)" -H 'Content-Type: application/json'       -d '{"subject":"账单有疑问需要核对","category":"billing","body":"上月扣费与我预期不符，麻烦帮忙核对一下明细。"}')
P2=$(echo "$R2" | jqr "['priority']")
[ "$P2" = "high" ] && ok "billing 分类自动置为 high" || bad "优先级推导异常" "$R2"

# 校验
C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-short)" -H 'Content-Type: application/json' \
      -d '{"subject":"短","category":"technical","body":"太短"}')
[ "$C" = "422" ] && ok "过短的标题与内容被拒（422）" || bad "校验未生效" "HTTP $C"

C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-category-invalid)" -H 'Content-Type: application/json' \
      -d '{"subject":"分类不存在的工单标题","category":"nonexistent","body":"这里的内容长度是足够的，超过十个字。"}')
[ "$C" = "422" ] && ok "非法分类被拒（422）" || bad "分类校验未生效" "HTTP $C"

#-------------------------------------------------------------------------------
sec "3. 客服处理"

Q=$(curl -s "$ADM/v1/tickets?q=$TNO" -H "$AH")
FOUND=$(echo "$Q" | jqr "['tickets'][0]['ticket_no']")
UEMAIL=$(echo "$Q" | jqr "['tickets'][0]['user_email']")
[ "$FOUND" = "$TNO" ] && ok "客服队列可检索到该工单，提单人 $UEMAIL" || bad "队列检索失败" "$Q"

# 内部备注
R=$(curl -s -X POST "$ADM/v1/tickets/$TID/reply" -H "$AH" -H "$(idem agent-internal-note)" -H 'Content-Type: application/json' \
      -d "{\"body\":\"$SECRET\",\"internal_note\":true}")
[ "$(echo "$R" | jqr "['ok']")" = "True" ] && ok "客服已写入内部备注" || bad "写内部备注失败" "$R"

# 内部备注不应改变工单状态，也不该算作首次响应
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
FR=$($PSQL -tAc "SELECT coalesce(first_responded_at::text,'null') FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "open" ] && ok "内部备注未改变工单状态（仍为 open）" || bad "内部备注改了状态" "$ST"
[ "$FR" = "null" ] && ok "内部备注不计入首次响应（SLA 计时未停）" || bad "内部备注被计为首次响应" "$FR"

# 对外回复
curl -s -X POST "$ADM/v1/tickets/$TID/reply" -H "$AH" -H "$(idem agent-public-reply)" -H 'Content-Type: application/json' \
  -d '{"body":"您好，已收到反馈，正在排查香港节点，请稍候。","internal_note":false}' >/dev/null
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
FR=$($PSQL -tAc "SELECT coalesce(first_responded_at::text,'null') FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
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

# 列表接口也不能通过消息计数暴露内部备注的存在
UCOUNT=$(curl -s "$PUB/v1/support/tickets" -H "$UH" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['tickets']:
    if t['ticket_no']=='$TNO': print(t['message_count']); break" 2>/dev/null)
DBALL=$($PSQL -tAc "SELECT count(*) FROM ticket_messages WHERE ticket_id='$TID'" | tr -d '[:space:]')
DBPUB=$($PSQL -tAc "SELECT count(*) FROM ticket_messages WHERE ticket_id='$TID' AND internal_note=false" | tr -d '[:space:]')
[ "$UCOUNT" = "$DBPUB" ] && [ "$DBALL" -gt "$DBPUB" ] \
  && ok "用户侧消息数=$UCOUNT，实际含内部备注共 $DBALL 条（计数未泄露）" \
  || bad "消息计数不符" "用户看到 $UCOUNT，公开 $DBPUB，全部 $DBALL"

# 客服视角必须能看到备注，否则功能本身没意义
ADETAIL=$(curl -s "$ADM/v1/tickets/$TID" -H "$AH")
echo "$ADETAIL" | grep -qF "$SECRET" && ok "客服详情接口可见内部备注" || bad "客服看不到内部备注" ""

# 数据库层：用户身份写内部备注必须被 CHECK 拒绝
C=$($PSQL -tAc "INSERT INTO ticket_messages (tenant_id,ticket_id,author_kind,body,internal_note)
     VALUES ('00000000-0000-7000-8000-000000000001','$TID','user','偷写',true)" 2>&1 | grep -c "ticket_messages_internal_only_from_staff")
[ "$C" -ge 1 ] && ok "数据库 CHECK 拒绝用户身份写内部备注" || bad "数据库层未拦截" ""

#-------------------------------------------------------------------------------
sec "5. 越权隔离"

C=$(code "$PUB/v1/support/tickets/$TID" -H "Authorization: Bearer $OTOK")
[ "$C" = "404" ] && ok "他人工单返回 404（与不存在同一响应）" || bad "越权可读他人工单" "HTTP $C"

C=$(code -X POST "$PUB/v1/support/tickets/$TID/reply" -H "Authorization: Bearer $OTOK" \
      -H "$(idem outsider-reply)" -H 'Content-Type: application/json' -d '{"body":"插话"}')
[ "$C" = "404" ] && ok "他人工单不可回复（404）" || bad "越权可回复他人工单" "HTTP $C"

C=$(code "$PUB/v1/support/tickets/$TID")
[ "$C" = "401" ] && ok "未认证访问工单返回 401" || bad "未认证可访问" "HTTP $C"

# 用户不能碰管理端工单接口
C=$(code "$ADM/v1/tickets" -H "$UH")
[ "$C" = "401" ] || [ "$C" = "403" ] && ok "用户令牌访问管理端工单被拒（$C）" \
                                     || bad "用户可访问管理端工单" "HTTP $C"

#-------------------------------------------------------------------------------
sec "6. 用户回复与状态流转"

curl -s -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-observe)" -H 'Content-Type: application/json' \
  -d '{"body":"好的，我这边再观察一下，附上测速截图说明。"}' >/dev/null
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "pending_agent" ] && ok "用户回复后球回到客服（pending_agent）" || bad "状态未回转" "$ST"

curl -s -X POST "$ADM/v1/tickets/$TID/status" -H "$AH" -H "$(idem status-resolved)" -H 'Content-Type: application/json' \
  -d '{"status":"resolved","reason":"香港节点已恢复"}' >/dev/null
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
RS=$($PSQL -tAc "SELECT resolved_at IS NOT NULL FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "resolved" ] && [ "$RS" = "t" ] && ok "客服标记为已解决，解决时间已记录" || bad "解决状态异常" "$ST/$RS"

# 已解决的工单被追问应自动重开
curl -s -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-reopen)" -H 'Content-Type: application/json' \
  -d '{"body":"今天又出现了同样的问题，麻烦再看一下。"}' >/dev/null
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
RS=$($PSQL -tAc "SELECT resolved_at IS NULL FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "pending_agent" ] && [ "$RS" = "t" ] && ok "追问后自动重开，解决时间已清空" || bad "重开异常" "$ST/$RS"

# 用户关单
curl -s -X POST "$PUB/v1/support/tickets/$TID/close" -H "$UH" -H "$(idem user-close)" >/dev/null
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$TID'" | tr -d '[:space:]')
[ "$ST" = "closed" ] && ok "用户可自行关闭工单" || bad "关单失败" "$ST"

C=$(code -X POST "$PUB/v1/support/tickets/$TID/reply" -H "$UH" -H "$(idem user-reply-after-close)" -H 'Content-Type: application/json' -d '{"body":"再补充一句"}')
[ "$C" = "409" ] && ok "已关闭工单不可再回复（409）" || bad "已关闭仍可回复" "HTTP $C"

#-------------------------------------------------------------------------------
sec "7. SLA 超时自动升级"

# 造一个已超时的工单：把截止时间人为拨到过去
T2=$(curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-sla)" -H 'Content-Type: application/json' \
      -d '{"subject":"这是一个用于验证SLA升级的工单","category":"general","body":"用于验证首次响应超时后能否自动升级并提高优先级。"}' | jqr "['id']")
[ -n "$T2" ] && ok "已创建待超时工单" || bad "创建失败" ""

$PSQL -c "UPDATE tickets SET sla_first_response_due = now() - interval '1 hour' WHERE id='$T2'" >/dev/null 2>&1
PRIO_BEFORE=$($PSQL -tAc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')

R=$(curl -s -X POST "$ADM/v1/tickets/escalate" -H "$AH" -H "$(idem escalate-first)")
N1=$(echo "$R" | jqr "['escalated']")
ST=$($PSQL -tAc "SELECT status FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
PRIO_AFTER=$($PSQL -tAc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$ST" = "escalated" ] && ok "超时工单已升级为 escalated（本次升级 $N1 个）" || bad "未升级" "$ST"
[ "$PRIO_BEFORE" = "normal" ] && [ "$PRIO_AFTER" = "high" ] \
  && ok "优先级已提升 $PRIO_BEFORE → $PRIO_AFTER" || bad "优先级未提升" "$PRIO_BEFORE → $PRIO_AFTER"

# XBD-024：定时任务必须幂等，重复运行不能重复升级
ESC1=$($PSQL -tAc "SELECT escalated_at FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
R=$(curl -s -X POST "$ADM/v1/tickets/escalate" -H "$AH" -H "$(idem escalate-second)")
N2=$(echo "$R" | jqr "['escalated']")
ESC2=$($PSQL -tAc "SELECT escalated_at FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
PRIO2=$($PSQL -tAc "SELECT priority FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$N2" = "0" ] && ok "重复扫描不再升级（幂等，第二次 escalated=0）" || bad "重复升级了 $N2 个" ""
[ "$ESC1" = "$ESC2" ] && ok "升级时间未被覆盖" || bad "escalated_at 被改写" "$ESC1 → $ESC2"
[ "$PRIO2" = "high" ] && ok "优先级未被二次抬高" || bad "优先级重复提升到 $PRIO2" ""

# 升级后队列排序应把它排在前面
FIRST=$(curl -s "$ADM/v1/tickets?limit=5" -H "$AH" | jqr "['tickets'][0]['status']")
[ "$FIRST" = "escalated" ] && ok "队列把升级工单排在最前" || echo "  [note] 队首状态为 $FIRST"

# 只看超时的过滤器
NB=$(curl -s "$ADM/v1/tickets?breached=1" -H "$AH" | jqr "['total']")
[ -n "$NB" ] && ok "SLA 超时过滤器可用（当前 $NB 个未响应超时）" || bad "过滤器异常" ""

#-------------------------------------------------------------------------------
sec "8. 未结工单上限"

# 已有 2 个未结（第 2、第 7 步各一个，第一个已关闭）+ 再建 3 个 = 5
for i in 1 2 3; do
  curl -s -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-limit-$i)" -H 'Content-Type: application/json' \
    -d "{\"subject\":\"批量测试工单编号 $i 号\",\"category\":\"general\",\"body\":\"用于验证未结工单数量上限的测试工单内容。\"}" >/dev/null
done
OPENN=$($PSQL -tAc "SELECT count(*) FROM tickets WHERE user_id=(SELECT id FROM users WHERE email='$UE')
                     AND status NOT IN ('resolved','closed')" | tr -d '[:space:]')
C=$(code -X POST "$PUB/v1/support/tickets" -H "$UH" -H "$(idem create-over-limit)" -H 'Content-Type: application/json' \
      -d '{"subject":"这一单应当被上限拦下来","category":"general","body":"未结工单达到上限后不应再允许创建新的工单。"}')
[ "$C" = "409" ] && ok "未结工单达上限后拒绝新建（当前 $OPENN 个，409）" || bad "上限未生效" "HTTP $C，未结 $OPENN"

#-------------------------------------------------------------------------------
sec "9. 指派与审计"

# 指派给没有工单权限的普通用户必须被拒
OUID=$($PSQL -tAc "SELECT id FROM users WHERE email='$OE'" | tr -d '[:space:]')
C=$(code -X POST "$ADM/v1/tickets/$T2/assign" -H "$AH" -H "$(idem assign-invalid)" -H 'Content-Type: application/json' \
      -d "{\"assigned_to\":\"$OUID\"}")
[ "$C" = "422" ] && ok "不能指派给无工单权限的人（422）" || bad "指派校验未生效" "HTTP $C"

AUID=$($PSQL -tAc "SELECT id FROM users WHERE email='$ADMIN_EMAIL'" | tr -d '[:space:]')
curl -s -X POST "$ADM/v1/tickets/$T2/assign" -H "$AH" -H "$(idem assign-admin)" -H 'Content-Type: application/json' \
  -d "{\"assigned_to\":\"$AUID\"}" >/dev/null
ASG=$($PSQL -tAc "SELECT assigned_to FROM tickets WHERE id='$T2'" | tr -d '[:space:]')
[ "$ASG" = "$AUID" ] && ok "已指派给管理员" || bad "指派失败" "$ASG"

NA=$($PSQL -tAc "SELECT count(*) FROM audit_events WHERE action IN ('ticket.create','ticket.assign','ticket.status_change','ticket.close','ticket.reply','ticket.internal_note','ticket.sla_escalate')" | tr -d '[:space:]')
[ "$NA" -ge 9 ] && ok "工单动作已写入 $NA 条审计（SEC-012）" || bad "审计不足" "count=$NA"

#-------------------------------------------------------------------------------
echo
echo "==============================================="
echo "  通过 $pass 项，失败 $fail 项"
echo "==============================================="
[ "$fail" -eq 0 ]
