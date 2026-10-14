package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestExclusivePoolsByGroupPG(t *testing.T) {
	e, ctx := newPoolEnv(t)

	// 建组 g1、g2、g3
	g1 := seedPoolGroup(t, ctx, e, "Group 1")
	g2 := seedPoolGroup(t, ctx, e, "Group 2")
	g3 := seedPoolGroup(t, ctx, e, "Group 3")

	// 建两个池，名字让排序可验证（例如 "b-pool" 和 "a-pool"）
	// 两个池都限定 g1；其中一个（如 "b-pool"）也限定 g2
	g1g2 := []uuid.UUID{g1, g2}
	poolB, err := e.svc.CreatePool(ctx, PoolCreate{
		Name:         "b-pool",
		UserGroupIDs: &g1g2,
	})
	if err != nil {
		t.Fatal(err)
	}

	g1only := []uuid.UUID{g1}
	poolA, err := e.svc.CreatePool(ctx, PoolCreate{
		Name:         "a-pool",
		UserGroupIDs: &g1only,
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := e.svc.ExclusivePoolsByGroup(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// 断言 g1 的两个池按名称升序：a-pool 在前，b-pool 在后
	g1Pools, ok := res[g1]
	if !ok {
		t.Fatalf("g1 应该在结果里")
	}
	if len(g1Pools) != 2 {
		t.Fatalf("g1 应有 2 个限定池，得到 %d", len(g1Pools))
	}
	if g1Pools[0].ID != poolA || g1Pools[0].Name != "a-pool" {
		t.Errorf("g1 第一个池应为 a-pool (%s)，得到 %+v", poolA, g1Pools[0])
	}
	if g1Pools[1].ID != poolB || g1Pools[1].Name != "b-pool" {
		t.Errorf("g1 第二个池应为 b-pool (%s)，得到 %+v", poolB, g1Pools[1])
	}

	// 断言 g2 只有一个池 (b-pool)
	g2Pools, ok := res[g2]
	if !ok {
		t.Fatalf("g2 应该在结果里")
	}
	if len(g2Pools) != 1 {
		t.Fatalf("g2 应有 1 个限定池，得到 %d", len(g2Pools))
	}
	if g2Pools[0].ID != poolB || g2Pools[0].Name != "b-pool" {
		t.Errorf("g2 限定池应为 b-pool (%s)，得到 %+v", poolB, g2Pools[0])
	}

	// 断言未被列入名单的 g3 不在结果里
	if _, ok := res[g3]; ok {
		t.Fatalf("g3 未被任何池列入名单，不应在结果里")
	}
}
