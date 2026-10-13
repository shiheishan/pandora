package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var (
	opsGroupID   = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	opsGenJobID  = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	opsProfileID = uuid.MustParse("33333333-3333-4333-8333-333333333333")
	opsDisableU1 = uuid.MustParse("44444444-4444-4444-8444-444444444444")
	opsDisableU2 = uuid.MustParse("55555555-5555-4555-8555-555555555555")
	opsActorID   = uuid.MustParse("66666666-6666-4666-8666-666666666666")
)

// opsKit 记录 RecentReauth 与 Idempotency 作用域是否经过。
type opsKit struct {
	reauth int
	idem   []string
}

// opsFake 实现运营相关 11 个 service 方法并记下调用参数。
type opsFake struct {
	Service
	lastBulk      service.BulkFilter
	lastExportLim int
	lastGenIn     service.GenerateInput
	lastProfile   uuid.UUID
	lastAccess    service.AccessLogQuery
	lastInclRev   bool
	lastRevKey    string
	lastRevNote   string
	lastDisKey    string
	lastDisIDs    []uuid.UUID
	lastDisReason string
}

func (f *opsFake) PreviewBulk(_ context.Context, fl service.BulkFilter) (service.BulkPreview, error) {
	f.lastBulk = fl
	plan := "基础套餐"
	end := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	return service.BulkPreview{
		Total: 2,
		Samples: []service.BulkSample{
			{Email: "with-plan@example.test", PlanName: &plan, PeriodEnd: &end},
			{Email: "no-plan@example.test", PlanName: nil, PeriodEnd: nil},
		},
	}, nil
}

func (f *opsFake) ExportUsers(_ context.Context, fl service.BulkFilter, limit int) ([]service.ExportRow, error) {
	f.lastBulk = fl
	f.lastExportLim = limit
	return []service.ExportRow{{
		Email: "=cmd@example.test", Status: "active", GroupName: "+grp",
		ActiveSubs: 1, Orders: 3, Paid: "CNY 12.30", CreatedAt: time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC), LastLoginAt: nil,
	}}, nil
}

func (f *opsFake) SubmitGeneration(_ context.Context, in service.GenerateInput) (service.GenerationJob, error) {
	f.lastGenIn = in
	return service.GenerationJob{
		ID: opsGenJobID, ActorID: opsActorID, Status: service.JobQueued,
		Total: in.Count, Completed: 0, Failed: 0,
		EmailPrefix: in.EmailPrefix, EmailDomain: in.EmailDomain, GroupID: nil,
		Reason: in.Reason, Error: nil, ResultAvailable: false, ResultExpiresAt: nil,
		CreatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		StartedAt: nil, FinishedAt: nil,
	}, nil
}

func (f *opsFake) ListGenerationJobs(context.Context) ([]service.GenerationJob, error) {
	return []service.GenerationJob{f.genJob()}, nil
}

func (f *opsFake) genJob() service.GenerationJob {
	return service.GenerationJob{
		ID: opsGenJobID, ActorID: opsActorID, Status: service.JobSucceeded,
		Total: 3, Completed: 3, Failed: 0, EmailPrefix: "gen", EmailDomain: "example.test",
		GroupID: nil, Reason: "压测账号批量准备", Error: nil, ResultAvailable: true,
		ResultExpiresAt: nil,
		CreatedAt:       time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		StartedAt:       nil, FinishedAt: nil,
	}
}

func (f *opsFake) GetGenerationJob(_ context.Context, id uuid.UUID) (service.GenerationJob, error) {
	if id != opsGenJobID {
		return service.GenerationJob{}, nil
	}
	return f.genJob(), nil
}

func (f *opsFake) GenerationResult(context.Context, uuid.UUID) ([]service.GeneratedUser, error) {
	return []service.GeneratedUser{{Email: "gen1@example.test", Password: "=Ab12cd34"}}, nil
}

func (f *opsFake) UserProfile(_ context.Context, id uuid.UUID) (service.UserProfile, error) {
	f.lastProfile = id
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	return service.UserProfile{
		Events: []service.ProfileEvent{{
			Action: "login", Outcome: "success", IP: "203.0.113.10",
			UserAgent: "ua-test", Domain: "admin", At: at,
		}},
		IPs: []service.ProfileIP{{
			IP: "203.0.113.10", Count: 2, First: at, Last: at, Accounts: 1,
		}},
		Related: []service.UserRef{},
		Fetches: []service.FetchRecord{{
			IP: "203.0.113.11", UserAgent: "fetch-ua", Family: "clash",
			Result: "ok", Format: "yaml", At: at,
		}},
		FetchSources7d: 1, RegisteredIP: "203.0.113.1",
	}, nil
}

func (f *opsFake) AccessLog(_ context.Context, q service.AccessLogQuery) ([]service.AccessItem, error) {
	f.lastAccess = q
	uid := opsProfileID
	fullAt := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	minAt := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	return []service.AccessItem{
		{
			Category: "login", Action: "password", UserID: &uid, UserEmail: "u@example.test",
			IP: "203.0.113.20", Geo: "测试", NetworkKind: "residential",
			UserAgent: "ua", Outcome: "success", At: fullAt,
		},
		{Category: "other", At: minAt},
	}, nil
}

func (f *opsFake) IPClusters(_ context.Context, includeReviewed bool) ([]service.IPCluster, error) {
	f.lastInclRev = includeReviewed
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	decided := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	return []service.IPCluster{
		{
			Key: "key-a", IP: "203.0.113.30", Geo: "", NetworkKind: "", Risk: "low",
			Accounts: 2, Events: 5, First: at, Last: at,
			Emails: []string{"a@example.test"},
			Users: []service.ClusterUser{{
				ID: opsDisableU1, Email: "a@example.test", Status: "active", ActivePlan: nil,
			}},
			Review: nil,
		},
		{
			Key: "key-b", IP: "203.0.113.31", Geo: "", NetworkKind: "", Risk: "mid",
			Accounts: 1, Events: 1, First: at, Last: at, Emails: []string{"b@example.test"},
			Users: []service.ClusterUser{{
				ID: opsDisableU2, Email: "b@example.test", Status: "active", ActivePlan: nil,
			}},
			Review: &service.ClusterReview{Decision: "normal", DecidedAt: decided, ExpiresAt: nil},
		},
	}, nil
}

func (f *opsFake) ReviewIPCluster(_ context.Context, key, note string) (time.Time, error) {
	f.lastRevKey, f.lastRevNote = key, note
	return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), nil
}

func (f *opsFake) DisableIPClusterAccounts(_ context.Context, key string, ids []uuid.UUID, reason string) (service.DisableClusterResult, error) {
	f.lastDisKey, f.lastDisIDs, f.lastDisReason = key, ids, reason
	return service.DisableClusterResult{Disabled: 1, Skipped: []service.ClusterSkip{}}, nil
}

// opsEngine 挂后台运营路由并返回记录型 kit。
func opsEngine(svc Service) (*gin.Engine, *opsKit) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	p := &authn.Principal{UserID: opsActorID, SessionID: "adm", Audience: authn.AudienceAdmin}
	e.Use(func(c *gin.Context) { authn.Set(c, *p); c.Next() })
	kit := &opsKit{}
	rk := route.Kit{
		RequireAuth: func(c *gin.Context) { c.Next() },
		RecentReauth: func(c *gin.Context) {
			kit.reauth++
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				kit.idem = append(kit.idem, scope)
				c.Next()
			}
		},
		Switch: func(string) gin.HandlerFunc { return func(c *gin.Context) { c.Next() } },
	}
	New(svc, nil, 0).RegisterAdmin(e.Group("/v1"), rk)
	return e, kit
}

func decodeMap(t *testing.T, body []byte) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("json: %v body=%s", err, body)
	}
	return m
}

func keySet(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertKeys(t *testing.T, m map[string]any, want ...string) {
	sort.Strings(want)
	if got := keySet(m); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("键集 got %v want %v", got, want)
	}
}

func errFields(t *testing.T, body []byte) map[string]string {
	m := decodeMap(t, body)
	errObj, _ := m["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("无 error: %v", m)
	}
	fields, _ := errObj["fields"].(map[string]any)
	out := map[string]string{}
	for k, v := range fields {
		out[k], _ = v.(string)
	}
	return out
}

func TestOpsBulkPreviewShape(t *testing.T) {
	f := &opsFake{}
	e, _ := opsEngine(f)
	body := `{"status":"active","group_id":"` + opsGroupID.String() + `","expires_within_days":7,"sub_state":"active"}`
	w := do(e, http.MethodPost, "/v1/users/bulk/preview", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "sample_rows", "samples", "total")
	if f.lastBulk.GroupID == nil || *f.lastBulk.GroupID != opsGroupID || f.lastBulk.ExpiresWithinDays != 7 {
		t.Fatalf("filter %+v", f.lastBulk)
	}
	samples, _ := got["samples"].([]any)
	if len(samples) != 2 || samples[0] != "with-plan@example.test" || samples[1] != "no-plan@example.test" {
		t.Fatalf("samples %v", samples)
	}
	rows, _ := got["sample_rows"].([]any)
	row1, _ := rows[1].(map[string]any)
	if row1["plan_name"] != nil || row1["current_period_end"] != nil {
		t.Fatalf("row1 nulls %v", row1)
	}
	for _, k := range []string{"plan_name", "current_period_end"} {
		if _, ok := row1[k]; !ok {
			t.Fatalf("缺 null 键 %s: %v", k, row1)
		}
	}

	w = do(e, http.MethodPost, "/v1/users/bulk/preview", strings.TrimSuffix(body, "}")+`,"extra":1}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status %d", w.Code)
	}
	w = do(e, http.MethodPost, "/v1/users/bulk/preview", `{"group_id":"x"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 group_id status %d", w.Code)
	}
	if errFields(t, w.Body.Bytes())["group_id"] == "" {
		t.Fatal("fields 缺 group_id")
	}
	w = do(e, http.MethodPost, "/v1/users/bulk/preview", `{"status":"pending"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("坏 status status %d", w.Code)
	}
}

func TestOpsBulkExportCSV(t *testing.T) {
	f := &opsFake{}
	e, kit := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/users/bulk/export?status=active&has_active_sub=true&expires_within_days=abc&limit=5", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if kit.reauth != 1 {
		t.Fatalf("reauth %d", kit.reauth)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/csv") {
		t.Fatalf("Content-Type %q", ct)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "users.csv") {
		t.Fatal("Content-Disposition")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("Cache-Control")
	}
	raw := w.Body.Bytes()
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatal("缺 BOM")
	}
	recs, err := csv.NewReader(strings.NewReader(string(raw[3:]))).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("csv %v %d", err, len(recs))
	}
	if strings.Join(recs[0], ",") != "邮箱,状态,分组,生效订阅,订单数,累计实付,注册时间,最近登录" {
		t.Fatalf("表头 %v", recs[0])
	}
	if recs[1][0] != "'=cmd@example.test" || recs[1][2] != "'+grp" || recs[1][3] != "1" || recs[1][4] != "3" || recs[1][5] != "CNY 12.30" || recs[1][7] != "" {
		t.Fatalf("行 %v", recs[1])
	}
	if f.lastExportLim != 5 || f.lastBulk.HasActiveSub == nil || !*f.lastBulk.HasActiveSub || f.lastBulk.ExpiresWithinDays != -1 {
		t.Fatalf("export 参数 lim=%d bulk=%+v", f.lastExportLim, f.lastBulk)
	}
}

func TestOpsBulkGenerateShape(t *testing.T) {
	f := &opsFake{}
	e, kit := opsEngine(f)
	w := do(e, http.MethodPost, "/v1/users/bulk/generate",
		`{"count":3,"email_prefix":"gen","email_domain":"example.test","reason":"压测账号批量准备"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if kit.reauth != 1 || len(kit.idem) != 1 || kit.idem[0] != "user_bulk_generate" {
		t.Fatalf("kit reauth=%d idem=%v", kit.reauth, kit.idem)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got,
		"actor_id", "completed", "created_at", "email_domain", "email_prefix", "error", "failed",
		"finished_at", "group_id", "id", "reason", "result_available", "result_expires_at",
		"started_at", "status", "total",
	)
	for _, k := range []string{"group_id", "error", "result_expires_at", "started_at", "finished_at"} {
		if got[k] != nil {
			t.Fatalf("%s 应为 null got %v", k, got[k])
		}
	}
	w = do(e, http.MethodPost, "/v1/users/bulk/generate", `{"count":1,"email_prefix":"a","email_domain":"example.test","reason":"x","group_id":"x"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("group_id status %d", w.Code)
	}
}

type opsFakeEmptyJobs struct {
	opsFake
}

func (f *opsFakeEmptyJobs) ListGenerationJobs(context.Context) ([]service.GenerationJob, error) {
	return []service.GenerationJob{}, nil
}

func TestOpsGenerationJobsShape(t *testing.T) {
	e, _ := opsEngine(&opsFake{})
	w := do(e, http.MethodGet, "/v1/users/bulk/generate/jobs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status %d", w.Code)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "jobs")
	jobs, _ := got["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs len %d", len(jobs))
	}
	job0, _ := jobs[0].(map[string]any)
	assertKeys(t, job0,
		"actor_id", "completed", "created_at", "email_domain", "email_prefix", "error", "failed",
		"finished_at", "group_id", "id", "reason", "result_available", "result_expires_at",
		"started_at", "status", "total",
	)

	e2, _ := opsEngine(&opsFakeEmptyJobs{})
	w = do(e2, http.MethodGet, "/v1/users/bulk/generate/jobs", "")
	got = decodeMap(t, w.Body.Bytes())
	jobs, _ = got["jobs"].([]any)
	if jobs == nil {
		t.Fatal("jobs 不应为 null")
	}
	if len(jobs) != 0 {
		t.Fatalf("空 jobs %v", jobs)
	}

	w = do(e, http.MethodGet, "/v1/users/bulk/generate/jobs/not-a-uuid", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("坏 id status %d", w.Code)
	}
	w = do(e, http.MethodGet, "/v1/users/bulk/generate/jobs/"+opsGenJobID.String(), "")
	if w.Code != http.StatusOK {
		t.Fatalf("get job %d", w.Code)
	}
	assertKeys(t, decodeMap(t, w.Body.Bytes()),
		"actor_id", "completed", "created_at", "email_domain", "email_prefix", "error", "failed",
		"finished_at", "group_id", "id", "reason", "result_available", "result_expires_at",
		"started_at", "status", "total",
	)
}

func TestOpsGenerationResultCSV(t *testing.T) {
	e, kit := opsEngine(&opsFake{})
	w := do(e, http.MethodGet, "/v1/users/bulk/generate/jobs/"+opsGenJobID.String()+"/result", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if kit.reauth != 1 {
		t.Fatalf("reauth %d", kit.reauth)
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "generated-users.csv") {
		t.Fatal("Content-Disposition")
	}
	raw := w.Body.Bytes()
	recs, err := csv.NewReader(strings.NewReader(string(raw[3:]))).ReadAll()
	if err != nil || len(recs) != 2 {
		t.Fatalf("csv %v", err)
	}
	if strings.Join(recs[0], ",") != "邮箱,初始密码" || recs[1][1] != "=Ab12cd34" {
		t.Fatalf("行 %v", recs[1])
	}
}

func TestOpsUserProfileShape(t *testing.T) {
	f := &opsFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/users/"+opsProfileID.String()+"/profile", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if f.lastProfile != opsProfileID {
		t.Fatalf("profile id %v", f.lastProfile)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "events", "fetch_sources_7d", "fetches", "ips", "registered_ip", "related")
	related, _ := got["related"].([]any)
	if related == nil || len(related) != 0 {
		t.Fatalf("related %v", related)
	}
	ev0, _ := got["events"].([]any)[0].(map[string]any)
	assertKeys(t, ev0, "action", "at", "domain", "ip", "outcome", "ua")
	ip0, _ := got["ips"].([]any)[0].(map[string]any)
	assertKeys(t, ip0, "accounts", "count", "first", "ip", "last")
	fx0, _ := got["fetches"].([]any)[0].(map[string]any)
	assertKeys(t, fx0, "at", "family", "format", "ip", "result", "ua")

	w = do(e, http.MethodGet, "/v1/users/not-a-uuid/profile", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("404 %d", w.Code)
	}
}

func TestOpsAccessLogShape(t *testing.T) {
	f := &opsFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/access-log?category=login&limit=abc&offset=-3&user=u@example.test", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if f.lastAccess.Limit != 50 || f.lastAccess.Offset != 0 || f.lastAccess.Category != "login" || f.lastAccess.User != "u@example.test" {
		t.Fatalf("query %+v", f.lastAccess)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "items")
	items, _ := got["items"].([]any)
	assertKeys(t, items[0].(map[string]any),
		"action", "category", "geo", "ip", "network_kind", "occurred_at", "outcome", "user_agent", "user_email", "user_id",
	)
	assertKeys(t, items[1].(map[string]any), "category", "occurred_at")

	_ = do(e, http.MethodGet, "/v1/access-log?limit=20&offset=40", "")
	if f.lastAccess.Limit != 20 || f.lastAccess.Offset != 40 {
		t.Fatalf("分页 %+v", f.lastAccess)
	}
}

func TestOpsIPClustersShape(t *testing.T) {
	f := &opsFake{}
	e, _ := opsEngine(f)
	w := do(e, http.MethodGet, "/v1/ip-clusters?include_reviewed=1", "")
	if !f.lastInclRev {
		t.Fatal("include_reviewed 应为 true")
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "clusters")
	c0, _ := got["clusters"].([]any)[0].(map[string]any)
	assertKeys(t, c0, "accounts", "emails", "events", "first", "geo", "ip", "key", "last", "network_kind", "review", "risk", "users")
	if c0["review"] != nil {
		t.Fatalf("review %v", c0["review"])
	}
	u0, _ := c0["users"].([]any)[0].(map[string]any)
	assertKeys(t, u0, "active_plan", "email", "id", "status")
	if u0["active_plan"] != nil {
		t.Fatal("active_plan 应为 null")
	}
	c1, _ := got["clusters"].([]any)[1].(map[string]any)
	rev, _ := c1["review"].(map[string]any)
	assertKeys(t, rev, "decided_at", "decision", "expires_at")
	if rev["expires_at"] != nil {
		t.Fatalf("expires_at %v", rev["expires_at"])
	}

	f.lastInclRev = true
	_ = do(e, http.MethodGet, "/v1/ip-clusters", "")
	if f.lastInclRev {
		t.Fatal("默认应为 false")
	}
}

func TestOpsReviewIPCluster(t *testing.T) {
	f := &opsFake{}
	e, kit := opsEngine(f)
	w := do(e, http.MethodPost, "/v1/ip-clusters/abc/review", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if kit.reauth != 0 || len(kit.idem) != 0 {
		t.Fatalf("不应 reauth/idem reauth=%d idem=%v", kit.reauth, kit.idem)
	}
	if f.lastRevKey != "abc" {
		t.Fatalf("key %q", f.lastRevKey)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"key":"abc"`) || !strings.Contains(body, `"decision":"normal"`) ||
		!strings.Contains(body, `"expires_at":"2026-11-12T00:00:00Z"`) {
		t.Fatalf("body %s", body)
	}
}

func TestOpsDisableIPClusterAccounts(t *testing.T) {
	f := &opsFake{}
	e, kit := opsEngine(f)
	body := `{"user_ids":["` + opsDisableU1.String() + `","` + opsDisableU2.String() + `"],"reason":"批量注册嫌疑账号"}`
	w := do(e, http.MethodPost, "/v1/ip-clusters/abc/disable-accounts", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if kit.reauth != 1 || len(kit.idem) != 1 || kit.idem[0] != "ip_cluster_disable" {
		t.Fatalf("kit reauth=%d idem=%v", kit.reauth, kit.idem)
	}
	if f.lastDisKey != "abc" || f.lastDisReason != "批量注册嫌疑账号" || len(f.lastDisIDs) != 2 {
		t.Fatalf("disable args key=%q ids=%v", f.lastDisKey, f.lastDisIDs)
	}
	got := decodeMap(t, w.Body.Bytes())
	assertKeys(t, got, "disabled", "skipped")
	skipped, _ := got["skipped"].([]any)
	if skipped == nil || len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	w = do(e, http.MethodPost, "/v1/ip-clusters/abc/disable-accounts",
		`{"user_ids":["x"],"reason":"批量注册嫌疑账号"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("user_ids status %d", w.Code)
	}
	if errFields(t, w.Body.Bytes())["user_ids"] == "" {
		t.Fatal("fields 缺 user_ids")
	}
}

func TestListUsersSubState(t *testing.T) {
	f := &fakeSvc{}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/users?sub_state=active", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if f.lastFilter.SubState != "active" {
		t.Fatalf("SubState %q", f.lastFilter.SubState)
	}
}
