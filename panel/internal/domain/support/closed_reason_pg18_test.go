// [INPUT]: 依赖 platform/pg18test 打开 support 域的一次性库，依赖 service.go 的 Create / CloseByUser / SetStatus 与 withdraw.go 的 WithdrawByUser
// [OUTPUT]: 对外提供 TestTicketClosedReasonPG18
// [POS]: domain/support 的 PG18 测试：三种关闭方式各自写对 closed_reason，重新打开时清空（缺陷 17）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"testing"

	"github.com/aegispanel/aegis/internal/platform/pg18test"
)

func TestTicketClosedReasonPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, pg18test.Fixture{
		Domain: "SUPPORT", DatabasePrefix: "pandora_node_preview_",
		MarkerTable: "pandora_support_test_marker", CommentTag: "pandora-node-preview-pg18",
	})

	const (
		tenantID = "79000000-0000-4000-8000-000000000001"
		ownerID  = "79000000-0000-4000-8000-000000000011"
		agentID  = "79000000-0000-4000-8000-000000000012"
	)
	for _, sql := range []string{
		`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES('` + tenantID + `','closed-reason','Closed Reason','USD')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + ownerID + `','` + tenantID + `','owner@closed-reason.invalid','Owner','active')`,
		`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES('` + agentID + `','` + tenantID + `','agent@closed-reason.invalid','Agent','active')`,
	} {
		if _, err := admin.Exec(ctx, sql); err != nil {
			t.Fatalf("seed closed reason fixture: %v", err)
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
	reasonOf := func(id string) (reason, note *string) {
		if err := admin.QueryRow(ctx, `SELECT closed_reason, closed_note FROM tickets WHERE id=$1`, id).Scan(&reason, &note); err != nil {
			t.Fatal(err)
		}
		return
	}
	is := func(p *string, want string) bool { return p != nil && *p == want }

	// 用户关闭：原先 closed_reason 留空，与撤回分不出来
	userClosed := newTicket()
	if err := svc.CloseByUser(ctx, tenantID, ownerID, userClosed); err != nil {
		t.Fatalf("close by user: %v", err)
	}
	if r, _ := reasonOf(userClosed); !is(r, "user_closed") {
		t.Fatalf("user close reason = %v, want user_closed", r)
	}

	// 撤回照旧是 withdrawn；客服重新打开后原因与说明一起清空
	withdrawn := newTicket()
	if err := svc.WithdrawByUser(ctx, tenantID, ownerID, withdrawn, "问题已经自己解决了"); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if r, n := reasonOf(withdrawn); !is(r, "withdrawn") || n == nil {
		t.Fatalf("withdraw reason=%v note=%v", r, n)
	}
	if err := svc.SetStatus(ctx, tenantID, agentID, withdrawn, "pending_agent", ""); err != nil {
		t.Fatalf("agent reopen: %v", err)
	}
	if r, n := reasonOf(withdrawn); r != nil || n != nil {
		t.Fatalf("reopened ticket kept reason=%v note=%v", r, n)
	}

	// 客服关闭记 agent_closed
	agentClosed := newTicket()
	if err := svc.SetStatus(ctx, tenantID, agentID, agentClosed, "closed", "已排查完毕"); err != nil {
		t.Fatalf("agent close: %v", err)
	}
	if r, _ := reasonOf(agentClosed); !is(r, "agent_closed") {
		t.Fatalf("agent close reason = %v, want agent_closed", r)
	}

	// 旧数据：重新打开后残留了原因的工单，客服关闭时不能沿用残留值
	stale := newTicket()
	if _, err := admin.Exec(ctx, `UPDATE tickets SET closed_reason='user_closed' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetStatus(ctx, tenantID, agentID, stale, "closed", ""); err != nil {
		t.Fatalf("agent close stale: %v", err)
	}
	if r, _ := reasonOf(stale); !is(r, "agent_closed") {
		t.Fatalf("stale ticket close reason = %v, want agent_closed", r)
	}
}
