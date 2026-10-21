package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func auditVersionCreatedCount(t *testing.T, e *env, ctx context.Context) int {
	return countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionVersionCreated)
}

func pageStatus(t *testing.T, e *env, ctx context.Context, slug string, version int) string {
	var st string
	if err := e.row(ctx, `SELECT status FROM content_pages WHERE slug = ? AND version = ?`, slug, version).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestContentPublishVersionsPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	slug := "pub-flow-" + randHex(8)
	body := longBody("")
	planA := uuid.New()
	e.plans.plans[planA] = true

	base := PublishInput{
		Slug: slug, Kind: "kb_article", Title: "T1", Body: body,
		Visibility: VisibilityAuthenticated, TargetPlanIDs: []string{planA.String()},
		ExpectedLatestVersion: 0, Status: StatusDraft,
	}
	audBefore := auditVersionCreatedCount(t, e, ctx)
	v1, err := e.svc.PublishVersion(actx, base)
	if err != nil || v1.Version != 1 {
		t.Fatalf("v1 draft: %+v err=%v", v1, err)
	}

	base.ExpectedLatestVersion = 1
	base.Status = StatusPublished
	base.Title = "T2"
	v2, err := e.svc.PublishVersion(actx, base)
	if err != nil || v2.Version != 2 {
		t.Fatalf("v2 publish: %+v err=%v", v2, err)
	}

	base.ExpectedLatestVersion = 2
	base.Title = "T3"
	v3, err := e.svc.PublishVersion(actx, base)
	if err != nil || v3.Version != 3 {
		t.Fatalf("v3 publish: %+v err=%v", v3, err)
	}
	if pageStatus(t, e, ctx, slug, 2) != StatusArchived {
		t.Fatalf("v2 should be archived")
	}
	if pageStatus(t, e, ctx, slug, 3) != StatusPublished {
		t.Fatalf("v3 should be published")
	}
	audAfter := auditVersionCreatedCount(t, e, ctx)
	if audAfter-audBefore != 3 {
		t.Fatalf("audit delta=%d want 3", audAfter-audBefore)
	}

	rows, err := e.svc.ListAdmin(actx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var latestOK, v1Latest bool
	for _, p := range rows {
		if p.Slug != slug {
			continue
		}
		if p.LatestVersion != 3 {
			t.Fatalf("LatestVersion=%d", p.LatestVersion)
		}
		latestOK = true
		if p.Version == 3 && !p.IsLatestInAudience {
			t.Fatalf("v3 should be latest in audience")
		}
		if p.Version == 1 && p.IsLatestInAudience {
			v1Latest = true
		}
	}
	if !latestOK || v1Latest {
		t.Fatalf("list flags latestOK=%v v1Latest=%v", latestOK, v1Latest)
	}

	base.ExpectedLatestVersion = 3
	base.Visibility = VisibilityInternal
	base.Title = "T4 other audience"
	base.Status = StatusPublished
	v4, err := e.svc.PublishVersion(actx, base)
	if err != nil || v4.Version != 4 {
		t.Fatalf("v4: %+v err=%v", v4, err)
	}
	if pageStatus(t, e, ctx, slug, 3) != StatusPublished {
		t.Fatalf("v3 should stay published (different audience)")
	}

	base.ExpectedLatestVersion = 4
	base.Visibility = VisibilityAuthenticated
	base.Title = "T5 same audience as v3"
	base.Status = StatusPublished
	v5, err := e.svc.PublishVersion(actx, base)
	if err != nil || v5.Version != 5 {
		t.Fatalf("v5: %+v err=%v", v5, err)
	}
	if pageStatus(t, e, ctx, slug, 3) != StatusArchived {
		t.Fatalf("v3 should be archived (same audience)")
	}
	if pageStatus(t, e, ctx, slug, 4) != StatusPublished {
		t.Fatalf("v4 should stay published (different audience)")
	}
	if pageStatus(t, e, ctx, slug, 5) != StatusPublished {
		t.Fatalf("v5 should be published")
	}
}

func TestContentPublishStaleCASPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	slug := "stale-" + randHex(8)

	in := PublishInput{
		Slug: slug, Title: "首版", Body: longBody(""), ExpectedLatestVersion: 0, Status: StatusDraft,
	}
	if _, err := e.svc.PublishVersion(actx, in); err != nil {
		t.Fatal(err)
	}
	pagesBefore := countRows(t, e, ctx, `SELECT count(*) FROM content_pages WHERE slug = ?`, slug)
	audBefore := auditVersionCreatedCount(t, e, ctx)

	in.ExpectedLatestVersion = 0
	in.Title = "过期乐观锁"
	_, err := e.svc.PublishVersion(actx, in)
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Code != errs.Conflict {
		t.Fatalf("want conflict: %v", err)
	}
	pagesAfter := countRows(t, e, ctx, `SELECT count(*) FROM content_pages WHERE slug = ?`, slug)
	audAfter := auditVersionCreatedCount(t, e, ctx)
	if pagesAfter != pagesBefore || audAfter != audBefore {
		t.Fatalf("state changed pages %d/%d audits %d/%d", pagesAfter, pagesBefore, audAfter, audBefore)
	}
}

func TestContentTargetPlanValidationPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	missing := uuid.New()
	_, err := e.svc.PublishVersion(actx, PublishInput{
		Slug: "plan-miss-" + randHex(6), Title: "套餐", Body: longBody(""),
		TargetPlanIDs: []string{missing.String()}, ExpectedLatestVersion: 0,
	})
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Code != errs.ValidationFailed || ee.Fields["target_plan_ids"] == "" {
		t.Fatalf("want target_plan_ids 422: %v", err)
	}
}

func TestContentPortalVisibilityPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	member := newUser(t, e, ctx)
	outsider := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)

	planA := uuid.New()
	e.plans.plans[planA] = true
	e.subs.byUser[member] = []uuid.UUID{planA}

	slug := "getting-started-" + randHex(8)
	pub := PublishInput{
		Slug: slug, Kind: "kb_article", Title: "GS", Body: longBody("detail body here"),
		Visibility: VisibilityAuthenticated, Status: StatusPublished,
		TargetPlanIDs: []string{planA.String()}, ExpectedLatestVersion: 0,
	}
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}

	mctx := userCtx(ctx, member)
	vf := VisibleFilter{Kind: "kb_article"}
	list, err := e.svc.ListVisible(mctx, member, vf)
	if err != nil || len(list) != 1 || list[0].Body != "" {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	detail, err := e.svc.GetVisible(mctx, member, slug, VisibleFilter{})
	if err != nil || detail.Body == "" {
		t.Fatalf("detail body empty err=%v", err)
	}

	cases := []struct {
		name string
		uid  uuid.UUID
		f    VisibleFilter
	}{
		{"no-plan", outsider, vf},
	}
	for _, tc := range cases {
		got, err := e.svc.ListVisible(userCtx(ctx, tc.uid), tc.uid, tc.f)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s leaked len=%d err=%v", tc.name, len(got), err)
		}
	}

	internalSlug := "internal-only-" + randHex(6)
	pub.Slug = internalSlug
	pub.Visibility = VisibilityInternal
	pub.ExpectedLatestVersion = 0
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.svc.ListVisible(mctx, member, VisibleFilter{Kind: "kb_article"}); len(n) > 1 {
		t.Fatalf("internal leaked")
	}

	draftSlug := "draft-only-" + randHex(6)
	pub.Slug = draftSlug
	pub.Visibility = VisibilityAuthenticated
	pub.Status = StatusDraft
	pub.ExpectedLatestVersion = 0
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GetVisible(mctx, member, draftSlug, VisibleFilter{}); err == nil {
		t.Fatal("draft should not be visible")
	}
}

func TestContentSearchPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	user := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	uctx := userCtx(ctx, user)

	slug := "search-" + randHex(8)
	needle := "xyzzyneedle" + randHex(4)
	pub := PublishInput{
		Slug: slug, Title: "Search", Body: longBody(needle), Status: StatusPublished,
		ExpectedLatestVersion: 0,
	}
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}
	pub.ExpectedLatestVersion = 1
	pub.Body = longBody("no keyword in current version")
	pub.Title = "Search v2"
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}

	hit, err := e.svc.ListVisible(uctx, user, VisibleFilter{Query: needle})
	if err != nil || len(hit) != 0 {
		t.Fatalf("old version hit should not return: %+v err=%v", hit, err)
	}
	pub.Body = longBody("visible " + needle)
	pub.ExpectedLatestVersion = 2
	if _, err := e.svc.PublishVersion(actx, pub); err != nil {
		t.Fatal(err)
	}
	hit, err = e.svc.ListVisible(uctx, user, VisibleFilter{Query: needle})
	if err != nil || len(hit) != 1 {
		t.Fatalf("body hit want 1: %+v err=%v", hit, err)
	}
}

// 没有在用套餐的用户搜不到 access 区块里的文字（防逐段试探）；有套餐的照常能搜到；列表都不带正文。
func TestContentSearchHidesAccessBlockPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	plain := newUser(t, e, ctx)
	member := newUser(t, e, ctx)
	e.subs.byUser[member] = []uuid.UUID{uuid.New()}
	actx := adminCtx(ctx, admin)

	open := "openneedle" + randHex(4)
	secret := "secretneedle" + randHex(4)
	if _, err := e.svc.PublishVersion(actx, PublishInput{
		Slug: "gated-" + randHex(8), Title: "区块搜索", Status: StatusPublished, ExpectedLatestVersion: 0,
		Body: "公开部分 " + open + "\n<!-- access start -->\n节点口令 " + secret + "\n<!-- access end -->\n结尾",
	}); err != nil {
		t.Fatal(err)
	}
	f := func(q string) VisibleFilter {
		return VisibleFilter{Query: q}
	}

	hit, err := e.svc.ListVisible(userCtx(ctx, plain), plain, f(secret))
	if err != nil || len(hit) != 0 {
		t.Fatalf("没有套餐不应搜到区块内文字: %+v err=%v", hit, err)
	}
	hit, err = e.svc.ListVisible(userCtx(ctx, plain), plain, f(open))
	if err != nil || len(hit) != 1 || hit[0].Body != "" {
		t.Fatalf("没有套餐应能搜到公开部分且不带正文: %+v err=%v", hit, err)
	}
	hit, err = e.svc.ListVisible(userCtx(ctx, member), member, f(secret))
	if err != nil || len(hit) != 1 || hit[0].Body != "" {
		t.Fatalf("有套餐应能搜到区块内文字且不带正文: %+v err=%v", hit, err)
	}
}

func TestContentAdminAuthorPG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	e.users.names[admin] = "编辑小王"
	actx := adminCtx(ctx, admin)
	slug := "author-" + randHex(8)

	if _, err := e.svc.PublishVersion(actx, PublishInput{
		Slug: slug, Title: "作者", Body: longBody(""), Status: StatusPublished, ExpectedLatestVersion: 0,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.svc.ListAdmin(actx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range rows {
		if p.Slug != slug {
			continue
		}
		found = true
		if p.CreatedBy == nil || *p.CreatedBy != admin {
			t.Fatalf("CreatedBy=%v", p.CreatedBy)
		}
		if p.CreatedByName == nil || *p.CreatedByName != "编辑小王" {
			t.Fatalf("CreatedByName=%v", p.CreatedByName)
		}
		detail, err := e.svc.GetAdmin(actx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.CreatedBy != nil || detail.Body == "" {
			t.Fatalf("GetAdmin CreatedBy=%v body empty=%v", detail.CreatedBy, detail.Body == "")
		}
	}
	if !found {
		t.Fatal("slug not in admin list")
	}
}

func TestContentArchivePG(t *testing.T) {
	e := newEnv(t)
	ctx := e.txCtx()
	admin := newUser(t, e, ctx)
	actx := adminCtx(ctx, admin)
	slug := "arch-" + randHex(8)

	res, err := e.svc.PublishVersion(actx, PublishInput{
		Slug: slug, Title: "归档", Body: longBody(""), Status: StatusPublished, ExpectedLatestVersion: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	audArch := func() int {
		return countRows(t, e, ctx, `SELECT count(*) FROM audit_events WHERE action = ?`, ActionArchived)
	}
	before := audArch()
	ar, err := e.svc.Archive(actx, res.ID, res.Version)
	if err != nil || ar.Status != StatusArchived || ar.AlreadyArchived {
		t.Fatalf("archive: %+v err=%v", ar, err)
	}
	if audArch()-before != 1 {
		t.Fatalf("audit archived delta")
	}
	before = audArch()
	ar2, err := e.svc.Archive(actx, res.ID, res.Version)
	if err != nil || !ar2.AlreadyArchived {
		t.Fatalf("second archive: %+v err=%v", ar2, err)
	}
	if audArch() != before {
		t.Fatal("duplicate archive wrote audit")
	}

	_, err = e.svc.Archive(actx, res.ID, res.Version+99)
	var ee *errs.Error
	if !errors.As(err, &ee) || ee.Code != errs.Conflict {
		t.Fatalf("wrong version: %v", err)
	}

	_, err = e.svc.Archive(actx, uuid.New(), 1)
	if !errors.As(err, &ee) || ee.Code != errs.NotFound {
		t.Fatalf("missing id: %v", err)
	}
}

func TestContentConcurrentPublishPG(t *testing.T) {
	e := newEnv(t)
	slug := "race-" + randHex(12)
	ctx := context.Background()
	var admin uuid.UUID
	if err := e.row(ctx, `INSERT INTO users (email) VALUES (?) RETURNING id`,
		fmt.Sprintf("content-%s@example.test", randHex(16))).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	actx := adminCtx(ctx, admin)

	// 这条用例真提交（两个并发事务），结束时删掉自己写的页面与用户
	t.Cleanup(func() {
		_ = e.exec(context.Background(), `DELETE FROM content_pages WHERE slug = ?`, slug)
		_ = e.exec(context.Background(), `DELETE FROM users WHERE id = ?`, admin)
	})

	base := PublishInput{
		Slug: slug, Title: "v1", Body: longBody(""), Status: StatusDraft, ExpectedLatestVersion: 0,
	}
	if _, err := e.svc.PublishVersion(actx, base); err != nil {
		t.Fatal(err)
	}
	base.ExpectedLatestVersion = 1
	base.Status = StatusDraft

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := base
			in.Title = fmt.Sprintf("Concurrent %d", i)
			_, err := e.svc.PublishVersion(actx, in)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	success, conflict := 0, 0
	for err := range errCh {
		if err == nil {
			success++
			continue
		}
		var ee *errs.Error
		if errors.As(err, &ee) && ee.Code == errs.Conflict {
			conflict++
		} else {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
