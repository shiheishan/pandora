package service

import (
	"context"
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
)

func firstDelivery(st *fakeStore, hookID uuid.UUID) *model.Delivery {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, id := range st.order {
		d := st.deliveries[id]
		if d != nil && d.HookID == hookID {
			return d
		}
	}
	return nil
}

func TestDispatchSentSignsRequest(t *testing.T) {
	var mu sync.Mutex // 处理函数在服务器的 goroutine 里写，-race 下要加锁
	var gotEvent, gotDelivery, gotTS, gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotEvent = r.Header.Get("X-Pandora-Event")
		gotDelivery = r.Header.Get("X-Pandora-Delivery")
		gotTS = r.Header.Get("X-Pandora-Timestamp")
		gotSig = r.Header.Get("X-Pandora-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "pay", srv.URL, "whsec_test", "order.paid")

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k1", map[string]any{"order_id": "o1"}); err != nil {
		t.Fatal(err)
	}
	d := firstDelivery(st, h.ID)
	if d == nil {
		t.Fatal("no delivery")
	}

	n, err := svc.Dispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dispatch count %d", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotEvent != "order.paid" {
		t.Fatalf("event %q", gotEvent)
	}
	if gotDelivery != d.ID.String() {
		t.Fatalf("delivery %q", gotDelivery)
	}
	wantSig := Sign("whsec_test", gotTS, gotBody)
	if gotSig != wantSig {
		t.Fatalf("signature %q want %q", gotSig, wantSig)
	}

	d = firstDelivery(st, h.ID)
	if d.Status != model.StatusSent {
		t.Fatalf("status %q", d.Status)
	}
	if d.ResponseCode == nil || *d.ResponseCode != 200 {
		t.Fatalf("response code %v", d.ResponseCode)
	}
	if d.LastDurationMS == nil {
		t.Fatal("duration nil")
	}
	if d.Attempts != 1 {
		t.Fatalf("attempts %d", d.Attempts)
	}
}

func TestDispatch4xxIsTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "h404", srv.URL, "whsec_test", "order.paid")

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Dispatch(ctx)
	if err != nil || n != 1 {
		t.Fatalf("dispatch %d err %v", n, err)
	}
	d := firstDelivery(st, h.ID)
	if d.Status != model.StatusFailed {
		t.Fatalf("status %q", d.Status)
	}
	if d.Attempts != 1 || d.MaxAttempts != 5 {
		t.Fatalf("attempts %d max %d", d.Attempts, d.MaxAttempts)
	}
	if d.ResponseCode == nil || *d.ResponseCode != 404 {
		t.Fatalf("code %v", d.ResponseCode)
	}
	if !strings.Contains(d.ErrorMessage, "HTTP 404") {
		t.Fatalf("msg %q", d.ErrorMessage)
	}
}

func TestDispatch5xxRetriesWithBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "h500", srv.URL, "whsec_test", "order.paid")

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Dispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := firstDelivery(st, h.ID)
	if d.Status != model.StatusQueued {
		t.Fatalf("status %q", d.Status)
	}
	if st.lastDelay != time.Minute {
		t.Fatalf("delay %v", st.lastDelay)
	}
	if d.NextRetryAt == nil || !d.NextRetryAt.After(st.now()) {
		t.Fatal("next retry not in future")
	}

	d.Attempts = 4
	past := st.now().Add(-time.Minute)
	d.NextRetryAt = &past
	_, err = svc.Dispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d = firstDelivery(st, h.ID)
	if d.Status != model.StatusFailed {
		t.Fatalf("status %q attempts %d", d.Status, d.Attempts)
	}
}

func TestDispatchBlocksPrivateAtDial(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, false)

	// http 地址在静态检查阶段就被拒
	h1 := addHook(t, svc, st, "http_hook", srv.URL, "whsec_test", "order.paid")
	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k-http", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Dispatch(ctx)
	d1 := firstDelivery(st, h1.ID)
	if d1.Status != model.StatusQueued {
		t.Fatalf("http hook status %q", d1.Status)
	}
	if !strings.Contains(d1.ErrorMessage, "地址不可用") {
		t.Fatalf("msg %q", d1.ErrorMessage)
	}

	// https 127.0.0.1 在拨号阶段被拒，查询串不应出现在错误里
	u := srv.URL
	port := strings.TrimPrefix(u, "http://127.0.0.1:")
	httpsURL := "https://127.0.0.1:" + port + "/p?token=abc"
	h2 := addHook(t, svc, st, "https_priv", httpsURL, "whsec_test", "order.paid")
	if err := svc.Emit(ctx, "order.paid", "k-https", map[string]any{"b": 2}); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	_, _ = svc.Dispatch(ctx)
	if hits.Load() != before {
		t.Fatalf("server hit %d want %d", hits.Load(), before)
	}
	d2 := firstDelivery(st, h2.ID)
	if d2.Status != model.StatusQueued {
		t.Fatalf("status %q", d2.Status)
	}
	if strings.Contains(d2.ErrorMessage, "token=abc") {
		t.Fatalf("msg leaked query: %q", d2.ErrorMessage)
	}
}

func TestDispatchUnsealableSecretFails(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "badsec", srv.URL, "whsec_test", "order.paid")
	h.SecretEnc = []byte{1, 2, 3}
	_ = st.UpdateHook(context.Background(), h, true)

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	_, _ = svc.Dispatch(ctx)
	if hits.Load() != 0 {
		t.Fatalf("hits %d", hits.Load())
	}
	d := firstDelivery(st, h.ID)
	if d.Status != model.StatusFailed {
		t.Fatalf("status %q", d.Status)
	}
	if d.ErrorMessage != "签名密钥无法解开" {
		t.Fatalf("msg %q", d.ErrorMessage)
	}
}

func TestDispatchSkipsDisabledHook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	h := addHook(t, svc, st, "off", srv.URL, "whsec_test", "order.paid")

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "k1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	h.Enabled = false
	_ = st.UpdateHook(context.Background(), h, false)
	n, err := svc.Dispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("n %d", n)
	}
	d := firstDelivery(st, h.ID)
	if d.Status != model.StatusQueued {
		t.Fatalf("status %q", d.Status)
	}
}

func TestDispatchCallsFailExhaustedFirst(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	_, _ = svc.Dispatch(context.Background())
	if len(st.calls) == 0 || st.calls[0] != "FailExhausted" {
		t.Fatalf("calls %v", st.calls)
	}
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{0, time.Minute},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute},
		{7, 30 * time.Minute},
		{100, 30 * time.Minute},
	}
	for _, c := range cases {
		if got := backoff(c.n); got != c.want {
			t.Fatalf("backoff(%d)=%v want %v", c.n, got, c.want)
		}
	}
}

func TestEmitUnknownEventRejected(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	addHook(t, svc, st, "h", "https://example.com/hook", "whsec_test", "order.paid")
	ctx := context.Background()

	if err := svc.Emit(ctx, "no.such", "k", map[string]any{}); err == nil {
		t.Fatal("expected error")
	}
	if len(st.deliveries) != 0 {
		t.Fatalf("deliveries %d", len(st.deliveries))
	}
	if err := svc.Emit(ctx, "order.paid", "k", []int{1}); err == nil {
		t.Fatal("expected error for non-object payload")
	}
}

func TestEmitDedupe(t *testing.T) {
	st := newFakeStore()
	svc, _ := newTestService(t, st, true)
	addHook(t, svc, st, "a", "https://example.com/a", "whsec_test", "order.paid")
	addHook(t, svc, st, "b", "https://example.com/b", "whsec_test", "user.registered")

	ctx := context.Background()
	if err := svc.Emit(ctx, "order.paid", "same", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Emit(ctx, "order.paid", "same", map[string]any{"x": 2}); err != nil {
		t.Fatal(err)
	}
	if len(st.deliveries) != 1 {
		t.Fatalf("deliveries %d", len(st.deliveries))
	}
	for _, d := range st.deliveries {
		if d.HookID == uuid.Nil {
			continue
		}
		h := st.hooks[d.HookID]
		if h != nil && h.Code == "b" {
			t.Fatal("hook b should have no delivery")
		}
	}
}

func TestDispatchSkipsWhenLockHeld(t *testing.T) {
	st := newFakeStore()
	st.lockBusy = true
	svc, _ := newTestService(t, st, true)
	addHook(t, svc, st, "busy", "https://example.com/h", "whsec_test", "order.paid")
	if err := svc.Emit(context.Background(), "order.paid", "k1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	n, err := svc.Dispatch(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	if len(st.calls) != 0 {
		t.Fatalf("抢不到锁不应认领: %v", st.calls)
	}
}
