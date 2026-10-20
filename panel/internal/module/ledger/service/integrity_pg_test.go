package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// 巡检计数是全库口径，别的包可能留下提交过的数据：一律比「造数据前后」的差，样例按自己建的 id 查（新的在前）。

func TestCheckDriftPG(t *testing.T) {
	e, ctx := newEnv(t)

	before, err := e.svc.CheckDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 正常：余额 0、没有分录的科目不算漂移
	okRef := "drift-ok-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	if err := e.exec(ctx, `INSERT INTO ledger_accounts (account_type, owner_ref, balance) VALUES ('suspense', ?, 0)`, okRef); err != nil {
		t.Fatal(err)
	}
	same, err := e.svc.CheckDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if same.Count != before.Count {
		t.Fatalf("正常科目不该算漂移: before=%d after=%d", before.Count, same.Count)
	}

	// 违例：缓存写了 5.00 却没有分录
	badRef := "drift-bad-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	var badID uuid.UUID
	if err := e.row(ctx, `INSERT INTO ledger_accounts (account_type, owner_ref, balance) VALUES ('suspense', ?, 5.00) RETURNING id`, badRef).Scan(&badID); err != nil {
		t.Fatal(err)
	}
	after, err := e.svc.CheckDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Count != before.Count+1 {
		t.Fatalf("漂移计数: before=%d after=%d", before.Count, after.Count)
	}
	if len(after.Sample) == 0 || after.Sample[0] != badID.String() {
		t.Fatalf("样例应以新建的漂移科目开头: %v want %s", after.Sample, badID)
	}
}

func TestCheckHoldsPG(t *testing.T) {
	e, ctx := newEnv(t)

	before, err := e.svc.CheckHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 正常：冻结 10 元后，冻结科目余额 = held 冻结合计
	user := e.mkUser(t, ctx)
	e.fund(t, ctx, user, "20.00")
	order := e.newOrder(t, ctx, user)
	if _, err := e.svc.HoldForOrder(ctx, HoldInput{OrderID: order, UserID: user, Amount: dec("10.00")}); err != nil {
		t.Fatal(err)
	}
	same, err := e.svc.CheckHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if same.Count != before.Count || slices.Contains(same.Sample, user.String()) {
		t.Fatalf("正常冻结不该算违例: before=%d after=%d sample=%v", before.Count, same.Count, same.Sample)
	}

	// 违例：冻结行被改成 released，却没记释放分录（冻结科目仍是 10 元）
	if err := e.exec(ctx, `
		UPDATE balance_holds SET status = 'released', release_txn_id = hold_txn_id, released_at = now()
		 WHERE order_id = ?`, order); err != nil {
		t.Fatal(err)
	}
	after, err := e.svc.CheckHolds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Count != before.Count+1 {
		t.Fatalf("冻结对不上计数: before=%d after=%d", before.Count, after.Count)
	}
	if !slices.Contains(after.Sample, user.String()) {
		t.Fatalf("样例应含该用户: %v want %s", after.Sample, user)
	}
}
