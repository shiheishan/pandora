package service

// 后台订阅模板管理的 PG 集成测试。

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestAdminTemplatesListPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	list, err := e.svc.AdminTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(render.TemplateFormats) {
		t.Fatalf("格式数 %d", len(list))
	}
	for i, f := range render.TemplateFormats {
		if list[i].Format != string(f) {
			t.Fatalf("顺序[%d] want %s got %s", i, f, list[i].Format)
		}
		if list[i].Customized {
			t.Fatalf("[%s] 应未自定义", f)
		}
	}
}

func TestAdminTemplateDetailDefaultPG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	d, err := e.svc.AdminTemplate(ctx, "clash")
	if err != nil {
		t.Fatal(err)
	}
	if d.Customized || d.Content != d.Default || d.Default == "" {
		t.Fatalf("缺省详情 %+v", d)
	}
}

func TestAdminSaveResetPreviewTemplatePG(t *testing.T) {
	e := newEnv(t)
	ctx := adminCtx(e.txCtx(), uuid.New())
	def := render.DefaultTemplate(render.FormatClash, render.Options{})
	custom := strings.Replace(def, "mixed-port: 7890", "mixed-port: 7891", 1)
	if custom == def {
		t.Fatal("应改 mixed-port")
	}

	d, err := e.svc.AdminSaveTemplate(ctx, "clash", custom)
	if err != nil {
		t.Fatalf("保存: %v", err)
	}
	if !d.Customized || d.Content != custom {
		t.Fatalf("保存后详情 %+v", d)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_templates WHERE format = 'clash'`); n != 1 {
		t.Fatalf("模板行 %d", n)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ? AND data->>'format' = 'clash'`, ActionTemplateSaved); n != 1 {
		t.Fatalf("保存审计 %d", n)
	}

	auditN := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionTemplateSaved)
	_, err = e.svc.AdminSaveTemplate(ctx, "clash", custom)
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionTemplateSaved); n != auditN {
		t.Fatalf("重复保存审计应从 %d 保持", auditN)
	}

	bad := strings.Replace(custom, "{{proxies}}", "", 1)
	_, err = e.svc.AdminSaveTemplate(ctx, "clash", bad)
	var ve *errs.Error
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("不合格模板: %v", err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_templates WHERE format = 'clash'`); n != 1 {
		t.Fatal("不合格不应改库")
	}

	for _, fmt := range []string{"uri", "nope"} {
		_, err = e.svc.AdminSaveTemplate(ctx, fmt, "x")
		if !errors.As(err, &ve) || ve.Code != errs.NotFound {
			t.Fatalf("未知格式 %s: %v", fmt, err)
		}
	}

	user := newUser(t, e, e.txCtx())
	_, err = e.svc.AdminSaveTemplate(userCtx(e.txCtx(), user), "clash", custom)
	if !errors.As(err, &ve) || ve.Code != errs.Forbidden {
		t.Fatalf("门户主体: %v", err)
	}

	resetAudit := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionTemplateReset)
	_, err = e.svc.AdminResetTemplate(ctx, "clash")
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM subscription_templates WHERE format = 'clash'`); n != 0 {
		t.Fatal("恢复后应无行")
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionTemplateReset); n != resetAudit+1 {
		t.Fatalf("恢复审计 %d", n)
	}
	_, err = e.svc.AdminResetTemplate(ctx, "clash")
	if err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionTemplateReset); n != resetAudit+1 {
		t.Fatal("再恢复应无操作")
	}

	prev, err := e.svc.AdminPreviewTemplate(ctx, "loon", "")
	if err != nil {
		t.Fatal(err)
	}
	if prev.NodeCount <= 0 || prev.Body == "" {
		t.Fatalf("预览 Loon %+v", prev)
	}
	_, err = e.svc.AdminPreviewTemplate(ctx, "loon", bad)
	if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
		t.Fatalf("预览不合格: %v", err)
	}

	m, err := e.svc.repo.TemplateContents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 保存后又恢复，map 里不应再有 clash；再存一次测 TemplateContents
	_, err = e.svc.AdminSaveTemplate(ctx, "clash", custom)
	if err != nil {
		t.Fatal(err)
	}
	m, err = e.svc.repo.TemplateContents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m["clash"] != custom {
		t.Fatalf("TemplateContents clash %q", m["clash"])
	}
}

func TestTrafficWindowEndPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	user := newUser(t, e, ctx)
	planID, versionID := newPlan(t, e, ctx, quotaSpec{MetricTraffic, PeriodCycle, i64(10 * gib)})
	sub := provision(t, e, ctx, user, planID, versionID, "")
	_, err := e.svc.repo.TrafficWindowEnd(ctx, sub.ID, e.svc.clock())
	if err != nil {
		t.Fatalf("TrafficWindowEnd: %v", err)
	}
}
