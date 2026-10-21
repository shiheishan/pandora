package repo

// UpdateNodeRuntimes 的 runtime_reason_node_id 与证书列（PG 集成测试）。

import (
	"context"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/db"
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

func assertNodeCert(t *testing.T, d *db.DB, ctx context.Context, nodeID uuid.UUID, wantCert bool, stateAt *time.Time) {
	var id *string
	var notAfter, next *time.Time
	var code, detail *string
	var certAt *time.Time
	err := d.Conn(ctx).Raw(`SELECT cert_identifier, cert_not_after, cert_error_code, cert_error_detail,
		cert_next_attempt_at, cert_state_at FROM nodes WHERE id = ?`, nodeID).
		Row().Scan(&id, &notAfter, &code, &detail, &next, &certAt)
	if err != nil {
		t.Fatal(err)
	}
	if !wantCert {
		if id != nil || notAfter != nil || code != nil || detail != nil || next != nil || certAt != nil {
			t.Fatalf("证书列应全 NULL：id=%v not_after=%v code=%v detail=%v next=%v state_at=%v",
				id, notAfter, code, detail, next, certAt)
		}
		return
	}
	if id == nil || *id != "node.example.com" || certAt == nil || stateAt == nil || !certAt.Equal(*stateAt) {
		t.Fatalf("证书应有值：id=%v state_at=%v want=%v", id, certAt, stateAt)
	}
}

func TestUpdateNodeRuntimesCertState(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)

	srv := sessInsertServer(t, d, ctx)
	nodeID := sessInsertNode(t, d, ctx, srv)

	at1 := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	notAfter := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	next1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	row := NodeRuntimeRow{
		NodeID:          nodeID,
		Status:          "running",
		CertIdentifier:  sessStr("node.example.com"),
		CertNotAfter:    &notAfter,
		CertErrorCode:   sessStr("challenge_failed"),
		CertErrorDetail: sessStr("detail"),
		CertNextAttempt: &next1,
	}
	ids, err := r.UpdateNodeRuntimes(ctx, srv, []NodeRuntimeRow{row}, at1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != nodeID {
		t.Fatalf("第一次写: %v", ids)
	}
	assertNodeCert(t, d, ctx, nodeID, true, &at1)

	at2 := time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC)
	ids, err = r.UpdateNodeRuntimes(ctx, srv, []NodeRuntimeRow{row}, at2)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("同样值再写: %v", ids)
	}

	next2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	at3 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	row2 := row
	row2.CertNextAttempt = &next2
	ids, err = r.UpdateNodeRuntimes(ctx, srv, []NodeRuntimeRow{row2}, at3)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("只改 next_attempt: %v", ids)
	}
	var runtimeAt, certAt3 *time.Time
	if err := d.Conn(ctx).Raw(`SELECT runtime_state_at, cert_state_at FROM nodes WHERE id = ?`, nodeID).
		Row().Scan(&runtimeAt, &certAt3); err != nil {
		t.Fatal(err)
	}
	if runtimeAt == nil || !runtimeAt.Equal(at1) {
		t.Fatalf("runtime_state_at 应保持 %v，得到 %v", at1, runtimeAt)
	}
	if certAt3 == nil || !certAt3.Equal(at3) {
		t.Fatalf("cert_state_at 应前进到 %v，得到 %v", at3, certAt3)
	}

	at4 := time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC)
	empty := NodeRuntimeRow{NodeID: nodeID, Status: "running"}
	ids, err = r.UpdateNodeRuntimes(ctx, srv, []NodeRuntimeRow{empty}, at4)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("证书全空: %v", ids)
	}
	assertNodeCert(t, d, ctx, nodeID, false, nil)
}
