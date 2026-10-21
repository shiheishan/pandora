package service

import (
	"testing"

	"github.com/google/uuid"
)

func TestAuditLabelsPG(t *testing.T) {
	e, ctx := newEnv(t)
	ctx, _ = seedAdmin(t, ctx, e)
	plan := publishedPlan(t, ctx, e, publishedPlanOpts{})
	detail, err := e.svc.GetPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	v1 := *detail.CurrentVersionID

	traffic := int64(20 << 30)
	after, err := e.svc.UpdateUsage(ctx, plan.ID, UsageUpdate{
		ExpectedRowVersion: detail.RowVersion,
		Usage:              UsageInput{TrafficBytes: &traffic},
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.DraftVersionID == nil {
		t.Fatal("应有版本 2 草稿")
	}
	if after.DraftVersion == nil || *after.DraftVersion != 2 {
		t.Fatalf("草稿版本号应为 2，实际 %v", after.DraftVersion)
	}
	v2 := *after.DraftVersionID

	missing := uuid.New()

	names, err := e.svc.PlanLabels(ctx, []uuid.UUID{plan.ID, missing})
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 {
		t.Fatalf("套餐名应只命中 1 条，实际 %d", len(names))
	}
	if names[plan.ID] != detail.Name {
		t.Fatalf("套餐名=%q，期望 %q", names[plan.ID], detail.Name)
	}

	vers, err := e.svc.PlanVersionLabels(ctx, []uuid.UUID{v1, v2, missing})
	if err != nil {
		t.Fatal(err)
	}
	if len(vers) != 2 {
		t.Fatalf("版本名应命中 2 条，实际 %d", len(vers))
	}
	wantV1 := detail.Name + " · v1"
	wantV2 := detail.Name + " · v2"
	if vers[v1] != wantV1 {
		t.Fatalf("v1=%q，期望 %q", vers[v1], wantV1)
	}
	if vers[v2] != wantV2 {
		t.Fatalf("v2=%q，期望 %q", vers[v2], wantV2)
	}

	namesEmpty, err := e.svc.PlanLabels(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(namesEmpty) != 0 {
		t.Fatalf("空切片套餐名应返回空 map，实际 %d 条", len(namesEmpty))
	}
	versEmpty, err := e.svc.PlanVersionLabels(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versEmpty) != 0 {
		t.Fatalf("空切片版本名应返回空 map，实际 %d 条", len(versEmpty))
	}
}
