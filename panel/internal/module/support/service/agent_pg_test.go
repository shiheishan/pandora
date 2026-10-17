package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestAgentReplyPublicAndInternal(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)
	actx := e.asAdmin(ctx, admin)

	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "内部", InternalNote: true}); err != nil {
		t.Fatal(err)
	}
	var fr *time.Time
	if err := e.row(ctx, `SELECT first_responded_at FROM tickets WHERE id = ?`, tk.ID).Scan(&fr); err != nil {
		t.Fatal(err)
	}
	if fr != nil {
		t.Fatal("内部备注不应写 first_responded_at")
	}

	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "公开回复"}); err != nil {
		t.Fatal(err)
	}
	if len(e.notifier.notices) != 1 {
		t.Fatalf("应通知 1 次，得到 %d", len(e.notifier.notices))
	}
	n := e.notifier.notices[0]
	if n.UserID != user || n.TicketID != tk.ID {
		t.Fatal("通知主体不对")
	}
	var msgID uuid.UUID
	if err := e.row(ctx, `SELECT id FROM ticket_messages WHERE ticket_id = ? AND internal_note = false AND author_kind = 'agent' ORDER BY created_at DESC LIMIT 1`, tk.ID).Scan(&msgID); err != nil {
		t.Fatal(err)
	}
	if n.MessageID != msgID {
		t.Fatal("MessageID 应对上")
	}

	var status string
	var fr2 time.Time
	if err := e.row(ctx, `SELECT status, first_responded_at FROM tickets WHERE id = ?`, tk.ID).Scan(&status, &fr2); err != nil {
		t.Fatal(err)
	}
	if status != StatusPendingUser || fr2.IsZero() {
		t.Fatal("公开回复后 pending_user 且 first_responded_at 有值")
	}

	before := fr2
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "第二条公开"}); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT first_responded_at FROM tickets WHERE id = ?`, tk.ID).Scan(&fr2); err != nil {
		t.Fatal(err)
	}
	if !fr2.Equal(before) {
		t.Fatal("第二次公开回复不应改 first_responded_at")
	}
	if len(e.notifier.notices) != 2 {
		t.Fatal("第二条公开也应通知")
	}
}

func TestAgentReplyNotifierError(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	errDown := errors.New("notify down")
	e.notifier.err = errDown
	err = e.svc.AgentReply(e.asAdmin(ctx, admin), AgentReplyInput{TicketID: tk.ID, Body: "公开"})
	if !errors.Is(err, errDown) {
		t.Fatalf("应返回 notifier 错误，得到 %v", err)
	}
}

func TestAssign(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	notAdmin := e.mkUser(t, ctx, nil)
	actx := e.asAdmin(ctx, admin)
	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}

	err = e.svc.Assign(actx, AssignInput{TicketID: tk.ID, AssigneeID: &notAdmin})
	ve := errs.As(err)
	if ve == nil || ve.Code != errs.ValidationFailed || ve.Fields["assigned_to"] == "" {
		t.Fatalf("非管理员指派应 422，得到 %v", err)
	}

	if err := e.svc.Assign(actx, AssignInput{TicketID: tk.ID, AssigneeID: &admin}); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.AdminTicket(actx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.AssignedTo == nil || *d.AssignedTo != admin || d.AssigneeEmail == nil {
		t.Fatal("指派信息不对")
	}

	if err := e.svc.Assign(actx, AssignInput{TicketID: tk.ID, AssigneeID: nil}); err != nil {
		t.Fatal(err)
	}
	d, err = e.svc.AdminTicket(actx, tk.ID)
	if err != nil || d.AssignedTo != nil {
		t.Fatalf("取消指派后 AssignedTo 应为 nil: %v %+v", err, d.AssignedTo)
	}
}

func TestSetStatus(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	actx := e.asAdmin(ctx, admin)
	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}

	mustCode(t, e.svc.SetStatus(actx, StatusInput{TicketID: tk.ID, Status: StatusPendingUser}), errs.ValidationFailed)

	if err := e.svc.SetStatus(actx, StatusInput{TicketID: tk.ID, Status: StatusClosed}); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := e.row(ctx, `SELECT closed_reason FROM tickets WHERE id = ?`, tk.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != ClosedByAgent {
		t.Fatal(reason)
	}

	if err := e.svc.SetStatus(actx, StatusInput{TicketID: tk.ID, Status: StatusResolved}); err != nil {
		t.Fatal(err)
	}
	var resolvedAt *time.Time
	var closedAt *time.Time
	if err := e.row(ctx, `SELECT resolved_at, closed_at FROM tickets WHERE id = ?`, tk.ID).Scan(&resolvedAt, &closedAt); err != nil {
		t.Fatal(err)
	}
	if resolvedAt == nil || closedAt != nil {
		t.Fatal("resolved 应清 closed 列")
	}

	tk2, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("2")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(actx, StatusInput{TicketID: tk2.ID, Status: StatusEscalated}); err != nil {
		t.Fatal(err)
	}
	var pri string
	if err := e.row(ctx, `SELECT priority FROM tickets WHERE id = ?`, tk2.ID).Scan(&pri); err != nil {
		t.Fatal(err)
	}
	if pri != PriorityHigh {
		t.Fatalf("normal 升级 escalated 后 priority=%s", pri)
	}

	mustCode(t, e.svc.SetStatus(actx, StatusInput{TicketID: tk2.ID, Status: StatusClosed, Reason: strings.Repeat("r", 501)}), errs.ValidationFailed)

	var sysCnt int
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND author_kind = 'system'`, tk.ID).Scan(&sysCnt); err != nil {
		t.Fatal(err)
	}
	if sysCnt < 2 {
		t.Fatalf("每次改状态应有系统消息，得到 %d", sysCnt)
	}
}

func TestEscalateOverdue(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)
	t1, err := e.svc.Create(uctx, CreateInput{Body: createBody("a")})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := e.svc.Create(uctx, CreateInput{Body: createBody("b")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE tickets SET sla_first_response_due = now() - interval '1 hour', sla_resolution_due = now() + interval '1 hour' WHERE id = ?`, t1.ID); err != nil {
		t.Fatal(err)
	}

	n, err := e.svc.EscalateOverdueScheduled(ctx)
	if err != nil || n != 1 {
		t.Fatalf("首轮应升级 1 张，得到 n=%d err=%v", n, err)
	}
	var st string
	if err := e.row(ctx, `SELECT status FROM tickets WHERE id = ?`, t1.ID).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != StatusEscalated {
		t.Fatal(st)
	}
	var sys int
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND body LIKE '%SLA%'`, t1.ID).Scan(&sys); err != nil {
		t.Fatal(err)
	}
	if sys < 1 {
		t.Fatal("应有 SLA 系统消息")
	}

	if _, err := e.svc.EscalateOverdue(uctx, nil); err == nil {
		t.Fatal("门户会话不应能手动触发升级")
	}
	n, err = e.svc.EscalateOverdue(e.asAdmin(ctx, admin), func(int) (idemsvc.Response, error) { return idemsvc.Response{}, nil })
	if err != nil || n != 0 {
		t.Fatalf("第二轮应 0，得到 n=%d err=%v", n, err)
	}

	// 客服手动降回待处理、仍没公开回复：不会被再次自动升级
	if err := e.exec(ctx, `UPDATE tickets SET status = 'pending_agent' WHERE id = ?`, t1.ID); err != nil {
		t.Fatal(err)
	}
	n, err = e.svc.EscalateOverdueScheduled(ctx)
	if err != nil || n != 0 {
		t.Fatalf("升级过的单不应再次自动升级，n=%d err=%v", n, err)
	}

	if err := e.exec(ctx, `UPDATE tickets SET sla_first_response_due = now() - interval '2 hour' WHERE id = ?`, t2.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(e.asAdmin(ctx, admin), AgentReplyInput{TicketID: t2.ID, Body: "已回复"}); err != nil {
		t.Fatal(err)
	}
	n, err = e.svc.EscalateOverdueScheduled(ctx)
	if err != nil || n != 0 {
		t.Fatalf("已公开回复的过期单不应升级，n=%d err=%v", n, err)
	}
}

func TestQueueFiltersAndShape(t *testing.T) {
	e, ctx := newEnv(t)
	plan := "专业版"
	user := e.mkUser(t, ctx, nil)
	e.plans.plans[user] = plan
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)
	actx := e.asAdmin(ctx, admin)

	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("queue")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "内", InternalNote: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UserReply(uctx, tk.ID, "用户再说话", nil); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(actx, StatusInput{TicketID: tk.ID, Status: StatusEscalated}); err != nil {
		t.Fatal(err)
	}

	tk2, err := e.svc.Create(uctx, CreateInput{Body: createBody("plain")})
	if err != nil {
		t.Fatal(err)
	}

	rows, total, err := e.svc.Queue(actx, QueueFilter{Statuses: []string{StatusOpen, StatusEscalated, StatusPendingAgent}, Limit: 50})
	if err != nil || total < 2 {
		t.Fatalf("队列 total=%d err=%v", total, err)
	}
	if rows[0].ID != tk.ID || rows[0].Status != StatusEscalated {
		t.Fatal("升级的应排最前")
	}
	if rows[0].UserEmail == "" || rows[0].UserActivePlan == nil || *rows[0].UserActivePlan != plan {
		t.Fatal("UserEmail / UserActivePlan")
	}
	// 最后一条是改状态的系统消息，但未读只看人发的消息（用户 2026-10-14 定）：取到的是用户追问
	if rows[0].LastMessageAuthorKind != AuthorUser {
		t.Fatalf("最后非内部、非系统消息应为用户，得到 %q", rows[0].LastMessageAuthorKind)
	}
	// 用户首帖、内部备注、用户追问、改状态系统消息
	if rows[0].MessageCount != 4 {
		t.Fatalf("MessageCount 应含内部与系统，得到 %d", rows[0].MessageCount)
	}

	frag := strings.Split(e.users.contacts[user].Email, "@")[0]
	rows, _, err = e.svc.Queue(actx, QueueFilter{Query: frag, Limit: 50})
	if err != nil || len(rows) == 0 {
		t.Fatalf("按邮箱搜: %v len=%d", err, len(rows))
	}
	rows, _, err = e.svc.Queue(actx, QueueFilter{Query: tk.TicketNo[2:6], Limit: 50})
	if err != nil || len(rows) == 0 {
		t.Fatalf("按工单号搜: %v", err)
	}

	zero := uuid.Nil
	rows, total, err = e.svc.Queue(actx, QueueFilter{AssignedTo: &zero, Limit: 50})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("uuid.Nil 指派筛应 0 条 total=%d len=%d", total, len(rows))
	}

	if err := e.exec(ctx, `UPDATE tickets SET sla_first_response_due = now() - interval '1 hour' WHERE id = ?`, tk2.ID); err != nil {
		t.Fatal(err)
	}
	rows, _, err = e.svc.Queue(actx, QueueFilter{OnlyBreached: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if r.ID == tk2.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("OnlyBreached 应包含过期未响应工单")
	}
}

func TestAdminTicketAndAssignees(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	an := "管理员显示名"
	admin := e.mkAdmin(t, ctx, &an)
	actx := e.asAdmin(ctx, admin)
	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "备注", InternalNote: true}); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.AdminTicket(actx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	seenInternal := false
	for _, m := range d.Messages {
		if m.InternalNote && m.Body == "备注" {
			seenInternal = true
		}
		if m.AuthorKind == AuthorAgent && m.AuthorName != nil && *m.AuthorName != an {
			t.Fatalf("客服 AuthorName 应为 %q", an)
		}
	}
	if !seenInternal {
		t.Fatal("后台应见内部备注")
	}
	_, err = e.svc.AdminTicket(actx, uuid.New())
	mustCode(t, err, errs.NotFound)

	list, err := e.svc.Assignees(actx)
	if err != nil || len(list) < 1 {
		t.Fatal(err)
	}
	for i := 1; i < len(list); i++ {
		if list[i].Email < list[i-1].Email {
			t.Fatal("Assignees 应按邮箱升序")
		}
	}
}

func TestMacros(t *testing.T) {
	e, ctx := newEnv(t)
	admin := e.mkAdmin(t, ctx, nil)
	actx := e.asAdmin(ctx, admin)

	id, err := e.svc.SaveMacro(actx, nil, MacroInput{Title: "问候", Body: "您好"})
	if err != nil {
		t.Fatal(err)
	}
	macros, err := e.svc.ListMacros(actx)
	if err != nil || len(macros) != 1 || macros[0].Title != "问候" {
		t.Fatalf("列表: %v %+v", err, macros)
	}
	_, err = e.svc.SaveMacro(actx, &id, MacroInput{Title: "新标题", Body: "新正文"})
	if err != nil {
		t.Fatal(err)
	}
	macros, _ = e.svc.ListMacros(actx)
	if macros[0].Title != "新标题" {
		t.Fatal("覆盖后标题应变")
	}
	bad := uuid.New()
	_, err = e.svc.SaveMacro(actx, &bad, MacroInput{Title: "x", Body: "y"})
	mustCode(t, err, errs.NotFound)
	mustCode(t, e.svc.DeleteMacro(actx, bad), errs.NotFound)
	_, err = e.svc.SaveMacro(actx, nil, MacroInput{Title: strings.Repeat("标", 21), Body: "正文"})
	mustCode(t, err, errs.ValidationFailed)
	if err := e.svc.DeleteMacro(actx, id); err != nil {
		t.Fatal(err)
	}
	macros, _ = e.svc.ListMacros(actx)
	if len(macros) != 0 {
		t.Fatal("删除后列表应空")
	}
}

func TestPortalForbiddenOnAdminAPI(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	_, _, err := e.svc.Queue(asUser(ctx, user), QueueFilter{})
	mustCode(t, err, errs.Forbidden)
}

func TestTicketLabels(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	labels, err := e.svc.TicketLabels(ctx, []uuid.UUID{tk.ID})
	if err != nil || labels[tk.ID] != tk.TicketNo {
		t.Fatalf("TicketLabels: %v %v", err, labels)
	}
}
