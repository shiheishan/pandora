package service

// 改绑的 PG 测试（审查补的几条）：草稿从 A 改绑到 B 后 A 的端口释放、B 的端口被占；同机只换端口撞上别的节点回 409；
// 绑到不存在的服务器 422；对本来没绑的节点解绑是幂等的，版本号不变。

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestRebindMovesPortClaimPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	a := e.server(t, ctx, "mv-a")
	b := e.server(t, ctx, "mv-b")
	n := e.ssNode(t, ctx, a.ID, "mv-n", 23021)

	got, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: b.ID, Reason: "换机"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID == nil || *got.ServerID != b.ID || got.ServerPort == nil || *got.ServerPort != 23021 {
		t.Fatalf("改绑后应在 B、端口沿用：%+v", got)
	}

	// A 上同端口已释放
	e.ssNode(t, ctx, a.ID, "mv-reuse", 23021)

	// B 上同端口被它占住
	_, err = e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("mv-clash"), ServerID: &b.ID, NodeType: "shadowsocks", ServerPort: 23021,
		ServerHost: "203.0.113.21", ProtocolConfig: json.RawMessage(invSSConfig),
	})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("B 上同端口冲突应标在 server_port：%v", ee.Fields)
	}
}

func TestRebindSameServerPortConflictPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "sp-a")
	e.ssNode(t, ctx, srv.ID, "sp-busy", 23023)
	n := e.ssNode(t, ctx, srv.ID, "sp-n", 23022)

	// 同一台只换端口：撞上别的节点 → 409
	busy := 23023
	_, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID, ServerPort: &busy})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["server_port"] == "" {
		t.Fatalf("同机换端口冲突应标在 server_port：%v", ee.Fields)
	}

	// 换到空闲端口成功
	free := 23024
	got, err := e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: srv.ID, ServerPort: &free})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerPort == nil || *got.ServerPort != free {
		t.Fatalf("换到空闲端口应成功：%v", got.ServerPort)
	}
}

func TestBindMissingServerAndIdempotentUnbindPG(t *testing.T) {
	e, ctx := newInvEnv(t)
	n, err := e.svc.CreateNode(ctx, NodeInput{
		Name: e.name("miss-n"), NodeType: "shadowsocks", ServerPort: 23025, ProtocolConfig: json.RawMessage(invSSConfig),
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.svc.BindNode(ctx, n.ID, NodeBindInput{RowVersion: n.RowVersion, ServerID: uuid.New()})
	ee := invWantCode(t, err, errs.ValidationFailed)
	if ee.Fields["server_id"] == "" {
		t.Fatalf("服务器不存在应标在 server_id：%v", ee.Fields)
	}

	// 本来就没绑：解绑什么也不做，版本号不变
	got, err := e.svc.UnbindNode(ctx, n.ID, NodeUnbindInput{RowVersion: n.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID != nil || got.RowVersion != n.RowVersion {
		t.Fatalf("未绑节点解绑应幂等：server_id=%v 版本 %d → %d", got.ServerID, n.RowVersion, got.RowVersion)
	}
}
