// [INPUT]: 依赖 platform/pg18test 打开 support 域的一次性库，依赖 user_tickets.go 的 Create、agent_tickets.go 的 ListForAgent / GetForAgent / SetStatus
// [OUTPUT]: 对外提供 TestAgentQueueFieldsPG18
// [POS]: domain/support 的 PG18 测试（契约后台-02）：队列 status 多值筛选与 last_message_author_kind（跳过内部备注）、详情 user_active_plan、详情 message_count / last_reply_at 与队列一致及 related_order（R75）、人工升级把优先级提到至少 high
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"sort"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestAgentQueueFieldsPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUPPORT", DatabasePrefix: "pandora_node_preview_",
		MarkerTable: "pandora_support_test_marker", CommentTag: "pandora-node-preview-pg18",
	})
	const (
		tenantID = "79000000-0000-4000-8000-000000000101"
		ownerID  = "79000000-0000-4000-8000-000000000111"
		agentID  = "79000000-0000-4000-8000-000000000112"
		product  = "79000000-0000-4000-8000-000000000121"
		plan     = "79000000-0000-4000-8000-000000000122"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenantID + `','agent-queue','Agent Queue','USD')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + ownerID + `','` + tenantID + `','owner@agent-queue.invalid','Owner','active')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + agentID + `','` + tenantID + `','agent@agent-queue.invalid','Agent','active')`,
		`INSERT INTO products(id,tenant_id,code,name,status) VALUES('` + product + `','` + tenantID + `','queue-product','Queue Product','active')`,
		`INSERT INTO plans(id,tenant_id,product_id,code,name,status) VALUES('` + plan + `','` + tenantID + `','` + product + `','queue-plan','Pro 月付','draft')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, sql)
		}
	}
	svc := NewService(app)
	newTicket := func() string {
		tk, err := svc.Create(ctx, tenantID, CreateInput{UserID: ownerID, Body: "节点连不上了，麻烦帮忙看一下具体原因。"})
		if err != nil {
			t.Fatalf("create ticket: %v", err)
		}
		return tk.ID
	}
	userOnly, noted, resolved := newTicket(), newTicket(), newTicket()
	// 内部备注不算「最后一条消息」：noted 最后一条可见消息仍是用户的
	if _, err := admin.Exec(ctx, `INSERT INTO ticket_messages(tenant_id,ticket_id,author_id,author_kind,body,internal_note)
		VALUES($1,$2,$3,'agent','内部：先查节点日志',true)`, tenantID, noted, agentID); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetStatus(ctx, tenantID, agentID, resolved, "resolved", ""); err != nil {
		t.Fatal(err)
	}

	list := func(status string) map[string]Ticket {
		t.Helper()
		items, total, err := svc.ListForAgent(ctx, tenantID, ListFilter{Status: status})
		if err != nil || total != len(items) {
			t.Fatalf("list %q: total=%d items=%d err=%v", status, total, len(items), err)
		}
		out := map[string]Ticket{}
		for _, it := range items {
			out[it.ID] = it
		}
		return out
	}
	open := list("open, pending_agent,escalated")
	if len(open) != 2 || open[userOnly].ID == "" || open[noted].ID == "" {
		t.Fatalf("multi-status filter = %v, want the two unresolved tickets", keys(open))
	}
	if open[userOnly].LastMessageAuthorKind != "user" || open[noted].LastMessageAuthorKind != "user" {
		t.Fatalf("last author: plain=%q noted=%q, want user (internal notes skipped)",
			open[userOnly].LastMessageAuthorKind, open[noted].LastMessageAuthorKind)
	}
	// 改状态会写一条 system 消息
	all := list("")
	if len(all) != 3 || all[resolved].LastMessageAuthorKind != "system" {
		t.Fatalf("all tickets=%d resolved last author=%q", len(all), all[resolved].LastMessageAuthorKind)
	}

	// 详情：没有生效订阅时为空；有 active 订阅时是套餐名
	if tk, err := svc.GetForAgent(ctx, tenantID, userOnly); err != nil || tk.UserActivePlan != nil || tk.RelatedOrder != nil {
		t.Fatalf("detail without subscription: plan=%v order=%v err=%v", tk.UserActivePlan, tk.RelatedOrder, err)
	}

	// 详情的 message_count / last_reply_at 与队列同口径（R75）：内部备注与系统消息都算
	for id, want := range map[string]int{userOnly: 1, noted: 2, resolved: 2} {
		tk, err := svc.GetForAgent(ctx, tenantID, id)
		if err != nil {
			t.Fatal(err)
		}
		row := all[id]
		if tk.MessageCount != want || tk.MessageCount != row.MessageCount ||
			!tk.LastReplyAt.Equal(row.LastReplyAt) || tk.LastReplyAt.Before(tk.CreatedAt) {
			t.Fatalf("detail %s: count=%d last=%s, queue count=%d last=%s, want count %d",
				id, tk.MessageCount, tk.LastReplyAt, row.MessageCount, row.LastReplyAt, want)
		}
	}
	if !all[noted].LastReplyAt.After(all[userOnly].LastReplyAt) {
		t.Fatalf("internal note did not move last_reply_at: noted=%s plain=%s",
			all[noted].LastReplyAt, all[userOnly].LastReplyAt)
	}

	// 详情的 related_order（R75）：后台和门户一样回单号
	const orderID = "79000000-0000-4000-8000-000000000131"
	// 订单只作关联对象：绕过建单幂等触发器直接种一行
	otx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO orders(id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,
		   tax_amount,total_amount,balance_applied,payable_amount,expires_at,business_request_id)
		 VALUES('` + orderID + `','` + tenantID + `','AQ-ORDER-1','` + ownerID + `','new','pending_payment','USD',100,0,0,100,0,100,
		   now()+interval '30 minutes',gen_random_uuid())`,
	} {
		if _, err := otx.Exec(ctx, sql); err != nil {
			_ = otx.Rollback(ctx)
			t.Fatalf("seed order: %v", err)
		}
	}
	if err := otx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE tickets SET related_order_id=$1 WHERE id=$2`, orderID, noted); err != nil {
		t.Fatal(err)
	}
	if tk, err := svc.GetForAgent(ctx, tenantID, noted); err != nil || tk.RelatedOrder == nil ||
		tk.RelatedOrder.ID != orderID || tk.RelatedOrder.OrderNo != "AQ-ORDER-1" {
		t.Fatalf("detail related order=%+v err=%v", tk.RelatedOrder, err)
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`SET LOCAL session_replication_role = replica`,
		`INSERT INTO subscriptions(tenant_id,user_id,plan_id,plan_version_id,status,snapshot_currency,snapshot_amount,current_period_end)
		 VALUES('` + tenantID + `','` + ownerID + `','` + plan + `',gen_random_uuid(),'active','USD',0,now()+interval '30 days')`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("seed subscription: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if tk, err := svc.GetForAgent(ctx, tenantID, userOnly); err != nil || tk.UserActivePlan == nil || *tk.UserActivePlan != "Pro 月付" {
		t.Fatalf("detail active plan=%v err=%v", tk.UserActivePlan, err)
	}

	// 人工升级：normal → high；urgent 不降
	priorityOf := func(id string) string {
		var p string
		if err := admin.QueryRow(ctx, `SELECT priority FROM tickets WHERE id=$1`, id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := svc.SetStatus(ctx, tenantID, agentID, userOnly, "escalated", "需要二线"); err != nil {
		t.Fatal(err)
	}
	if p := priorityOf(userOnly); p != "high" {
		t.Fatalf("escalated priority=%s, want high", p)
	}
	if _, err := admin.Exec(ctx, `UPDATE tickets SET priority='urgent' WHERE id=$1`, noted); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetStatus(ctx, tenantID, agentID, noted, "escalated", ""); err != nil {
		t.Fatal(err)
	}
	if p := priorityOf(noted); p != "urgent" {
		t.Fatalf("escalating an urgent ticket changed priority to %s", p)
	}
}

func keys(m map[string]Ticket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
