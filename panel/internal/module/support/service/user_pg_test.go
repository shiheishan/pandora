package service

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestCreateTicketDefaultsAndSLA(t *testing.T) {
	e, ctx := newEnv(t)
	name := "门户用户"
	user := e.mkUser(t, ctx, &name)
	uctx := asUser(ctx, user)

	body := "首行当标题\n" + createBody("")
	tk, err := e.svc.Create(uctx, CreateInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if tk.Subject != "首行当标题" || tk.Category != "general" || tk.Priority != PriorityNormal || tk.Status != StatusOpen {
		t.Fatalf("字段不对: %+v", tk)
	}
	if tk.MessageCount != 1 || tk.RelatedOrder != nil {
		t.Fatal("计数与订单")
	}

	var msgCnt int
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND author_kind = 'user'`, tk.ID).Scan(&msgCnt); err != nil {
		t.Fatal(err)
	}
	if msgCnt != 1 {
		t.Fatal("应有一条用户消息")
	}

	var slaDue time.Time
	if err := e.row(ctx, `SELECT sla_first_response_due FROM tickets WHERE id = ?`, tk.ID).Scan(&slaDue); err != nil {
		t.Fatal(err)
	}
	want := tk.CreatedAt.Add(12 * time.Hour)
	if d := slaDue.Sub(want); d < -time.Second || d > time.Second {
		t.Fatalf("SLA 首次响应应为建单+12h，差 %v", d)
	}

	abuse, err := e.svc.Create(uctx, CreateInput{Body: createBody("abuse"), Category: "abuse"})
	if err != nil {
		t.Fatal(err)
	}
	if abuse.Priority != PriorityUrgent {
		t.Fatalf("abuse 应为 urgent，得到 %s", abuse.Priority)
	}
}

func TestCreateOpenLimitAndClose(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	uctx := asUser(ctx, user)

	for i := 0; i < settingssvc.DefaultSupportMaxOpenTickets; i++ {
		_, err := e.svc.Create(uctx, CreateInput{Body: createBody(randHex(4))})
		if err != nil {
			t.Fatalf("第 %d 张: %v", i+1, err)
		}
	}
	_, err := e.svc.Create(uctx, CreateInput{Body: createBody("sixth")})
	mustCode(t, err, errs.Conflict)

	list, err := e.svc.MyTickets(uctx)
	if err != nil || len(list) != settingssvc.DefaultSupportMaxOpenTickets {
		t.Fatalf("应有 %d 张未结，得到 %d err=%v", settingssvc.DefaultSupportMaxOpenTickets, len(list), err)
	}
	if err := e.svc.UserClose(uctx, list[0].ID, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Create(uctx, CreateInput{Body: createBody("after-close")})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateOpenLimitFollowsSettings(t *testing.T) {
	e, ctx := newEnv(t)
	rules := &fakeRules{rules: settingssvc.SupportRules{AutoCloseHours: 24, MaxOpenTickets: 2}}
	e.svc.UseSettings(rules)
	user := e.mkUser(t, ctx, nil)
	uctx := asUser(ctx, user)

	for i := 0; i < 2; i++ {
		_, err := e.svc.Create(uctx, CreateInput{Body: createBody(randHex(4))})
		if err != nil {
			t.Fatalf("第 %d 张: %v", i+1, err)
		}
	}
	_, err := e.svc.Create(uctx, CreateInput{Body: createBody("third")})
	mustCode(t, err, errs.Conflict)
	if !strings.Contains(err.Error(), "2 个进行中的工单") {
		t.Fatalf("期望文案含 2 个进行中的工单，得到 %v", err)
	}

	rules.rules.MaxOpenTickets = 3
	_, err = e.svc.Create(uctx, CreateInput{Body: createBody("third-ok")})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateOrderNotOwned(t *testing.T) {
	e, ctx := newEnv(t)
	owner := e.mkUser(t, ctx, nil)
	other := e.mkUser(t, ctx, nil)
	oid := uuid.New()
	e.orders.orders[oid] = struct {
		owner uuid.UUID
		no    string
	}{owner: owner, no: "ORD-1"}

	_, err := e.svc.Create(asUser(ctx, other), CreateInput{
		Body: createBody(""), OrderID: &oid,
	})
	ve := errs.As(err)
	if ve == nil || ve.Code != errs.ValidationFailed || ve.Fields["order_id"] == "" {
		t.Fatalf("期望 order_id 422，得到 %v", err)
	}
}

func TestMyTicketsIsolation(t *testing.T) {
	e, ctx := newEnv(t)
	u1 := e.mkUser(t, ctx, nil)
	u2 := e.mkUser(t, ctx, nil)
	tk, err := e.svc.Create(asUser(ctx, u1), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}

	mine, err := e.svc.MyTickets(asUser(ctx, u1))
	if err != nil || len(mine) != 1 || mine[0].ID != tk.ID {
		t.Fatalf("u1 应看到自己的工单: %v %d", err, len(mine))
	}
	other, err := e.svc.MyTickets(asUser(ctx, u2))
	if err != nil || len(other) != 0 {
		t.Fatalf("u2 应为空: %v len=%d", err, len(other))
	}
	_, err = e.svc.MyTicket(asUser(ctx, u2), tk.ID)
	mustCode(t, err, errs.NotFound)
}

func TestInternalNotesHiddenFromPortal(t *testing.T) {
	e, ctx := newEnv(t)
	userName := "提单人"
	user := e.mkUser(t, ctx, &userName)
	adminName := "客服甲"
	admin := e.mkAdmin(t, ctx, &adminName)

	tk, err := e.svc.Create(asUser(ctx, user), CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	actx := e.asAdmin(ctx, admin)
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "内部备注内容", InternalNote: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "公开回复内容"}); err != nil {
		t.Fatal(err)
	}

	detail, err := e.svc.MyTicket(asUser(ctx, user), tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range detail.Messages {
		if m.Body == "内部备注内容" {
			t.Fatal("门户不应看到内部备注")
		}
		if m.AuthorKind == AuthorAgent && m.AuthorName != nil {
			t.Fatal("客服消息 AuthorName 应为 nil")
		}
		if m.AuthorKind == AuthorUser {
			if m.AuthorName == nil || *m.AuthorName != userName {
				t.Fatalf("用户消息应带显示名 %q", userName)
			}
		}
	}

	list, err := e.svc.MyTickets(asUser(ctx, user))
	if err != nil {
		t.Fatal(err)
	}
	if list[0].MessageCount != 2 {
		t.Fatalf("MessageCount 不数内部备注，期望 2 得到 %d", list[0].MessageCount)
	}
}

func TestUserReplyStates(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)
	actx := e.asAdmin(ctx, admin)

	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetStatus(actx, StatusInput{TicketID: tk.ID, Status: StatusResolved}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UserReply(uctx, tk.ID, "用户追问一下", nil); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.MyTicket(uctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != StatusPendingAgent || d.ResolvedAt != nil {
		t.Fatalf("resolved 追问后 pending_agent 且 resolved_at 空: status=%s", d.Status)
	}

	if err := e.svc.UserClose(uctx, tk.ID, nil); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.svc.UserReply(uctx, tk.ID, "不能再回", nil), errs.Conflict)
	mustCode(t, e.svc.UserReply(uctx, tk.ID, "", nil), errs.ValidationFailed)
}

func TestUserClose(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	uctx := asUser(ctx, user)
	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UserClose(uctx, tk.ID, nil); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := e.row(ctx, `SELECT closed_reason FROM tickets WHERE id = ?`, tk.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != ClosedByUser {
		t.Fatal(reason)
	}
	var sys int
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND author_kind = 'system'`, tk.ID).Scan(&sys); err != nil {
		t.Fatal(err)
	}
	if sys < 1 {
		t.Fatal("应有系统消息")
	}
	mustCode(t, e.svc.UserClose(uctx, tk.ID, nil), errs.NotFound)
}

func TestWithdraw(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)

	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(e.asAdmin(ctx, admin), AgentReplyInput{
		TicketID: tk.ID, Body: "仅内部", InternalNote: true,
	}); err != nil {
		t.Fatal(err)
	}
	note := "撤回说明"
	if err := e.svc.Withdraw(uctx, tk.ID, note); err != nil {
		t.Fatal(err)
	}
	var reason string
	var closedNote *string
	if err := e.row(ctx, `SELECT closed_reason, closed_note FROM tickets WHERE id = ?`, tk.ID).Scan(&reason, &closedNote); err != nil {
		t.Fatal(err)
	}
	if reason != ClosedWithdrawn || closedNote == nil || *closedNote != note {
		t.Fatalf("withdraw 字段 reason=%s note=%v", reason, closedNote)
	}

	tk2, err := e.svc.Create(uctx, CreateInput{Body: createBody("2")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(e.asAdmin(ctx, admin), AgentReplyInput{TicketID: tk2.ID, Body: "公开了"}); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.svc.Withdraw(uctx, tk2.ID, ""), errs.Conflict)

	mustCode(t, e.svc.Withdraw(uctx, tk.ID, ""), errs.Conflict)

	tk3, err := e.svc.Create(uctx, CreateInput{Body: createBody("3")})
	if err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.svc.Withdraw(uctx, tk3.ID, strings.Repeat("x", 501)), errs.ValidationFailed)
}

func TestAuditOmitsTicketBody(t *testing.T) {
	e, ctx := newEnv(t)
	user := e.mkUser(t, ctx, nil)
	uctx := asUser(ctx, user)
	secret := "秘密片段-" + randHex(8)
	body := createBody(secret)
	tk, err := e.svc.Create(uctx, CreateInput{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.UserReply(uctx, tk.ID, "回复也含"+secret, nil); err != nil {
		t.Fatal(err)
	}

	var blob *string
	q := `SELECT string_agg(data::text, E'\n') FROM audit_events WHERE action IN ('ticket.create','ticket.reply') AND resource_id = ?`
	if err := e.row(ctx, q, tk.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if blob == nil || !strings.Contains(*blob, "ticket_no") {
		t.Fatal("应能读到建单与回复的审计")
	}
	if strings.Contains(*blob, secret) {
		t.Fatalf("审计 data 含正文片段: %s", *blob)
	}
}
