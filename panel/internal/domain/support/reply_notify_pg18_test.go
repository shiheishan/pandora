// [INPUT]: 依赖 platform/pg18test 打开 support 域的一次性库，依赖 service.go 的 Create / ReplyAsAgent / ReplyAsAgentAtomic / SetReplyNotifier，依赖 domain/notify 的真实 Enqueue
// [OUTPUT]: 对外提供 TestTicketReplyNotifyPG18
// [POS]: domain/support 的 PG18 测试：客服非内部回复在同一事务里给提单人排 ticket.replied（R115），回滚不排、同键重放不多排、内部备注不排、关掉 service 类别不排，提交后才 Kick
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

// kickCounter 用真实的 notify.Service 排队，只把 Kick 数出来：派发循环没起，Kick 本身是空操作
type kickCounter struct {
	*notify.Service
	kicks int
}

func (k *kickCounter) Kick() { k.kicks++; k.Service.Kick() }

func TestTicketReplyNotifyPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUPPORT", DatabasePrefix: "pandora_node_preview_",
		MarkerTable: "pandora_support_test_marker", CommentTag: "pandora-node-preview-pg18",
	})

	const (
		tenantID = "7f000000-0000-4000-8000-000000000001"
		ownerID  = "7f000000-0000-4000-8000-000000000011"
		agentID  = "7f000000-0000-4000-8000-000000000012"
		subject  = "香港节点晚高峰断流"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenantID + `','reply-notify','Reply Notify','USD')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + ownerID + `','` + tenantID + `','owner@reply-notify.invalid','Owner','active')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + agentID + `','` + tenantID + `','agent@reply-notify.invalid','Agent','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed reply notify fixture: %v", err)
		}
	}
	// 模板由建租户触发器（00090）种下：没有它 Enqueue 静默排 0 条，下面的断言就失去意义
	var templated bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_templates
		WHERE tenant_id=$1 AND code='ticket.replied' AND channel='inapp' AND category='service' AND status='active')`,
		tenantID).Scan(&templated); err != nil || !templated {
		t.Fatalf("ticket.replied inapp template seeded=%v err=%v", templated, err)
	}

	notifier := &kickCounter{Service: notify.New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("reply-notify-pg18-salt"))}
	svc := NewService(app)
	svc.SetReplyNotifier(notifier)

	tk, err := svc.Create(ctx, tenantID, CreateInput{UserID: ownerID, Subject: subject, Body: "每天晚上八点以后香港节点频繁断开，麻烦排查。"})
	if err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	inapp := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM notification_deliveries
			WHERE tenant_id=$1 AND user_id=$2 AND template_code='ticket.replied' AND channel='inapp'`,
			tenantID, ownerID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	messages := func() int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM ticket_messages WHERE ticket_id=$1`, tk.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	expect := func(step string, wantInapp, wantKicks int) {
		t.Helper()
		if got := inapp(); got != wantInapp || notifier.kicks != wantKicks {
			t.Fatalf("%s: inapp=%d kicks=%d, want inapp=%d kicks=%d", step, got, notifier.kicks, wantInapp, wantKicks)
		}
	}
	expect("before any reply", 0, 0)

	// 幂等路径：审计写不进去时整个回复回滚，通知一起消失、也不 Kick
	claim := newSupportPG18Claim(t, ctx, admin, tenantID, agentID, AgentReplyIdempotencyScope, "reply-notify")
	reply := AgentReplyInput{AgentID: agentID, TicketID: tk.ID, Body: "已定位到上游线路拥塞，今晚切换备用线路。"}
	if _, err := admin.Exec(ctx, `REVOKE INSERT ON audit_events FROM aegis_app`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `GRANT INSERT ON audit_events TO aegis_app`) })
	if _, err := svc.ReplyAsAgentAtomic(ctx, tenantID, reply, claim); err == nil {
		t.Fatal("reply committed without audit permission")
	}
	expect("rolled back reply", 0, 0)
	if _, err := admin.Exec(ctx, `GRANT INSERT ON audit_events TO aegis_app`); err != nil {
		t.Fatal(err)
	}

	// 同一个键重试：回复一条，提单人多一条站内通知，变量与去重键都对
	if _, err := svc.ReplyAsAgentAtomic(ctx, tenantID, reply, claim); err != nil {
		t.Fatalf("atomic agent reply: %v", err)
	}
	expect("atomic reply", 1, 1)
	var messageID, dedupeKey, status, gotSubject string
	if err := admin.QueryRow(ctx, `SELECT m.id::text, d.dedupe_key, d.status, d.payload->>'subject'
		FROM notification_deliveries d
		JOIN ticket_messages m ON m.ticket_id=$2 AND m.author_kind='agent' AND NOT m.internal_note
		WHERE d.tenant_id=$1 AND d.user_id=$3 AND d.template_code='ticket.replied' AND d.channel='inapp'`,
		tenantID, tk.ID, ownerID).Scan(&messageID, &dedupeKey, &status, &gotSubject); err != nil {
		t.Fatal(err)
	}
	if dedupeKey != "ticket-replied:"+messageID+":inapp" || status != "queued" || gotSubject != subject {
		t.Fatalf("queued delivery dedupe=%q status=%q subject=%q message=%s", dedupeKey, status, gotSubject, messageID)
	}

	// 同键重放：键已完成，第二次提交撞 claim lost，整个事务回滚，不多一条回复也不多一条通知
	before := messages()
	if _, err := svc.ReplyAsAgentAtomic(ctx, tenantID, reply, claim); !errors.Is(err, middleware.ErrIdempotencyClaimLost) {
		t.Fatalf("replayed claim error=%v, want claim lost", err)
	}
	if after := messages(); after != before {
		t.Fatalf("replay added messages %d -> %d", before, after)
	}
	expect("replayed claim", 1, 1)

	// 内部备注用户看不见，不排
	internal := newSupportPG18Claim(t, ctx, admin, tenantID, agentID, AgentReplyIdempotencyScope, "reply-notify-internal")
	if _, err := svc.ReplyAsAgentAtomic(ctx, tenantID, AgentReplyInput{
		AgentID: agentID, TicketID: tk.ID, Body: "上游工单号 A-1024，内部跟进。", InternalNote: true,
	}, internal); err != nil {
		t.Fatalf("internal note: %v", err)
	}
	expect("internal note", 1, 1)

	// 非幂等路径同样排队
	if err := svc.ReplyAsAgent(ctx, tenantID, AgentReplyInput{AgentID: agentID, TicketID: tk.ID, Body: "备用线路已切换，请今晚再观察。"}); err != nil {
		t.Fatalf("plain agent reply: %v", err)
	}
	expect("plain reply", 2, 2)

	// 用户关掉 service 类别（两个渠道）后：回复照常成功，一条都不排，也不 Kick
	if _, err := admin.Exec(ctx, `INSERT INTO notification_preferences(tenant_id,user_id,category,channel,enabled)
		VALUES($1,$2,'service','inapp',false),($1,$2,'service','telegram',false)`, tenantID, ownerID); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReplyAsAgent(ctx, tenantID, AgentReplyInput{AgentID: agentID, TicketID: tk.ID, Body: "如仍有问题请随时回复。"}); err != nil {
		t.Fatalf("reply after opt-out: %v", err)
	}
	expect("service opted out", 2, 2)
	var anyChannel int
	if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM notification_deliveries d
		JOIN ticket_messages m ON d.dedupe_key LIKE 'ticket-replied:' || m.id::text || ':%'
		WHERE m.ticket_id=$1 AND m.body='如仍有问题请随时回复。'`, tk.ID).Scan(&anyChannel); err != nil || anyChannel != 0 {
		t.Fatalf("opted-out reply queued %d deliveries err=%v", anyChannel, err)
	}
}
