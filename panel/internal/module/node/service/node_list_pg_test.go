package service

// 后台节点列表的 PG 测试：分页与总数、筛选、单取详情、下发说明、服务器列。
// 库里可能有别的用例的节点，所以每个查询都用本用例的 tag 做 Search 收窄。

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// invPooledActive 在一台 ready、有心跳的服务器上建一个划进 active 池的在役节点。
func invPooledActive(t *testing.T, e *invEnv, ctx context.Context, srvName, name string, port int) (Server, AdminNode) {
	t.Helper()
	srv := e.readyServer(t, ctx, srvName)
	e.enroll(t, ctx, srv.ID)
	pool := e.pool(t, ctx, "active")
	n := e.ssNode(t, ctx, srv.ID, name, port)
	n, err := e.svc.PatchNode(ctx, n.ID, NodePatch{RowVersion: n.RowVersion, PoolID: OptionalUUID{Set: true, Value: &pool}})
	if err != nil {
		t.Fatal(err)
	}
	n, err = e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	return srv, n
}

// invFindRow 用本用例的 tag 收窄列表后按 id 找一行（不带详情）。
func invFindRow(t *testing.T, e *invEnv, ctx context.Context, id uuid.UUID) NodeRow {
	t.Helper()
	rows, _, err := e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, IncludeRetired: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("列表里没有节点 %s", id)
	return NodeRow{}
}

func TestQueryNodesPaging(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv := e.server(t, ctx, "list-a")
	for i := 0; i < 3; i++ {
		e.ssNode(t, ctx, srv.ID, "n"+strconv.Itoa(i), 22001+i)
	}
	rows, total, err := e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || total != 3 {
		t.Fatalf("第一页 %d 行、总数 %d", len(rows), total)
	}
	rows, total, err = e.svc.QueryNodes(ctx, NodeQuery{Search: e.tag, Limit: 2, Offset: 4})
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 || total != 3 {
		t.Fatalf("越界页 %v 行、总数 %d", rows, total)
	}
}

func TestQueryNodesFilters(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv := e.server(t, ctx, "list-b")
	draft := e.ssNode(t, ctx, srv.ID, "draft", 22011)
	gone := e.ssNode(t, ctx, srv.ID, "gone", 22012)
	if _, err := e.svc.RetireNode(ctx, gone.ID, gone.RowVersion, ""); err != nil {
		t.Fatal(err)
	}

	ids := func(q NodeQuery) map[uuid.UUID]bool {
		t.Helper()
		q.Limit = 100
		rows, _, err := e.svc.QueryNodes(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]bool{}
		for _, r := range rows {
			out[r.ID] = true
		}
		return out
	}
	if got := ids(NodeQuery{Search: e.tag}); !got[draft.ID] || got[gone.ID] {
		t.Fatalf("默认不含已退役：%v", got)
	}
	if got := ids(NodeQuery{Search: e.tag, IncludeRetired: true}); !got[draft.ID] || !got[gone.ID] {
		t.Fatalf("include_retired 应含全部：%v", got)
	}
	if got := ids(NodeQuery{Search: e.tag, State: "retired"}); got[draft.ID] || !got[gone.ID] {
		t.Fatalf("retired 只有已退役：%v", got)
	}
	if got := ids(NodeQuery{Search: e.tag, State: "disabled"}); !got[draft.ID] || got[gone.ID] {
		t.Fatalf("disabled 含草稿：%v", got)
	}
	if got := ids(NodeQuery{Search: strings.ToUpper(e.name("draft"))}); !got[draft.ID] {
		t.Fatalf("按名字（不分大小写）应命中：%v", got)
	}
	if got := ids(NodeQuery{Search: strconv.FormatInt(draft.NodeNo, 10)}); !got[draft.ID] {
		t.Fatalf("按编号应命中：%v", got)
	}

	_, _, err := e.svc.QueryNodes(ctx, NodeQuery{State: "bogus", Limit: 10})
	_ = invWantCode(t, err, errs.ValidationFailed)
	_, _, err = e.svc.QueryNodes(ctx, NodeQuery{Search: strings.Repeat("字", 121), Limit: 10})
	_ = invWantCode(t, err, errs.ValidationFailed)
}

func TestQueryNodesDetail(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv := e.server(t, ctx, "list-c")
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("hy2"), ServerID: &srv.ID, NodeType: "hysteria2", ServerHost: "203.0.113.10",
		ServerPort: 22021, ProtocolConfig: []byte(invHy2SecretField),
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err := e.svc.QueryNodes(ctx, NodeQuery{ID: &n.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || total != 1 || rows[0].Detail == nil {
		t.Fatalf("单取应得一行带 Detail：%d 行", len(rows))
	}
	d := rows[0].Detail
	if strings.Contains(string(d.ProtocolConfig), invHy2Secret) || d.ProtocolSchemaVersion <= 0 {
		t.Fatalf("详情不对：%s %d", d.ProtocolConfig, d.ProtocolSchemaVersion)
	}
	if !d.TrafficRate.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("倍率缺省应为 1：%s", d.TrafficRate)
	}

	// 列表不带 Detail
	if r := invFindRow(t, e, ctx, n.ID); r.Detail != nil {
		t.Fatal("列表行不应带 Detail")
	}
}

func TestQueryNodesDelivery(t *testing.T) {
	e, _, ctx := newInvListEnv(t)

	_, ok := invPooledActive(t, e, ctx, "dl-ok", "ok", 22031)
	if r := invFindRow(t, e, ctx, ok.ID); !r.Delivered || r.DeliveryNote != "" || r.Stale {
		t.Fatalf("正常节点应下发：%v %q stale=%v", r.Delivered, r.DeliveryNote, r.Stale)
	}

	srv := e.readyServer(t, ctx, "dl-nopool")
	e.enroll(t, ctx, srv.ID)
	np := e.ssNode(t, ctx, srv.ID, "nopool", 22032)
	if _, err := e.svc.ActivateNode(ctx, np.ID, np.RowVersion); err != nil {
		t.Fatal(err)
	}
	if r := invFindRow(t, e, ctx, np.ID); r.Delivered || r.DeliveryNote != "未划入节点池，不服务任何用户" {
		t.Fatalf("没划池：%v %q", r.Delivered, r.DeliveryNote)
	}

	never, nn := invPooledActive(t, e, ctx, "dl-never", "never", 22033)
	if err := e.exec(ctx, `UPDATE servers SET last_heartbeat_at = NULL WHERE id = ?`, never.ID); err != nil {
		t.Fatal(err)
	}
	if r := invFindRow(t, e, ctx, nn.ID); r.Delivered || !strings.Contains(r.DeliveryNote, "从未上报过心跳") || !r.Stale {
		t.Fatalf("从没心跳：%v %q", r.Delivered, r.DeliveryNote)
	}

	stale, sn := invPooledActive(t, e, ctx, "dl-stale", "stale", 22034)
	if err := e.exec(ctx, `UPDATE servers SET last_heartbeat_at = now() - interval '5 minutes' WHERE id = ?`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if r := invFindRow(t, e, ctx, sn.ID); !r.Delivered || !r.Stale {
		t.Fatalf("心跳 5 分钟前：delivered=%v stale=%v", r.Delivered, r.Stale)
	}

	_, dn := invPooledActive(t, e, ctx, "dl-degraded", "degraded", 22035)
	if err := e.exec(ctx, `UPDATE nodes SET runtime_status = 'degraded', runtime_state_at = now() WHERE id = ?`, dn.ID); err != nil {
		t.Fatal(err)
	}
	if r := invFindRow(t, e, ctx, dn.ID); !r.DeliveryDegraded || !strings.Contains(r.DeliveryNote, "运行异常") {
		t.Fatalf("运行异常：degraded=%v %q", r.DeliveryDegraded, r.DeliveryNote)
	}
}

func TestQueryNodesServerColumns(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv := e.server(t, ctx, "list-d")
	n := e.ssNode(t, ctx, srv.ID, "n", 22041)
	if err := e.exec(ctx, `UPDATE servers SET agent_version = '1.2.3', hostname = 'edge-1.example.test', cpu_cores = 4 WHERE id = ?`, srv.ID); err != nil {
		t.Fatal(err)
	}
	r := invFindRow(t, e, ctx, n.ID)
	if r.AgentVersion == nil || *r.AgentVersion != "1.2.3" || r.Hostname == nil || *r.Hostname != "edge-1.example.test" {
		t.Fatalf("服务器列不对：%v %v", r.AgentVersion, r.Hostname)
	}
	if r.CPUCores == nil || *r.CPUCores != 4 || r.ServerName == nil || *r.ServerName != srv.Name {
		t.Fatalf("服务器列不对：%v %v", r.CPUCores, r.ServerName)
	}
	if r.PublicIPv4 == nil || *r.PublicIPv4 != "203.0.113.10" {
		t.Fatalf("public_ipv4 应是纯地址：%v", r.PublicIPv4)
	}
}
