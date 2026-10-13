package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/content/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeSvc struct {
	Service
	listAdmin      func(service.ListFilter) ([]service.Page, error)
	getAdmin       func(uuid.UUID) (service.Page, error)
	publish        func(service.PublishInput) (service.PublishResult, error)
	archive        func(uuid.UUID, int) (service.ArchiveResult, error)
	listVisible    func(uuid.UUID, service.VisibleFilter) ([]service.Page, error)
	getVisible     func(uuid.UUID, string, service.VisibleFilter) (service.Page, error)
	submitFeedback func(uuid.UUID, string, int, bool, service.VisibleFilter) error
	called         bool
}

func (f *fakeSvc) ListAdmin(_ context.Context, fl service.ListFilter) ([]service.Page, error) {
	f.called = true
	if f.listAdmin != nil {
		return f.listAdmin(fl)
	}
	return nil, nil
}

func (f *fakeSvc) GetAdmin(_ context.Context, id uuid.UUID) (service.Page, error) {
	f.called = true
	if f.getAdmin != nil {
		return f.getAdmin(id)
	}
	return service.Page{}, nil
}

func (f *fakeSvc) PublishVersion(_ context.Context, in service.PublishInput) (service.PublishResult, error) {
	f.called = true
	if f.publish != nil {
		return f.publish(in)
	}
	return service.PublishResult{}, nil
}

func (f *fakeSvc) Archive(_ context.Context, id uuid.UUID, expected int) (service.ArchiveResult, error) {
	f.called = true
	if f.archive != nil {
		return f.archive(id, expected)
	}
	return service.ArchiveResult{}, nil
}

func (f *fakeSvc) ListVisible(_ context.Context, userID uuid.UUID, fl service.VisibleFilter) ([]service.Page, error) {
	f.called = true
	if f.listVisible != nil {
		return f.listVisible(userID, fl)
	}
	return nil, nil
}

func (f *fakeSvc) GetVisible(_ context.Context, userID uuid.UUID, slug string, fl service.VisibleFilter) (service.Page, error) {
	f.called = true
	if f.getVisible != nil {
		return f.getVisible(userID, slug, fl)
	}
	return service.Page{}, nil
}

func (f *fakeSvc) SubmitFeedback(_ context.Context, userID uuid.UUID, slug string, version int, helpful bool, fl service.VisibleFilter) error {
	f.called = true
	if f.submitFeedback != nil {
		return f.submitFeedback(userID, slug, version, helpful, fl)
	}
	return nil
}

func newEngine(svc Service, admin bool, p *authn.Principal, kit route.Kit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	if p != nil {
		e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	}
	if kit.RequireAuth == nil {
		pass := func(c *gin.Context) { c.Next() }
		kit = route.Kit{
			RequireAuth:  pass,
			RecentReauth: pass,
			Idempotency:  func(string) gin.HandlerFunc { return pass },
		}
	}
	h := New(svc, nil, 30)
	if admin {
		h.RegisterAdmin(e.Group("/v1"), kit)
	} else {
		h.RegisterPublic(e.Group("/v1"), kit)
	}
	return e
}

func do(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func decMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("json: %v body=%s", err, w.Body.String())
	}
	return got
}

func hasKey(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func TestAdminListPagesShape(t *testing.T) {
	review := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	pub := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	createdBy := uuid.New()
	author := "编辑"
	fullID := uuid.New()
	sparseID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	var lastFilter service.ListFilter
	f := &fakeSvc{
		listAdmin: func(fl service.ListFilter) ([]service.Page, error) {
			lastFilter = fl
			return []service.Page{
				{
					ID: fullID, Slug: "full", Kind: "kb_article", Category: "账单", Version: 2,
					Title: "全字段", Summary: "摘要", Body: "正文", Locale: "zh-CN", SanitizerVersion: "plain-v1",
					TargetPlatforms: []string{"web"}, MinClientVersion: "1.0", MaxClientVersion: "2.0",
					TargetPlanIDs: []string{uuid.New().String()}, Visibility: "authenticated", Status: "published",
					ReviewDueAt: &review, PublishedAt: &pub, CreatedAt: now, UpdatedAt: now,
					LatestVersion: 3, IsLatestInAudience: true, CreatedBy: &createdBy, CreatedByName: &author,
				},
				{
					ID: sparseID, Slug: "sparse", Kind: "tutorial", Version: 1, Title: "空字段",
					Locale: "zh-CN", SanitizerVersion: "plain-v1",
					TargetPlatforms: []string{}, TargetPlanIDs: []string{},
					Visibility: "internal", Status: "draft", CreatedAt: now, UpdatedAt: now,
				},
			}, nil
		},
	}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	w := do(e, http.MethodGet, "/v1/content-pages?kind=kb_article&status=published&q=vip&limit=50", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if lastFilter.Kind != "kb_article" || lastFilter.Status != "published" || lastFilter.Query != "vip" || lastFilter.Limit != 50 {
		t.Fatalf("filter %+v", lastFilter)
	}
	got := decMap(t, w)
	pages, _ := got["pages"].([]any)
	if len(pages) != 2 {
		t.Fatalf("pages len %d", len(pages))
	}
	p0, _ := pages[0].(map[string]any)
	p1, _ := pages[1].(map[string]any)
	if !hasKey(p0, "category") || !hasKey(p0, "created_by") || p0["target_platforms"] == nil {
		t.Fatalf("full row %v", p0)
	}
	for _, absent := range []string{"category", "summary", "body", "min_client_version", "max_client_version", "review_due_at", "published_at", "latest_version", "is_latest_in_audience", "created_by", "created_by_name"} {
		if hasKey(p1, absent) {
			t.Fatalf("sparse should omit %s: %v", absent, p1)
		}
	}
	if tp, _ := p1["target_platforms"].([]any); tp == nil {
		t.Fatalf("target_platforms should be []")
	}
	if tpid, _ := p1["target_plan_ids"].([]any); tpid == nil {
		t.Fatalf("target_plan_ids should be []")
	}
}

func TestAdminGetPublishArchive(t *testing.T) {
	pageID := uuid.New()
	review := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var gotPublish service.PublishInput
	var gotArchiveID uuid.UUID
	var gotArchiveVer int

	f := &fakeSvc{
		getAdmin: func(id uuid.UUID) (service.Page, error) {
			if id != pageID {
				t.Fatalf("get id %s", id)
			}
			return service.Page{ID: pageID, Slug: "x", Kind: "page", Version: 1, Title: "T", Locale: "zh-CN",
				SanitizerVersion: "plain-v1", TargetPlatforms: []string{}, TargetPlanIDs: []string{},
				Visibility: "authenticated", Status: "draft", CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
		},
		publish: func(in service.PublishInput) (service.PublishResult, error) {
			gotPublish = in
			return service.PublishResult{ID: pageID, Slug: in.Slug, Version: 2, Status: in.Status}, nil
		},
		archive: func(id uuid.UUID, expected int) (service.ArchiveResult, error) {
			gotArchiveID = id
			gotArchiveVer = expected
			return service.ArchiveResult{ID: id, Version: expected, Status: service.StatusArchived, AlreadyArchived: false}, nil
		},
	}
	e := newEngine(f, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})

	w := do(e, http.MethodGet, "/v1/content-pages/"+pageID.String(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("get %d", w.Code)
	}
	got := decMap(t, w)
	page, _ := got["page"].(map[string]any)
	if page["id"] != pageID.String() {
		t.Fatalf("page %v", page)
	}

	body := `{"slug":"new-slug","kind":"kb_article","category":"c","title":"标题","summary":"s","body":"正文够长","locale":"zh-CN","target_platforms":["web"],"min_client_version":"1.0","max_client_version":"2.0","target_plan_ids":[],"visibility":"authenticated","status":"published","review_due_at":"2026-11-01T00:00:00Z","expected_latest_version":1}`
	w = do(e, http.MethodPost, "/v1/content-pages", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("publish %d %s", w.Code, w.Body)
	}
	if gotPublish.Slug != "new-slug" || gotPublish.ExpectedLatestVersion != 1 || gotPublish.ReviewDueAt == nil || !gotPublish.ReviewDueAt.Equal(review) {
		t.Fatalf("publish input %+v", gotPublish)
	}
	saved := decMap(t, w)
	inner, _ := saved["page"].(map[string]any)
	if inner["version"] != float64(2) || inner["status"] != "published" {
		t.Fatalf("saved %v", inner)
	}

	w = do(e, http.MethodPost, "/v1/content-pages", `{"slug":"a","kind":"page","title":"标题","body":"1234567890","locale":"zh-CN","target_platforms":[],"target_plan_ids":[],"visibility":"authenticated","status":"draft","expected_latest_version":0,"bogus":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field %d", w.Code)
	}

	fNoArchive := &fakeSvc{}
	eNo := newEngine(fNoArchive, true, &authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: authn.AudienceAdmin}, route.Kit{})
	w = do(eNo, http.MethodPost, "/v1/content-pages/not-a-uuid/archive", `{"expected_version":3}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("bad id %d", w.Code)
	}
	if fNoArchive.called {
		t.Fatal("archive service called on bad id")
	}

	w = do(e, http.MethodPost, "/v1/content-pages/"+pageID.String()+"/archive", `{"expected_version":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("archive %d", w.Code)
	}
	if gotArchiveID != pageID || gotArchiveVer != 3 {
		t.Fatalf("archive args %s %d", gotArchiveID, gotArchiveVer)
	}
	arch := decMap(t, w)
	ap, _ := arch["page"].(map[string]any)
	if ap["already_archived"] != false {
		t.Fatalf("already_archived key %v", ap)
	}
}

func TestPortalListGetAndFeedback(t *testing.T) {
	userID := uuid.New()
	pub := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var listFl service.VisibleFilter
	var getSlug string
	var getFl service.VisibleFilter
	var fbUser uuid.UUID
	var fbSlug string
	var fbVer int
	var fbHelpful bool
	var fbFl service.VisibleFilter

	f := &fakeSvc{
		listVisible: func(uid uuid.UUID, fl service.VisibleFilter) ([]service.Page, error) {
			listFl = fl
			if uid != userID {
				t.Fatalf("user %s", uid)
			}
			return []service.Page{{
				Slug: "help", Kind: "kb_article", Category: "入门", Version: 1, Title: "帮助",
				Summary: "s", Locale: "zh-CN", TargetPlatforms: []string{"any"}, PublishedAt: &pub,
			}}, nil
		},
		getVisible: func(uid uuid.UUID, slug string, fl service.VisibleFilter) (service.Page, error) {
			getSlug = slug
			getFl = fl
			return service.Page{Slug: slug, Kind: "kb_article", Version: 1, Title: "帮助", Body: "正文", Locale: "zh-CN", TargetPlatforms: []string{"any"}}, nil
		},
		submitFeedback: func(uid uuid.UUID, slug string, version int, helpful bool, fl service.VisibleFilter) error {
			fbUser, fbSlug, fbVer, fbHelpful, fbFl = uid, slug, version, helpful, fl
			return nil
		},
	}
	e := newEngine(f, false, &authn.Principal{UserID: userID, SessionID: "s", Audience: authn.AudiencePublic}, route.Kit{})

	w := do(e, http.MethodGet, "/v1/content/pages?kind=kb_article&platform=any&client_version=1.0&locale=zh-CN&q=搜索", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache %q", w.Header().Get("Cache-Control"))
	}
	if listFl.Query != "搜索" || listFl.Platform != "any" {
		t.Fatalf("list filter %+v", listFl)
	}
	got := decMap(t, w)
	pages, _ := got["pages"].([]any)
	if len(pages) != 1 {
		t.Fatalf("pages %v", got)
	}
	row, _ := pages[0].(map[string]any)
	for _, forbidden := range []string{"id", "target_plan_ids", "status", "visibility", "created_at", "sanitizer_version"} {
		if hasKey(row, forbidden) {
			t.Fatalf("portal list exposes %s", forbidden)
		}
	}

	w = do(e, http.MethodGet, "/v1/content/pages/my-slug?kind=tutorial&platform=web&client_version=2.0&locale=en-US", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get %d", w.Code)
	}
	if getSlug != "my-slug" || getFl.Query != "" || getFl.Kind != "tutorial" {
		t.Fatalf("get %s filter %+v", getSlug, getFl)
	}
	detail := decMap(t, w)
	pg, _ := detail["page"].(map[string]any)
	if pg["body"] != "正文" || hasKey(pg, "id") {
		t.Fatalf("detail %v", pg)
	}

	f.called = false
	w = do(e, http.MethodPost, "/v1/content/pages/my-slug/feedback?platform=any", `{"version":2}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing helpful %d", w.Code)
	}
	if f.called {
		t.Fatal("feedback service called without helpful")
	}
	fields := decMap(t, w)
	errObj, _ := fields["error"].(map[string]any)
	fld, _ := errObj["fields"].(map[string]any)
	if fld["helpful"] == nil {
		t.Fatalf("fields %v", fld)
	}

	w = do(e, http.MethodPost, "/v1/content/pages/my-slug/feedback?platform=any", `{"helpful":false,"version":2}`)
	if w.Code != http.StatusOK {
		t.Fatalf("feedback %d %s", w.Code, w.Body)
	}
	if fbHelpful != false || fbVer != 2 || fbSlug != "my-slug" || fbUser != userID || fbFl.Platform != "any" {
		t.Fatalf("feedback args %v %d %s", fbHelpful, fbVer, fbSlug)
	}
	ok := decMap(t, w)
	if ok["ok"] != true {
		t.Fatalf("ok %v", ok)
	}
}

func TestAnonymousRoutesRequireAuth(t *testing.T) {
	require401 := func(c *gin.Context) {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		c.Abort()
	}
	kit := route.Kit{
		RequireAuth:  require401,
		RecentReauth: func(c *gin.Context) { c.Next() },
		Idempotency:  func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	}
	f := &fakeSvc{}
	adminE := newEngine(f, true, nil, kit)
	publicE := newEngine(f, false, nil, kit)

	adminPaths := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/content-pages", ""},
		{http.MethodGet, "/v1/content-pages/" + uuid.New().String(), ""},
		{http.MethodPost, "/v1/content-pages", `{"slug":"a","kind":"page","title":"tt","body":"1234567890","locale":"zh-CN","target_platforms":[],"target_plan_ids":[],"visibility":"authenticated","status":"draft","expected_latest_version":0}`},
		{http.MethodPost, "/v1/content-pages/" + uuid.New().String() + "/archive", `{"expected_version":1}`},
	}
	for _, r := range adminPaths {
		w := do(adminE, r.method, r.path, r.body)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("admin %s %s -> %d", r.method, r.path, w.Code)
		}
	}

	publicPaths := []struct {
		method, path, body string
	}{
		{http.MethodGet, "/v1/content/pages", ""},
		{http.MethodGet, "/v1/content/pages/foo", ""},
		{http.MethodPost, "/v1/content/pages/foo/feedback", `{"helpful":true,"version":1}`},
	}
	for _, r := range publicPaths {
		w := do(publicE, r.method, r.path, r.body)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("public %s %s -> %d", r.method, r.path, w.Code)
		}
	}
	if f.called {
		t.Fatal("service called without auth")
	}
}
