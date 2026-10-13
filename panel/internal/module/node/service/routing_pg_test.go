package service

// 全局与节点私有路由、生效预览的 PostgreSQL 集成测试。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func TestRoutingGlobalEmptyRevisionPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	g, err := e.svc.GetGlobalRouting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Revision == "" {
		t.Fatal("空库全局路由也应返回 revision")
	}
}

func TestRoutingGlobalPutRevisionAndAffectedPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	rtNode(t, ctx, e, srv, "active", 20001)
	rtNode(t, ctx, e, srv, "retired", 20002)

	g0, err := e.svc.GetGlobalRouting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantLive, err := e.svc.repo.AllLiveNodeCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if wantLive < 1 {
		t.Fatalf("本用例至少应有 1 个未退役节点，AllLiveNodeCount=%d", wantLive)
	}

	pub, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G0", "socks")},
		Routes:    []RoutingRule{rule(`{"domain_suffix":["t.example"]}`, "G0")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pub.Revision == "" || pub.Revision == g0.Revision {
		t.Fatalf("期望新 revision，得到 %q", pub.Revision)
	}
	if pub.AffectedNodes != wantLive {
		t.Fatalf("affected %d，期望 AllLiveNodeCount %d", pub.AffectedNodes, wantLive)
	}
	if pub.AffectedNodes < 1 {
		t.Fatal("affected 应至少为 1")
	}
}

func TestRoutingGlobalStaleRevisionPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	if _, err := e.svc.GetGlobalRouting(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.SetGlobalRouting(ctx, "deadbeef", RoutingInput{
		Outbounds: []RoutingOutbound{ob("x", "socks")},
	})
	_ = invWantCode(t, err, errs.Conflict)
	if fieldErr(err, "expected_revision") == "" {
		t.Fatalf("期望 Fields expected_revision，得到 %v", err)
	}
}

func TestRoutingGlobalUnknownOutboundRefPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	g, err := e.svc.GetGlobalRouting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.SetGlobalRouting(ctx, g.Revision, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[443]}`, "missing-out")},
	})
	_ = invWantCode(t, err, errs.ValidationFailed)
}

func TestRoutingNodeNotFoundPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	_, err := e.svc.GetNodeRouting(ctx, uuid.New())
	_ = invWantCode(t, err, errs.NotFound)
}

func TestRoutingNodeRowVersionConflictPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20100)
	ver := rtNodeVersion(t, ctx, e, nid)
	_, err := e.svc.SetNodeRouting(ctx, nid, ver+1, RoutingInput{})
	_ = invWantCode(t, err, errs.Conflict)
	if fieldErr(err, "row_version") == "" {
		t.Fatalf("期望 row_version 字段，得到 %v", err)
	}
}

func TestRoutingNodePutBumpsRowVersionPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20101)
	ver := rtNodeVersion(t, ctx, e, nid)
	newVer, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[80]}`, "direct")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if newVer != ver+1 {
		t.Fatalf("返回 rowVersion %d，期望 %d", newVer, ver+1)
	}
	if rtNodeVersion(t, ctx, e, nid) != ver+1 {
		t.Fatal("库里 row_version 未 +1")
	}
}

func TestRoutingNodeRefsGlobalAndGroupOutboundPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20102)
	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G1", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	grp := rtGroup(t, ctx, e, "rg-a", 1)
	gr, _ := e.svc.GetGroupRouting(ctx, grp.ID)
	if _, err := e.svc.SetGroupRouting(ctx, grp.ID, gr.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("GR1", "http")},
	}); err != nil {
		t.Fatal(err)
	}
	gr2 := routeGroupRow(t, ctx, e, grp.ID)
	if _, err := e.svc.SetRouteGroupMembers(ctx, grp.ID, gr2.RowVersion, []uuid.UUID{nid}); err != nil {
		t.Fatal(err)
	}
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{
			rule(`{"port":[1]}`, "G1"),
			rule(`{"port":[2]}`, "GR1"),
		},
	}); err != nil {
		t.Fatal(err)
	}

	other := rtGroup(t, ctx, e, "rg-b", 2)
	ogr, _ := e.svc.GetGroupRouting(ctx, other.ID)
	if _, err := e.svc.SetGroupRouting(ctx, other.ID, ogr.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("OTHER", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	ver = rtNodeVersion(t, ctx, e, nid)
	_, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[3]}`, "OTHER")},
	})
	_ = invWantCode(t, err, errs.ValidationFailed)
}

func TestRoutingDanglingRefBlocksNewDeletePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nA := rtNode(t, ctx, e, srv, "active", 20110)
	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G1", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	ver := rtNodeVersion(t, ctx, e, nA)
	if _, err := e.svc.SetNodeRouting(ctx, nA, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[9]}`, "G1")},
	}); err != nil {
		t.Fatal(err)
	}
	g1, _ := e.svc.GetGlobalRouting(ctx)
	_, err := e.svc.SetGlobalRouting(ctx, g1.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G2", "socks")},
	})
	_ = invWantCode(t, err, errs.Conflict)
	if !strings.Contains(errs.As(err).Message, "G1") {
		t.Fatalf("消息应含 G1 与节点名，得到 %q", errs.As(err).Message)
	}
}

func TestRoutingPreexistingDanglingAllowsGlobalPutPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nB := rtNode(t, ctx, e, srv, "active", 20111)
	if err := e.exec(ctx, `INSERT INTO node_routes (node_id, priority, sort_order, matcher, outbound_tag, enabled)
		VALUES (?, 10, 0, '{}'::jsonb, 'nope', true)`, nB); err != nil {
		t.Fatal(err)
	}
	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("only", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingSoftDeletedNodeSkipsDanglingPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nC := rtNode(t, ctx, e, srv, "active", 20112)
	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G2", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	ver := rtNodeVersion(t, ctx, e, nC)
	if _, err := e.svc.SetNodeRouting(ctx, nC, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[8]}`, "G2")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'retired', retired_at = now(), deleted_at = now() WHERE id = ?`, nC); err != nil {
		t.Fatal(err)
	}
	g1, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g1.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("G3", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingBuiltinOutboundCanonicalPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20120)
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{{Matcher: json.RawMessage(`{"port":[1]}`), OutboundTag: " Direct ", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	nr, err := e.svc.GetNodeRouting(ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	if len(nr.Routes) != 1 || nr.Routes[0].OutboundTag != "direct" {
		t.Fatalf("读回 outbound_tag %q", nr.Routes[0].OutboundTag)
	}
}

func TestRoutingDuplicateTagSameScopePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20121)
	ver := rtNodeVersion(t, ctx, e, nid)
	_, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("HK", "socks"), ob("hk", "http")},
	})
	_ = invWantCode(t, err, errs.ValidationFailed)

	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("HK", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	ver = rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("HK", "http")},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingRulePriorityOrderPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20122)
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("o1", "socks"), ob("o2", "http"), ob("o3", "socks")},
		Routes: []RoutingRule{
			{Priority: 30, Matcher: json.RawMessage(`{"port":[30]}`), OutboundTag: "o1", Enabled: true},
			{Priority: 10, Matcher: json.RawMessage(`{"port":[10]}`), OutboundTag: "o2", Enabled: true},
			{Priority: 20, Matcher: json.RawMessage(`{"port":[20]}`), OutboundTag: "o3", Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	nr, _ := e.svc.GetNodeRouting(ctx, nid)
	if len(nr.Routes) != 3 {
		t.Fatalf("routes %d", len(nr.Routes))
	}
	for i, wantP := range []int{10, 20, 30} {
		if nr.Routes[i].Priority != wantP {
			t.Fatalf("[%d] priority %d want %d", i, nr.Routes[i].Priority, wantP)
		}
	}

	ver = rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("z", "socks")},
		Routes: []RoutingRule{
			{Priority: 0, Matcher: json.RawMessage(`{"port":[1]}`), OutboundTag: "z", Enabled: true},
			{Priority: 0, Matcher: json.RawMessage(`{"port":[2]}`), OutboundTag: "z", Enabled: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	nr, _ = e.svc.GetNodeRouting(ctx, nid)
	for i, wantP := range []int{10, 20} {
		if nr.Routes[i].Priority != wantP {
			t.Fatalf("补 priority [%d] got %d want %d", i, nr.Routes[i].Priority, wantP)
		}
	}
}

func TestRoutingEffectivePreviewLayersPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20130)

	g0, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g0.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{ob("A", "socks"), ob("X", "http")},
		Routes: []RoutingRule{
			rule(`{"port":[1]}`, "A"),
			rule(`{"port":[2]}`, "X"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	g1 := rtGroup(t, ctx, e, "pv-g1", 1)
	gr1, _ := e.svc.GetGroupRouting(ctx, g1.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g1.ID, gr1.RowVersion, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[11]}`, "X")},
	}); err != nil {
		t.Fatal(err)
	}
	g2 := rtGroup(t, ctx, e, "pv-g2", 2)
	gr2, _ := e.svc.GetGroupRouting(ctx, g2.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g2.ID, gr2.RowVersion, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[12]}`, "block")},
	}); err != nil {
		t.Fatal(err)
	}
	gg1 := routeGroupRow(t, ctx, e, g1.ID)
	if _, err := e.svc.SetRouteGroupMembers(ctx, g1.ID, gg1.RowVersion, []uuid.UUID{nid}); err != nil {
		t.Fatal(err)
	}
	gg2 := routeGroupRow(t, ctx, e, g2.ID)
	if _, err := e.svc.SetRouteGroupMembers(ctx, g2.ID, gg2.RowVersion, []uuid.UUID{nid}); err != nil {
		t.Fatal(err)
	}
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Outbounds: []RoutingOutbound{ob("X", "shadowsocks")},
		Routes: []RoutingRule{
			rule(`{"port":[100]}`, "X"),
			{Priority: 50, Matcher: json.RawMessage(`{"port":[99]}`), OutboundTag: "block", Enabled: false},
		},
	}); err != nil {
		t.Fatal(err)
	}

	eff, err := e.svc.PreviewNodeRouting(ctx, nid)
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Groups) != 2 || eff.Groups[0].SortOrder != 1 || eff.Groups[1].SortOrder != 2 {
		t.Fatalf("groups %v", eff.Groups)
	}
	// 节点 1 条（停用的不算）+ 两个组各 1 条 + 全局 2 条
	if len(eff.Routes) != 5 {
		t.Fatalf("routes %d want 5", len(eff.Routes))
	}
	wantScopes := []string{"node", "group", "group", "global", "global"}
	for i, want := range wantScopes {
		if eff.Routes[i].Source.Scope != want {
			t.Fatalf("route[%d] scope %q want %q", i, eff.Routes[i].Source.Scope, want)
		}
	}
	if eff.Routes[1].Source.GroupID == nil || *eff.Routes[1].Source.GroupID != g1.ID {
		t.Fatalf("第 2 条应来自 sort_order 小的组 g1：%+v", eff.Routes[1].Source)
	}
	if eff.Routes[0].Outbound != "X" || eff.Routes[3].Outbound != "A" || eff.Routes[4].Outbound != "X" {
		t.Fatalf("outbound tags on routes: %q %q %q", eff.Routes[0].Outbound, eff.Routes[3].Outbound, eff.Routes[4].Outbound)
	}

	var xCount int
	seenA := false
	for _, o := range eff.Outbounds {
		if o.Tag == "A" {
			seenA = true
		}
		if o.Tag == "X" {
			xCount++
			if o.Source.Scope != routingScopeNode {
				t.Fatalf("X source %q", o.Source.Scope)
			}
			if !seenA {
				t.Fatal("出站列表里 A 应在 X 前（全局层顺序）")
			}
		}
	}
	if xCount != 1 {
		t.Fatalf("出站 X 应只出现一次，得到 %d", xCount)
	}
}

func TestRoutingConfigEpochOnNodePutPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	rtCleanupGroups(t, e)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20140)
	before := e.epoch(t, ConfigEpochKey)
	ver := rtNodeVersion(t, ctx, e, nid)
	if _, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[44]}`, "direct")},
	}); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, ConfigEpochKey) != before+1 {
		t.Fatalf("成功 PUT 后纪元 %d，期望 %d", e.epoch(t, ConfigEpochKey), before+1)
	}
}

func TestRoutingConfigEpochUnchangedOn422PG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	rtCleanupGroups(t, e)
	srv := rtServer(t, ctx, e, "ready")
	nid := rtNode(t, ctx, e, srv, "active", 20141)
	before := e.epoch(t, ConfigEpochKey)
	ver := rtNodeVersion(t, ctx, e, nid)
	_, err := e.svc.SetNodeRouting(ctx, nid, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[45]}`, "no-such-out")},
	})
	_ = invWantCode(t, err, errs.ValidationFailed)
	if e.epoch(t, ConfigEpochKey) != before {
		t.Fatal("422 不应推进配置纪元")
	}
}
