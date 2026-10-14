package service

import (
	"context"
	"errors"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// xb-tg：须等客服回复（#35）、自动关闭（#36）、管理员工单提醒端口（#20）。

// fakeRules 返回预设的工单规则。
type fakeRules struct {
	rules settingssvc.SupportRules
}

func (f *fakeRules) SupportRules(context.Context) (settingssvc.SupportRules, error) {
	return f.rules, nil
}

// fakeAdminNotifier 记下管理员工单提醒。
type fakeAdminNotifier struct {
	notices []AdminTicketNotice
	err     error
}

func (f *fakeAdminNotifier) TicketActivity(_ context.Context, n AdminTicketNotice) error {
	if f.err != nil {
		return f.err
	}
	f.notices = append(f.notices, n)
	return nil
}

func TestWaitForReply(t *testing.T) {
	e, ctx := newEnv(t)
	rules := &fakeRules{rules: settingssvc.SupportRules{WaitForReply: true, AutoCloseHours: 24}}
	e.svc.UseSettings(rules)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)

	tk, err := e.svc.Create(uctx, CreateInput{Body: createBody("wait")})
	if err != nil {
		t.Fatal(err)
	}
	// 最后一条是用户自己的：不许再发，详情标记禁用
	err = e.svc.UserReply(uctx, tk.ID, "我再补充一句", nil)
	mustCode(t, err, errs.Conflict)
	if ee := errs.As(err); ee == nil || ee.Message != ErrWaitForAgent.Message {
		t.Fatalf("应为 ErrWaitForAgent，得到 %v", err)
	}
	d, err := e.svc.MyTicket(uctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !d.ReplyBlocked {
		t.Fatal("最后一条是用户消息时 ReplyBlocked 应为 true")
	}

	// 内部备注不算客服回复
	actx := e.asAdmin(ctx, admin)
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "内部备注", InternalNote: true}); err != nil {
		t.Fatal(err)
	}
	mustCode(t, e.svc.UserReply(uctx, tk.ID, "还是不行吗", nil), errs.Conflict)

	// 客服公开回复后放行
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: tk.ID, Body: "客服公开回复"}); err != nil {
		t.Fatal(err)
	}
	d, err = e.svc.MyTicket(uctx, tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplyBlocked {
		t.Fatal("客服回复后 ReplyBlocked 应为 false")
	}
	if err := e.svc.UserReply(uctx, tk.ID, "收到，谢谢", nil); err != nil {
		t.Fatal(err)
	}

	// 开关关着：连发不拦
	rules.rules.WaitForReply = false
	if err := e.svc.UserReply(uctx, tk.ID, "再补一句", nil); err != nil {
		t.Fatal(err)
	}
	d, err = e.svc.MyTicket(uctx, tk.ID)
	if err != nil || d.ReplyBlocked {
		t.Fatalf("开关关着 ReplyBlocked 应为 false，err=%v", err)
	}
}

func TestAdminTicketNotifier(t *testing.T) {
	e, ctx := newEnv(t)
	n := &fakeAdminNotifier{}
	e.svc.UseAdminNotifier(n)
	user := e.mkUser(t, ctx, nil)
	uctx := asUser(ctx, user)

	tk, err := e.svc.Create(uctx, CreateInput{Body: "提醒标题\n" + createBody("notice")})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.notices) != 1 {
		t.Fatalf("建单应有一条提醒，得到 %d", len(n.notices))
	}
	got := n.notices[0]
	if got.Kind != AdminNoticeOpened || got.UserID != user || got.TicketID != tk.ID || got.TicketNo != tk.TicketNo ||
		got.Subject != "提醒标题" || got.MessageID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatalf("建单提醒 %+v", got)
	}

	if err := e.svc.UserReply(uctx, tk.ID, "用户回复正文", nil); err != nil {
		t.Fatal(err)
	}
	if len(n.notices) != 2 || n.notices[1].Kind != AdminNoticeUserReplied || n.notices[1].Body != "用户回复正文" {
		t.Fatalf("回复提醒 %+v", n.notices)
	}
	if n.notices[1].MessageID == got.MessageID {
		t.Fatal("两条提醒的消息 id 应不同")
	}

	// 提醒写失败：建单一起回滚
	n.err = errors.New("notify down")
	if _, err := e.svc.Create(uctx, CreateInput{Body: createBody("rollback")}); err == nil {
		t.Fatal("提醒失败时建单应返回错误")
	}
	list, err := e.svc.MyTickets(uctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("回滚后应只有 1 张工单，得到 %d err=%v", len(list), err)
	}
}

func TestAutoCloseScheduled(t *testing.T) {
	e, ctx := newEnv(t)
	rules := &fakeRules{rules: settingssvc.SupportRules{AutoClose: false, AutoCloseHours: 24}}
	e.svc.UseSettings(rules)
	user := e.mkUser(t, ctx, nil)
	admin := e.mkAdmin(t, ctx, nil)
	uctx := asUser(ctx, user)
	actx := e.asAdmin(ctx, admin)

	old, err := e.svc.Create(uctx, CreateInput{Body: createBody("old")})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := e.svc.Create(uctx, CreateInput{Body: createBody("fresh")})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := e.svc.Create(uctx, CreateInput{Body: createBody("waiting")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: old.ID, Body: "请提供截图"}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.AgentReply(actx, AgentReplyInput{TicketID: fresh.ID, Body: "请提供截图"}); err != nil {
		t.Fatal(err)
	}
	// old 与 waiting 都很久没动；waiting 还在等客服，不该被关
	if err := e.exec(ctx, `UPDATE tickets SET updated_at = now() - interval '25 hours' WHERE id IN (?, ?)`, old.ID, waiting.ID); err != nil {
		t.Fatal(err)
	}

	// 开关关着：什么都不做
	n, err := e.svc.AutoCloseScheduled(ctx)
	if err != nil || n != 0 {
		t.Fatalf("开关关着应关 0 张，得到 %d err=%v", n, err)
	}

	rules.rules.AutoClose = true
	n, err = e.svc.AutoCloseScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("应至少关 1 张，得到 %d", n)
	}

	var status, reason string
	if err := e.row(ctx, `SELECT status, coalesce(closed_reason, '') FROM tickets WHERE id = ?`, old.ID).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != StatusClosed || reason != ClosedAuto {
		t.Fatalf("old 应已自动关闭，得到 %s/%s", status, reason)
	}
	for _, id := range []any{fresh.ID, waiting.ID} {
		if err := e.row(ctx, `SELECT status FROM tickets WHERE id = ?`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == StatusClosed {
			t.Fatalf("工单 %v 不该被关", id)
		}
	}

	var sysMsgs int
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND author_kind = 'system'`, old.ID).Scan(&sysMsgs); err != nil {
		t.Fatal(err)
	}
	if sysMsgs != 1 {
		t.Fatalf("应有 1 条系统消息，得到 %d", sysMsgs)
	}
	var actorKind string
	if err := e.row(ctx, `SELECT actor_kind FROM audit_events WHERE action = ? AND resource_id = ?`, ActionAutoClose, old.ID).Scan(&actorKind); err != nil {
		t.Fatal(err)
	}
	if actorKind != "system" {
		t.Fatalf("自动关闭审计应记 system，得到 %s", actorKind)
	}

	// 再跑一轮不重复关
	n, err = e.svc.AutoCloseScheduled(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM ticket_messages WHERE ticket_id = ? AND author_kind = 'system'`, old.ID).Scan(&sysMsgs); err != nil {
		t.Fatal(err)
	}
	if sysMsgs != 1 {
		t.Fatalf("重跑后系统消息仍应为 1 条，得到 %d（本轮关 %d 张）", sysMsgs, n)
	}
}
