package repo

// UpdateNodeRuntimes 的 runtime_reason_node_id 只认同一服务器上未删除的节点（PG 集成测试）。

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func TestUpdateNodeRuntimesReasonNodeScoped(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	sS := sessInsertServer(t, d, ctx)
	sT := sessInsertServer(t, d, ctx)
	n1 := sessInsertNode(t, d, ctx, sS)
	n2 := sessInsertNode(t, d, ctx, sS)
	n4 := sessInsertNode(t, d, ctx, sS)
	n3 := sessInsertNode(t, d, ctx, sT)
	// n4 软删：nodes_deleted_shape 要求先退役，nodes_retired_shape 要求退役带 retired_at
	if err := d.Conn(ctx).Exec(`UPDATE nodes SET serving_status = 'retired', retired_at = now(), deleted_at = now() WHERE id = ?`, n4).Error; err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	steps := []struct {
		what   string
		reason string
		ref    uuid.UUID
		want   *uuid.UUID
	}{
		{"同服务器的节点", "port_in_use_a", n2, &n2},
		{"别的服务器的节点", "port_in_use_b", n3, nil},
		{"已删除的节点", "port_in_use_c", n4, nil},
		{"不存在的节点", "port_in_use_d", uuid.New(), nil},
	}
	for i, s := range steps {
		at := base.Add(time.Duration(i) * time.Minute)
		ref := s.ref
		ids, err := r.UpdateNodeRuntimes(ctx, sS, []NodeRuntimeRow{
			{NodeID: n1, Status: "degraded", Reason: sessStr(s.reason), ReasonNodeID: &ref},
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != n1 {
			t.Fatalf("%s：期望写 [%v]，得到 %v", s.what, n1, ids)
		}
		assertNodeRuntime(t, d, ctx, n1, sessStr("degraded"), sessStr(s.reason), s.want, at)
	}
}
