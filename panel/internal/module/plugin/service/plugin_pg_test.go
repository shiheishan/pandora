package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/aegispanel/aegis/internal/module/plugin/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

type pgEnv struct {
	svc  *Service
	repo *repo.Repo
	txm  *tx.Manager
	ctx  context.Context
	scan func(context.Context, any, string, ...any) error
	exec func(context.Context, string, ...any) error
	seal *crypto.Sealer
}

func randHookCode() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)
}

func newPG(t *testing.T, commit bool) *pgEnv {
	d := testkit.DB(t)
	r := repo.New(d)
	txm := tx.NewManager(d)
	audit := &fakeAudit{}
	sl := testSealer(t)
	svc := newService(r, txm, audit, sl, nil, Options{AllowPrivate: true})
	scan := func(ctx context.Context, dest any, q string, args ...any) error {
		return d.Conn(ctx).Raw(q, args...).Scan(dest).Error
	}
	exec := func(ctx context.Context, q string, args ...any) error {
		return d.Conn(ctx).Exec(q, args...).Error
	}
	ctx := testkit.TxCtx(t, d)
	if commit {
		testkit.Truncate(t, d, "plugin_hook_deliveries", "plugin_hooks")
		t.Cleanup(func() { testkit.Truncate(t, d, "plugin_hook_deliveries", "plugin_hooks") })
		ctx = context.Background()
	}
	return &pgEnv{svc: svc, repo: r, txm: txm, ctx: ctx, scan: scan, exec: exec, seal: sl}
}

func saveHook(t *testing.T, e *pgEnv, in SaveHookInput) string {
	t.Helper()
	gen, err := e.svc.SaveHook(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return gen
}

func hookDeliveryCount(t *testing.T, e *pgEnv, code string) int64 {
	t.Helper()
	var n int64
	if err := e.scan(e.ctx, &n, `SELECT count(*) FROM plugin_hook_deliveries d
		JOIN plugin_hooks h ON h.id = d.hook_id WHERE h.code = ?`, code); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPGSaveHookCreateAndUpdate(t *testing.T) {
	e := newPG(t, false)
	code := randHookCode()
	gen := saveHook(t, e, SaveHookInput{
		Code: code, Name: "原名", EndpointURL: "https://hooks.example.com/x",
		Enabled: true, Events: []string{"order.paid"},
	})
	if gen == "" || !strings.HasPrefix(gen, "whsec_") {
		t.Fatalf("secret %q", gen)
	}
	list, err := e.svc.List(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found Hook
	for _, h := range list {
		if h.Code == code {
			found = h
			break
		}
	}
	if found.Code == "" {
		t.Fatal("not in list")
	}
	if !found.HasSecret || len(found.Events) != 1 || found.Events[0] != "order.paid" ||
		found.TimeoutMS != 5000 || found.MaxAttempts != 5 {
		t.Fatalf("hook %+v", found)
	}

	var enc1 []byte
	if err := e.scan(e.ctx, &enc1, `SELECT secret_enc FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}

	gen2, err := e.svc.SaveHook(e.ctx, SaveHookInput{
		Code: code, Name: "新名", EndpointURL: "https://hooks.example.com/x",
		Enabled: true, Events: []string{"order.paid"},
	})
	if err != nil || gen2 != "" {
		t.Fatalf("update name gen=%q err=%v", gen2, err)
	}
	var name string
	if err := e.scan(e.ctx, &name, `SELECT name FROM plugin_hooks WHERE code = ?`, code); err != nil || name != "新名" {
		t.Fatalf("name %q err %v", name, err)
	}
	var enc2 []byte
	if err := e.scan(e.ctx, &enc2, `SELECT secret_enc FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(enc1, enc2) {
		t.Fatal("secret_enc changed on empty secret update")
	}
	plain, err := e.seal.Open(enc2, secretAAD(code))
	if err != nil || string(plain) != gen {
		t.Fatalf("open %q err %v", plain, err)
	}

	_, err = e.svc.SaveHook(e.ctx, SaveHookInput{
		Code: code, Name: "新名", EndpointURL: "https://hooks.example.com/x",
		Enabled: true, Events: []string{"order.paid"}, Secret: "whsec_new",
	})
	if err != nil {
		t.Fatal(err)
	}
	var enc3 []byte
	if err := e.scan(e.ctx, &enc3, `SELECT secret_enc FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(enc2, enc3) {
		t.Fatal("secret_enc should change")
	}
	plain3, err := e.seal.Open(enc3, secretAAD(code))
	if err != nil || string(plain3) != "whsec_new" {
		t.Fatalf("new secret %q", plain3)
	}
}

func TestPGSaveHookRejectsBadInput(t *testing.T) {
	e := newPG(t, false)
	cases := []SaveHookInput{
		{Code: randHookCode(), Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"unknown.evt"}},
		{Code: randHookCode(), Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{}},
		{Code: randHookCode(), Name: "n", EndpointURL: "https://example.com/h", Enabled: false, Events: []string{"order.paid"}, TimeoutMS: 100},
	}
	codes := make([]string, 0, len(cases))
	for _, in := range cases {
		codes = append(codes, in.Code)
		_, err := e.svc.SaveHook(e.ctx, in)
		if err == nil || errs.As(err).Code != errs.ValidationFailed {
			t.Fatalf("in %+v err %v", in, err)
		}
	}
	var n int64
	if err := e.scan(e.ctx, &n, `SELECT count(*) FROM plugin_hooks WHERE code IN ?`, codes); err != nil || n != 0 {
		t.Fatalf("rows %d err %v", n, err)
	}
}

func TestPGEmitFanOutAndDedupe(t *testing.T) {
	e := newPG(t, false)
	codeA := randHookCode()
	codeB := randHookCode()
	codeC := randHookCode()
	saveHook(t, e, SaveHookInput{Code: codeA, Name: "A", EndpointURL: "https://example.com/a", Enabled: true, Events: []string{"order.paid"}})
	saveHook(t, e, SaveHookInput{Code: codeB, Name: "B", EndpointURL: "https://example.com/b", Enabled: true, Events: []string{"user.registered"}})
	saveHook(t, e, SaveHookInput{Code: codeC, Name: "C", EndpointURL: "https://example.com/c", Enabled: false, Events: []string{"order.paid"}})

	for i := 0; i < 2; i++ {
		if err := e.svc.Emit(e.ctx, "order.paid", "order.paid:1", map[string]any{"order_id": "1"}); err != nil {
			t.Fatal(err)
		}
	}
	if hookDeliveryCount(t, e, codeA) != 1 {
		t.Fatalf("A count %d", hookDeliveryCount(t, e, codeA))
	}
	var row struct {
		Status      string
		Attempts    int
		MaxAttempts int
		OrderID     string `gorm:"column:order_id"`
	}
	if err := e.scan(e.ctx, &row, `SELECT d.status, d.attempts, d.max_attempts, d.payload->>'order_id' AS order_id
		FROM plugin_hook_deliveries d JOIN plugin_hooks h ON h.id = d.hook_id WHERE h.code = ?`, codeA); err != nil {
		t.Fatal(err)
	}
	if row.Status != model.StatusQueued || row.Attempts != 0 || row.MaxAttempts != 5 || row.OrderID != "1" {
		t.Fatalf("row %+v", row)
	}
}

func TestPGEmitRollsBackWithBusiness(t *testing.T) {
	e := newPG(t, true)
	code := randHookCode()
	saveHook(t, e, SaveHookInput{Code: code, Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"order.paid"}})
	err := e.txm.Do(context.Background(), func(ctx context.Context) error {
		if err := e.svc.Emit(ctx, "order.paid", "k-rb", map[string]any{"a": 1}); err != nil {
			return err
		}
		return errors.New("业务失败")
	})
	if err == nil {
		t.Fatal("expected business error")
	}
	if hookDeliveryCount(t, e, code) != 0 {
		t.Fatalf("after rollback %d", hookDeliveryCount(t, e, code))
	}
	err = e.txm.Do(context.Background(), func(ctx context.Context) error {
		return e.svc.Emit(ctx, "order.paid", "k-rb-ok", map[string]any{"a": 1})
	})
	if err != nil {
		t.Fatal(err)
	}
	if hookDeliveryCount(t, e, code) != 1 {
		t.Fatalf("after commit %d", hookDeliveryCount(t, e, code))
	}
}

func TestPGDispatchSendsAndMarks(t *testing.T) {
	e := newPG(t, true)
	var mu sync.Mutex
	var gotTS, gotSig string
	var gotBody []byte
	var hit int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hit++
		gotTS = r.Header.Get("X-Pandora-Timestamp")
		gotSig = r.Header.Get("X-Pandora-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	code := randHookCode()
	secret := saveHook(t, e, SaveHookInput{
		Code: code, Name: "n", EndpointURL: srv.URL,
		Enabled: true, Events: []string{"order.paid"},
	})
	if err := e.svc.Emit(e.ctx, "order.paid", "disp-1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.Dispatch(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	mu.Lock()
	if hit != 1 {
		t.Fatalf("hits %d", hit)
	}
	wantSig := Sign(secret, gotTS, gotBody)
	if gotSig != wantSig {
		t.Fatalf("sig %q want %q", gotSig, wantSig)
	}
	mu.Unlock()

	var st struct {
		Status       string
		Attempts     int
		ResponseCode *int
		SentAt       *time.Time
		DurationMS   *int       `gorm:"column:last_duration_ms"`
		NextRetry    *time.Time `gorm:"column:next_retry_at"`
	}
	if err := e.scan(context.Background(), &st, `SELECT status, attempts, response_code, sent_at, last_duration_ms, next_retry_at
		FROM plugin_hook_deliveries d JOIN plugin_hooks h ON h.id = d.hook_id WHERE h.code = ?`, code); err != nil {
		t.Fatal(err)
	}
	if st.Status != model.StatusSent || st.Attempts != 1 || st.ResponseCode == nil || *st.ResponseCode != 200 ||
		st.SentAt == nil || st.DurationMS == nil || st.NextRetry != nil {
		t.Fatalf("st %+v", st)
	}
	n2, err := e.svc.Dispatch(context.Background())
	if err != nil || n2 != 0 {
		t.Fatalf("second dispatch %d %v", n2, err)
	}
}

func TestPGDispatch5xxThenRetryLease(t *testing.T) {
	e := newPG(t, true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	code := randHookCode()
	saveHook(t, e, SaveHookInput{
		Code: code, Name: "n", EndpointURL: srv.URL,
		Enabled: true, Events: []string{"order.paid"},
	})
	if err := e.svc.Emit(e.ctx, "order.paid", "503-1", map[string]any{"x": 1}); err != nil {
		t.Fatal(err)
	}
	n, err := e.svc.Dispatch(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("dispatch %d %v", n, err)
	}
	var st struct {
		Status       string
		Attempts     int
		ResponseCode *int
		NextRetry    time.Time `gorm:"column:next_retry_at"`
	}
	if err := e.scan(context.Background(), &st, `SELECT status, attempts, response_code, next_retry_at
		FROM plugin_hook_deliveries d JOIN plugin_hooks h ON h.id = d.hook_id WHERE h.code = ?`, code); err != nil {
		t.Fatal(err)
	}
	if st.Status != model.StatusQueued || st.Attempts != 1 || st.ResponseCode == nil || *st.ResponseCode != 503 {
		t.Fatalf("st %+v", st)
	}
	now := time.Now()
	if !st.NextRetry.After(now.Add(50 * time.Second)) {
		t.Fatalf("next_retry %v now %v", st.NextRetry, now)
	}
	n2, err := e.svc.Dispatch(context.Background())
	if err != nil || n2 != 0 {
		t.Fatalf("second dispatch %d %v", n2, err)
	}
}

func TestPGClaimSkipLocked(t *testing.T) {
	e := newPG(t, true)
	code := randHookCode()
	saveHook(t, e, SaveHookInput{Code: code, Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"order.paid"}})
	var hookID uuid.UUID
	if err := e.scan(context.Background(), &hookID, `SELECT id FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		if err := e.exec(context.Background(), `INSERT INTO plugin_hook_deliveries
			(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, next_retry_at)
			VALUES (?, 'order.paid', ?, '{}'::jsonb, 'queued', 0, 5, ?)`, hookID, "dk"+randHookCode(), past); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	var a, b []model.Delivery
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		a, errA = e.repo.ClaimDue(context.Background(), 10, time.Minute)
	}()
	go func() {
		defer wg.Done()
		b, errB = e.repo.ClaimDue(context.Background(), 10, time.Minute)
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("claim %v %v", errA, errB)
	}
	seen := make(map[uuid.UUID]bool)
	for _, d := range append(a, b...) {
		if seen[d.ID] {
			t.Fatalf("duplicate id %s", d.ID)
		}
		seen[d.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("claimed %d", len(seen))
	}
}

func TestPGDeleteCascades(t *testing.T) {
	e := newPG(t, false)
	code := randHookCode()
	saveHook(t, e, SaveHookInput{Code: code, Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"order.paid"}})
	if err := e.svc.Emit(e.ctx, "order.paid", "del-1", map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if hookDeliveryCount(t, e, code) != 1 {
		t.Fatal("expected delivery")
	}
	if err := e.svc.DeleteHook(e.ctx, code); err != nil {
		t.Fatal(err)
	}
	var hooks int64
	if err := e.scan(e.ctx, &hooks, `SELECT count(*) FROM plugin_hooks WHERE code = ?`, code); err != nil || hooks != 0 {
		t.Fatalf("hooks %d", hooks)
	}
	var dels int64
	if err := e.scan(e.ctx, &dels, `SELECT count(*) FROM plugin_hook_deliveries`); err != nil || dels != 0 {
		t.Fatalf("deliveries %d", dels)
	}
	err := e.svc.DeleteHook(e.ctx, code)
	if err == nil || errs.As(err).Code != errs.NotFound {
		t.Fatalf("second delete %v", err)
	}
}

func TestPGListStatsAndDeliveries(t *testing.T) {
	e := newPG(t, false)
	code := randHookCode()
	saveHook(t, e, SaveHookInput{Code: code, Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"order.paid"}})
	var hookID uuid.UUID
	if err := e.scan(e.ctx, &hookID, `SELECT id FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := e.exec(e.ctx, `INSERT INTO plugin_hook_deliveries
		(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, next_retry_at)
		VALUES (?, 'order.paid', ?, '{}'::jsonb, 'queued', 0, 5, ?)`, hookID, "q"+randHookCode(), future); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(e.ctx, `INSERT INTO plugin_hook_deliveries
		(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, sent_at, next_retry_at)
		VALUES (?, 'order.paid', ?, '{}'::jsonb, 'sent', 1, 5, now(), NULL)`, hookID, "s"+randHookCode()); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(e.ctx, `INSERT INTO plugin_hook_deliveries
		(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, next_retry_at, sent_at)
		VALUES (?, 'order.paid', ?, '{}'::jsonb, 'failed', 5, 5, NULL, NULL)`, hookID, "f"+randHookCode()); err != nil {
		t.Fatal(err)
	}

	list, err := e.svc.List(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var h Hook
	for _, row := range list {
		if row.Code == code {
			h = row
			break
		}
	}
	if h.QueuedCount != 1 || h.FailedCount != 1 || h.SentCount7d != 1 || h.LastSentAt == "" {
		t.Fatalf("stats %+v", h)
	}
	dels, err := e.svc.Deliveries(e.ctx, code, 0)
	if err != nil || len(dels) != 3 {
		t.Fatalf("deliveries %d %v", len(dels), err)
	}
	var sentN int
	for _, d := range dels {
		if d.Status == model.StatusSent {
			if d.SentAt == "" {
				t.Fatalf("sent row %+v", d)
			}
			sentN++
		} else if d.SentAt != "" {
			t.Fatalf("non-sent with sent_at %+v", d)
		}
	}
	if sentN != 1 {
		t.Fatalf("sent count %d", sentN)
	}
	empty, err := e.svc.Deliveries(e.ctx, "no_such_"+randHookCode(), 0)
	if err != nil || len(empty) != 0 {
		t.Fatalf("missing code %v len %d", err, len(empty))
	}
}

func TestPGPurgeDeliveries(t *testing.T) {
	e := newPG(t, false)
	code := randHookCode()
	saveHook(t, e, SaveHookInput{Code: code, Name: "n", EndpointURL: "https://example.com/h", Enabled: true, Events: []string{"order.paid"}})
	var hookID uuid.UUID
	if err := e.scan(e.ctx, &hookID, `SELECT id FROM plugin_hooks WHERE code = ?`, code); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * 24 * time.Hour)
	if err := e.exec(e.ctx, `INSERT INTO plugin_hook_deliveries
		(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, created_at, next_retry_at)
		VALUES (?, 'order.paid', ?, '{}'::jsonb, 'failed', 1, 5, ?, NULL)`, hookID, "old"+randHookCode(), old); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(e.ctx, `INSERT INTO plugin_hook_deliveries
		(hook_id, event, dedupe_key, payload, status, attempts, max_attempts, next_retry_at)
		VALUES (?, 'order.paid', ?, '{}'::jsonb, 'queued', 0, 5, now())`, hookID, "new"+randHookCode()); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-90 * 24 * time.Hour)
	n, err := e.svc.PurgeDeliveries(e.ctx, before, 10)
	if err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
	var cnt int64
	if err := e.scan(e.ctx, &cnt, `SELECT count(*) FROM plugin_hook_deliveries WHERE hook_id = ?`, hookID); err != nil || cnt != 1 {
		t.Fatalf("remaining %d", cnt)
	}
}
