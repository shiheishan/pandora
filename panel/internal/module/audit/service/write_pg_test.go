package service

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

func TestWriteSubjectIsActorAdmin(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	u := uuid.New()
	rid := uuid.New()
	a := uniqueAction(t)

	ctx = reqCtx(ctx, u, authn.AudienceAdmin)
	if err := svc.Write(ctx, Event{
		Action:       a,
		ResourceType: "user",
		ResourceID:   &rid,
		Data:         map[string]any{"reason": "r1"},
	}); err != nil {
		t.Fatal(err)
	}

	raw := scanRaw(t, d.Conn(ctx).Raw(rawColsSQL, a).Row())
	if raw.ActorKind != "admin" {
		t.Fatalf("actor_kind %q", raw.ActorKind)
	}
	if raw.ActorID == nil || *raw.ActorID != u {
		t.Fatalf("actor_id %v want %v", raw.ActorID, u)
	}
	if raw.ActorAudience == nil || *raw.ActorAudience != "admin" {
		t.Fatalf("actor_audience %v", raw.ActorAudience)
	}
	if raw.AuthContext == nil || *raw.AuthContext != "session" {
		t.Fatalf("auth_context %v", raw.AuthContext)
	}
	ridStr := reqid.From(ctx)
	if raw.RequestID == nil || *raw.RequestID != ridStr {
		t.Fatalf("request_id %v want %s", raw.RequestID, ridStr)
	}
	if len(raw.IPHash) != 32 {
		t.Fatalf("ip_hash len %d", len(raw.IPHash))
	}
	if len(raw.IPEnc) == 0 {
		t.Fatal("ip_enc empty")
	}
	if raw.UserAgent == nil || *raw.UserAgent != "test-agent/1.0" {
		t.Fatalf("user_agent %v", raw.UserAgent)
	}
	if raw.Outcome != OutcomeSuccess {
		t.Fatalf("outcome %q", raw.Outcome)
	}
	if raw.Reason == nil || *raw.Reason != "r1" {
		t.Fatalf("reason %v", raw.Reason)
	}

	rows, total, err := svc.List(ctx, Filter{ActionPrefix: a}, Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("list total=%d len=%d", total, len(rows))
	}
	if rows[0].SourceIP == nil || *rows[0].SourceIP != "203.0.113.7" {
		t.Fatalf("source_ip %v", rows[0].SourceIP)
	}
	if rows[0].Reason == nil || *rows[0].Reason != "r1" {
		t.Fatalf("reason %v", rows[0].Reason)
	}
	if rows[0].APIDomain == nil || *rows[0].APIDomain != "admin" {
		t.Fatalf("api_domain %v", rows[0].APIDomain)
	}
}

func TestWriteSubjectIsActorPublic(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	u := uuid.New()
	a := uniqueAction(t)
	ctx = reqCtx(ctx, u, authn.AudiencePublic)
	if err := svc.Write(ctx, Event{Action: a}); err != nil {
		t.Fatal(err)
	}
	raw := scanRaw(t, d.Conn(ctx).Raw(rawColsSQL, a).Row())
	if raw.ActorKind != "user" {
		t.Fatalf("actor_kind %q", raw.ActorKind)
	}
	if raw.ActorAudience == nil || *raw.ActorAudience != "public" {
		t.Fatalf("actor_audience %v", raw.ActorAudience)
	}
}

func TestWriteExplicitActorNotSubject(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))
	subject := uuid.New()
	explicit := uuid.New()
	a := uniqueAction(t)
	ctx = reqCtx(ctx, subject, authn.AudienceAdmin)
	if err := svc.Write(ctx, Event{Action: a, ActorID: &explicit}); err != nil {
		t.Fatal(err)
	}
	raw := scanRaw(t, d.Conn(ctx).Raw(rawColsSQL, a).Row())
	if raw.ActorKind != "user" {
		t.Fatalf("actor_kind %q", raw.ActorKind)
	}
	if raw.ActorID == nil || *raw.ActorID != explicit {
		t.Fatalf("actor_id %v", raw.ActorID)
	}
	if raw.ActorAudience != nil {
		t.Fatalf("actor_audience should be nil, got %v", raw.ActorAudience)
	}
	if raw.AuthContext != nil {
		t.Fatalf("auth_context should be nil, got %v", raw.AuthContext)
	}
}

func TestWriteAnonymousAndSystem(t *testing.T) {
	d := testkit.DB(t)
	svc := mustService(t, repo.New(d))

	ctxA := testkit.TxCtx(t, d)
	a1 := uniqueAction(t)
	ctxA = reqmeta.With(ctxA, reqmeta.Meta{IP: "203.0.113.7"})
	if err := svc.Write(ctxA, Event{Action: a1}); err != nil {
		t.Fatal(err)
	}
	raw1 := scanRaw(t, d.Conn(ctxA).Raw(rawColsSQL, a1).Row())
	if raw1.ActorKind != "anonymous" {
		t.Fatalf("anonymous actor_kind %q", raw1.ActorKind)
	}
	if raw1.ActorID != nil {
		t.Fatalf("anonymous actor_id %v", raw1.ActorID)
	}

	ctxS := testkit.TxCtx(t, d)
	a2 := uniqueAction(t)
	if err := svc.Write(ctxS, Event{Action: a2}); err != nil {
		t.Fatal(err)
	}
	raw2 := scanRaw(t, d.Conn(ctxS).Raw(rawColsSQL, a2).Row())
	if raw2.ActorKind != "system" {
		t.Fatalf("system actor_kind %q", raw2.ActorKind)
	}
	if raw2.RequestID != nil {
		t.Fatalf("system request_id %v", raw2.RequestID)
	}
	if len(raw2.IPHash) != 0 {
		t.Fatalf("system ip_hash %v", raw2.IPHash)
	}
}

func TestWriteOutcomeAndValidation(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	svc := mustService(t, repo.New(d))

	aFail := uniqueAction(t)
	if err := svc.Write(ctx, Event{Action: aFail, Data: map[string]any{"outcome": "failure"}}); err != nil {
		t.Fatal(err)
	}
	raw := scanRaw(t, d.Conn(ctx).Raw(rawColsSQL, aFail).Row())
	if raw.Outcome != OutcomeFailure {
		t.Fatalf("outcome %q", raw.Outcome)
	}

	aBogus := uniqueAction(t)
	if err := svc.Write(ctx, Event{Action: aBogus, Data: map[string]any{"outcome": "bogus"}}); err == nil {
		t.Fatal("bogus outcome should error")
	}

	if err := svc.Write(ctx, Event{Action: "  "}); err == nil {
		t.Fatal("blank action should error")
	}
}

func TestWriteRollbackOnTxError(t *testing.T) {
	d := testkit.DB(t)
	svc := mustService(t, repo.New(d))
	a := uniqueAction(t)
	m := tx.NewManager(d)

	err := m.Do(context.Background(), func(ctx context.Context) error {
		if err := svc.Write(ctx, Event{Action: a}); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("want boom, got %v", err)
	}

	_, total, err := svc.List(context.Background(), Filter{ActionPrefix: a}, Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("total after rollback %d", total)
	}
}

func TestWriteSameIPSameHashDifferentEnc(t *testing.T) {
	d := testkit.DB(t)
	svc := mustService(t, repo.New(d))
	a1 := uniqueAction(t)
	a2 := uniqueAction(t)

	ctx1 := testkit.TxCtx(t, d)
	ctx1 = reqmeta.With(ctx1, reqmeta.Meta{IP: "203.0.113.7"})
	if err := svc.Write(ctx1, Event{Action: a1}); err != nil {
		t.Fatal(err)
	}

	ctx2 := testkit.TxCtx(t, d)
	ctx2 = reqmeta.With(ctx2, reqmeta.Meta{IP: "203.0.113.7"})
	if err := svc.Write(ctx2, Event{Action: a2}); err != nil {
		t.Fatal(err)
	}

	r1 := scanRaw(t, d.Conn(ctx1).Raw(rawColsSQL, a1).Row())
	r2 := scanRaw(t, d.Conn(ctx2).Raw(rawColsSQL, a2).Row())
	if len(r1.IPHash) != len(r2.IPHash) {
		t.Fatal("ip_hash length mismatch")
	}
	if !bytes.Equal(r1.IPHash, r2.IPHash) {
		t.Fatal("ip_hash should match for same IP")
	}
	if len(r1.IPEnc) == 0 || len(r2.IPEnc) == 0 {
		t.Fatal("ip_enc empty")
	}
	if bytes.Equal(r1.IPEnc, r2.IPEnc) {
		t.Fatal("ip_enc should differ (random nonce)")
	}
}
