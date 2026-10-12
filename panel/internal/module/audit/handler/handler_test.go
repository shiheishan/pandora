package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// fakeSvc 记录 List/Export 入参，行为由字段开关控制。
type fakeSvc struct {
	lastListF   service.Filter
	lastListP   service.Page
	lastExportF service.Filter

	listRows  []service.Row
	listTotal int64

	exportBeforeOpen error  // 在 open 之前返回
	exportBody       string // open 后写入
}

func (f *fakeSvc) List(ctx context.Context, filter service.Filter, page service.Page) ([]service.Row, int64, error) {
	f.lastListF = filter
	f.lastListP = page
	return f.listRows, f.listTotal, nil
}

func (f *fakeSvc) ExportCSV(ctx context.Context, filter service.Filter, open func() io.Writer) error {
	f.lastExportF = filter
	if f.exportBeforeOpen != nil {
		return f.exportBeforeOpen
	}
	w := open()
	_, err := w.Write([]byte(f.exportBody))
	return err
}

// kitTrace 按顺序记录挂上的路由级中间件（auth = RequireAuth，reauth = RecentReauth）。
type kitTrace struct {
	steps []string
}

func newTestRouter(svc Service) (*gin.Engine, *kitTrace) {
	gin.SetMode(gin.TestMode)
	tr := &kitTrace{}
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	v1 := e.Group("/v1")
	kit := route.Kit{
		RequireAuth: func(c *gin.Context) {
			tr.steps = append(tr.steps, "auth")
			c.Next()
		},
		RecentReauth: func(c *gin.Context) {
			tr.steps = append(tr.steps, "reauth")
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) { c.Next() }
		},
		Switch: func(code string) gin.HandlerFunc {
			return func(c *gin.Context) { c.Next() }
		},
	}
	New(svc).RegisterAdmin(v1, kit)
	return e, tr
}

type errEnvelope struct {
	Error struct {
		Code   string            `json:"code"`
		Fields map[string]string `json:"fields"`
	} `json:"error"`
}

func getJSON(t *testing.T, e *gin.Engine, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func getExport(t *testing.T, e *gin.Engine, path string) (*httptest.ResponseRecorder, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w, w.Body.Bytes()
}

var wantEventKeys = []string{
	"id", "occurred_at", "actor_kind", "actor_email", "action", "resource_type", "resource_id",
	"api_domain", "outcome", "reason", "resource_label", "auth_context", "source_ip",
}

func TestListAuditQueryAndResponse(t *testing.T) {
	email := "ops@example.com"
	fake := &fakeSvc{
		listTotal: 7,
		listRows: []service.Row{{
			ID:         uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab"),
			OccurredAt: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
			ActorKind:  "system",
			ActorEmail: &email,
			Action:     "order.paid",
			Outcome:    "failure",
		}},
	}
	e, tr := newTestRouter(fake)

	path := "/v1/audit?limit=20&offset=40&action=order.&actor_kind=system&outcome=failure&q=abc"
	code, body := getJSON(t, e, path)
	if code != http.StatusOK {
		t.Fatalf("status %d body %s", code, body)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(top) != 2 || top["events"] == nil || top["total"] == nil {
		t.Fatalf("顶层键应为 events、total，实际 %v", mapKeys(top))
	}

	var total int64
	if err := json.Unmarshal(top["total"], &total); err != nil || total != 7 {
		t.Fatalf("total 应为 7，得 %d err %v", total, err)
	}

	var events []map[string]any
	if err := json.Unmarshal(top["events"], &events); err != nil || len(events) != 1 {
		t.Fatalf("events: %v len %d", err, len(events))
	}
	ev := events[0]
	got := keysOf(ev)
	sort.Strings(got)
	want := append([]string(nil), wantEventKeys...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("事件键集合不对，got %v want %v", got, want)
	}
	if _, ok := ev["id"].(string); !ok {
		t.Fatalf("id 应为字符串，得 %T", ev["id"])
	}
	for _, k := range []string{"resource_type", "resource_id", "api_domain", "reason", "resource_label", "auth_context", "source_ip"} {
		if ev[k] != nil {
			t.Fatalf("%s 应为 null，得 %v", k, ev[k])
		}
	}
	if ev["actor_email"] != "ops@example.com" {
		t.Fatalf("actor_email %v", ev["actor_email"])
	}

	if fake.lastListP.Limit != 20 || fake.lastListP.Offset != 40 {
		t.Fatalf("Page %+v", fake.lastListP)
	}
	if fake.lastListF.ActionPrefix != "order." || fake.lastListF.ActorKind != "system" ||
		fake.lastListF.Outcome != "failure" || fake.lastListF.Query != "abc" {
		t.Fatalf("Filter %+v", fake.lastListF)
	}
	if strings.Join(tr.steps, ",") != "auth" {
		t.Fatalf("中间件 %v", tr.steps)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestListAuditEmptyEvents(t *testing.T) {
	fake := &fakeSvc{listTotal: 0, listRows: nil}
	e, _ := newTestRouter(fake)
	code, body := getJSON(t, e, "/v1/audit")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !strings.Contains(string(body), `"events":[]`) {
		t.Fatalf("body %s", body)
	}
}

func TestListAuditValidation(t *testing.T) {
	fake := &fakeSvc{}
	e, _ := newTestRouter(fake)

	code, body := getJSON(t, e, "/v1/audit?actor_kind=robot")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("robot status %d", code)
	}
	var env errEnvelope
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "validation_failed" {
		t.Fatalf("code %q", env.Error.Code)
	}

	code, body = getJSON(t, e, "/v1/audit?limit=abc")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("limit status %d", code)
	}
	_ = json.Unmarshal(body, &env)
	if env.Error.Code != "validation_failed" {
		t.Fatalf("limit code %q", env.Error.Code)
	}
}

func TestExportAuditHeadersAndFilter(t *testing.T) {
	fake := &fakeSvc{exportBody: "x\n"}
	e, tr := newTestRouter(fake)

	w, body := getExport(t, e, "/v1/audit/export?from=2026-01-01&to=2026-01-31")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, body)
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type %q", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `attachment; filename="audit-`) {
		t.Fatalf("Content-Disposition %q", cd)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control %q", w.Header().Get("Cache-Control"))
	}
	if string(body) != "x\n" {
		t.Fatalf("body %q", body)
	}

	wantFrom := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	if fake.lastExportF.From == nil || !fake.lastExportF.From.Equal(wantFrom) {
		t.Fatalf("From %v want %v", fake.lastExportF.From, wantFrom)
	}
	if fake.lastExportF.To == nil || !fake.lastExportF.To.Equal(wantTo) {
		t.Fatalf("To %v want %v", fake.lastExportF.To, wantTo)
	}
	if strings.Join(tr.steps, ",") != "auth,reauth" {
		t.Fatalf("中间件顺序 %v", tr.steps)
	}
}

func TestExportAuditDateValidation(t *testing.T) {
	fake := &fakeSvc{exportBody: "x\n"}
	e, _ := newTestRouter(fake)

	code, body := getJSON(t, e, "/v1/audit/export?from=2026-13-01")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("bad from status %d", code)
	}
	var env errEnvelope
	_ = json.Unmarshal(body, &env)
	if env.Error.Fields["from"] != "日期格式应为 YYYY-MM-DD" {
		t.Fatalf("fields %v", env.Error.Fields)
	}

	code, body = getJSON(t, e, "/v1/audit/export?from=2026-02-01&to=2026-01-01")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("range status %d", code)
	}
	_ = json.Unmarshal(body, &env)
	if env.Error.Fields["to"] != "结束日期不能早于开始日期" {
		t.Fatalf("fields %v", env.Error.Fields)
	}

	w, _ := getExport(t, e, "/v1/audit/export?from=2026-01-01&to=2026-01-01")
	if w.Code != http.StatusOK {
		t.Fatalf("same day status %d", w.Code)
	}
}

func TestExportAuditErrorBeforeOpen(t *testing.T) {
	fake := &fakeSvc{
		exportBeforeOpen: errs.Newf(errs.ValidationFailed, "超过 50000 行，请缩小时间范围"),
	}
	e, _ := newTestRouter(fake)

	code, body := getJSON(t, e, "/v1/audit/export?from=2026-01-01&to=2026-01-31")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d body %s", code, body)
	}
	var env errEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("json: %v", err)
	}
	if env.Error.Code != "validation_failed" {
		t.Fatalf("code %q body %s", env.Error.Code, body)
	}
}
