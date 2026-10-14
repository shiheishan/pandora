package service

// 同机端口门禁的 PG 测试：同一台服务器同一端口上 L4 冲突的未退役节点先到先得（shadowsocks 是 tcp+udp，与同端口的 tcp、udp 都冲突）。

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNodePortClaimSameServerSameL4(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-a")
	first := e.ssNode(t, ctx, srv.ID, "ss-1", 20001)

	_, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ss-2"), ServerID: srv.ID, NodeType: "shadowsocks", ServerHost: "203.0.113.10",
		ServerPort: 20001, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], first.Name) {
		t.Fatalf("server_port 应写明占用者 %q：%v", first.Name, ee.Fields)
	}
}

func TestNodePortClaimTCPAndUDPShare(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-b")
	e.node(t, ctx, srv.ID, "mieru", "mieru", 20002, `{"transport":"TCP"}`)
	e.hy2Node(t, ctx, srv.ID, "hy2", 20002)

	var l4s []string
	for _, n := range []string{e.name("mieru"), e.name("hy2")} {
		var l4 string
		if err := e.row(ctx, `SELECT listen_l4 FROM nodes WHERE name = ?`, n).Scan(&l4); err != nil {
			t.Fatal(err)
		}
		l4s = append(l4s, l4)
	}
	if l4s[0] != "tcp" || l4s[1] != "udp" {
		t.Fatalf("listen_l4 = %v，期望 [tcp udp]", l4s)
	}
}

func TestNodePortClaimReleasedOnRetire(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-c")
	first := e.ssNode(t, ctx, srv.ID, "ss-1", 20003)
	if _, err := e.svc.RetireNode(ctx, first.ID, first.RowVersion, ""); err != nil {
		t.Fatal(err)
	}
	e.ssNode(t, ctx, srv.ID, "ss-2", 20003)
}

func TestNodePortClaimOtherServerIndependent(t *testing.T) {
	e, ctx := newInvEnv(t)
	a := e.server(t, ctx, "ports-d1")
	b := e.server(t, ctx, "ports-d2")
	e.ssNode(t, ctx, a.ID, "ss-a", 20004)
	e.ssNode(t, ctx, b.ID, "ss-b", 20004)
}

func TestNodePortClaimOnPatch(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-e")
	e.ssNode(t, ctx, srv.ID, "ss-1", 20005)
	second := e.ssNode(t, ctx, srv.ID, "ss-2", 20006)

	port := 20005
	_, err := e.svc.PatchNode(ctx, second.ID, NodePatch{RowVersion: second.RowVersion, ServerPort: &port})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], e.name("ss-1")) {
		t.Fatalf("server_port 应写明占用者：%v", ee.Fields)
	}

	// 只改名字：端口没变，不走门禁
	name := e.name("ss-2b")
	got, err := e.svc.PatchNode(ctx, second.ID, NodePatch{RowVersion: second.RowVersion, Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name || got.RowVersion != second.RowVersion+1 {
		t.Fatalf("改名结果不对：%+v", got)
	}
}

func TestNodePortClaimOnMove(t *testing.T) {
	e, ctx := newInvEnv(t)
	a := e.server(t, ctx, "ports-f1")
	b := e.server(t, ctx, "ports-f2")
	e.ssNode(t, ctx, b.ID, "ss-b", 20007)
	n := e.ssNode(t, ctx, a.ID, "ss-a", 20007)

	_, err := e.svc.MoveNode(ctx, n.ID, NodeMoveInput{RowVersion: n.RowVersion, ServerID: b.ID})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestNodePortReserved(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-g")
	_, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ssh"), ServerID: srv.ID, NodeType: "shadowsocks", ServerHost: "203.0.113.10",
		ServerPort: 22, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("应标在 server_port：%v", ee.Fields)
	}
}

// 面板端口在判断不了是不是面板机时只提示。
func TestNodePortPanelWarning(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-h")
	n := e.ssNode(t, ctx, srv.ID, "https", 443)
	if len(n.Warnings) == 0 {
		t.Fatal("443 应带一条面板端口的提示")
	}
}

func TestNodePortClaimShadowsocksTCPUDP(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-ss")
	ss := e.ssNode(t, ctx, srv.ID, "ss", 20011)
	var l4 string
	if err := e.row(ctx, `SELECT listen_l4 FROM nodes WHERE id = ?`, ss.ID).Scan(&l4); err != nil {
		t.Fatal(err)
	}
	if l4 != "tcp+udp" {
		t.Fatalf("shadowsocks listen_l4=%q", l4)
	}

	_, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("hy2"), ServerID: srv.ID, NodeType: "hysteria2", ServerHost: "203.0.113.10",
		ServerPort: 20011, ProtocolConfig: json.RawMessage(invHy2Config),
	})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], ss.Name) || !strings.Contains(ee.Fields["server_port"], "20011/UDP") {
		t.Fatalf("hy2 与 ss 冲突：%v", ee.Fields)
	}

	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("mieru"), ServerID: srv.ID, NodeType: "mieru", ServerHost: "203.0.113.10",
		ServerPort: 20011, ProtocolConfig: json.RawMessage(`{"transport":"TCP"}`),
	})
	ee = invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], ss.Name) {
		t.Fatalf("mieru 与 ss 冲突：%v", ee.Fields)
	}

	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("hy2-ok"), ServerID: srv.ID, NodeType: "hysteria2", ServerHost: "203.0.113.10",
		ServerPort: 20012, ProtocolConfig: json.RawMessage(invHy2Config),
	})
	if err != nil {
		t.Fatalf("不同端口应成功：%v", err)
	}
}

func TestNodePortClaimShadowsocksAfterUDP(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-ss2")
	e.hy2Node(t, ctx, srv.ID, "hy2", 20013)

	_, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("ss"), ServerID: srv.ID, NodeType: "shadowsocks", ServerHost: "203.0.113.10",
		ServerPort: 20013, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], e.name("hy2")) || !strings.Contains(ee.Fields["server_port"], "20013/TCP+UDP") {
		t.Fatalf("ss 后建与 hy2 冲突：%v", ee.Fields)
	}
}

func TestNodePortClaimShadowsocksOnPatch(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "ports-ss3")
	e.hy2Node(t, ctx, srv.ID, "hy2", 20014)
	ss := e.ssNode(t, ctx, srv.ID, "ss", 20015)

	port := 20014
	_, err := e.svc.PatchNode(ctx, ss.ID, NodePatch{RowVersion: ss.RowVersion, ServerPort: &port})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.Contains(ee.Fields["server_port"], e.name("hy2")) {
		t.Fatalf("改端口与 hy2 冲突：%v", ee.Fields)
	}
}
