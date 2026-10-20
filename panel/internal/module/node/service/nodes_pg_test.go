package service

// 节点建改、复制、排序与纪元通知的 PG 集成测试。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNodeCreateDefaultsAndRedact(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "nd-cr")
	tcp := e.ssNode(t, ctx, srv.ID, "ss-tcp", 22001)
	var l4 string
	if err := e.row(ctx, `SELECT listen_l4 FROM nodes WHERE id = ?`, tcp.ID).Scan(&l4); err != nil {
		t.Fatal(err)
	}
	if l4 != "tcp+udp" {
		t.Fatalf("shadowsocks listen_l4=%q", l4)
	}
	if !tcp.TrafficRate.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("缺省倍率 1.00：%s", tcp.TrafficRate)
	}
	hy2, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("hy2-sec"), ServerID: &srv.ID, NodeType: "hysteria2",
		ServerHost: "203.0.113.10", ServerPort: 22002, ProtocolConfig: json.RawMessage(invHy2SecretField),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT listen_l4 FROM nodes WHERE id = ?`, hy2.ID).Scan(&l4); err != nil {
		t.Fatal(err)
	}
	if l4 != "udp" {
		t.Fatalf("hysteria2 listen_l4=%q", l4)
	}
	if strings.Contains(string(hy2.ProtocolConfig), invHy2Secret) {
		t.Fatalf("回包不应含明文密钥：%s", hy2.ProtocolConfig)
	}
	n443, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ss-443"), ServerID: &srv.ID, NodeType: "shadowsocks",
		ServerHost: "203.0.113.10", ServerPort: 443, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(n443.Warnings) == 0 {
		t.Fatal("443 在普通地址上应有一条警告")
	}
}

func TestNodeCreateConflicts(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "nd-conf")
	first := e.ssNode(t, ctx, srv.ID, "dup", 22010)
	err := e.invSavepoint(t, ctx, func() error {
		_, err := e.svc.CreateNode(ctx, NodeInput{
			Name: first.Name, ServerID: &srv.ID, NodeType: "shadowsocks",
			ServerHost: "203.0.113.10", ServerPort: 22011, ProtocolConfig: json.RawMessage(invSSConfig),
		})
		return err
	})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["name"] == "" {
		t.Fatalf("重名应标 name：%v", ee.Fields)
	}
	pool := e.pool(t, ctx, "disabled")
	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("bad-pool"), ServerID: &srv.ID, PoolID: &pool, NodeType: "shadowsocks",
		ServerHost: "203.0.113.10", ServerPort: 22012, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	_ = invWantCode(t, err, errs.ValidationFailed)
	small := e.server(t, ctx, "nd-cap")
	one := 1
	_, err = e.svc.PatchServer(ctx, small.ID, ServerPatch{RowVersion: small.RowVersion, CapacityNodes: &one})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.ssNode(t, ctx, small.ID, "full-1", 22013)
	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("full-2"), ServerID: &small.ID, NodeType: "shadowsocks",
		ServerHost: "203.0.113.10", ServerPort: 22014, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	_ = invWantCode(t, err, errs.Conflict)
	ret := e.server(t, ctx, "nd-ret")
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now() WHERE id = ?`, ret.ID); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("on-ret"), ServerID: &ret.ID, NodeType: "shadowsocks",
		ServerHost: "203.0.113.10", ServerPort: 22015, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestNodePatchSecretsAndPool(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "nd-patch")
	e.enroll(t, ctx, srv.ID)
	hy2, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("hy2-p"), ServerID: &srv.ID, NodeType: "hysteria2",
		ServerHost: "203.0.113.10", ServerPort: 22020, ProtocolConfig: json.RawMessage(invHy2SecretField),
	})
	if err != nil {
		t.Fatal(err)
	}
	stale := hy2.RowVersion + 1
	_, err = e.svc.PatchNode(ctx, hy2.ID, NodePatch{RowVersion: stale, ProtocolConfig: json.RawMessage(`{"bandwidth":{"up":100}}`)})
	_ = invWantCode(t, err, errs.Conflict)
	patchCfg := json.RawMessage(`{"cert_path":"/etc/pandora-native/certs/t.pem","key_path":"/etc/pandora-native/certs/t.key","bandwidth":{"up":100},"obfs":{"type":"salamander"}}`)
	got, err := e.svc.PatchNode(ctx, hy2.ID, NodePatch{RowVersion: hy2.RowVersion, ProtocolConfig: patchCfg})
	if err != nil {
		t.Fatal(err)
	}
	var pw string
	if err := e.row(ctx, `SELECT protocol_config->'obfs'->>'password' FROM nodes WHERE id = ?`, hy2.ID).Scan(&pw); err != nil {
		t.Fatal(err)
	}
	if pw != invHy2Secret {
		t.Fatalf("库里密码应保留：%q", pw)
	}
	ssType := "shadowsocks"
	_, err = e.svc.PatchNode(ctx, got.ID, NodePatch{
		RowVersion: got.RowVersion, NodeType: &ssType, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT protocol_config->'obfs'->>'password' FROM nodes WHERE id = ?`, hy2.ID).Scan(&pw); err == nil && pw == invHy2Secret {
		t.Fatal("换 shadowsocks 后不应仍留 hy2 的 obfs 密码")
	}
	pool := e.pool(t, ctx, "active")
	active := e.ssNode(t, ctx, srv.ID, "act-pool", 22021)
	if _, err := e.svc.ActivateNode(ctx, active.ID, active.RowVersion); err != nil {
		t.Fatal(err)
	}
	active, _ = e.svc.GetAdminNode(ctx, active.ID)
	_, err = e.svc.PatchNode(ctx, active.ID, NodePatch{RowVersion: active.RowVersion, PoolID: OptionalUUID{Set: true}})
	_ = invWantCode(t, err, errs.ValidationFailed)
	retired, err := e.svc.RetireNode(ctx, active.ID, active.RowVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.PatchNode(ctx, retired.ID, NodePatch{RowVersion: retired.RowVersion, PoolID: OptionalUUID{Set: true, Value: &pool}})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestNodePatchEpochOnlyWhenNeededCommitted(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "nd-ep")
	n := e.ssNode(t, ctx, srv.ID, "ep", 22030)
	full, err := e.svc.GetAdminNode(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	c0 := e.epoch(t, ConfigEpochKey)
	u0 := e.epoch(t, UsersEpochKey)
	got, err := e.svc.PatchNode(ctx, n.ID, NodePatch{
		RowVersion: full.RowVersion, ProtocolConfig: full.ProtocolConfig,
		ServerPort: full.ServerPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.RowVersion != full.RowVersion+1 {
		t.Fatalf("row_version 应 +1：%d→%d", full.RowVersion, got.RowVersion)
	}
	if e.epoch(t, ConfigEpochKey) != c0 {
		t.Fatal("原样带回协议与端口时不应推进 ConfigEpoch")
	}
	port := *full.ServerPort + 1
	_, err = e.svc.PatchNode(ctx, got.ID, NodePatch{RowVersion: got.RowVersion, ServerPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, ConfigEpochKey) != c0+1 {
		t.Fatalf("改端口应 ConfigEpoch +1")
	}
	pool := e.pool(t, ctx, "active")
	full, _ = e.svc.GetAdminNode(ctx, n.ID)
	c1 := e.epoch(t, ConfigEpochKey)
	_, err = e.svc.PatchNode(ctx, n.ID, NodePatch{
		RowVersion: full.RowVersion, PoolID: OptionalUUID{Set: true, Value: &pool},
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, UsersEpochKey) != u0+1 {
		t.Fatal("换池应 UsersEpoch +1")
	}
	if e.epoch(t, ConfigEpochKey) != c1 {
		t.Fatal("换池不应动 ConfigEpoch")
	}
}

func TestNodeCopyAndReorder(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "nd-copy")
	other := e.server(t, ctx, "nd-copy2")
	src := e.ssNode(t, ctx, srv.ID, "src", 22040)
	_, err := e.svc.CopyNode(ctx, src.ID, NodeCopyInput{RowVersion: src.RowVersion})
	if ee := invWantCode(t, err, errs.Conflict); !strings.Contains(ee.Fields["server_port"], src.Name) {
		t.Fatalf("同机同端口应点名原节点：%v", ee.Fields)
	}
	port := 22041
	dup, err := e.svc.CopyNode(ctx, src.ID, NodeCopyInput{RowVersion: src.RowVersion, ServerPort: &port})
	if err != nil {
		t.Fatal(err)
	}
	if dup.ServingStatus != NodeDraft || dup.Name != e.name("src")+"-copy" {
		t.Fatalf("副本：%+v", dup)
	}
	var cfgSrc, cfgCopy string
	if err := e.row(ctx, `SELECT protocol_config::text FROM nodes WHERE name = ?`, src.Name).Scan(&cfgSrc); err != nil {
		t.Fatal(err)
	}
	if err := e.row(ctx, `SELECT protocol_config::text FROM nodes WHERE id = ?`, dup.ID).Scan(&cfgCopy); err != nil {
		t.Fatal(err)
	}
	if cfgSrc != cfgCopy {
		t.Fatalf("protocol_config 应相同\n%q\n%q", cfgSrc, cfgCopy)
	}
	_, err = e.svc.CopyNode(ctx, src.ID, NodeCopyInput{RowVersion: src.RowVersion, Name: e.name("src-other"), TargetServerID: &other.ID})
	if err != nil {
		t.Fatal(err)
	}
	a := e.ssNode(t, ctx, srv.ID, "ord-1", 22050)
	b := e.ssNode(t, ctx, srv.ID, "ord-2", 22051)
	if err := e.svc.ReorderNodes(ctx, []NodeOrderItem{
		{ID: a.ID, RowVersion: a.RowVersion, SortOrder: 5},
		{ID: b.ID, RowVersion: b.RowVersion, SortOrder: 15},
	}); err != nil {
		t.Fatal(err)
	}
	var sa, sb int
	_ = e.row(ctx, `SELECT sort_order FROM nodes WHERE id = ?`, a.ID).Scan(&sa)
	_ = e.row(ctx, `SELECT sort_order FROM nodes WHERE id = ?`, b.ID).Scan(&sb)
	if sa != 5 || sb != 15 {
		t.Fatalf("排序应为 5/15，得 %d/%d", sa, sb)
	}
	a, _ = e.svc.GetAdminNode(ctx, a.ID)
	b, _ = e.svc.GetAdminNode(ctx, b.ID)
	err = e.svc.ReorderNodes(ctx, []NodeOrderItem{
		{ID: a.ID, RowVersion: a.RowVersion - 1, SortOrder: 99},
		{ID: b.ID, RowVersion: b.RowVersion, SortOrder: 88},
	})
	_ = invWantCode(t, err, errs.Conflict)
	_ = e.row(ctx, `SELECT sort_order FROM nodes WHERE id = ?`, a.ID).Scan(&sa)
	_ = e.row(ctx, `SELECT sort_order FROM nodes WHERE id = ?`, b.ID).Scan(&sb)
	if sa != 5 || sb != 15 {
		t.Fatalf("版本冲突后排序不应变：%d/%d", sa, sb)
	}
}

// 复制节点勾选复制路由：私有出站、规则和路由组成员一起复制，组版本推进；不勾选时副本没有路由。
func TestNodeCopyRouting(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := rtServer(t, ctx, e, "ready")
	src := rtNode(t, ctx, e, srv, "active", 21101)
	g := rtGroup(t, ctx, e, "copy-g", 0)
	gr, _ := e.svc.GetGroupRouting(ctx, g.ID)
	if _, err := e.svc.SetGroupRouting(ctx, g.ID, gr.RowVersion, RoutingInput{
		Outbounds: []RoutingOutbound{ob("gout", "socks")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetNodeRouteGroups(ctx, src, rtNodeVersion(t, ctx, e, src), []uuid.UUID{g.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SetNodeRouting(ctx, src, rtNodeVersion(t, ctx, e, src), RoutingInput{
		Outbounds: []RoutingOutbound{ob("priv", "socks")},
		Routes:    []RoutingRule{rule(`{"port":[1]}`, "priv"), rule(`{"port":[2]}`, "gout")},
	}); err != nil {
		t.Fatal(err)
	}
	groupVersion := routeGroupRow(t, ctx, e, g.ID).RowVersion

	port := 21102
	dup, err := e.svc.CopyNode(ctx, src, NodeCopyInput{RowVersion: rtNodeVersion(t, ctx, e, src), ServerPort: &port, CopyRouting: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.GetNodeRouting(ctx, dup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outbounds) != 1 || got.Outbounds[0].Tag != "priv" || len(got.Routes) != 2 {
		t.Fatalf("副本的私有路由：%+v", got)
	}
	if len(got.Groups) != 1 || got.Groups[0].ID != g.ID {
		t.Fatalf("副本的路由组：%+v", got.Groups)
	}
	if v := routeGroupRow(t, ctx, e, g.ID).RowVersion; v != groupVersion+1 {
		t.Fatalf("组加了成员，版本应 +1：%d → %d", groupVersion, v)
	}

	plainPort := 21103
	plain, err := e.svc.CopyNode(ctx, src, NodeCopyInput{RowVersion: rtNodeVersion(t, ctx, e, src), Name: e.name("copy-plain"), ServerPort: &plainPort})
	if err != nil {
		t.Fatal(err)
	}
	got, err = e.svc.GetNodeRouting(ctx, plain.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outbounds) != 0 || len(got.Routes) != 0 || len(got.Groups) != 0 {
		t.Fatalf("不勾选复制路由，副本应没有路由：%+v", got)
	}

	// 源节点有一条悬空规则（接口写不进去，直接插库）：复制路由会给副本造出新的悬空引用，拒绝
	if err := e.exec(ctx, `INSERT INTO node_routes (node_id, priority, sort_order, matcher, outbound_tag)
		VALUES (?, 9, 9, '{"port":[9]}'::jsonb, 'nope')`, src); err != nil {
		t.Fatal(err)
	}
	brokenPort := 21104
	_, err = e.svc.CopyNode(ctx, src, NodeCopyInput{RowVersion: rtNodeVersion(t, ctx, e, src), Name: e.name("copy-broken"), ServerPort: &brokenPort, CopyRouting: true})
	if ee := invWantCode(t, err, errs.Conflict); !strings.Contains(ee.Message, "nope") {
		t.Fatalf("应点名悬空的出站：%s", ee.Message)
	}
}
