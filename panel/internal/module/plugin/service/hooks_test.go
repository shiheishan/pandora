package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestSaveHookValidationMessages(t *testing.T) {
	longName := strings.Repeat("字", 81)
	longDesc := strings.Repeat("a", 501)
	longSecret := strings.Repeat("s", 257)

	cases := []struct {
		name  string
		in    SaveHookInput
		field string
		want  string
		allow bool
	}{
		{"空 code", SaveHookInput{Code: "", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "code", "插件标识必填，小写字母开头", true},
		{"数字开头的 code", SaveHookInput{Code: "1bad", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "code", "插件标识只能用小写字母、数字、- 和 _，以小写字母开头，2–39 个字符", true},
		{"空 name", SaveHookInput{Code: "ab", Name: "", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "name", "插件名称必填", true},
		{"81 字 name", SaveHookInput{Code: "ab", Name: longName, EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "name", "插件名称最多 80 个字", true},
		{"501 字说明", SaveHookInput{Code: "ab", Name: "n", Description: longDesc, EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "description", "说明最多 500 个字", true},
		{"http 地址", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "http://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "endpoint_url", "必须使用 https", false},
		{"未知事件", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"no.such.event"}}, "events", "未知事件：no.such.event", true},
		{"启用无事件", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{}}, "events", "启用前至少要订阅一个事件，否则这个钩子永远不会触发", true},
		{"timeout 100", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}, TimeoutMS: 100}, "timeout_ms", "超时必须在 500–30000 毫秒之间", true},
		{"max_attempts 11", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}, MaxAttempts: 11}, "max_attempts", "最多尝试次数必须在 1–10 之间", true},
		{"257 字节密钥", SaveHookInput{Code: "ab", Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}, Secret: longSecret}, "secret", "签名密钥最多 256 个字符", true},
		{"name 含空字符", SaveHookInput{Code: "ab", Name: "a\x00b", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}}, "name", "不能包含空字符", true},
	}

	for _, c := range cases {
		st := newFakeStore()
		svc, _ := newTestService(t, st, c.allow)
		_, err := svc.SaveHook(context.Background(), c.in)
		if err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
		e := errs.As(err)
		if e.Code != errs.ValidationFailed {
			t.Fatalf("%s: code %v", c.name, e.Code)
		}
		if e.Fields[c.field] != c.want {
			t.Fatalf("%s: field %q got %q want %q", c.name, c.field, e.Fields[c.field], c.want)
		}
	}
}

func TestSaveHookDefaultsAndDedupeEvents(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	code := "dedupe"
	_, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code:        code,
		Name:        "n",
		EndpointURL: "https://example.com/hook",
		Enabled:     true,
		Events:      []string{"order.paid", "order.paid", "user.registered"},
		TimeoutMS:   0,
		MaxAttempts: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	h, ok, err := st.HookByCode(context.Background(), code, false)
	if err != nil || !ok {
		t.Fatalf("hook %v %v", ok, err)
	}
	if h.TimeoutMS != 5000 || h.MaxAttempts != 5 {
		t.Fatalf("defaults timeout=%d max=%d", h.TimeoutMS, h.MaxAttempts)
	}
	var evs []string
	if err := json.Unmarshal([]byte(h.Events), &evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0] != "order.paid" || evs[1] != "user.registered" {
		t.Fatalf("events %v", evs)
	}
}

func TestSaveHookAuditHasNoSecret(t *testing.T) {
	st := newFakeStore()
	svc, audit := newTestService(t, st, true)
	url := "https://example.com/secret/path"
	_, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code:        "auditsec",
		Name:        "n",
		EndpointURL: url,
		Enabled:     true,
		Events:      []string{"order.paid"},
		Secret:      "whsec_abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit %d", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != ActionHookSave {
		t.Fatalf("action %q", ev.Action)
	}
	raw, _ := json.Marshal(ev.Data)
	s := string(raw)
	if strings.Contains(s, "whsec_abc") {
		t.Fatalf("audit leaked secret: %s", s)
	}
	if strings.Contains(s, "/secret/path") {
		t.Fatalf("audit leaked path: %s", s)
	}
	if ev.Data["endpoint_host"] != "https://example.com" {
		t.Fatalf("host %v", ev.Data["endpoint_host"])
	}
}

func TestDeleteHookAudit(t *testing.T) {
	st := newFakeStore()
	svc, audit := newTestService(t, st, true)
	_, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code: "delme", Name: "n", EndpointURL: "https://example.com/h",
		Enabled: true, Events: []string{"order.paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audit.events = nil
	if err := svc.DeleteHook(context.Background(), "delme"); err != nil {
		t.Fatal(err)
	}
	if len(audit.events) != 1 || audit.events[0].Action != ActionHookDelete {
		t.Fatalf("audit %+v", audit.events)
	}
}

func TestDeleteHookNotFound(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	err := svc.DeleteHook(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("code %v", errs.As(err).Code)
	}
}

func TestTestHookAuditBeforeSend(t *testing.T) {
	var mu sync.Mutex // 处理函数在服务器的 goroutine 里写，-race 下要加锁
	var gotEvent string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotEvent = r.Header.Get("X-Pandora-Event")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, audit := newTestService(t, st, true)
	code := "testhook"
	_, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code: code, Name: "n", EndpointURL: srv.URL,
		Enabled: true, Events: []string{"order.paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audit.events = nil

	res, err := svc.TestHook(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	if res.ResponseCode != 200 {
		t.Fatalf("code %d", res.ResponseCode)
	}
	if len(audit.events) != 1 {
		t.Fatalf("audit %d", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Action != ActionTestSent || ev.Data["kind"] != testKind || ev.Data["target"] != code {
		t.Fatalf("audit %+v", ev)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotEvent != "panel.test" {
		t.Fatalf("header event %q", gotEvent)
	}
	var body map[string]string
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["event"] != "panel.test" {
		t.Fatalf("body event %q", body["event"])
	}

	// HTTP 500 → 422
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv500.Close)
	_, err = svc.SaveHook(context.Background(), SaveHookInput{
		Code: "test500", Name: "n", EndpointURL: srv500.URL,
		Enabled: true, Events: []string{"order.paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.TestHook(context.Background(), "test500")
	if err == nil {
		t.Fatal("expected error")
	}
	if errs.As(err).Code != errs.ValidationFailed {
		t.Fatalf("code %v", errs.As(err).Code)
	}
	if !strings.Contains(errs.As(err).Message, "HTTP 500") {
		t.Fatalf("msg %q", errs.As(err).Message)
	}
}

func TestTestHookAuditFailureBlocksSend(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, audit := newTestService(t, st, true)
	_, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code: "noblock", Name: "n", EndpointURL: srv.URL,
		Enabled: true, Events: []string{"order.paid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	audit.err = errs.New(errs.Internal) // 保存之后再让审计失败
	_, err = svc.TestHook(context.Background(), "noblock")
	if err == nil {
		t.Fatal("expected error")
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits %d", hits.Load())
	}
}

// 投递记录与最近送达时刻固定按北京时间显示（产品定，不随站点时区）。
func TestDisplayTimesAreBeijing(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "bjtime", "https://example.com/h", "whsec_test", "order.paid")
	created := time.Date(2026, 1, 1, 16, 0, 5, 0, time.UTC)
	sent := created.Add(time.Minute)
	code := 200
	id := uuid.New()
	st.deliveries[id] = &model.Delivery{
		ID: id, HookID: h.ID, Event: "order.paid", DedupeKey: "k", Payload: "{}",
		Status: model.StatusSent, Attempts: 1, MaxAttempts: 5, ResponseCode: &code,
		SentAt: &sent, CreatedAt: created, UpdatedAt: sent,
	}
	st.order = append(st.order, id)

	dels, err := svc.Deliveries(context.Background(), "bjtime", 0)
	if err != nil || len(dels) != 1 {
		t.Fatalf("deliveries %d %v", len(dels), err)
	}
	if dels[0].CreatedAt != "2026-01-02 00:00:05" || dels[0].SentAt != "2026-01-02 00:01:05" {
		t.Fatalf("created %q sent %q", dels[0].CreatedAt, dels[0].SentAt)
	}
	hooks, err := svc.List(context.Background())
	if err != nil || len(hooks) != 1 {
		t.Fatalf("list %d %v", len(hooks), err)
	}
	if hooks[0].LastSentAt != "2026-01-02 00:01" {
		t.Fatalf("last sent %q", hooks[0].LastSentAt)
	}
}

// scheme 与主机名规整成小写再落库（库里 CHECK 按小写 https:// 判断）；路径与查询串原样保留。
func TestSaveHookLowercasesSchemeAndHost(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	if _, err := svc.SaveHook(context.Background(), SaveHookInput{
		Code: "upper", Name: "n", EndpointURL: "HTTPS://Hooks.Example.COM/Path?Q=A",
		Enabled: true, Events: []string{"order.paid"},
	}); err != nil {
		t.Fatal(err)
	}
	h, ok, err := st.HookByCode(context.Background(), "upper", false)
	if err != nil || !ok {
		t.Fatalf("hook %v %v", ok, err)
	}
	if h.EndpointURL != "https://hooks.example.com/Path?Q=A" {
		t.Fatalf("endpoint %q", h.EndpointURL)
	}
}
