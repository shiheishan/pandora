package service

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNodeRelaysPG(t *testing.T) {
	env, ctx := newInvEnv(t)
	srv := env.readyServer(t, ctx, "relay-srv")
	node := env.ssNode(t, ctx, srv.ID, "relay-parent", 8388)

	// 1. 新建 → 列表能看到、字段对、Enabled 缺省 true、SNI 空串存成 nil
	emptySNI := ""
	hostHeader := "custom-host.example.com"
	created, err := env.svc.CreateNodeRelay(ctx, node.ID, NodeRelayInput{
		Name:       "中转-1",
		ServerHost: "relay-1.example.com",
		ServerPort: 8443,
		SNI:        &emptySNI,
		HostHeader: &hostHeader,
		SortOrder:  10,
	})
	if err != nil {
		t.Fatalf("CreateNodeRelay: %v", err)
	}

	if created.ID == uuid.Nil {
		t.Fatal("created.ID 不应为 Nil")
	}
	if created.ParentNodeID != node.ID {
		t.Fatalf("ParentNodeID = %v, want %v", created.ParentNodeID, node.ID)
	}
	if created.Name != "中转-1" || created.ServerHost != "relay-1.example.com" || created.ServerPort != 8443 {
		t.Errorf("基本字段不符合: %+v", created)
	}
	if !created.Enabled {
		t.Errorf("Enabled 应该缺省为 true")
	}
	if created.SNI != nil {
		t.Errorf("SNI 为空串时应存为 nil, got: %v", *created.SNI)
	}
	if created.HostHeader == nil || *created.HostHeader != "custom-host.example.com" {
		t.Errorf("HostHeader = %v, want custom-host.example.com", created.HostHeader)
	}
	if created.RowVersion != 1 {
		t.Errorf("RowVersion = %d, want 1", created.RowVersion)
	}

	// 查列表
	list, err := env.svc.ListNodeRelays(ctx, node.ID)
	if err != nil {
		t.Fatalf("ListNodeRelays: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("列表条数 = %d, want 1", len(list))
	}
	if list[0].ID != created.ID {
		t.Errorf("列表项 ID = %v, want %v", list[0].ID, created.ID)
	}

	// 2. 父节点不存在 → 404
	_, err = env.svc.CreateNodeRelay(ctx, uuid.New(), NodeRelayInput{
		Name:       "不存在父节点",
		ServerHost: "relay-x.example.com",
		ServerPort: 8443,
	})
	invWantCode(t, err, errs.NotFound)

	_, err = env.svc.ListNodeRelays(ctx, uuid.New())
	invWantCode(t, err, errs.NotFound)

	// SNI 给 IP 192.0.2.1 → 422 且 Fields 有 sni
	sniIP := "192.0.2.1"
	_, err = env.svc.CreateNodeRelay(ctx, node.ID, NodeRelayInput{
		Name:       "无效SNI",
		ServerHost: "relay-ip.example.com",
		ServerPort: 8443,
		SNI:        &sniIP,
	})
	invWantCode(t, err, errs.ValidationFailed)
	var e *errs.Error
	if errs.As(err, &e) {
		if _, ok := e.Fields["sni"]; !ok {
			t.Errorf("Fields 应包含 sni, got: %v", e.Fields)
		}
	}

	// 端口 0 → 422
	_, err = env.svc.CreateNodeRelay(ctx, node.ID, NodeRelayInput{
		Name:       "无效端口",
		ServerHost: "relay-port.example.com",
		ServerPort: 0,
	})
	invWantCode(t, err, errs.ValidationFailed)

	// 3. 第 21 条 → 409（当前已有 1 条，再循环建 19 条到达 20 条，第 21 条失败）
	for i := 2; i <= 20; i++ {
		_, err := env.svc.CreateNodeRelay(ctx, node.ID, NodeRelayInput{
			Name:       fmt.Sprintf("中转-%d", i),
			ServerHost: fmt.Sprintf("relay-%d.example.com", i),
			ServerPort: 8000 + i,
		})
		if err != nil {
			t.Fatalf("建第 %d 条中转失败: %v", i, err)
		}
	}

	_, err = env.svc.CreateNodeRelay(ctx, node.ID, NodeRelayInput{
		Name:       "中转-21",
		ServerHost: "relay-21.example.com",
		ServerPort: 8021,
	})
	invWantCode(t, err, errs.Conflict)

	// 4. Patch：改端口、清空 SNI（SNI: OptionalString{Set: true}）成功，RowVersion 加 1
	// 先将 created 改上一个 SNI
	sniDomain := "sni.example.com"
	patch1, err := env.svc.PatchNodeRelay(ctx, created.ID, NodeRelayPatch{
		RowVersion: created.RowVersion,
		SNI:        OptionalString{Set: true, Value: &sniDomain},
	})
	if err != nil {
		t.Fatalf("设置 SNI 失败: %v", err)
	}
	if patch1.SNI == nil || *patch1.SNI != sniDomain {
		t.Fatalf("patch1 SNI = %v, want %v", patch1.SNI, sniDomain)
	}

	newPort := 9443
	patch2, err := env.svc.PatchNodeRelay(ctx, created.ID, NodeRelayPatch{
		RowVersion: patch1.RowVersion,
		ServerPort: &newPort,
		SNI:        OptionalString{Set: true, Value: nil},
	})
	if err != nil {
		t.Fatalf("PatchNodeRelay 失败: %v", err)
	}
	if patch2.ServerPort != newPort {
		t.Errorf("ServerPort = %d, want %d", patch2.ServerPort, newPort)
	}
	if patch2.SNI != nil {
		t.Errorf("SNI 清空后应为 nil, got: %v", *patch2.SNI)
	}
	if patch2.RowVersion != patch1.RowVersion+1 {
		t.Errorf("RowVersion = %d, want %d", patch2.RowVersion, patch1.RowVersion+1)
	}

	// 用旧版本再改 → 409
	_, err = env.svc.PatchNodeRelay(ctx, created.ID, NodeRelayPatch{
		RowVersion: patch1.RowVersion,
		ServerPort: &newPort,
	})
	invWantCode(t, err, errs.Conflict)

	// 5. Delete：旧版本 → 409
	err = env.svc.DeleteNodeRelay(ctx, created.ID, patch1.RowVersion)
	invWantCode(t, err, errs.Conflict)

	// 正确版本删除
	err = env.svc.DeleteNodeRelay(ctx, created.ID, patch2.RowVersion)
	if err != nil {
		t.Fatalf("DeleteNodeRelay: %v", err)
	}

	// 6. 审计：查 SELECT count(*) FROM audit_events WHERE resource_type = 'node_relay' AND action = 'node.relay_create' >= 1
	var auditCount int
	err = env.row(ctx, `
		SELECT count(*)
		FROM audit_events
		WHERE resource_type = 'node_relay' AND action = 'node.relay_create'
	`).Scan(&auditCount)
	if err != nil {
		t.Fatalf("查询审计事件: %v", err)
	}
	if auditCount < 1 {
		t.Errorf("node.relay_create 审计计数 = %d, want >= 1", auditCount)
	}
}
