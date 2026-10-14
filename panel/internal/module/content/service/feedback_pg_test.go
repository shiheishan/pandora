package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestContentFeedbackPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	user := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	uctx := userCtx(ctx, user)

	publish := func(slug string, expected int, planIDs []string) {
		t.Helper()
		_, err := e.svc.PublishVersion(actx, PublishInput{
			Slug: slug, Kind: "kb_article", Title: "连接失败怎么办",
			Body:   "## 先检查\n确认订阅没有过期，再重新导入。",
			Status: StatusPublished, TargetPlanIDs: planIDs, ExpectedLatestVersion: expected,
		})
		if err != nil {
			t.Fatalf("publish %s v%d: %v", slug, expected+1, err)
		}
	}
	publish("connect-failed", 0, nil)
	publish("connect-failed", 1, nil)

	helpfulOf := func(version int) (n int, helpful bool) {
		t.Helper()
		if err := e.row(ctx, `SELECT count(*), coalesce(bool_or(helpful), false) FROM content_page_feedback
			WHERE page_slug = 'connect-failed' AND page_version = ? AND user_id = ?`,
			version, user).Scan(&n, &helpful); err != nil {
			t.Fatal(err)
		}
		return
	}

	if err := e.svc.SubmitFeedback(uctx, user, "connect-failed", 2, true, VisibleFilter{}); err != nil {
		t.Fatalf("submit helpful: %v", err)
	}
	if err := e.svc.SubmitFeedback(uctx, user, "Connect-Failed", 2, false, VisibleFilter{}); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n, helpful := helpfulOf(2); n != 1 || helpful {
		t.Fatalf("v2 feedback rows=%d helpful=%v, want 1/false", n, helpful)
	}
	if err := e.svc.SubmitFeedback(uctx, user, "connect-failed", 1, true, VisibleFilter{}); err != nil {
		t.Fatalf("superseded v1: %v", err)
	}
	if n, helpful := helpfulOf(1); n != 1 || !helpful {
		t.Fatalf("v1 feedback rows=%d helpful=%v, want 1/true", n, helpful)
	}

	for _, v := range []int{0, 9} {
		err := e.svc.SubmitFeedback(uctx, user, "connect-failed", v, true, VisibleFilter{})
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Code != errs.ValidationFailed || ee.Fields["version"] == "" {
			t.Fatalf("version %d: %v, want 422 fields.version", v, err)
		}
	}

	plan := uuid.New()
	e.plans.plans[plan] = true
	publish("vip-only", 0, []string{plan.String()})

	rowsBefore := countRows(t, e, ctx, `SELECT count(*) FROM content_page_feedback`)
	for _, slug := range []string{"vip-only", "missing-page", "Bad Slug"} {
		err := e.svc.SubmitFeedback(uctx, user, slug, 1, true, VisibleFilter{})
		var ee *errs.Error
		if !errors.As(err, &ee) || ee.Code != errs.NotFound {
			t.Fatalf("invisible %q: %v, want 404", slug, err)
		}
	}
	if after := countRows(t, e, ctx, `SELECT count(*) FROM content_page_feedback`); after != rowsBefore {
		t.Fatalf("feedback rows=%d want %d", after, rowsBefore)
	}
}
