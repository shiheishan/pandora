package service

// 路由组、组内路由与成员的 PostgreSQL 集成测试。

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestRouteGroupCreateDuplicateNamePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	base := e.name("dup")
	if _, err := e.svc.CreateRouteGroup(ctx, RouteGroupCreate{Name: base, SortOrder: 0}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.CreateRouteGroup(ctx, RouteGroupCreate{Name: " " + strings.ToUpper(base) + " ", SortOrder: 0})
	_ = invWantCode(t, err, errs.Conflict)
	if fieldErr(err, "name") == "" {
		t.Fatalf("期望 Fields name，得到 %v", err)
	}
}

func TestRouteGroupCreateValidationPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	_, err := e.svc.CreateRouteGroup(ctx, RouteGroupCreate{Name: "   ", SortOrder: 0})
	_ = invWantCode(t, err, errs.ValidationFailed)

	long := strings.Repeat("字", 65)
	_, err = e.svc.CreateRouteGroup(ctx, RouteGroupCreate{Name: long, SortOrder: 0})
	_ = invWantCode(t, err, errs.ValidationFailed)

	_, err = e.svc.CreateRouteGroup(ctx, RouteGroupCreate{Name: e.name("sort"), SortOrder: 1_000_001})
	_ = invWantCode(t, err, errs.ValidationFailed)
	if fieldErr(err, "sort_order") == "" {
		t.Fatal("期望 sort_order 字段")
	}
}

func TestRouteGroupListMembersCountsPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	active := rtNode(t, ctx, e, srv, "active", 21001)
	gone := rtNode(t, ctx, e, srv, "active", 21002)
	g := rtGroup(t, ctx, e, "list-g", 5)
	joinRouteGroup(t, ctx, e, g.ID, active, gone)
	// 入组之后再删：成员数只算未删除的节点（加成员时就拒绝已删节点）
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'retired', retired_at = now(), deleted_at = now() WHERE id = ?`, gone); err != nil {
		t.Fatal(err)
	}
	grt, _ := e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("ob1", "socks")},
		Routes:    []RoutingRule{rule(`{"port":[25]}`, "direct")},
	}); err != nil {
		t.Fatal(err)
	}
	g2 := rtGroup(t, ctx, e, "list-g2", 1)

	list, err := e.svc.ListRouteGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found RouteGroup
	for _, row := range list {
		if row.ID == g.ID {
			found = row
		}
	}
	if found.ID == uuid.Nil {
		t.Fatal("列表里没有测试组")
	}
	if len(found.Members) != 1 || found.Members[0].ID != active {
		t.Fatalf("成员 %v，期望只有 active %s", found.Members, active)
	}
	if found.OutboundCount != 1 || found.RuleCount != 1 {
		t.Fatalf("计数 ob=%d rule=%d", found.OutboundCount, found.RuleCount)
	}
	var ordG, ordG2 int
	for i, row := range list {
		if row.ID == g.ID {
			ordG = i
		}
		if row.ID == g2.ID {
			ordG2 = i
		}
	}
	if ordG2 >= ordG {
		t.Fatalf("sort_order 小的组应靠前：g2@%d g@%d", ordG2, ordG)
	}
}

func TestRouteGroupPatchRowVersionPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	g := rtGroup(t, ctx, e, "patch-v", 0)
	name := e.name("new")
	_, _, err := e.svc.UpdateRouteGroup(ctx, g.ID, RouteGroupPatch{RowVersion: g.RowVersion + 1, Name: &name})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestRouteGroupPatchSortOrderAffectedPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	n1 := rtNode(t, ctx, e, srv, "active", 21010)
	n2 := rtNode(t, ctx, e, srv, "retired", 21011)
	g := rtGroup(t, ctx, e, "patch-so", 0)
	joinRouteGroup(t, ctx, e, g.ID, n1, n2)
	gr := routeGroupRow(t, ctx, e, g.ID)
	so := 99
	updated, affected, err := e.svc.UpdateRouteGroup(ctx, g.ID, RouteGroupPatch{RowVersion: gr.RowVersion, SortOrder: &so})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 1 {
		t.Fatalf("affected %d want 1", affected)
	}
	if updated.RowVersion != gr.RowVersion+1 {
		t.Fatalf("row_version %d want %d", updated.RowVersion, gr.RowVersion+1)
	}
}

func TestRouteGroupPatchNameOnlyAffectedPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	n1 := rtNode(t, ctx, e, srv, "active", 21012)
	g := rtGroup(t, ctx, e, "patch-nm", 0)
	joinRouteGroup(t, ctx, e, g.ID, n1)
	gr := routeGroupRow(t, ctx, e, g.ID)
	nm := e.name("renamed")
	_, affected, err := e.svc.UpdateRouteGroup(ctx, g.ID, RouteGroupPatch{RowVersion: gr.RowVersion, Name: &nm})
	if err != nil {
		t.Fatal(err)
	}
	if affected != 0 {
		t.Fatalf("只改名 affected %d want 0", affected)
	}
}

func TestRouteGroupRoutingConflictAndRefsPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21020)
	g := rtGroup(t, ctx, e, "grt", 0)
	gr, _ := e.svc.GetGroupRouting(ctx, g.ID)
	_, err := e.svc.SetGroupRouting(ctx, g.ID, gr.RowVersion+1, RoutingInput{})
	_ = invWantCode(t, err, errs.Conflict)

	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("priv", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	grt, _ := e.svc.GetGroupRouting(ctx, g.ID)
	_, err = e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[1]}`, "priv")},
	})
	_ = invWantCode(t, err, errs.ValidationFailed)

	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("GLOB", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	grt, _ = e.svc.GetGroupRouting(ctx, g.ID)
	wr, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("gin", "http")},
		Routes:    []RoutingRule{rule(`{"port":[2]}`, "GLOB")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wr.RowVersion != grt.RowVersion+1 {
		t.Fatalf("组 row_version %d", wr.RowVersion)
	}

	joinRouteGroup(t, ctx, e, g.ID, nid)
	ver = rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[3]}`, "gin")},
	}); err != nil {
		t.Fatal(err)
	}
	grt, _ = e.svc.GetGroupRouting(ctx, g.ID)
	_, err = e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("gone", "socks")},
	})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestRouteGroupMemberDanglingOnLeavePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21031)
	g := rtGroup(t, ctx, e, "mem-dang", 0)
	grt, _ := e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("inner", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	joinRouteGroup(t, ctx, e, g.ID, nid)
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[4]}`, "inner")},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.SetNodeRouteGroups(ctx, nid, rtNodeVersion(t, ctx, e, nid), nil)
	_ = invWantCode(t, err, errs.Conflict)
}

func TestRouteGroupNodeGroupsSyncPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21040)
	g1 := rtGroup(t, ctx, e, "ng1", 1)
	g2 := rtGroup(t, ctx, e, "ng2", 2)
	ver := rtNodeVersion(t, ctx, e, nid)
	_, err := e.svc.SetNodeRouteGroups(ctx, nid, ver, []uuid.UUID{uuid.New()})
	_ = invWantCode(t, err, errs.ValidationFailed)
	if fieldErr(err, "group_ids") == "" {
		t.Fatal("期望 group_ids")
	}

	ver = rtNodeVersion(t, ctx, e, nid)
	_, err = e.svc.SetNodeRouteGroups(ctx, nid, ver+1, []uuid.UUID{g1.ID})
	_ = invWantCode(t, err, errs.Conflict)

	ver = rtNodeVersion(t, ctx, e, nid)
	wr, err := e.svc.SetNodeRouteGroups(ctx, nid, ver, []uuid.UUID{g1.ID, g2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if wr.RowVersion != ver+1 {
		t.Fatalf("节点 row_version 返回 %d", wr.RowVersion)
	}
	gv1 := routeGroupRow(t, ctx, e, g1.ID).RowVersion
	gv2 := routeGroupRow(t, ctx, e, g2.ID).RowVersion

	ver = rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouteGroups(ctx, nid, ver, []uuid.UUID{g2.ID}); err != nil {
		t.Fatal(err)
	}
	if routeGroupRow(t, ctx, e, g1.ID).RowVersion != gv1+1 {
		t.Fatal("离开的组 row_version 应 +1")
	}
	if routeGroupRow(t, ctx, e, g2.ID).RowVersion != gv2 {
		t.Fatal("未变的组 row_version 不变")
	}
}

func TestRouteGroupDeletePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21050)
	g := rtGroup(t, ctx, e, "del", 0)
	gr := routeGroupRow(t, ctx, e, g.ID)
	_, err := e.svc.DeleteRouteGroup(ctx, g.ID, gr.RowVersion+1)
	_ = invWantCode(t, err, errs.Conflict)

	grt, _ := e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("delob", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	joinRouteGroup(t, ctx, e, g.ID, nid)
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[5]}`, "delob")},
	}); err != nil {
		t.Fatal(err)
	}
	gr = routeGroupRow(t, ctx, e, g.ID)
	_, err = e.svc.DeleteRouteGroup(ctx, g.ID, gr.RowVersion)
	_ = invWantCode(t, err, errs.Conflict)

	ver = rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{}); err != nil {
		t.Fatal(err)
	}
	g = rtGroup(t, ctx, e, "del2", 0)
	joinRouteGroup(t, ctx, e, g.ID, nid)
	nodeVer := rtNodeVersion(t, ctx, e, nid)
	grt, _ = e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("x", "socks")},
		Routes:    []RoutingRule{rule(`{"port":[6]}`, "direct")},
	}); err != nil {
		t.Fatal(err)
	}
	gr = routeGroupRow(t, ctx, e, g.ID)
	if _, err := e.svc.DeleteRouteGroup(ctx, g.ID, gr.RowVersion); err != nil {
		t.Fatal(err)
	}
	if rtNodeVersion(t, ctx, e, nid) != nodeVer+1 {
		t.Fatal("删组后成员节点 row_version +1")
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM route_groups WHERE id = ?`, g.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("组行应删除 n=%d err=%v", n, err)
	}
	if err := e.row(ctx, `SELECT count(*) FROM node_outbounds WHERE group_id = ?`, g.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("组出站应删 n=%d", n)
	}
	if err := e.row(ctx, `SELECT count(*) FROM route_group_members WHERE group_id = ?`, g.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("成员应删 n=%d", n)
	}
}

func TestRouteGroupConfigEpochMembersPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	rtCleanupGroups(t, e)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 21060)
	before := e.epoch(t, ConfigEpochKey)
	g := rtGroup(t, ctx, e, "ep-m", 0)
	joinRouteGroup(t, ctx, e, g.ID, nid)
	if e.epoch(t, ConfigEpochKey) != before+1 {
		t.Fatal("active 成员进组应 +1 纪元")
	}
}

func TestRouteGroupConfigEpochEmptyGroupPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	rtCleanupGroups(t, e)
	before := e.epoch(t, ConfigEpochKey)
	_ = rtGroup(t, ctx, e, "ep-empty", 0)
	if e.epoch(t, ConfigEpochKey) != before {
		t.Fatal("建空组不应推进纪元")
	}
}

func TestRouteGroupConfigEpochRetiredOnlyPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	rtCleanupGroups(t, e)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "retired", 21061)
	before := e.epoch(t, ConfigEpochKey)
	g := rtGroup(t, ctx, e, "ep-ret", 0)
	joinRouteGroup(t, ctx, e, g.ID, nid)
	grt, _ := e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, grt.RowVersion, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[7]}`, "direct")},
	}); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, ConfigEpochKey) != before {
		t.Fatal("仅 retired 成员的组改路由不应推进纪元")
	}
}
