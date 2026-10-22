package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

func traceRevealAuditCount(e *env, ctx context.Context, actorID uuid.UUID) int64 {
	var n int64
	q := `SELECT count(*) FROM audit_events WHERE action = 'security.source_ip_viewed' AND actor_id = ? AND data->>'view' = 'trace'`
	if err := e.row(ctx, q, actorID).Scan(&n); err != nil {
		return 0
	}
	return n
}

func TestPGTraceReveal(t *testing.T) {
	e, ctx, _ := opsEnv(t)
	_, _, _, _, admin, actx := riskUsers(t, e, ctx, "treveal", "198.51.100.1")
	ip := "198.51.100.23"

	sealer, err := e.svc.keys.Sealer()
	if err != nil {
		t.Fatal(err)
	}
	now := e.svc.now()
	tok := traceToken(sealer, admin, ip, now)

	n0 := traceRevealAuditCount(e, ctx, admin)

	rev, err := e.svc.TraceReveal(actx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if rev.IP != ip {
		t.Fatalf("IP=%q want %q", rev.IP, ip)
	}
	if traceRevealAuditCount(e, ctx, admin) != n0+1 {
		t.Fatalf("audit count after success")
	}

	var masked, ipHash string
	rowQ := `SELECT data->'filter'->>'masked', data->'filter'->>'ip_hash' FROM audit_events
		WHERE action = 'security.source_ip_viewed' AND actor_id = ? ORDER BY occurred_at DESC, id DESC LIMIT 1`
	if err := e.row(ctx, rowQ, admin).Scan(&masked, &ipHash); err != nil {
		t.Fatal(err)
	}
	if masked != "198.51.*.*" {
		t.Fatalf("masked=%q", masked)
	}
	wantHash := e.svc.traceAuditIPHash(ip)
	if ipHash != wantHash {
		t.Fatalf("ip_hash=%q want %q", ipHash, wantHash)
	}
	if len(ipHash) != 64 || ipHash != strings.ToLower(ipHash) {
		t.Fatalf("ip_hash format %q", ipHash)
	}

	afterSuccess := n0 + 1

	assertRevealRejected := func(t *testing.T, token string, callCtx context.Context) {
		rev, err := e.svc.TraceReveal(callCtx, token)
		if err == nil || errs.As(err).Code != errs.ValidationFailed {
			t.Fatalf("want validation_failed rev=%+v err=%v", rev, err)
		}
		if rev.IP != "" {
			t.Fatalf("IP=%q want empty", rev.IP)
		}
		if traceRevealAuditCount(e, ctx, admin) != afterSuccess {
			t.Fatalf("audit count changed after reject")
		}
	}

	t.Run("tampered", func(t *testing.T) {
		assertRevealRejected(t, tamperBase64URLToken(tok), actx)
	})

	t.Run("other_admin", func(t *testing.T) {
		assertRevealRejected(t, tok, asAdmin(ctx, uuid.New()))
	})

	t.Run("expired", func(t *testing.T) {
		expiredAt := now.Add(-TraceRevealTTL - time.Minute)
		expTok := traceToken(sealer, admin, ip, expiredAt)
		assertRevealRejected(t, expTok, actx)
	})
}
