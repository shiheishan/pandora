package service

// 门户线路预览的 PG 集成测试。

import (
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/module/subscription/render"
)

func TestMyNodes(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	de.nodes.nodes = []render.Node{
		{Name: "香港 01", Type: "vless", Host: "hk.example.test", Port: 443, TrafficRate: 1},
		{Name: "日本 02", Type: "trojan", Host: "jp.example.test", Port: 443, TrafficRate: 1.5},
	}
	got, err := de.svc.MyNodes(userCtx(ctx, user), sub.ID)
	if err != nil {
		t.Fatalf("线路预览: %v", err)
	}
	if len(got) != 2 || got[0] != (NodePreview{Name: "香港 01", Protocol: "vless", TrafficRate: 1}) ||
		got[1] != (NodePreview{Name: "日本 02", Protocol: "trojan", TrafficRate: 1.5}) {
		t.Fatalf("预览: %+v", got)
	}
	qs := de.nodes.queries
	if len(qs) == 0 || qs[len(qs)-1].PlanVersionID != versionID || qs[len(qs)-1].SubscriptionID != sub.ID {
		t.Fatalf("查询条件: %+v", qs)
	}

	de.nodes.nodes = nil
	if got, err := de.svc.MyNodes(userCtx(ctx, user), sub.ID); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("没有线路: %v %#v", err, got)
	}
}

func TestMyNodesNotLive(t *testing.T) {
	de := newDeliveryEnv(t, true)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	other := newUser(t, de.env, ctx)
	if _, err := de.svc.MyNodes(userCtx(ctx, other), sub.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别人的订阅: %v", err)
	}
	lapse(t, de.env, ctx, sub.ID, 1)
	if _, err := de.svc.MyNodes(userCtx(ctx, user), sub.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期: %v", err)
	}
}

func TestMyNodesNoSource(t *testing.T) {
	de := newDeliveryEnv(t, false)
	ctx := de.txCtx()
	user := newUser(t, de.env, ctx)
	planID, versionID := newPlan(t, de.env, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, de.env, ctx, user, planID, versionID, "")
	if _, err := de.svc.MyNodes(userCtx(ctx, user), sub.ID); !errors.Is(err, ErrDeliveryUnavailable) {
		t.Fatalf("没装配线路来源: %v", err)
	}
}
