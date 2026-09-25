// [INPUT]: 依赖 traffic_packs.go 的流量包目录用例，依赖 catalog_sales_pg18_test.go 的 openCatalogSalesPG18 夹具与 catalog_sales_capability_test.go 的 staticSalesCapability / requireCatalogErrorCode
// [OUTPUT]: 对外提供 TestTrafficPackAdminPG18（run-pg18-gates.sh 的 catalog_sales 域）
// [POS]: adminops 流量包目录管理的 PG18 集成门禁：新建、改价、上下架各写一条审计，updated_at 乐观锁拦住过期修改，销售闸门关着时只能下架
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestTrafficPackAdminPG18(t *testing.T) {
	ctx, admin, app := openCatalogSalesPG18(t)

	const (
		tenantID = "73300000-0000-7000-8000-000000000001"
		actorID  = "73300000-0000-7000-8000-000000000011"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'traffic-pack-admin-pg18', 'Traffic Pack Admin PG18', 'CNY')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, 'traffic-pack-admin@example.test', 'Pack Admin', 'active')`, []any{tenantID, actorID}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed traffic pack fixture: %v\nSQL: %s", err, seed.sql)
		}
	}
	audits := func(action string) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(*)::int FROM audit_events
			WHERE tenant_id = $1 AND action = $2 AND resource_type = 'traffic_pack'
			  AND actor_id = $3`, tenantID, action, actorID).Scan(&n); err != nil {
			t.Fatalf("count %s audits: %v", action, err)
		}
		return n
	}
	svc := NewService(app, staticSalesCapability(true))

	// 1) 新建：在售、销量 0、名称去首尾空白，写一条 create 审计。
	created, err := svc.CreateTrafficPack(ctx, tenantID, TrafficPackInput{
		ActorID: actorID, Name: "  100 GB 加油包 ", TrafficBytes: 100 << 30,
		Currency: "CNY", UnitAmount: 1500, Recommended: true, SortOrder: 10,
	})
	if err != nil {
		t.Fatalf("create traffic pack: %v", err)
	}
	if created.Name != "100 GB 加油包" || created.Status != "active" || created.SoldCount != 0 ||
		created.TrafficBytes != 100<<30 || !created.Recommended || audits("traffic_pack.create") != 1 {
		t.Fatalf("created pack=%+v create audits=%d", created, audits("traffic_pack.create"))
	}
	t.Log("marker=traffic_pack_admin_create_ok")

	// 2) 修改：拿列表里的 updated_at 改价；拿旧值再改一次被 409 拦住。
	list, err := svc.ListTrafficPacks(ctx, tenantID, "")
	if err != nil || len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list after create=%+v err=%v", list, err)
	}
	stale := list[0].UpdatedAt
	update := TrafficPackInput{
		ActorID: actorID, ExpectedUpdatedAt: &stale, Name: "100 GB 加油包",
		TrafficBytes: 120 << 30, Currency: "CNY", UnitAmount: 1800, SortOrder: 5,
	}
	updated, err := svc.UpdateTrafficPack(ctx, tenantID, created.ID, update)
	if err != nil {
		t.Fatalf("update traffic pack: %v", err)
	}
	if updated.UnitAmount != 1800 || updated.TrafficBytes != 120<<30 || updated.Recommended ||
		!updated.UpdatedAt.After(stale) || audits("traffic_pack.update") != 1 {
		t.Fatalf("updated pack=%+v update audits=%d", updated, audits("traffic_pack.update"))
	}
	_, err = svc.UpdateTrafficPack(ctx, tenantID, created.ID, update)
	requireCatalogErrorCode(t, err, httpx.CodeConflict)
	if audits("traffic_pack.update") != 1 {
		t.Fatal("stale update wrote an audit row")
	}
	t.Log("marker=traffic_pack_admin_update_optimistic_lock_ok")

	// 3) 销售闸门关着：改价与上架被拒（503），下架照常可以。
	closed := NewService(app, staticSalesCapability(false))
	current := updated.UpdatedAt
	_, err = closed.UpdateTrafficPack(ctx, tenantID, created.ID, TrafficPackInput{
		ActorID: actorID, ExpectedUpdatedAt: &current, Name: "x",
		TrafficBytes: 1, Currency: "CNY", UnitAmount: 1,
	})
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
	archived, err := closed.SetTrafficPackStatus(ctx, tenantID, created.ID, actorID, "archived", &current)
	if err != nil || archived.Status != "archived" || audits("traffic_pack.archive") != 1 {
		t.Fatalf("archive with closed sales gate pack=%+v err=%v audits=%d",
			archived, err, audits("traffic_pack.archive"))
	}
	if active, err := svc.ListTrafficPacks(ctx, tenantID, "active"); err != nil || len(active) != 0 {
		t.Fatalf("active list after archive=%+v err=%v", active, err)
	}
	_, err = closed.SetTrafficPackStatus(ctx, tenantID, created.ID, actorID, "active", &archived.UpdatedAt)
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
	t.Log("marker=traffic_pack_admin_sales_gate_ok")

	// 4) 重复下架 409；重新上架写 restore 审计，回到在售列表。
	_, err = svc.SetTrafficPackStatus(ctx, tenantID, created.ID, actorID, "archived", &archived.UpdatedAt)
	requireCatalogErrorCode(t, err, httpx.CodeConflict)
	restored, err := svc.SetTrafficPackStatus(ctx, tenantID, created.ID, actorID, "active", &archived.UpdatedAt)
	if err != nil || restored.Status != "active" || audits("traffic_pack.restore") != 1 {
		t.Fatalf("restore pack=%+v err=%v audits=%d", restored, err, audits("traffic_pack.restore"))
	}
	if active, err := svc.ListTrafficPacks(ctx, tenantID, "active"); err != nil || len(active) != 1 {
		t.Fatalf("active list after restore=%+v err=%v", active, err)
	}
	t.Log("marker=traffic_pack_admin_archive_restore_ok")

	// 5) 不存在与格式不对的 id 都是中性 404。
	now := time.Now()
	for _, id := range []string{"73300000-0000-7000-8000-000000000099", "not-a-uuid"} {
		_, err = svc.SetTrafficPackStatus(ctx, tenantID, id, actorID, "archived", &now)
		requireCatalogErrorCode(t, err, httpx.CodeNotFound)
	}
	t.Log("marker=traffic_pack_admin_not_found_ok")
}
