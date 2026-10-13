package service

// 服务器后台编排的 PG 集成测试。

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestServerCreateGetListCounts(t *testing.T) {
	e, ctx := newInvEnv(t)
	created, err := e.svc.CreateServer(ctx, ServerInput{Name: e.name("srv-list"), PublicIPv4: "203.0.113.11"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != ServerDraft || created.CapacityNodes != 32 {
		t.Fatalf("新建应为 draft、容量 32：%+v", created)
	}
	got, err := e.svc.GetServer(ctx, created.ID)
	if err != nil || got.Name != created.Name {
		t.Fatalf("GetServer: %v %+v", err, got)
	}
	srv := e.server(t, ctx, "srv-nodes")
	_ = e.ssNode(t, ctx, srv.ID, "n1", 21001)
	gotSrv, _ := e.svc.GetServer(ctx, srv.ID)
	if gotSrv.NodeCount != 1 || gotSrv.ActiveNodeCount != 0 {
		t.Fatalf("计数应为 1/0：%+v", gotSrv)
	}
	all, err := e.svc.ListServers(ctx, ServerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !invServerListed(all, created.ID) {
		t.Fatal("列表应含新建服务器")
	}
	byStatus, err := e.svc.ListServers(ctx, ServerFilter{Status: ServerDraft})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range byStatus {
		if s.Status != ServerDraft {
			t.Fatalf("按 draft 筛出非 draft：%+v", s)
		}
	}
	byQ, err := e.svc.ListServers(ctx, ServerFilter{Query: "srv-list"})
	if err != nil {
		t.Fatal(err)
	}
	if !invServerListed(byQ, created.ID) {
		t.Fatalf("按 q 应命中 %q", created.Name)
	}
}

func invServerListed(list []Server, id uuid.UUID) bool {
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

func TestServerPatchVersionCapacityNameRegion(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "patch-a")
	region := "ap-east"
	_, err := e.svc.PatchServer(ctx, s.ID, ServerPatch{RowVersion: s.RowVersion, Region: &region})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.PatchServer(ctx, s.ID, ServerPatch{RowVersion: s.RowVersion, Name: &region})
	ee := invWantCode(t, err, errs.Conflict)
	if !strings.HasPrefix(ee.Fields["row_version"], "current=") {
		t.Fatalf("版本冲突字段：%v", ee.Fields)
	}
	s2 := e.server(t, ctx, "patch-b")
	_ = e.ssNode(t, ctx, s2.ID, "occ", 21002)
	_ = e.ssNode(t, ctx, s2.ID, "occ2", 21003)
	one := 1
	_, err = e.svc.PatchServer(ctx, s2.ID, ServerPatch{RowVersion: s2.RowVersion, CapacityNodes: &one})
	ee = invWantCode(t, err, errs.Conflict)
	if ee.Fields["capacity_nodes"] != "minimum=2" {
		t.Fatalf("容量下限：%v", ee.Fields)
	}
	dup := s.Name
	err = e.invSavepoint(t, ctx, func() error {
		_, err := e.svc.PatchServer(ctx, s2.ID, ServerPatch{RowVersion: s2.RowVersion, Name: &dup})
		return err
	})
	ee = invWantCode(t, err, errs.Conflict)
	if ee.Fields["name"] == "" {
		t.Fatalf("重名应标 name：%v", ee.Fields)
	}
	empty := ""
	got, err := e.svc.PatchServer(ctx, s.ID, ServerPatch{RowVersion: s.RowVersion + 1, Region: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if got.Region != nil {
		t.Fatalf("空串应清空 region：%+v", got.Region)
	}
}

func TestServerStatusIllegalAndReadyRequiresActive(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "st-ill")
	_, err := e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerReady, "")
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["nodes"] != "requires_valid_active_node" {
		t.Fatalf("无在役节点进 ready：%v", ee.Fields)
	}
	s = e.readyServer(t, ctx, "st-ready-ret")
	_, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerRetired, "")
	_ = invWantCode(t, err, errs.Conflict)
	s = e.server(t, ctx, "st-draft-drain")
	_, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerDraining, "")
	_ = invWantCode(t, err, errs.Conflict)
}

func TestServerStatusLegalPathToRetired(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "st-path")
	n := e.ssNode(t, ctx, s.ID, "st-node", 21003)
	s, err := e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerMaintenance, "m")
	if err != nil {
		t.Fatal(err)
	}
	s, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerDraining, "d")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion); err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.GetServer(ctx, s.ID)
	if s.Status != ServerReady {
		t.Fatalf("上线后服务器应为 ready，实际 %s", s.Status)
	}
	s, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerDraining, "again")
	if err != nil {
		t.Fatal(err)
	}
	s, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerRetired, "done")
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != ServerRetired {
		t.Fatalf("应为 retired：%+v", s)
	}
}

func TestServerStatusRetiredNoChange(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "st-ret")
	_, err := e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerRetired, "")
	if err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.GetServer(ctx, s.ID)
	_, err = e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerMaintenance, "")
	_ = invWantCode(t, err, errs.Conflict)
}

func TestServerDeleteReadyConflictDraftSuccess(t *testing.T) {
	e, ctx := newInvEnv(t)
	ready := e.readyServer(t, ctx, "del-ready")
	err := e.svc.DeleteServer(ctx, ready.ID, ready.RowVersion)
	_ = invWantCode(t, err, errs.Conflict)
	draft := e.server(t, ctx, "del-draft")
	id := draft.ID
	if err := e.svc.DeleteServer(ctx, id, draft.RowVersion); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.GetServer(ctx, id)
	_ = invWantCode(t, err, errs.NotFound)
	list, _ := e.svc.ListServers(ctx, ServerFilter{Query: draft.Name})
	if invServerListed(list, id) {
		t.Fatal("列表不应再含已删服务器")
	}
}

func TestServerDeleteRetiredDetachesActiveNodes(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "del-cascade")
	n := e.ssNode(t, ctx, s.ID, "live", 21004)
	if _, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion); err != nil {
		t.Fatal(err)
	}
	n, _ = e.svc.GetAdminNode(ctx, n.ID)
	if err := e.exec(ctx, `UPDATE servers SET status = 'retired', retired_at = now() WHERE id = ?`, s.ID); err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.GetServer(ctx, s.ID)
	var rvBefore int64
	if err := e.row(ctx, `SELECT row_version FROM nodes WHERE id = ?`, n.ID).Scan(&rvBefore); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteServer(ctx, s.ID, s.RowVersion); err != nil {
		t.Fatal(err)
	}
	var status string
	var serverID *uuid.UUID
	var retiredAt *string
	var rvAfter int64
	if err := e.row(ctx, `SELECT serving_status, server_id, retired_at::text, row_version FROM nodes WHERE id = ?`, n.ID).
		Scan(&status, &serverID, &retiredAt, &rvAfter); err != nil {
		t.Fatal(err)
	}
	if status != NodeRetired || serverID != nil || retiredAt == nil || rvAfter <= rvBefore {
		t.Fatalf("节点应退役、摘机、版本增：status=%s server=%v retired=%v rv %d→%d",
			status, serverID, retiredAt, rvBefore, rvAfter)
	}
}

func TestListServerNodesAndMissingServer(t *testing.T) {
	e, ctx := newInvEnv(t)
	s := e.server(t, ctx, "ls-nodes")
	n1 := e.ssNode(t, ctx, s.ID, "ord-a", 21005)
	n2 := e.ssNode(t, ctx, s.ID, "ord-b", 21006)
	if err := e.svc.ReorderNodes(ctx, []NodeOrderItem{
		{ID: n1.ID, RowVersion: n1.RowVersion, SortOrder: 20},
		{ID: n2.ID, RowVersion: n2.RowVersion, SortOrder: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RetireNode(ctx, n2.ID, n2.RowVersion+1, ""); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.ListServerNodes(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("应含退役节点：%d", len(rows))
	}
	if len(rows) != 2 || rows[0].ID != n2.ID || rows[1].ID != n1.ID || rows[0].ServingStatus != NodeRetired {
		t.Fatalf("应按 sort_order 列出、含已退役：%+v", rows)
	}
	_, err = e.svc.ListServerNodes(ctx, uuid.New())
	_ = invWantCode(t, err, errs.NotFound)
}

func TestServerSetStatusBumpsEpochsCommitted(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	s := e.server(t, ctx, "epoch-srv")
	n := e.ssNode(t, ctx, s.ID, "epoch-n", 21007)
	if _, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion); err != nil {
		t.Fatal(err)
	}
	s, _ = e.svc.GetServer(ctx, s.ID)
	u0, c0 := e.epoch(t, UsersEpochKey), e.epoch(t, ConfigEpochKey)
	_, err := e.svc.SetServerStatus(ctx, s.ID, s.RowVersion, ServerMaintenance, "notify")
	if err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, UsersEpochKey) != u0+1 || e.epoch(t, ConfigEpochKey) != c0+1 {
		t.Fatalf("改状态应各推进 1：users %d→%d config %d→%d",
			u0, e.epoch(t, UsersEpochKey), c0, e.epoch(t, ConfigEpochKey))
	}
}
