package service

// 删池时归档套餐绑定：真 catalog 的 ReleaseArchivedPoolBindings 与 DeletePool 联调。

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"

	auditrepo "github.com/aegispanel/aegis/internal/module/audit/repo"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogrepo "github.com/aegispanel/aegis/internal/module/catalog/repo"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

func newArchivedPoolEnv(t *testing.T) (*poolEnv, context.Context) {
	e, ctx := newPoolEnv(t)
	d := testkit.DB(t)
	master := make([]byte, 32)
	for i := range master {
		master[i] = 0x01
	}
	keys, err := crypto.NewKeys(base64.StdEncoding.EncodeToString(master))
	if err != nil {
		t.Fatal(err)
	}
	auditSvc, err := auditsvc.New(auditrepo.New(d), keys)
	if err != nil {
		t.Fatal(err)
	}
	cat := catalogsvc.New(catalogrepo.New(d), tx.NewManager(d), auditSvc, nil)
	e.svc.UseCatalog(cat)
	return e, ctx
}

func seedPlanRow(t *testing.T, ctx context.Context, e *poolEnv) uuid.UUID {
	t.Helper()
	var planID uuid.UUID
	code := poolRandName(t, "pl")
	if err := e.row(ctx, `INSERT INTO plans (code, name, kind) VALUES (?, ?, 'subscription') RETURNING id`, code, "套餐 "+code).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	return planID
}

func archivePlan(t *testing.T, ctx context.Context, e *poolEnv, planID uuid.UUID) {
	t.Helper()
	if err := e.exec(ctx, `UPDATE plans SET status = 'archived', allow_new_purchase = false WHERE id = ?`, planID); err != nil {
		t.Fatal(err)
	}
}

func bindPlanToPool(t *testing.T, ctx context.Context, e *poolEnv, planID, poolID uuid.UUID) {
	t.Helper()
	if err := e.exec(ctx, `INSERT INTO plan_node_pools (plan_id, pool_id) VALUES (?, ?)`, planID, poolID); err != nil {
		t.Fatal(err)
	}
}

func poolBindingCount(t *testing.T, ctx context.Context, e *poolEnv, poolID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM plan_node_pools WHERE pool_id = ?`, poolID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func poolExists(t *testing.T, ctx context.Context, e *poolEnv, poolID uuid.UUID) bool {
	t.Helper()
	var n int64
	if err := e.row(ctx, `SELECT count(*) FROM node_pools WHERE id = ?`, poolID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestDeletePoolReleasesArchivedBindings(t *testing.T) {
	e, ctx := newArchivedPoolEnv(t)
	poolID := seedPoolRow(t, ctx, e, PoolActive)

	p1 := seedPlanRow(t, ctx, e)
	p2 := seedPlanRow(t, ctx, e)
	archivePlan(t, ctx, e, p1)
	archivePlan(t, ctx, e, p2)
	bindPlanToPool(t, ctx, e, p1, poolID)
	bindPlanToPool(t, ctx, e, p2, poolID)

	if err := e.svc.DeletePool(ctx, poolID); err != nil {
		t.Fatal(err)
	}
	if poolBindingCount(t, ctx, e, poolID) != 0 {
		t.Fatalf("删池后 plan_node_pools 应为 0")
	}
	if poolExists(t, ctx, e, poolID) {
		t.Fatalf("池行应已删除")
	}
	if poolAuditCount(t, ctx, e, ActionPoolDeleted, poolID) != 1 {
		t.Fatalf("应有 deleted 审计 1 条")
	}
	var releasedLen int64
	if err := e.row(ctx, `SELECT jsonb_array_length(data->'released_archived_plans') FROM audit_events WHERE action = ? AND resource_id = ?`,
		ActionPoolDeleted, poolID).Scan(&releasedLen); err != nil {
		t.Fatal(err)
	}
	if releasedLen != 2 {
		t.Fatalf("released_archived_plans 长度应为 2，得到 %d", releasedLen)
	}
}

// 未归档（这里用草稿）套餐的绑定仍挡删池，归档套餐的绑定也不会被提前删掉。
func TestDeletePoolStillRefusedByLivePlans(t *testing.T) {
	e, ctx := newArchivedPoolEnv(t)

	q := seedPoolRow(t, ctx, e, PoolActive)
	draft := seedPlanRow(t, ctx, e)
	archQ := seedPlanRow(t, ctx, e)
	archivePlan(t, ctx, e, archQ)
	bindPlanToPool(t, ctx, e, draft, q)
	bindPlanToPool(t, ctx, e, archQ, q)

	err := poolSavepoint(t, ctx, e, func() error { return e.svc.DeletePool(ctx, q) })
	if poolErrCode(err) != errs.Conflict {
		t.Fatalf("草稿套餐绑定应 Conflict，得到 %v", err)
	}
	if poolBindingCount(t, ctx, e, q) != 2 {
		t.Fatalf("拒绝后绑定应仍在")
	}
	if !poolExists(t, ctx, e, q) {
		t.Fatalf("拒绝后池应仍在")
	}
	// 在售套餐与草稿同属「未归档」，挡删池走同一个 Plans 计数；在售的计数口径见 catalog 的 pool_release_pg_test.go
}
