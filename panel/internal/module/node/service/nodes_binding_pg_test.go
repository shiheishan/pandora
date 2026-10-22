package service

// 节点绑定服务器的 PG 测试：节点可以不绑服务器（暂不绑定、不占端口），绑定 / 改绑 / 解绑只许草稿或停用，
// 绑定时查容量与同机端口，解绑释放端口并清掉运行状态与配置回执，两者都推进纪元；未绑节点不能上线、不进下发与名单。

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestCreateUnboundNodePG(t *testing.T) {
	e, ctx := newInvEnv(t)

	// 不给服务器、端口、地址：建得出来，只是暂不绑定
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ub"), NodeType: "shadowsocks", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.ServerID != nil || n.ServerPort != nil || n.ServerHost != nil {
		t.Fatalf("未绑节点不应有服务器、端口、地址：%+v", n)
	}
	if n.ServingStatus != NodeDraft {
		t.Fatalf("新建节点应为草稿：%s", n.ServingStatus)
	}

	// 两个未绑节点给同一个端口都能建：未绑不占端口
	port := 23001
	for _, name := range []string{"ub-p1", "ub-p2"} {
		got, err := e.svc.CreateNode(ctx, NodeInput{
			Name: e.name(name), NodeType: "shadowsocks", ServerPort: port, ProtocolConfig: json.RawMessage(invSSConfig),
		})
		if err != nil {
			t.Fatalf("未绑节点 %s 建失败：%v", name, err)
		}
		if got.ServerID != nil || got.ServerPort == nil || *got.ServerPort != port {
			t.Fatalf("未绑节点 %s 的端口不对：%+v", name, got)
		}
	}

	// 给了服务器但端口为 0 → 422
	srv := e.server(t, ctx, "ub-srv")
	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ub-noport"), ServerID: &srv.ID, NodeType: "shadowsocks",
		ServerHost: "203.0.113.20", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("绑了服务器没端口应标在 server_port：%v", ee.Fields)
	}
}

func TestBindNodePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "bind-a")

	port := 23002
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("bind-n"), NodeType: "shadowsocks", ServerPort: port, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID, Reason: "上线前绑定"})
	if err != nil {
		t.Fatal(err)
	}
	if bound.ServerID == nil || *bound.ServerID != srv.ID {
		t.Fatalf("server_id 没绑上：%v", bound.ServerID)
	}
	if bound.ServerPort == nil || *bound.ServerPort != port {
		t.Fatalf("端口应保留 %d：%v", port, bound.ServerPort)
	}
	if bound.RowVersion != n.RowVersion+1 {
		t.Fatalf("绑定应推进版本：%d → %d", n.RowVersion, bound.RowVersion)
	}

	// 同一台、同端口再绑一次：幂等，不改版本
	same, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: bound.RowVersion, ServerID: srv.ID})
	if err != nil {
		t.Fatal(err)
	}
	if same.RowVersion != bound.RowVersion {
		t.Fatalf("幂等绑定不应改版本：%d → %d", bound.RowVersion, same.RowVersion)
	}

	// 没有端口的未绑节点：绑定不给端口 → 422；请求里给端口就成功
	bare, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("bind-bare"), NodeType: "shadowsocks", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.BindNode(ctx, bare.ID, NodeBindInput{RowVersion: bare.RowVersion, ServerID: srv.ID})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("没有端口应标在 server_port：%v", ee.Fields)
	}
	fill := 23003
	got, err := e.svc.BindNode(ctx, bare.ID, NodeBindInput{RowVersion: bare.RowVersion, ServerID: srv.ID, ServerPort: &fill})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID == nil || *got.ServerID != srv.ID || got.ServerPort == nil || *got.ServerPort != fill {
		t.Fatalf("带端口绑定结果不对：%+v", got)
	}
}

func TestBindNodePortConflictPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "bind-conf")
	e.ssNode(t, ctx, srv.ID, "busy", 23005)

	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("conf-n"), NodeType: "shadowsocks", ServerPort: 23005, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("同机端口冲突应标在 server_port：%v", ee.Fields)
	}

	other := 23006
	got, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID, ServerPort: &other})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerPort == nil || *got.ServerPort != other {
		t.Fatalf("换端口后应绑成功：%v", got.ServerPort)
	}
}

func TestBindNodeCapacityPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "bind-cap")
	one := 1
	if _, err := e.svc.PatchServer(ctx, srv.ID, ServerPatch{RowVersion: srv.RowVersion, CapacityNodes: &one}); err != nil {
		t.Fatal(err)
	}
	e.ssNode(t, ctx, srv.ID, "cap-1", 23007)

	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("cap-n"), NodeType: "shadowsocks", ServerPort: 23008, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["capacity_nodes"] == "" {
		t.Fatalf("容量满应标在 capacity_nodes：%v", ee.Fields)
	}
}

func TestRebindRefusesActivePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv, active := invActiveNode(t, e, ctx, "rebind-a", "active", 23009)
	other := e.server(t, ctx, "rebind-b")

	// 在役：改绑、解绑都是 409
	_, err := e.svc.BindNode(ctx, active.ID, NodeBindInput{RowVersion: active.RowVersion, ServerID: other.ID})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Message != nodeRebindActiveMsg {
		t.Fatalf("在役改绑文案不对：%q", ee.Message)
	}
	_, err = e.svc.UnbindNode(ctx, active.ID, NodeUnbindInput{RowVersion: active.RowVersion})
	ee = invWantCode(t, err, errs.Conflict)
	if ee.Message != nodeRebindActiveMsg {
		t.Fatalf("在役解绑文案不对：%q", ee.Message)
	}

	// 停用后可以先写上运行状态与配置回执，解绑要把它们清掉
	_, v := invServingOf(t, e, ctx, active.ID)
	if err := e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeDisabled, Reason: "改绑前停用",
		Items: []NodeVersionRef{{ID: active.ID, RowVersion: v}}}); err != nil {
		t.Fatal(err)
	}
	_, v = invServingOf(t, e, ctx, active.ID)
	if err := e.exec(ctx, `UPDATE nodes SET runtime_status = 'running', runtime_state_at = now(),
		applied_config_sha256 = repeat('a', 64), applied_config_at = now() WHERE id = ?`, active.ID); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.UnbindNode(ctx, active.ID, NodeUnbindInput{RowVersion: v, Reason: "换机"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID != nil {
		t.Fatalf("解绑后 server_id 应为空：%v", got.ServerID)
	}
	if got.ServerPort == nil || *got.ServerPort != 23009 {
		t.Fatalf("解绑后端口值应留着：%v", got.ServerPort)
	}
	var runtimeStatus, appliedSHA sql.NullString
	if err := e.row(ctx, `SELECT runtime_status, applied_config_sha256 FROM nodes WHERE id = ?`, active.ID).
		Scan(&runtimeStatus, &appliedSHA); err != nil {
		t.Fatal(err)
	}
	if runtimeStatus.Valid || appliedSHA.Valid {
		t.Fatalf("解绑应清掉运行状态与配置回执：%v %v", runtimeStatus, appliedSHA)
	}

	// 端口已释放：原服务器上同端口能再建
	e.ssNode(t, ctx, srv.ID, "reuse", 23009)

	// 退役后不能再绑
	retired, err := e.svc.RetireNode(ctx, active.ID, got.RowVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.BindNode(ctx, active.ID, NodeBindInput{RowVersion: retired.RowVersion, ServerID: srv.ID})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestActivateUnboundNodePG(t *testing.T) {
	e, ctx := newInvEnv(t)
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("act-ub"), NodeType: "shadowsocks", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_id"] != nodeUnboundActivateMsg {
		t.Fatalf("上线未绑节点应报 server_id：%v", ee.Fields)
	}

	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeActive,
		Items: []NodeVersionRef{{ID: n.ID, RowVersion: n.RowVersion}}})
	ee = invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_id"] != nodeUnboundActivateMsg {
		t.Fatalf("批量启用未绑节点应报 server_id：%v", ee.Fields)
	}
}

func TestBindUnbindBumpEpochsPG(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "bind-ep")
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ep-n"), NodeType: "shadowsocks", ServerPort: 23011, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}

	users, config := e.epoch(t, UsersEpochKey), e.epoch(t, ConfigEpochKey)
	bound, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID})
	if err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, UsersEpochKey) != users+1 || e.epoch(t, ConfigEpochKey) != config+1 {
		t.Fatalf("绑定后两个纪元应各 +1：users %d→%d config %d→%d",
			users, e.epoch(t, UsersEpochKey), config, e.epoch(t, ConfigEpochKey))
	}

	users, config = e.epoch(t, UsersEpochKey), e.epoch(t, ConfigEpochKey)
	if _, err := e.svc.UnbindNode(ctx, n.ID, NodeUnbindInput{RowVersion: bound.RowVersion}); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, UsersEpochKey) != users+1 || e.epoch(t, ConfigEpochKey) != config+1 {
		t.Fatalf("解绑后两个纪元应再各 +1：users %d→%d config %d→%d",
			users, e.epoch(t, UsersEpochKey), config, e.epoch(t, ConfigEpochKey))
	}
}

func TestUnboundNodeNotDeliveredPG(t *testing.T) {
	e, ctx := newDeliveryEnv(t)
	f := seedDeliveryFixture(t, ctx, e)

	// fixture 的 n1 在 p1（已绑套餐、不限组），把未绑节点放进同一个池
	var pool uuid.UUID
	if err := e.row(ctx, `SELECT pool_id FROM nodes WHERE id = ?`, f.n1).Scan(&pool); err != nil {
		t.Fatal(err)
	}
	id := e.insertID(t, ctx, `INSERT INTO nodes (name, display_name, pool_id, serving_status, node_type, server_host,
		server_port, listen_l4, protocol_schema_version, protocol_config)
		VALUES (?, 'unbound', ?, 'draft', 'vless', 'node.example.test', 23012, 'tcp', 1,
		'{"flow":"xtls-rprx-vision"}'::jsonb) RETURNING id`, poolRandName(t, "unbound"), pool)

	nodes, err := e.svc.DeliverableNodes(ctx, subsvc.NodeQuery{
		SubscriptionID: f.uA.ID, UserID: f.userA, PlanVersionID: f.v, UserGroupID: &f.gA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(lineNames(nodes), "unbound") {
		t.Fatalf("未绑节点不应出现在订阅线路里：%v", lineNames(nodes))
	}

	rosters, err := e.svc.Rosters(ctx, []uuid.UUID{f.s1, f.s2, f.s3, f.s4})
	if err != nil {
		t.Fatal(err)
	}
	for server, rs := range rosters {
		for _, r := range rs {
			if r.NodeID == id {
				t.Fatalf("服务器 %s 的名单里不应有未绑节点", server)
			}
		}
	}

	// 库里的 nodes_active_shape 兜底：未绑节点不能直接在役
	e.mustExec(t, ctx, `SAVEPOINT unbound_active`)
	if err := e.exec(ctx, `UPDATE nodes SET serving_status = 'active' WHERE id = ?`, id); err == nil {
		t.Fatal("未绑节点不应能直接在役")
	}
	e.mustExec(t, ctx, `ROLLBACK TO SAVEPOINT unbound_active`)
}

func TestNodeListShowsUnboundPG(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("list-ub"), NodeType: "shadowsocks", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, _, err := e.svc.QueryNodes(ctx, NodeQuery{ID: &n.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应取到 1 行，得 %d", len(rows))
	}
	r := rows[0]
	if r.ServerID != nil || r.Delivered {
		t.Fatalf("未绑节点不应下发：server=%v delivered=%v", r.ServerID, r.Delivered)
	}
	if r.DeliveryNote != nodeNoteUnbound {
		t.Fatalf("下发说明应为未绑服务器：%q", r.DeliveryNote)
	}
	if !r.Movable {
		t.Fatal("草稿未绑节点应可改绑 / 解绑")
	}
}
