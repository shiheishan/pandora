package service

// ServerConfigs 的 PostgreSQL 集成测试。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func cdClearGlobal(t *testing.T, ctx context.Context, e *invEnv) {
	t.Helper()
	g, err := e.svc.GetGlobalRouting(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetGlobalRouting(ctx, g.Revision, RoutingInput{}); err != nil {
		t.Fatal(err)
	}
}

func cdFixtureServers(t *testing.T, ctx context.Context, e *invEnv) (s1, s2, s3, s4, s5 uuid.UUID, n1, n2, n3, n4, n5, n6 uuid.UUID) {
	t.Helper()
	cdClearGlobal(t, ctx, e)
	s1 = rtServer(t, ctx, e, "ready")
	n1 = rtNode(t, ctx, e, s1, "active", 20001)
	n2 = rtNode(t, ctx, e, s1, "active", 20002)
	n3 = rtNode(t, ctx, e, s1, "disabled", 20003)
	n4 = rtNode(t, ctx, e, s1, "retired", 20004)

	s2 = rtServer(t, ctx, e, "draining")
	n5 = rtNode(t, ctx, e, s2, "active", 20005)

	s3 = rtServer(t, ctx, e, "maintenance")

	s4 = rtServer(t, ctx, e, "draft")
	n6 = rtNode(t, ctx, e, s4, "active", 20006)

	s5 = rtServer(t, ctx, e, "retired")
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now(), deleted_at = now() WHERE id = ?`, s5); err != nil {
		t.Fatal(err)
	}
	_ = rtNode(t, ctx, e, s5, "active", 20007)
	return s1, s2, s3, s4, s5, n1, n2, n3, n4, n5, n6
}

func cdFindDoc(docs []NodeConfigDoc, id uuid.UUID) NodeConfigDoc {
	for _, d := range docs {
		if d.NodeID == id {
			return d
		}
	}
	return NodeConfigDoc{}
}

func TestServerConfigsKeysAndOrderingPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	s1, s2, s3, s4, s5, n1, n2, _, _, n5, _ := cdFixtureServers(t, ctx, e)
	random := uuid.New()
	m, err := e.svc.ServerConfigs(ctx, []uuid.UUID{s1, s2, s3, s4, s5, random})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{s1, s2, s3, s4} {
		if _, ok := m[id]; !ok {
			t.Fatalf("缺少服务器 %s", id)
		}
	}
	if _, ok := m[s5]; ok {
		t.Fatal("已删服务器 S5 不应出现")
	}
	if _, ok := m[random]; ok {
		t.Fatal("随机 uuid 不应出现")
	}
	if len(m) != 4 {
		t.Fatalf("key 数量 %d want 4", len(m))
	}
	if len(m[s1]) != 2 {
		t.Fatalf("S1 节点数 %d want 2", len(m[s1]))
	}
	if compareUUID(m[s1][0].NodeID, m[s1][1].NodeID) >= 0 {
		t.Fatalf("S1 应按 id 升序: %s %s", m[s1][0].NodeID, m[s1][1].NodeID)
	}
	ids := map[uuid.UUID]bool{n1: true, n2: true}
	for _, d := range m[s1] {
		if !ids[d.NodeID] {
			t.Fatalf("S1 出现意外节点 %s", d.NodeID)
		}
	}
	if len(m[s2]) != 1 || m[s2][0].NodeID != n5 {
		t.Fatalf("S2 %v", m[s2])
	}
	if len(m[s3]) != 0 {
		t.Fatalf("S3 应为空切片，得到 %d", len(m[s3]))
	}
	if len(m[s4]) != 0 {
		t.Fatalf("S4 draft 无该跑节点，应为空，得到 %d", len(m[s4]))
	}
}

func TestServerConfigsNodeFieldsPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	s1, _, _, _, _, n1, _, _, _, _, _ := cdFixtureServers(t, ctx, e)
	m, err := e.svc.ServerConfigs(ctx, []uuid.UUID{s1})
	if err != nil {
		t.Fatal(err)
	}
	doc := cdFindDoc(m[s1], n1)
	if doc.JSON == nil {
		t.Fatal("无 N1 文档")
	}
	var body map[string]any
	if err := json.Unmarshal(doc.JSON, &body); err != nil {
		t.Fatal(err)
	}
	if body["server_port"] != float64(20001) {
		t.Fatalf("server_port %v", body["server_port"])
	}
	if _, ok := body["kernel"]; ok {
		t.Fatal("文档不应有 kernel")
	}
	if body["flow"] == nil {
		t.Fatal("应有 flow")
	}
	for _, bad := range []string{"name", "id", "traffic_rate", "outbounds", "routes", "final"} {
		if _, ok := body[bad]; ok {
			t.Fatalf("不应有键 %s", bad)
		}
	}
}

func TestServerConfigsRoutingMergePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	s1, _, _, _, _, n1, n2, _, _, _, _ := cdFixtureServers(t, ctx, e)
	gGlob := `{"server":"198.51.100.1","port":8388}`
	gPriv := `{"server":"203.0.113.9","port":9393}`
	g, _ := e.svc.GetGlobalRouting(ctx)
	if _, err := e.svc.SetGlobalRouting(ctx, g.Revision, RoutingInput{
		Outbounds: []RoutingOutbound{{
			Tag: "G", Type: "shadowsocks", Settings: json.RawMessage(gGlob),
		}},
		Routes: []RoutingRule{rule(`{"domain_suffix":["example.com"]}`, "G")},
	}); err != nil {
		t.Fatal(err)
	}
	grp := rtGroup(t, ctx, e, "cd-rg", 1)
	gr := routeGroupRow(t, ctx, e, grp.ID)
	if _, err := e.svc.SetRouteGroupMembers(ctx, grp.ID, gr.RowVersion, []uuid.UUID{n1}); err != nil {
		t.Fatal(err)
	}
	grt, _ := e.svc.GetGroupRouting(ctx, grp.ID)
	if _, err := e.svc.SetGroupRouting(ctx, grp.ID, grt.RowVersion, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[25]}`, "block")},
	}); err != nil {
		t.Fatal(err)
	}
	ver := rtNodeVersion(t, ctx, e, n1)
	if _, err := e.svc.SetNodeRouting(ctx, n1, ver, RoutingInput{
		Outbounds: []RoutingOutbound{{
			Tag: "G", Type: "shadowsocks", Settings: json.RawMessage(gPriv),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	m, err := e.svc.ServerConfigs(ctx, []uuid.UUID{s1})
	if err != nil {
		t.Fatal(err)
	}
	d1 := cdFindDoc(m[s1], n1)
	d2 := cdFindDoc(m[s1], n2)
	var b1, b2 map[string]any
	if err := json.Unmarshal(d1.JSON, &b1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(d2.JSON, &b2); err != nil {
		t.Fatal(err)
	}
	rts1, ok := b1["routes"].([]any)
	if !ok || len(rts1) != 2 {
		t.Fatalf("N1 routes %v", b1["routes"])
	}
	obs1, ok := b1["outbounds"].([]any)
	if !ok || len(obs1) != 1 {
		t.Fatalf("N1 outbounds %v", b1["outbounds"])
	}
	ob0 := obs1[0].(map[string]any)
	st, _ := json.Marshal(ob0["settings"])
	if !jsonEqual(string(st), gPriv) {
		t.Fatalf("N1 私有 G settings %s want %s", st, gPriv)
	}
	if b1["final"] != "direct" {
		t.Fatalf("final %v", b1["final"])
	}
	rts2, ok := b2["routes"].([]any)
	if !ok || len(rts2) != 1 {
		t.Fatalf("N2 routes %v", b2["routes"])
	}
	obs2, ok := b2["outbounds"].([]any)
	if !ok || len(obs2) != 1 {
		t.Fatalf("N2 outbounds %v", b2["outbounds"])
	}
	st2, _ := json.Marshal(obs2[0].(map[string]any)["settings"])
	if !jsonEqual(string(st2), gGlob) {
		t.Fatalf("N2 全局 G %s", st2)
	}
}

func jsonEqual(a string, b string) bool {
	var ma, mb any
	if json.Unmarshal([]byte(a), &ma) != nil || json.Unmarshal([]byte(b), &mb) != nil {
		return a == b
	}
	ja, _ := json.Marshal(ma)
	jb, _ := json.Marshal(mb)
	return string(ja) == string(jb)
}

func TestServerConfigsDeterministicDigestPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	s1, _, _, _, _, n1, n2, _, _, _, _ := cdFixtureServers(t, ctx, e)
	m1, err := e.svc.ServerConfigs(ctx, []uuid.UUID{s1})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := e.svc.ServerConfigs(ctx, []uuid.UUID{s1})
	if err != nil {
		t.Fatal(err)
	}
	d1a := cdFindDoc(m1[s1], n1)
	d1b := cdFindDoc(m2[s1], n1)
	if string(d1a.JSON) != string(d1b.JSON) || d1a.SHA256 != d1b.SHA256 {
		t.Fatal("N1 两次计算应相同")
	}
	h := sha256.Sum256(d1a.JSON)
	if d1a.SHA256 != hex.EncodeToString(h[:]) {
		t.Fatal("SHA256 与 JSON 不一致")
	}

	ver := rtNodeVersion(t, ctx, e, n2)
	if _, err := e.svc.SetNodeRouting(ctx, n2, ver, RoutingInput{
		Routes: []RoutingRule{rule(`{"port":[99]}`, "direct")},
	}); err != nil {
		t.Fatal(err)
	}
	m3, _ := e.svc.ServerConfigs(ctx, []uuid.UUID{s1})
	d1c := cdFindDoc(m3[s1], n1)
	d2c := cdFindDoc(m3[s1], n2)
	if d1c.SHA256 != d1a.SHA256 {
		t.Fatal("改 N2 后 N1 摘要应不变")
	}
	if d2c.SHA256 == cdFindDoc(m1[s1], n2).SHA256 {
		t.Fatal("改 N2 后 N2 摘要应变化")
	}
}
