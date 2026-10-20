package service

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// traceViewCall 记录一次 RecordSourceIPView 的入参。
type traceViewCall struct {
	view   string
	filter map[string]any
}

// fakeTraceAudit 只实现 RecordSourceIPView；其余 auditOps 方法落到嵌入的 nil 接口上，调到就 panic（说明走错了路）。
type fakeTraceAudit struct {
	auditOps
	calls []traceViewCall
	err   error
}

func (f *fakeTraceAudit) RecordSourceIPView(_ context.Context, view string, _ *uuid.UUID, filter map[string]any) error {
	cp := map[string]any{}
	for k, v := range filter {
		cp[k] = v
	}
	f.calls = append(f.calls, traceViewCall{view: view, filter: cp})
	return f.err
}

func newTraceRevealTestSvc(fake *fakeTraceAudit, now time.Time) *Service {
	master := base64.StdEncoding.EncodeToString(make([]byte, 32))
	keys, err := crypto.NewKeys(master)
	if err != nil {
		panic(err)
	}
	return &Service{
		keys: keys,
		ops:  fake,
		now:  func() time.Time { return now },
	}
}

func traceRevealAdminCtx(admin uuid.UUID) context.Context {
	return authn.With(context.Background(), authn.Principal{
		UserID:   admin,
		Audience: authn.AudienceAdmin,
	})
}

func tamperBase64URLToken(tok string) string {
	if len(tok) < 3 {
		return tok + "x"
	}
	mid := len(tok) / 2
	old := tok[mid]
	for _, c := range "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_" {
		if byte(c) != old {
			return tok[:mid] + string(c) + tok[mid+1:]
		}
	}
	return tok[:mid] + "A" + tok[mid+1:]
}

func filterHasPlainIP(filter map[string]any, plain string) bool {
	for _, v := range filter {
		if v == plain {
			return true
		}
	}
	return false
}

func TestTraceRevealWritesAuditFirst(t *testing.T) {
	fixed := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := &fakeTraceAudit{}
	s := newTraceRevealTestSvc(fake, fixed)
	admin := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	ctx := traceRevealAdminCtx(admin)
	ip := "198.51.100.7"

	sealer, err := s.keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	tok := traceToken(sealer, admin, ip, fixed)

	got, err := s.TraceReveal(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.IP != ip {
		t.Fatalf("IP=%q want %q", got.IP, ip)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("audit calls=%d want 1", len(fake.calls))
	}
	call := fake.calls[0]
	if call.view != auditsvc.ViewTrace {
		t.Fatalf("view=%q want %q", call.view, auditsvc.ViewTrace)
	}
	if call.filter["masked"] != "198.51.*.*" {
		t.Fatalf("masked=%v", call.filter["masked"])
	}
	hash, _ := call.filter["ip_hash"].(string)
	if len(hash) != 64 || hash != strings.ToLower(hash) {
		t.Fatalf("ip_hash=%q", hash)
	}
	wantHash := s.traceAuditIPHash(ip)
	if hash != wantHash {
		t.Fatalf("ip_hash=%q want %q", hash, wantHash)
	}
	if filterHasPlainIP(call.filter, ip) {
		t.Fatal("filter 含明文 IP")
	}
}

func TestTraceRevealRejectsBadTokens(t *testing.T) {
	fixed := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	admin := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	other := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	ip := "198.51.100.7"

	assertReject := func(t *testing.T, s *Service, ctx context.Context, tok string, fake *fakeTraceAudit) {
		got, err := s.TraceReveal(ctx, tok)
		if err == nil || errs.As(err).Code != errs.ValidationFailed {
			t.Fatalf("want validation_failed, got rev=%+v err=%v", got, err)
		}
		if got.IP != "" {
			t.Fatalf("IP=%q want empty", got.IP)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("audit calls=%d want 0", len(fake.calls))
		}
	}

	t.Run("tampered", func(t *testing.T) {
		fake := &fakeTraceAudit{}
		s := newTraceRevealTestSvc(fake, fixed)
		sealer, _ := s.keys.Sealer()
		tok := tamperBase64URLToken(traceToken(sealer, admin, ip, fixed))
		assertReject(t, s, traceRevealAdminCtx(admin), tok, fake)
	})

	t.Run("expired", func(t *testing.T) {
		fake := &fakeTraceAudit{}
		expiredNow := fixed.Add(TraceRevealTTL + time.Second)
		s := newTraceRevealTestSvc(fake, expiredNow)
		sealer, _ := s.keys.Sealer()
		tok := traceToken(sealer, admin, ip, fixed)
		assertReject(t, s, traceRevealAdminCtx(admin), tok, fake)
	})

	t.Run("other_admin", func(t *testing.T) {
		fake := &fakeTraceAudit{}
		s := newTraceRevealTestSvc(fake, fixed)
		sealer, _ := s.keys.Sealer()
		tok := traceToken(sealer, admin, ip, fixed)
		assertReject(t, s, traceRevealAdminCtx(other), tok, fake)
	})

	t.Run("not_a_token", func(t *testing.T) {
		fake := &fakeTraceAudit{}
		s := newTraceRevealTestSvc(fake, fixed)
		assertReject(t, s, traceRevealAdminCtx(admin), "not-a-token", fake)
	})
}

func TestTraceRevealAuditFailureWithholdsIP(t *testing.T) {
	fixed := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := &fakeTraceAudit{err: errs.New(errs.Unavailable)}
	s := newTraceRevealTestSvc(fake, fixed)
	admin := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	ip := "198.51.100.7"

	sealer, err := s.keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	tok := traceToken(sealer, admin, ip, fixed)

	got, err := s.TraceReveal(traceRevealAdminCtx(admin), tok)
	if err == nil {
		t.Fatal("expected error")
	}
	if got.IP != "" {
		t.Fatalf("IP=%q want empty", got.IP)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("audit calls=%d want 1", len(fake.calls))
	}
}

func TestTraceRevealNeedsPrincipal(t *testing.T) {
	fixed := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	fake := &fakeTraceAudit{}
	s := newTraceRevealTestSvc(fake, fixed)
	admin := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	ip := "198.51.100.7"

	sealer, err := s.keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	tok := traceToken(sealer, admin, ip, fixed)

	got, err := s.TraceReveal(context.Background(), tok)
	if err == nil || errs.As(err).Code != errs.Unauthorized {
		t.Fatalf("want unauthorized, got rev=%+v err=%v", got, err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("audit calls=%d want 0", len(fake.calls))
	}
}
