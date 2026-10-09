package nodefabric

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// checkControlNodeBatchRetirePG18 挂在 TestServerBindingPG18 下（server_binding 域，同库同租户）：
// 接入过的节点是自己服务器的控制节点。服务器在役时，status:batch 与单个退役都拒绝（服务器会失去
// 管理入口）；服务器退役之后，批量退役放行。身份、任务两项依赖不因此放松。
// 修复前批量那条数全部服务器、不看状态，服务器退役后仍 409（10k-r1 实测 498 个节点退不掉）。
func checkControlNodeBatchRetirePG18(t *testing.T, ctx context.Context, admin *pgxpool.Pool, svc *Service, tenant, actor string) {
	t.Helper()
	must := func(sql string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture: %v\nSQL: %s", err, sql)
		}
	}
	var serverID, nodeID string
	if err := admin.QueryRow(ctx, `INSERT INTO servers (tenant_id, name, status) VALUES ($1,$2,'ready') RETURNING id::text`,
		tenant, "retire-ctl-"+uuid.NewString()).Scan(&serverID); err != nil {
		t.Fatal(err)
	}
	// 与压测库里的老节点同形：生命周期 active、服务状态 draining、是自己服务器的控制节点
	if err := admin.QueryRow(ctx, `INSERT INTO nodes (tenant_id, name, status, serving_status, server_id, node_type, protocol_config)
		VALUES ($1,$2,'active','draining',$3,'shadowsocks','{"method":"aes-128-gcm"}') RETURNING id::text`,
		tenant, "retire-ctl-node-"+uuid.NewString(), serverID).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	must(`UPDATE servers SET control_node_id=$2 WHERE id=$1`, serverID, nodeID)

	nodeVersion := func() int64 {
		t.Helper()
		var v int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM nodes WHERE id=$1`, nodeID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	batchRetire := func() error {
		return svc.BatchAdminNodeLifecycle(ctx, tenant, BatchNodeLifecycleInput{
			ActorID: actor, ServingStatus: "retired", Reason: "retire control node",
			Items: []BatchNodeLifecycleItem{{ID: nodeID, RowVersion: nodeVersion()}},
		})
	}
	wantRefusal := func(step string, err error, wantMessage string) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeConflict || !strings.Contains(he.Message, wantMessage) {
			t.Fatalf("%s: err=%v, want 409 containing %q", step, err, wantMessage)
		}
		if he.Fields["id"] != nodeID {
			t.Fatalf("%s: refusal does not name the node: %v", step, he.Fields)
		}
	}
	serving := func() string {
		t.Helper()
		var s string
		if err := admin.QueryRow(ctx, `SELECT serving_status FROM nodes WHERE id=$1`, nodeID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 1. 服务器在役：批量与单个退役都拒绝，口径相同
	wantRefusal("live server via status:batch", batchRetire(), "在役服务器的控制节点")
	_, err := svc.RetireNode(ctx, tenant, RetireNodeInput{ID: nodeID, RowVersion: nodeVersion(), ActorID: actor})
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeConflict || !strings.Contains(he.Message, "在役服务器的控制节点") {
		t.Fatalf("live server via RetireNode: %v, want the same 409", err)
	}

	// 2. 服务器退役（ready → draining → retired），节点仍有有效身份：仍拒绝，且说清是身份
	must(`INSERT INTO node_identities (tenant_id, node_id, serial, public_key, spiffe_id, fingerprint, expires_at)
		VALUES ($1,$2,1,'\x01',$3,$4,now() + interval '90 days')`,
		tenant, nodeID, "spiffe://aegis/test/"+nodeID, []byte("retire-ctl-fp-"+nodeID))
	for _, status := range []string{"draining", "retired"} {
		var rv int64
		if err := admin.QueryRow(ctx, `SELECT row_version FROM servers WHERE id=$1`, serverID).Scan(&rv); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SetServerStatus(ctx, tenant, serverID, SetServerStatusInput{ActorID: actor, Status: status, RowVersion: rv}); err != nil {
			t.Fatalf("server -> %s: %v", status, err)
		}
	}
	wantRefusal("active identity", batchRetire(), "有效的接入身份")

	// 3. 身份吊销后，还有执行中的任务：仍拒绝
	must(`UPDATE node_identities SET status='revoked', revoked_at=now(), revoked_reason='retire test' WHERE node_id=$1`, nodeID)
	must(`INSERT INTO node_tasks (tenant_id, node_id, task_type, expires_at) VALUES ($1,$2,'health.check',now() + interval '1 hour')`,
		tenant, nodeID)
	wantRefusal("pending task", batchRetire(), "任务")
	if got := serving(); got != "draining" {
		t.Fatalf("refused batches changed the node: serving_status=%s", got)
	}

	// 4. 依赖都清了：服务器已退役的控制节点经 status:batch 退役成功
	must(`UPDATE node_tasks SET status='failed', completed_at=now(), error_message='retire test' WHERE node_id=$1`, nodeID)
	if err := batchRetire(); err != nil {
		t.Fatalf("control node of a retired server: %v", err)
	}
	var desired *int64
	if err := admin.QueryRow(ctx, `SELECT desired_config_version FROM nodes WHERE id=$1`, nodeID).Scan(&desired); err != nil {
		t.Fatal(err)
	}
	if got := serving(); got != "retired" || desired != nil {
		t.Fatalf("after batch retirement: serving_status=%s desired_config_version=%v", got, desired)
	}
}
