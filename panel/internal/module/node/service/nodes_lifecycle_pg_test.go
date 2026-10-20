package service

// 节点生命周期的 PG 测试：批量改状态、上线、退役、删除、迁移、强制重新下发与纪元通知。

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// invServingOf 用 SQL 读节点的服务状态与版本（含已删除的行）。
func invServingOf(t *testing.T, e *invEnv, ctx context.Context, id uuid.UUID) (string, int64) {
	t.Helper()
	var status string
	var version int64
	if err := e.row(ctx, `SELECT serving_status, row_version FROM nodes WHERE id = ?`, id).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	return status, version
}

// invActiveNode 在一台 ready 的服务器上建一个节点并上线。
func invActiveNode(t *testing.T, e *invEnv, ctx context.Context, srvName, name string, port int) (Server, AdminNode) {
	t.Helper()
	srv := e.readyServer(t, ctx, srvName)
	e.enroll(t, ctx, srv.ID)
	n := e.ssNode(t, ctx, srv.ID, name, port)
	got, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatalf("上线: %v", err)
	}
	return srv, got
}

func TestBatchNodeStatusNeedsReadyServer(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "batch-a")
	e.enroll(t, ctx, srv.ID)
	n := e.ssNode(t, ctx, srv.ID, "n", 21001)

	err := e.svc.BatchNodeStatus(ctx, NodeStatusBatch{Items: []NodeVersionRef{{ID: n.ID, RowVersion: n.RowVersion}}, ServingStatus: NodeActive})
	_ = invWantCode(t, err, errs.Conflict)

	if err := e.exec(ctx, `UPDATE servers SET status = 'ready', last_heartbeat_at = now() WHERE id = ?`, srv.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.BatchNodeStatus(ctx, NodeStatusBatch{Items: []NodeVersionRef{{ID: n.ID, RowVersion: n.RowVersion}}, ServingStatus: NodeActive}); err != nil {
		t.Fatal(err)
	}
	if st, v := invServingOf(t, e, ctx, n.ID); st != NodeActive || v != n.RowVersion+1 {
		t.Fatalf("状态 %s 版本 %d", st, v)
	}
}

func TestBatchNodeStatusAllOrNothing(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.readyServer(t, ctx, "batch-b")
	e.enroll(t, ctx, srv.ID)
	a := e.ssNode(t, ctx, srv.ID, "a", 21002)
	b := e.ssNode(t, ctx, srv.ID, "b", 21003)
	b, err := e.svc.RetireNode(ctx, b.ID, b.RowVersion, "")
	if err != nil {
		t.Fatal(err)
	}

	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeActive, Items: []NodeVersionRef{
		{ID: a.ID, RowVersion: a.RowVersion}, {ID: b.ID, RowVersion: b.RowVersion},
	}})
	_ = invWantCode(t, err, errs.Conflict)
	if st, v := invServingOf(t, e, ctx, a.ID); st != NodeDraft || v != a.RowVersion {
		t.Fatalf("整批失败后 a 不应改动：%s %d", st, v)
	}

	// 旧版本
	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeActive, Items: []NodeVersionRef{{ID: a.ID, RowVersion: a.RowVersion + 5}}})
	ee := invWantCode(t, err, errs.Conflict)
	if ee.Fields["row_version"] == "" {
		t.Fatalf("版本冲突应带 row_version：%v", ee.Fields)
	}
	// 未知状态、不存在的 id
	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: "draining", Items: []NodeVersionRef{{ID: a.ID, RowVersion: a.RowVersion}}})
	_ = invWantCode(t, err, errs.ValidationFailed)
	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeDisabled, Items: []NodeVersionRef{{ID: uuid.New(), RowVersion: 1}}})
	_ = invWantCode(t, err, errs.NotFound)
}

// 两个请求拿同一个版本改同一个节点：恰好一个成功、一个 409。
func TestBatchNodeStatusConcurrent(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	_, n := invActiveNode(t, e, ctx, "batch-c", "n", 21004)

	var wg sync.WaitGroup
	errsOut := make([]error, 2)
	for i := range errsOut {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errsOut[i] = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeDisabled,
				Items: []NodeVersionRef{{ID: n.ID, RowVersion: n.RowVersion}}})
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range errsOut {
		var ee *errs.Error
		switch {
		case err == nil:
			ok++
		case errors.As(err, &ee) && ee.Code == errs.Conflict:
			conflict++
		default:
			t.Fatalf("意外错误: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("成功 %d 冲突 %d，期望各 1", ok, conflict)
	}
}

func TestActivateNodeReadiesServer(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "act-a")
	e.enroll(t, ctx, srv.ID)
	n := e.ssNode(t, ctx, srv.ID, "n", 21005)

	got, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServingStatus != NodeActive || len(got.Warnings) != 1 || got.Warnings[0] != "未划入节点池，不服务任何用户" {
		t.Fatalf("上线结果不对：%s %v", got.ServingStatus, got.Warnings)
	}
	s, err := e.svc.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != ServerReady {
		t.Fatalf("服务器应转 ready：%s", s.Status)
	}

	// 幂等：已在役再调，版本不变（带旧版本也不报错）
	again, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if again.RowVersion != got.RowVersion {
		t.Fatalf("幂等上线不应改版本：%d → %d", got.RowVersion, again.RowVersion)
	}
}

func TestActivateNodeWithPoolNoWarning(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "act-b")
	e.enroll(t, ctx, srv.ID)
	pool := e.pool(t, ctx, "active")
	n := e.ssNode(t, ctx, srv.ID, "n", 21006)
	n, err := e.svc.PatchNode(ctx, n.ID, NodePatch{RowVersion: n.RowVersion, PoolID: OptionalUUID{Set: true, Value: &pool}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("划了池不应有提示：%v", got.Warnings)
	}
}

func TestActivateNodeFromDrainingServer(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "act-c")
	e.enroll(t, ctx, srv.ID)
	n := e.ssNode(t, ctx, srv.ID, "n", 21007)
	if err := e.exec(ctx, `UPDATE servers SET status = 'draining' WHERE id = ?`, srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion); err != nil {
		t.Fatal(err)
	}
	s, err := e.svc.GetServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s.Status != ServerReady {
		t.Fatalf("draining 的服务器应被推到 ready：%s", s.Status)
	}
}

func TestActivateRetiredNodeRejected(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "act-d")
	n := e.ssNode(t, ctx, srv.ID, "n", 21008)
	n, err := e.svc.RetireNode(ctx, n.ID, n.RowVersion, "下线")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	_ = invWantCode(t, err, errs.Conflict)
}

func TestRetireNode(t *testing.T) {
	e, ctx := newInvEnv(t)
	_, n := invActiveNode(t, e, ctx, "ret-a", "n", 21009)

	got, err := e.svc.RetireNode(ctx, n.ID, n.RowVersion, "机房搬迁")
	if err != nil {
		t.Fatal(err)
	}
	if got.ServingStatus != NodeRetired {
		t.Fatalf("状态 %s", got.ServingStatus)
	}
	var retiredAtSet bool
	if err := e.row(ctx, `SELECT retired_at IS NOT NULL FROM nodes WHERE id = ?`, n.ID).Scan(&retiredAtSet); err != nil {
		t.Fatal(err)
	}
	if !retiredAtSet {
		t.Fatal("retired_at 应非空")
	}
	_, err = e.svc.RetireNode(ctx, n.ID, got.RowVersion, "")
	_ = invWantCode(t, err, errs.Conflict)
	err = e.svc.BatchNodeStatus(ctx, NodeStatusBatch{ServingStatus: NodeActive, Items: []NodeVersionRef{{ID: n.ID, RowVersion: got.RowVersion}}})
	_ = invWantCode(t, err, errs.Conflict)
}

func TestDeleteNode(t *testing.T) {
	e, _, ctx := newInvListEnv(t)
	srv, active := invActiveNode(t, e, ctx, "del-a", "active", 21010)
	err := e.svc.DeleteNode(ctx, active.ID, active.RowVersion, "")
	_ = invWantCode(t, err, errs.ValidationFailed)

	draft := e.ssNode(t, ctx, srv.ID, "draft", 21011)
	err = e.svc.DeleteNode(ctx, draft.ID, draft.RowVersion+1, "")
	_ = invWantCode(t, err, errs.Conflict)
	if err := e.svc.DeleteNode(ctx, draft.ID, draft.RowVersion, "建错了"); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	var status string
	if err := e.row(ctx, `SELECT deleted_at IS NOT NULL, serving_status FROM nodes WHERE id = ?`, draft.ID).Scan(&deleted, &status); err != nil {
		t.Fatal(err)
	}
	if !deleted || status != NodeRetired {
		t.Fatalf("软删后 deleted=%v status=%s", deleted, status)
	}
	if _, err := e.svc.GetAdminNode(ctx, draft.ID); err == nil {
		t.Fatal("删掉的节点不应再取到")
	}
	rows, _, err := e.svc.QueryNodes(ctx, NodeQuery{IncludeRetired: true, Search: draft.Name, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("列表里不应有删掉的节点：%d 行", len(rows))
	}
	// 名字只在未删除的行里唯一
	e.ssNode(t, ctx, srv.ID, "draft", 21011)

	retired, err := e.svc.RetireNode(ctx, active.ID, active.RowVersion, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteNode(ctx, retired.ID, retired.RowVersion, ""); err != nil {
		t.Fatal(err)
	}
}

func TestRepublishNode(t *testing.T) {
	e, ctx := newInvEnv(t)
	srv := e.server(t, ctx, "pub-a")
	n := e.ssNode(t, ctx, srv.ID, "n", 21015)
	v, err := e.svc.RepublishNode(ctx, n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v != n.RowVersion+1 {
		t.Fatalf("版本 %d，期望 %d", v, n.RowVersion+1)
	}
	if _, err := e.svc.RetireNode(ctx, n.ID, v, ""); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.RepublishNode(ctx, n.ID)
	_ = invWantCode(t, err, errs.Conflict)
	_, err = e.svc.RepublishNode(ctx, uuid.New())
	_ = invWantCode(t, err, errs.NotFound)
}

func TestNodeLifecycleEpochs(t *testing.T) {
	e, ctx := newInvEnvCommitted(t)
	srv := e.server(t, ctx, "epoch-a")
	e.enroll(t, ctx, srv.ID)
	n := e.ssNode(t, ctx, srv.ID, "n", 21016)

	users, config := e.epoch(t, UsersEpochKey), e.epoch(t, ConfigEpochKey)
	// 失败的调用（版本不对）不推纪元
	_, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion+3)
	_ = invWantCode(t, err, errs.Conflict)
	if e.epoch(t, UsersEpochKey) != users || e.epoch(t, ConfigEpochKey) != config {
		t.Fatal("失败的调用不应推进纪元")
	}

	got, err := e.svc.ActivateNode(ctx, n.ID, n.RowVersion)
	if err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, UsersEpochKey) != users+1 || e.epoch(t, ConfigEpochKey) != config+1 {
		t.Fatalf("上线后纪元应各 +1：users %d→%d config %d→%d",
			users, e.epoch(t, UsersEpochKey), config, e.epoch(t, ConfigEpochKey))
	}

	config = e.epoch(t, ConfigEpochKey)
	if _, err := e.svc.RepublishNode(ctx, got.ID); err != nil {
		t.Fatal(err)
	}
	if e.epoch(t, ConfigEpochKey) != config+1 {
		t.Fatal("强制重新下发应推进配置纪元")
	}
}
