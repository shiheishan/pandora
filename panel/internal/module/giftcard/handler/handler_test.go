package handler

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeSvc 只实现用例设置了的方法；没设置时走嵌入的 nil 接口（被调到会 panic）。
type fakeSvc struct {
	Service
	saveTemplate func(service.TemplateInput) (service.Template, error)
	generate     func(service.GenerateInput) (service.Generated, error)
	exportBatch  func(uuid.UUID) (service.BatchExport, error)
	listCodes    func(service.CodeFilter, int, int) ([]service.Code, int64, error)
	preview      func(string) (service.CardPreview, error)
	toggle       func(uuid.UUID, bool) error
	exportCodes  func(service.CodeFilter) ([]service.CodeReportRow, error)
	redeem       func(service.RedeemInput) (service.RedeemResult, error)
}

func (f *fakeSvc) SaveTemplate(_ context.Context, in service.TemplateInput) (service.Template, error) {
	return f.saveTemplate(in)
}

func (f *fakeSvc) GenerateCodes(_ context.Context, in service.GenerateInput) (service.Generated, error) {
	return f.generate(in)
}

func (f *fakeSvc) ExportBatch(_ context.Context, id uuid.UUID) (service.BatchExport, error) {
	return f.exportBatch(id)
}

func (f *fakeSvc) ListCodes(_ context.Context, cf service.CodeFilter, limit, offset int) ([]service.Code, int64, error) {
	return f.listCodes(cf, limit, offset)
}

func (f *fakeSvc) ToggleCode(_ context.Context, id uuid.UUID, disabled bool) error {
	return f.toggle(id, disabled)
}

func (f *fakeSvc) ExportCodes(_ context.Context, cf service.CodeFilter) ([]service.CodeReportRow, error) {
	return f.exportCodes(cf)
}

func (f *fakeSvc) Preview(_ context.Context, code string) (service.CardPreview, error) {
	return f.preview(code)
}

func (f *fakeSvc) Redeem(_ context.Context, in service.RedeemInput) (service.RedeemResult, error) {
	return f.redeem(in)
}

// kitRec 记下路由链路上用到的幂等作用域、开关与重认证。
type kitRec struct {
	scopes   []string
	switches []string
	reauth   int
}

func pass(c *gin.Context) { c.Next() }

// newEngine 搭一个挂了 giftcard 路由的 gin 引擎并放入登录主体；admin 为真挂后台路由。
func newEngine(svc Service, admin bool, rec *kitRec) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	audience := authn.AudiencePublic
	if admin {
		audience = authn.AudienceAdmin
	}
	p := authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: audience}
	e.Use(func(c *gin.Context) { authn.Set(c, p); c.Next() })
	if rec == nil {
		rec = &kitRec{}
	}
	kit := route.Kit{
		RequireAuth: pass,
		RecentReauth: func(c *gin.Context) {
			rec.reauth++
			c.Next()
		},
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.scopes = append(rec.scopes, scope)
				c.Next()
			}
		},
		Switch: func(code string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.switches = append(rec.switches, code)
				c.Next()
			}
		},
	}
	h := New(svc, nil)
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

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("状态 %d，期望 %d：%s", w.Code, want, w.Body)
	}
}

func obj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("不是 JSON 对象: %v %s", err, b)
	}
	return m
}

// 预览：service 的「不能用」原样回 422 与那一句话。
func TestPreviewUnusable(t *testing.T) {
	f := &fakeSvc{preview: func(string) (service.CardPreview, error) { return service.CardPreview{}, service.ErrCodeUnusable }}
	w := do(newEngine(f, false, nil), http.MethodPost, "/v1/gift-cards/preview", `{"code":"NOPE12345"}`)
	mustStatus(t, w, http.StatusUnprocessableEntity)
	if !strings.Contains(w.Body.String(), "卡密无效、已被使用或已过期") {
		t.Fatalf("错误文案 %s", w.Body)
	}
}

// 兑换：choice 透传、余额按分、挂了开关与幂等作用域、带重放转换。
func TestRedeemPassesChoiceAndCents(t *testing.T) {
	subID := uuid.New()
	var got service.RedeemInput
	f := &fakeSvc{redeem: func(in service.RedeemInput) (service.RedeemResult, error) {
		got = in
		return service.RedeemResult{TemplateName: "卡", Type: service.TypeGeneral, Balance: decimal.RequireFromString("12.34"), Summary: []string{"余额 +¥12.34"}}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/gift-cards/redeem",
		`{"code":"ABCD2345EFGH","choice":{"kind":"extend_days","subscription_id":"`+subID.String()+`"}}`)
	mustStatus(t, w, http.StatusOK)
	if got.Code != "ABCD2345EFGH" || got.Choice == nil || got.Choice.Kind != service.KindExtendDays ||
		got.Choice.SubscriptionID == nil || *got.Choice.SubscriptionID != subID || got.Reply == nil {
		t.Fatalf("service 收到 %+v", got)
	}
	if m := obj(t, w.Body.Bytes()); m["balance"] != float64(1234) {
		t.Fatalf("余额 %v，期望 1234 分", m["balance"])
	}
	if len(rec.switches) != 1 || rec.switches[0] != SwitchRedeem || len(rec.scopes) != 1 || rec.scopes[0] != ScopeRedeem {
		t.Fatalf("链路 %+v", rec)
	}

	bad := do(newEngine(f, false, nil), http.MethodPost, "/v1/gift-cards/redeem", `{"code":"X","choice":{"kind":"renew","subscription_id":"bad"}}`)
	mustStatus(t, bad, http.StatusUnprocessableEntity)
}

// 生码：首发响应带明文样例，重放响应的 sample 恒为空数组。
func TestGenerateReplayHasNoPlaintext(t *testing.T) {
	g := service.Generated{BatchID: uuid.New(), Count: 2, Sample: []string{"AAAA2222BBBB", "CCCC3333DDDD"},
		Batch: service.Batch{ID: uuid.New(), TemplateID: uuid.New(), Count: 2, CreatedAt: time.Now()}}
	var in service.GenerateInput
	f := &fakeSvc{generate: func(x service.GenerateInput) (service.Generated, error) { in = x; return g, nil }}
	rec := &kitRec{}
	tid := uuid.New()
	w := do(newEngine(f, true, rec), http.MethodPost, "/v1/gift-cards/"+tid.String()+"/codes",
		`{"count":2,"prefix":"T","expires_at":"2030-01-02T03:04:05Z"}`)
	mustStatus(t, w, http.StatusOK)
	if in.TemplateID != tid || in.Count != 2 || in.ExpiresAt == nil || in.Reply == nil {
		t.Fatalf("service 收到 %+v", in)
	}
	if s := obj(t, w.Body.Bytes())["sample"].([]any); len(s) != 2 {
		t.Fatalf("首发样例 %v", s)
	}
	if rec.reauth != 1 || len(rec.scopes) != 1 || rec.scopes[0] != ScopeCodesGenerate {
		t.Fatalf("链路 %+v", rec)
	}

	r, err := generatedReply(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.Body), "AAAA2222BBBB") {
		t.Fatal("重放响应里有明文卡密")
	}
	if s, ok := obj(t, r.Body)["sample"].([]any); !ok || len(s) != 0 {
		t.Fatalf("重放样例应为空数组: %s", r.Body)
	}

	bad := do(newEngine(f, true, nil), http.MethodPost, "/v1/gift-cards/"+tid.String()+"/codes", `{"count":1,"expires_at":"明天"}`)
	mustStatus(t, bad, http.StatusUnprocessableEntity)
}

// 一次性导出：BOM、不缓存、文件名、列名、公式注入防护。
func TestExportBatchCSV(t *testing.T) {
	bid := uuid.New()
	exp := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	f := &fakeSvc{exportBatch: func(id uuid.UUID) (service.BatchExport, error) {
		if id != bid {
			t.Fatalf("批次 id %s", id)
		}
		return service.BatchExport{
			Batch: service.Batch{ID: bid},
			Rows:  []service.ExportRow{{Code: "AAAA2222BBBB", Status: service.CodeUnused, ExpiresAt: &exp, TemplateName: "=cmd"}},
		}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, true, rec), http.MethodPost, "/v1/gift-cards/batches/"+bid.String()+"/export", "")
	mustStatus(t, w, http.StatusOK)
	if rec.reauth != 1 || len(rec.scopes) != 1 || rec.scopes[0] != ScopeBatchExport {
		t.Fatalf("链路 %+v", rec)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("缺 Cache-Control: no-store")
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "gift-codes-"+bid.String()[:8]+".csv") {
		t.Fatalf("文件名 %q", cd)
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, utf8BOM) {
		t.Fatal("缺 BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(body[len(utf8BOM):])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || strings.Join(rows[0], ",") != "卡密,状态,有效期,模板" {
		t.Fatalf("CSV %v", rows)
	}
	if rows[1][0] != "AAAA2222BBBB" || rows[1][2] != "2030-01-02 03:04" || rows[1][3] != "'=cmd" {
		t.Fatalf("数据行 %v", rows[1])
	}
}

// 卡密列表：筛选 id 格式不对回 400，service 不被调用。
func TestListCodesBadFilter(t *testing.T) {
	f := &fakeSvc{listCodes: func(service.CodeFilter, int, int) ([]service.Code, int64, error) {
		t.Fatal("不该调到 service")
		return nil, 0, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/gift-cards/codes?template_id=nope", "")
	mustStatus(t, w, http.StatusBadRequest)
}

// 保存模板：分转元、id 解析。
func TestSaveTemplateCents(t *testing.T) {
	planID := uuid.New()
	var got service.TemplateInput
	f := &fakeSvc{saveTemplate: func(in service.TemplateInput) (service.Template, error) {
		got = in
		return service.Template{ID: uuid.New(), Name: in.Name, Type: in.Type, Rewards: in.Rewards}, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodPost, "/v1/gift-cards",
		`{"name":"卡","type":"general","rewards":{"balance":1999},"conditions":{"allowed_plan_ids":["`+planID.String()+`"]}}`)
	mustStatus(t, w, http.StatusOK)
	if !got.Rewards.Balance.Equal(decimal.RequireFromString("19.99")) || got.ID != nil ||
		len(got.Conditions.AllowedPlanIDs) != 1 || got.Conditions.AllowedPlanIDs[0] != planID {
		t.Fatalf("service 收到 %+v", got)
	}
	tpl := obj(t, w.Body.Bytes())["template"].(map[string]any)
	if r := tpl["rewards"].(map[string]any); r["balance"] != float64(1999) {
		t.Fatalf("响应余额 %v", r["balance"])
	}

	bad := do(newEngine(f, true, nil), http.MethodPost, "/v1/gift-cards", `{"name":"卡","type":"plan","rewards":{"plan_id":"nope"}}`)
	mustStatus(t, bad, http.StatusUnprocessableEntity)
}

// 启停卡密要重认证（恢复等于让码重新生效）；disabled 透传。
func TestToggleCodeRequiresReauth(t *testing.T) {
	cid := uuid.New()
	var gotID uuid.UUID
	var gotDisabled bool
	f := &fakeSvc{toggle: func(id uuid.UUID, d bool) error { gotID, gotDisabled = id, d; return nil }}
	rec := &kitRec{}
	w := do(newEngine(f, true, rec), http.MethodPost, "/v1/gift-cards/codes/"+cid.String()+"/toggle", `{"disabled":false}`)
	mustStatus(t, w, http.StatusOK)
	if rec.reauth != 1 {
		t.Fatalf("启停应挂重认证，reauth=%d", rec.reauth)
	}
	if gotID != cid || gotDisabled {
		t.Fatalf("service 收到 %s %v", gotID, gotDisabled)
	}
	if m := obj(t, w.Body.Bytes()); m["disabled"] != false {
		t.Fatalf("响应 %v", m)
	}
}

// 掩码报表：重认证、BOM、不缓存、文件名、列名、筛选透传、公式注入防护、时间按 UTC。
func TestExportCodesReportCSV(t *testing.T) {
	tid, bid := uuid.New(), uuid.New()
	created := time.Date(2030, 1, 2, 3, 4, 0, 0, time.UTC)
	var got service.CodeFilter
	f := &fakeSvc{exportCodes: func(cf service.CodeFilter) ([]service.CodeReportRow, error) {
		got = cf
		return []service.CodeReportRow{{
			CodeMasked: "VIPABCD••••••••", Status: service.CodeUsed, TemplateName: "+sum", BatchID: bid,
			CreatedAt: created, UsedEmail: "@evil.example", UsedAt: &created,
		}}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, true, rec), http.MethodGet, "/v1/gift-cards/codes/report?template_id="+tid.String()+"&status=used", "")
	mustStatus(t, w, http.StatusOK)
	if rec.reauth != 1 {
		t.Fatalf("掩码报表应挂重认证，reauth=%d", rec.reauth)
	}
	if got.TemplateID == nil || *got.TemplateID != tid || got.Status != service.CodeUsed || got.BatchID != nil {
		t.Fatalf("筛选 %+v", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("缺 Cache-Control: no-store")
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "gift-codes-report-") || !strings.HasSuffix(cd, `.csv"`) {
		t.Fatalf("文件名 %q", cd)
	}
	body := w.Body.Bytes()
	if !bytes.HasPrefix(body, utf8BOM) {
		t.Fatal("缺 BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(body[len(utf8BOM):])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || strings.Join(rows[0], ",") != "卡密（掩码）,状态,模板,批次,有效期,生成时间,兑换人,兑换时间" {
		t.Fatalf("CSV %v", rows)
	}
	want := []string{"VIPABCD••••••••", "used", "'+sum", bid.String(), "", "2030-01-02 03:04", "'@evil.example", "2030-01-02 03:04"}
	if strings.Join(rows[1], "|") != strings.Join(want, "|") {
		t.Fatalf("数据行 %v，期望 %v", rows[1], want)
	}
}
