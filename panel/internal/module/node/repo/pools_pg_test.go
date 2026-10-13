package repo

// PoolAdmitsUserCond 的 PostgreSQL 集成测试。

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

// poolTestCode 生成随机 code，避免与并行跑的其他包的测试数据撞 UNIQUE。
func poolTestCode(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
}

func TestPoolAdmitsUserCond(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)

	var g1, g2 uuid.UUID
	if err := d.Conn(ctx).Raw(`INSERT INTO user_groups (code, name) VALUES (?, 'G1') RETURNING id`, poolTestCode("g")).Row().Scan(&g1); err != nil {
		t.Fatal(err)
	}
	if err := d.Conn(ctx).Raw(`INSERT INTO user_groups (code, name) VALUES (?, 'G2') RETURNING id`, poolTestCode("g")).Row().Scan(&g2); err != nil {
		t.Fatal(err)
	}

	var poolP, poolQ uuid.UUID
	if err := d.Conn(ctx).Raw(`INSERT INTO node_pools (code, name, status) VALUES (?, 'P', 'active') RETURNING id`, poolTestCode("p")).Row().Scan(&poolP); err != nil {
		t.Fatal(err)
	}
	if err := d.Conn(ctx).Raw(`INSERT INTO node_pools (code, name, status) VALUES (?, 'Q', 'active') RETURNING id`, poolTestCode("p")).Row().Scan(&poolQ); err != nil {
		t.Fatal(err)
	}
	if err := d.Conn(ctx).Exec(`INSERT INTO node_pool_user_groups (pool_id, user_group_id) VALUES (?, ?)`, poolQ, g1).Error; err != nil {
		t.Fatal(err)
	}

	var np, nq uuid.UUID
	if err := d.Conn(ctx).Raw(`INSERT INTO nodes (name, pool_id) VALUES ('np', ?) RETURNING id`, poolP).Row().Scan(&np); err != nil {
		t.Fatal(err)
	}
	if err := d.Conn(ctx).Raw(`INSERT INTO nodes (name, pool_id) VALUES ('nq', ?) RETURNING id`, poolQ).Row().Scan(&nq); err != nil {
		t.Fatal(err)
	}

	queryWithGroup := `SELECT EXISTS (SELECT 1 FROM nodes n CROSS JOIN (SELECT CAST(? AS uuid) AS user_group_id) u WHERE n.id = ? AND ` + PoolAdmitsUserCond + `)`
	queryNilGroup := `SELECT EXISTS (SELECT 1 FROM nodes n CROSS JOIN (SELECT CAST(NULL AS uuid) AS user_group_id) u WHERE n.id = ? AND ` + PoolAdmitsUserCond + `)`

	assertAdmits := func(nodeID uuid.UUID, groupID *uuid.UUID, want bool, label string) {
		var ok bool
		var err error
		if groupID == nil {
			err = d.Conn(ctx).Raw(queryNilGroup, nodeID).Row().Scan(&ok)
		} else {
			err = d.Conn(ctx).Raw(queryWithGroup, *groupID, nodeID).Row().Scan(&ok)
		}
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if ok != want {
			t.Fatalf("%s: 期望 %v，得到 %v (node=%s group=%v)", label, want, ok, nodeID, groupID)
		}
	}

	assertAdmits(np, &g1, true, "np+g1")
	assertAdmits(np, &g2, true, "np+g2")
	assertAdmits(np, nil, true, "np+nil")
	assertAdmits(nq, &g1, true, "nq+g1")
	assertAdmits(nq, &g2, false, "nq+g2")
	assertAdmits(nq, nil, false, "nq+nil")
}
