package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestIdempotencyFailsClosedWithoutAuthenticatedTenantActor(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nextCalls := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalls++ })
	handler := Idempotency(nil, "order_create", log)(next)

	for name, ctx := range map[string]context.Context{
		"anonymous": context.Background(),
		"tenant mismatch": httpx.WithPrincipal(
			httpx.WithTenantID(context.Background(), "tenant-a"),
			&httpx.Principal{
				Kind: "user", UserID: "actor-a", TenantID: "tenant-b",
			},
		),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/orders", nil).WithContext(ctx)
			req.Header.Set("Idempotency-Key", "key-1")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			if _, ok := IdempotencyClaimFrom(req.Context()); ok {
				t.Fatal("unauthorized request received a claim")
			}
		})
	}
	if nextCalls != 0 {
		t.Fatalf("unauthorized request invoked downstream %d times", nextCalls)
	}
}

func TestInvalidIdempotencyKeyFailsBeforeDatabase(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, key := range map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("k", 256),
		"nul control":    "key\x00value",
		"tab control":    "key\tvalue",
		"delete control": "key\x7fvalue",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/orders", nil)
			if key != "" {
				req.Header.Set("Idempotency-Key", key)
			}
			w := httptest.NewRecorder()
			Idempotency(nil, "order_create", log)(http.HandlerFunc(
				func(http.ResponseWriter, *http.Request) { t.Fatal("downstream was called") },
			)).ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
		})
	}
}

func TestOwnedIdempotencyClaimContextIsTypedAndCopied(t *testing.T) {
	base := context.Background()
	if _, ok := IdempotencyClaimFrom(base); ok {
		t.Fatal("claim existed before ownership")
	}
	if _, ok := IdempotencyClaimFrom(withIdempotencyClaim(base, nil)); ok {
		t.Fatal("replay/conflict/in-flight path received a claim")
	}

	hash := idempotencyRequestHash(http.MethodPost, "/orders", []byte(`{"plan":"p"}`))
	lockedUntil := time.Now().Add(5 * time.Minute).UTC()
	owned := newOwnedIdempotencyClaim(
		"record-1", "tenant-1", "actor-1", "order_create",
		"order_create:actor:0123456789abcdef01234567", "key-1", hash,
		lockedUntil,
	)
	ctx := withIdempotencyClaim(base, owned)
	claim, ok := IdempotencyClaimFrom(ctx)
	if !ok {
		t.Fatal("owner did not receive a claim")
	}
	if claim.ID != "record-1" || claim.TenantID != "tenant-1" ||
		claim.ActorID != "actor-1" || claim.Scope != "order_create" ||
		claim.StorageScope != "order_create:actor:0123456789abcdef01234567" ||
		claim.Key != "key-1" || claim.Generation != 1 ||
		!claim.LockedUntil.Equal(lockedUntil) || claim.RequestHash != hash {
		t.Fatalf("unexpected owned claim: %#v", claim)
	}

	claim.ID = "mutated"
	claim.StorageScope = "mutated"
	claim.LockedUntil = time.Time{}
	claim.RequestHash[0] ^= 0xff
	again, ok := IdempotencyClaimFrom(ctx)
	if !ok || again.ID != "record-1" ||
		again.StorageScope != "order_create:actor:0123456789abcdef01234567" ||
		!again.LockedUntil.Equal(lockedUntil) || again.RequestHash != hash {
		t.Fatal("accessor exposed mutable context state")
	}
}

func TestIdempotencySQLContracts(t *testing.T) {
	for name, tc := range map[string]struct {
		sql       string
		required  []string
		forbidden []string
	}{
		"insert": {
			sql: insertIdempotencyClaimSQL,
			required: []string{
				"actor_id", "ON CONFLICT", "DO NOTHING",
				"RETURNING id, locked_until, claim_generation",
			},
			forbidden: []string{"status)"},
		},
		"locked read": {
			sql: selectIdempotencyClaimForUpdateSQL,
			required: []string{
				"SELECT id", "actor_id", "request_hash", "FOR UPDATE",
			},
		},
		"takeover": {
			sql: takeoverIdempotencyClaimSQL,
			required: []string{
				"WHERE id = $1", "actor_id = $5", "request_hash = $6",
				"status = 'failed'", "claim_generation = claim_generation + 1",
				"claim_generation = $7", "response_format IN ('legacy_empty', 'bytes')",
				"RETURNING id, locked_until, claim_generation",
			},
			forbidden: []string{
				"locked_until < now()", "AND status = 'in_flight'", "response_body =",
				"response_format IN ('legacy_empty', 'bytes', 'unavailable')",
			},
		},
		"completion": {
			sql: completeIdempotencyClaimSQL,
			required: []string{
				"WHERE id = $1", "actor_id = $5", "request_hash = $6",
				"status = 'in_flight'", "claim_generation = $7",
				"locked_until = $17", "response_format = $10",
				"response_payload = $11", "RETURNING id",
			},
			forbidden: []string{"response_body ="},
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, required := range tc.required {
				if !strings.Contains(tc.sql, required) {
					t.Fatalf("SQL missing %q:\n%s", required, tc.sql)
				}
			}
			for _, forbidden := range tc.forbidden {
				if strings.Contains(tc.sql, forbidden) {
					t.Fatalf("SQL unexpectedly contains %q:\n%s", forbidden, tc.sql)
				}
			}
		})
	}
}

func TestInFlightClaimsNeverAutoTakeOver(t *testing.T) {
	for _, lease := range []string{"active", "expired", "completion-failed"} {
		t.Run(lease, func(t *testing.T) {
			if idempotencyStatusAllowsTakeover("in_flight") {
				t.Fatal("in-flight claim became takeoverable")
			}
		})
	}
	if !idempotencyStatusAllowsTakeover("failed") {
		t.Fatal("terminal failed claim cannot retry")
	}
}

func TestSucceededReplayWithEmptyBodyDoesNotInvokeHandler(t *testing.T) {
	storedStatus := http.StatusNoContent
	recorder := newIdempotencyRecorder(httptest.NewRecorder())
	recorder.WriteHeader(storedStatus)
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	decision, err := decideIdempotencyReplay("succeeded", &storedStatus, stored)
	if err != nil {
		t.Fatalf("decision failed: %v", err)
	}

	w := httptest.NewRecorder()
	invocations := 0
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invocations++ })
	if decision.replay {
		if err := serveSucceededIdempotencyReplay(
			w, &decision.status, decision.body, decision.contentType,
			decision.contentTypePresent,
		); err != nil {
			t.Fatalf("replay failed: %v", err)
		}
	} else {
		next.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
	}
	if invocations != 0 {
		t.Fatalf("downstream handler invoked %d times", invocations)
	}
	if w.Code != storedStatus || w.Body.Len() != 0 {
		t.Fatalf("replayed status/body = %d/%q", w.Code, w.Body.String())
	}
	if decision.contentType != "" {
		t.Fatalf("empty body gained content type %q", decision.contentType)
	}
}

func TestNonJSONResponseRoundTripsThroughEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	recorder := newIdempotencyRecorder(w)
	recorder.Header().Set("Content-Type", "application/octet-stream")
	recorder.WriteHeader(http.StatusCreated)
	want := []byte{0x00, 0xff, 'A', 'e', 'g', 'i', 's'}
	if _, err := recorder.Write(want); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	status := http.StatusCreated
	decision, err := decideIdempotencyReplay("succeeded", &status, stored)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !bytes.Equal(decision.body, want) || decision.contentType != "application/octet-stream" {
		t.Fatalf("round trip body/type = %q/%q", decision.body, decision.contentType)
	}
	replayed := httptest.NewRecorder()
	if err := serveSucceededIdempotencyReplay(
		replayed, &decision.status, decision.body, decision.contentType,
		decision.contentTypePresent,
	); err != nil {
		t.Fatalf("serve replay failed: %v", err)
	}
	if replayed.Code != http.StatusCreated || !bytes.Equal(replayed.Body.Bytes(), want) ||
		replayed.Header().Get("Content-Type") != "application/octet-stream" ||
		replayed.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("served replay status/body/headers = %d/%v/%v",
			replayed.Code, replayed.Body.Bytes(), replayed.Header())
	}
}

func TestExplicitWriteHeaderCommitsImmediatelyAndOnlyOnce(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.Header().Set("X-Commit", "first")
	recorder.WriteHeader(http.StatusCreated)
	if underlying.writeHeaderCalls != 1 || underlying.status != http.StatusCreated {
		t.Fatalf("first WriteHeader was not immediate: calls/status=%d/%d",
			underlying.writeHeaderCalls, underlying.status)
	}
	if underlying.committedHeader.Get("X-Commit") != "first" {
		t.Fatalf("committed header = %v", underlying.committedHeader)
	}
	recorder.WriteHeader(http.StatusAccepted)
	if underlying.writeHeaderCalls != 1 || underlying.status != http.StatusCreated {
		t.Fatalf("duplicate WriteHeader changed response: calls/status=%d/%d",
			underlying.writeHeaderCalls, underlying.status)
	}
}

func TestInformationalHeadersDoNotCommitFinalResponse(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.Header().Set("X-Phase", "informational")
	recorder.WriteHeader(http.StatusContinue)
	recorder.WriteHeader(http.StatusEarlyHints)
	if recorder.wroteHeader || recorder.status != 0 || recorder.committedHeader != nil {
		t.Fatalf("1xx mutated final state: status=%d wrote=%v header=%v",
			recorder.status, recorder.wroteHeader, recorder.committedHeader)
	}
	if got := underlying.informational; len(got) != 2 ||
		got[0] != http.StatusContinue || got[1] != http.StatusEarlyHints {
		t.Fatalf("informational writes = %v", got)
	}

	recorder.Header().Set("Content-Type", "application/json")
	recorder.WriteHeader(http.StatusCreated)
	_, _ = recorder.Write([]byte(`{"created":true}`))
	recorder.finish()
	if recorder.status != http.StatusCreated || !recorder.wroteHeader ||
		underlying.status != http.StatusCreated || underlying.writeHeaderCalls != 1 {
		t.Fatalf("final response = recorder %d/%v underlying %d/%d",
			recorder.status, recorder.wroteHeader,
			underlying.status, underlying.writeHeaderCalls)
	}
	if recorder.status < http.StatusOK || recorder.status >= http.StatusMultipleChoices {
		t.Fatal("1xx caused a successful final response to be classified as failed")
	}
}

func TestWriteHeaderCodeValidationMatchesNetHTTP(t *testing.T) {
	for _, code := range []int{99, 1000} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			underlying := newCommitTrackingWriter()
			recorder := newIdempotencyRecorder(underlying)
			recorder.Header().Set("X-Unchanged", "yes")
			func() {
				defer func() {
					if recover() == nil {
						t.Fatalf("WriteHeader(%d) did not panic", code)
					}
				}()
				recorder.WriteHeader(code)
			}()
			if recorder.status != 0 || recorder.wroteHeader ||
				recorder.committedHeader != nil || !recorder.captureComplete ||
				underlying.writeHeaderCalls != 0 || len(underlying.informational) != 0 ||
				recorder.Header().Get("X-Unchanged") != "yes" {
				t.Fatalf("invalid code changed state: recorder=%#v underlying=%#v",
					recorder, underlying)
			}
		})
	}

	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.WriteHeader(100)
	recorder.WriteHeader(999)
	recorder.WriteHeader(99) // Superfluous calls after a final are ignored.
	if len(underlying.informational) != 1 || underlying.informational[0] != 100 ||
		recorder.status != 999 || underlying.status != 999 {
		t.Fatalf("valid boundaries/final ignore = info=%v status=%d/%d",
			underlying.informational, recorder.status, underlying.status)
	}
}

func TestLateHeaderMutationDoesNotChangeCommittedOrStoredEvidence(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.Header().Set("Content-Type", "application/json")
	recorder.Header().Set("X-Frozen", "yes")
	recorder.WriteHeader(http.StatusCreated)
	recorder.Header().Set("Content-Type", "text/plain")
	recorder.Header().Set("X-Frozen", "late")
	_, _ = recorder.Write([]byte(`{"ok":true}`))

	if underlying.committedHeader.Get("Content-Type") != "application/json" ||
		underlying.committedHeader.Get("X-Frozen") != "yes" {
		t.Fatalf("first response header was not frozen: %v", underlying.committedHeader)
	}
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusCreated
	decision, err := decideIdempotencyReplay("succeeded", &status, stored)
	if err != nil {
		t.Fatal(err)
	}
	if decision.contentType != "application/json" {
		t.Fatalf("stored content type observed late mutation: %q", decision.contentType)
	}
}

func TestExplicitFinalBinaryCapturesServerGeneratedContentType(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.WriteHeader(http.StatusCreated)
	_, _ = recorder.Write([]byte{0x00, 0xff, 0x01})
	recorder.finish()
	if got := underlying.committedHeader.Get("Content-Type"); got != "" {
		t.Fatalf("wrapper mutated committed handler header to %q", got)
	}
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusCreated
	decision, err := decideIdempotencyReplay("succeeded", &status, stored)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.contentTypePresent || decision.contentType != "application/octet-stream" {
		t.Fatalf("server-generated content type evidence = %q/%v",
			decision.contentType, decision.contentTypePresent)
	}
}

func TestEnvelopeDistinguishesAbsentAndExplicitEmptyContentType(t *testing.T) {
	for name, configure := range map[string]func(http.Header){
		"absent": func(http.Header) {},
		"explicit empty": func(header http.Header) {
			header[http.CanonicalHeaderKey("Content-Type")] = []string{""}
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := newIdempotencyRecorder(httptest.NewRecorder())
			configure(recorder.Header())
			recorder.WriteHeader(http.StatusCreated)
			recorder.finish()
			stored, err := encodeStoredIdempotencyResponse(recorder)
			if err != nil {
				t.Fatal(err)
			}
			status := http.StatusCreated
			decision, err := decideIdempotencyReplay("succeeded", &status, stored)
			if err != nil {
				t.Fatal(err)
			}
			wantPresent := name == "explicit empty"
			if decision.contentTypePresent != wantPresent || decision.contentType != "" {
				t.Fatalf("decoded content type = %q/%v, want empty/%v",
					decision.contentType, decision.contentTypePresent, wantPresent)
			}
			replayed := httptest.NewRecorder()
			if err := serveSucceededIdempotencyReplay(
				replayed, &decision.status, decision.body, decision.contentType,
				decision.contentTypePresent,
			); err != nil {
				t.Fatal(err)
			}
			_, present := replayed.Header()[http.CanonicalHeaderKey("Content-Type")]
			if present != wantPresent {
				t.Fatalf("replayed content-type presence = %v, want %v", present, wantPresent)
			}
		})
	}
}

func TestContentAndTransferEncodingEvidenceFailsClosed(t *testing.T) {
	for name, configure := range map[string]func(http.Header){
		"content encoding": func(header http.Header) {
			header.Set("Content-Encoding", "identity")
		},
		"empty content encoding": func(header http.Header) {
			header[http.CanonicalHeaderKey("Content-Encoding")] = []string{""}
		},
		"transfer encoding": func(header http.Header) {
			header.Set("Transfer-Encoding", "chunked")
		},
		"empty transfer encoding": func(header http.Header) {
			header[http.CanonicalHeaderKey("Transfer-Encoding")] = []string{""}
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := newIdempotencyRecorder(httptest.NewRecorder())
			configure(recorder.Header())
			recorder.WriteHeader(http.StatusCreated)
			_, _ = recorder.Write([]byte("encoded representation"))
			recorder.finish()
			if recorder.captureComplete {
				t.Fatal("unrepresented coding metadata remained replayable")
			}
			stored, err := encodeStoredIdempotencyResponse(recorder)
			if err != nil {
				t.Fatal(err)
			}
			status := http.StatusCreated
			if decision, err := decideIdempotencyReplay("succeeded", &status, stored); err == nil || decision.replay {
				t.Fatalf("unsafe envelope replayed: %#v / %v", decision, err)
			}
		})
	}
}

func TestEmptyWriteThenNonEmptyStillSniffsAndBodyDisallowedFailsClosed(t *testing.T) {
	recorder := newIdempotencyRecorder(newCommitTrackingWriter())
	recorder.WriteHeader(http.StatusCreated)
	if n, err := recorder.Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write = %d/%v", n, err)
	}
	_, _ = recorder.Write([]byte{0x00, 0xff, 0x01})
	recorder.finish()
	if !recorder.captureComplete || !recorder.contentTypePresent ||
		recorder.contentType != "application/octet-stream" {
		t.Fatalf("later sniff evidence = complete=%v type=%q/%v",
			recorder.captureComplete, recorder.contentType, recorder.contentTypePresent)
	}

	disallowed := newIdempotencyRecorder(httptest.NewRecorder())
	disallowed.WriteHeader(http.StatusNoContent)
	_, _ = disallowed.Write([]byte("not allowed"))
	disallowed.finish()
	if disallowed.captureComplete {
		t.Fatal("body-disallowed write remained replayable")
	}
}

func TestImplicitFirstWriteRecordsSniffWithoutMutatingCommittedHeader(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	want := []byte{0x00, 0xff, 0x01}
	if _, err := recorder.Write(want); err != nil {
		t.Fatal(err)
	}
	recorder.finish()
	if underlying.status != http.StatusOK || underlying.writeHeaderCalls != 1 ||
		underlying.committedHeader.Get("Content-Type") != "" {
		t.Fatalf("implicit commit status/calls/header = %d/%d/%v",
			underlying.status, underlying.writeHeaderCalls, underlying.committedHeader)
	}
	if !recorder.contentTypePresent || recorder.contentType != "application/octet-stream" {
		t.Fatalf("generated content type evidence = %q/%v",
			recorder.contentType, recorder.contentTypePresent)
	}
	if !bytes.Equal(recorder.body.Bytes(), want) {
		t.Fatalf("captured implicit body = %v", recorder.body.Bytes())
	}
}

func TestEmpty204FirstResponseAndReplay(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.Header().Set("Content-Type", "application/x-empty")
	recorder.WriteHeader(http.StatusNoContent)
	recorder.finish()
	if underlying.writeHeaderCalls != 1 || underlying.status != http.StatusNoContent ||
		underlying.committedHeader.Get("Content-Type") != "application/x-empty" ||
		underlying.body.Len() != 0 {
		t.Fatalf("empty first response mismatch: %#v", underlying)
	}
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatal(err)
	}
	status := http.StatusNoContent
	decision, err := decideIdempotencyReplay("succeeded", &status, stored)
	if err != nil {
		t.Fatal(err)
	}
	replayed := httptest.NewRecorder()
	if err := serveSucceededIdempotencyReplay(
		replayed, &decision.status, decision.body, decision.contentType,
		decision.contentTypePresent,
	); err != nil {
		t.Fatal(err)
	}
	result := replayed.Result()
	if result.StatusCode != http.StatusNoContent || replayed.Body.Len() != 0 ||
		result.Header.Get("Content-Type") != "application/x-empty" ||
		result.Header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("empty replay status/body/header = %d/%q/%v",
			result.StatusCode, replayed.Body.String(), result.Header)
	}
}

func TestFinishCommitsImplicitEmpty200AndFreezesHeaders(t *testing.T) {
	underlying := newCommitTrackingWriter()
	recorder := newIdempotencyRecorder(underlying)
	recorder.Header().Set("X-Finish", "before")
	recorder.finish()
	recorder.Header().Set("X-Finish", "after")
	if underlying.writeHeaderCalls != 1 || underlying.status != http.StatusOK ||
		underlying.committedHeader.Get("X-Finish") != "before" || underlying.body.Len() != 0 {
		t.Fatalf("finish response mismatch: %#v", underlying)
	}
}

func TestRecorderMatchesRealHTTPServer(t *testing.T) {
	t.Run("explicit final binary first and replay", func(t *testing.T) {
		want := []byte{0x00, 0xff, 'A', 'e', 'g', 'i', 's'}
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(want)
			},
		))
		for name, got := range map[string]networkResponse{
			"first": first, "replay": replay,
		} {
			if got.status != http.StatusCreated || !bytes.Equal(got.body, want) ||
				got.header.Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("%s status/body/type = %d/%v/%q", name,
					got.status, got.body, got.header.Get("Content-Type"))
			}
		}
		if replay.header.Get("Idempotency-Replayed") != "true" {
			t.Fatal("replay marker missing")
		}
	})

	t.Run("explicit empty content type", func(t *testing.T) {
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.Header()[http.CanonicalHeaderKey("Content-Type")] = []string{""}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte("plain but explicitly untyped"))
			},
		))
		for name, got := range map[string]networkResponse{
			"first": first, "replay": replay,
		} {
			values, present := got.header[http.CanonicalHeaderKey("Content-Type")]
			if got.status != http.StatusCreated || !present || len(values) != 1 || values[0] != "" {
				t.Fatalf("%s explicit-empty status/header = %d/%v", name, got.status, got.header)
			}
		}
	})

	t.Run("multiple informational then final 201", func(t *testing.T) {
		first, replay, informational := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusProcessing)
				w.WriteHeader(http.StatusEarlyHints)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"created":true}`))
			},
		))
		if len(informational) != 2 || informational[0] != http.StatusProcessing ||
			informational[1] != http.StatusEarlyHints {
			t.Fatalf("observed informational statuses = %v", informational)
		}
		if first.status != http.StatusCreated || replay.status != http.StatusCreated ||
			replay.header.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("final/replay = %d/%d headers=%v",
				first.status, replay.status, replay.header)
		}
	})

	t.Run("late ordinary header mutation", func(t *testing.T) {
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Frozen", "before")
				w.WriteHeader(http.StatusCreated)
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("X-Frozen", "after")
				_, _ = w.Write([]byte(`{"ok":true}`))
			},
		))
		if first.header.Get("Content-Type") != "application/json" ||
			first.header.Get("X-Frozen") != "before" ||
			replay.header.Get("Content-Type") != "application/json" {
			t.Fatalf("late mutation leaked: first=%v replay=%v", first.header, replay.header)
		}
	})

	t.Run("empty write then later nonempty", func(t *testing.T) {
		want := []byte{0x00, 0xff, 0x01}
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(nil)
				_, _ = w.Write(want)
			},
		))
		if first.header.Get("Content-Type") != "application/octet-stream" ||
			replay.header.Get("Content-Type") != "application/octet-stream" ||
			!bytes.Equal(first.body, want) || !bytes.Equal(replay.body, want) {
			t.Fatalf("empty/later response mismatch: first=%#v replay=%#v", first, replay)
		}
	})

	for name, configure := range map[string]func(http.Header){
		"content encoding fails closed": func(header http.Header) {
			header.Set("Content-Encoding", "identity")
		},
		"transfer encoding fails closed": func(header http.Header) {
			header.Set("Transfer-Encoding", "chunked")
		},
	} {
		t.Run(name, func(t *testing.T) {
			first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
				func(w http.ResponseWriter, _ *http.Request) {
					configure(w.Header())
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte("wire representation"))
				},
			))
			if first.status != http.StatusCreated || replay.status != http.StatusInternalServerError ||
				replay.header.Get("Idempotency-Replayed") != "" {
				t.Fatalf("unsafe coding did not fail closed: first=%d replay=%d/%v",
					first.status, replay.status, replay.header)
			}
		})
	}

	t.Run("body disallowed write error fails closed", func(t *testing.T) {
		writeErr := make(chan error, 1)
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
				_, err := w.Write([]byte("forbidden"))
				writeErr <- err
			},
		))
		if err := <-writeErr; !errors.Is(err, http.ErrBodyNotAllowed) {
			t.Fatalf("real server body error = %v", err)
		}
		if first.status != http.StatusNoContent || replay.status != http.StatusInternalServerError {
			t.Fatalf("body-disallowed first/replay = %d/%d", first.status, replay.status)
		}
	})

	t.Run("one MiB exact network replay", func(t *testing.T) {
		want := bytes.Repeat([]byte("z"), maxIdempotencyResponseBytes)
		first, replay, _ := exerciseRecorderNetwork(t, http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(want)
			},
		))
		if !bytes.Equal(first.body, want) || !bytes.Equal(replay.body, want) ||
			first.header.Get("Content-Type") != replay.header.Get("Content-Type") {
			t.Fatalf("1MiB network replay len/type = %d/%d/%q/%q",
				len(first.body), len(replay.body), first.header.Get("Content-Type"),
				replay.header.Get("Content-Type"))
		}
	})
}

func TestResponseCaptureBoundaries(t *testing.T) {
	for name, tc := range map[string]struct {
		size     int
		complete bool
	}{
		"64KiB plus one": {size: 64*1024 + 1, complete: true},
		"exact 1MiB":     {size: maxIdempotencyResponseBytes, complete: true},
		"1MiB plus one":  {size: maxIdempotencyResponseBytes + 1, complete: false},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := newIdempotencyRecorder(httptest.NewRecorder())
			recorder.WriteHeader(http.StatusOK)
			payload := bytes.Repeat([]byte("x"), tc.size)
			_, _ = recorder.Write(payload)
			if recorder.captureComplete != tc.complete {
				t.Fatalf("capture_complete = %v, want %v", recorder.captureComplete, tc.complete)
			}
			stored, err := encodeStoredIdempotencyResponse(recorder)
			if err != nil {
				t.Fatalf("encode failed: %v", err)
			}
			status := http.StatusOK
			decision, err := decideIdempotencyReplay("succeeded", &status, stored)
			if !tc.complete {
				if err == nil || decision.replay {
					t.Fatal("over-limit successful response did not fail closed")
				}
				return
			}
			if err != nil || !decision.replay || !bytes.Equal(decision.body, payload) {
				t.Fatalf("replayable boundary failed: replay=%v bytes=%d err=%v",
					decision.replay, len(decision.body), err)
			}
		})
	}
}

func TestWriterFailureCaptureFailsClosed(t *testing.T) {
	recorder := newIdempotencyRecorder(&failingResponseWriter{})
	recorder.WriteHeader(http.StatusOK)
	_, _ = recorder.Write([]byte("response"))
	if recorder.captureComplete {
		t.Fatal("writer failure remained replayable")
	}
	stored, err := encodeStoredIdempotencyResponse(recorder)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	status := http.StatusOK
	if decision, err := decideIdempotencyReplay("succeeded", &status, stored); err == nil || decision.replay {
		t.Fatal("writer failure did not fail closed")
	}
}

func TestChunkedResponseCrossingOneMiBFailsClosed(t *testing.T) {
	recorder := newIdempotencyRecorder(httptest.NewRecorder())
	recorder.WriteHeader(http.StatusOK)
	first := bytes.Repeat([]byte("a"), maxIdempotencyResponseBytes-7)
	second := bytes.Repeat([]byte("b"), 8)
	_, _ = recorder.Write(first)
	if !recorder.captureComplete || recorder.body.Len() != len(first) {
		t.Fatal("complete first chunk was not captured")
	}
	_, _ = recorder.Write(second)
	if recorder.captureComplete || recorder.body.Len() != 0 {
		t.Fatal("cross-limit chunks did not fail closed")
	}
}

func TestShortWriteCaptureFailsClosed(t *testing.T) {
	recorder := newIdempotencyRecorder(newShortResponseWriter())
	recorder.WriteHeader(http.StatusOK)
	n, err := recorder.Write([]byte("response"))
	if err != nil || n != len("response")-1 {
		t.Fatalf("short writer result = %d/%v", n, err)
	}
	if recorder.captureComplete || recorder.body.Len() != 0 {
		t.Fatal("short write remained replayable")
	}
}

func TestHistoricalRawJSONAndNullRemainReplayable(t *testing.T) {
	status := http.StatusOK
	for name, tc := range map[string]struct {
		stored []byte
		body   []byte
	}{
		"raw JSON":        {stored: []byte(`{"ok":true}`), body: []byte(`{"ok":true}`)},
		"NULL empty body": {stored: nil, body: nil},
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := decideIdempotencyReplay("succeeded", &status, tc.stored)
			if err != nil {
				t.Fatalf("legacy replay failed: %v", err)
			}
			if !decision.replay || !bytes.Equal(decision.body, tc.body) {
				t.Fatalf("legacy body = %q, want %q", decision.body, tc.body)
			}
			if decision.contentType != "application/json; charset=utf-8" {
				t.Fatalf("legacy content type = %q", decision.contentType)
			}
		})
	}
}

func TestModernBytesReplayIsExactAndNeverInterpretsLegacySentinel(t *testing.T) {
	status := http.StatusCreated
	payload := []byte(`{"_aegis_envelope":"aegis-idempotency-response-v1","capture_complete":false}`)
	record := idempotencyRecord{
		status: "succeeded", responseCode: &status, responseFormat: "bytes",
		responsePayload: append([]byte{}, payload...), generation: 3,
		headers: idempotencyReplayHeaders{
			ContentType:     stringPointer("application/octet-stream"),
			Location:        stringPointer("/orders/42"),
			ETag:            stringPointer(`"v1"`),
			CacheControl:    stringPointer("private, max-age=0"),
			ContentLanguage: stringPointer("zh-CN"),
		},
	}
	decision, err := decideIdempotencyRecord(record)
	if err != nil {
		t.Fatalf("modern replay decision failed: %v", err)
	}
	w := httptest.NewRecorder()
	if err := serveIdempotencyReplayDecision(w, decision); err != nil {
		t.Fatalf("modern replay failed: %v", err)
	}
	if w.Code != status || !bytes.Equal(w.Body.Bytes(), payload) {
		t.Fatalf("modern replay status/body = %d/%q", w.Code, w.Body.Bytes())
	}
	if w.Header().Get("Location") != "/orders/42" ||
		w.Header().Get("ETag") != `"v1"` ||
		w.Header().Get("Cache-Control") != "private, max-age=0" ||
		w.Header().Get("Content-Language") != "zh-CN" {
		t.Fatalf("modern replay headers = %#v", w.Header())
	}
}

func TestModernEmptyBytesRemainNonNullAndReplayable(t *testing.T) {
	recorder := newIdempotencyRecorder(httptest.NewRecorder())
	recorder.WriteHeader(http.StatusNoContent)
	recorder.finish()
	completion := recorder.idempotencyCompletion("succeeded")
	if completion.responseFormat != "bytes" || completion.responsePayload == nil ||
		len(completion.responsePayload) != 0 {
		t.Fatalf("empty completion lost bytea presence: %#v", completion)
	}
	code := completion.responseCode
	decision, err := decideIdempotencyRecord(idempotencyRecord{
		status: completion.status, responseCode: &code,
		responseFormat:  completion.responseFormat,
		responsePayload: completion.responsePayload,
		headers:         completion.headers, generation: 1,
	})
	if err != nil || !decision.replay || len(decision.body) != 0 {
		t.Fatalf("empty modern bytes replay = %#v / %v", decision, err)
	}
}

func TestUnknownUnavailableAndResourceBoundRecordsFailClosed(t *testing.T) {
	failedCode := http.StatusBadGateway
	resourceType, resourceID := "order", "order-1"
	for name, record := range map[string]idempotencyRecord{
		"unknown format": {
			status: "failed", responseCode: &failedCode,
			responseFormat: "future", generation: 1,
		},
		"unavailable": {
			status: "failed", responseCode: &failedCode,
			responseFormat: "unavailable", generation: 1,
		},
		"resource bound": {
			status: "failed", responseCode: &failedCode, responseFormat: "bytes",
			responsePayload: []byte("failed"), generation: 1,
			resourceType: &resourceType, resourceID: &resourceID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := decideIdempotencyRecord(record)
			if name == "unknown format" {
				if err == nil || decision.replay || decision.retryable {
					t.Fatalf("unknown format accepted: %#v / %v", decision, err)
				}
				return
			}
			if err != nil || decision.replay || decision.retryable || !decision.terminalBlocked {
				t.Fatalf("terminal record did not fail closed: %#v / %v", decision, err)
			}
		})
	}
}

func TestCompletionProbeClassifiesExactResourceAndStaleGeneration(t *testing.T) {
	code := http.StatusCreated
	expected := idempotencyCompletion{
		status: "succeeded", responseCode: code, responseFormat: "bytes",
		responsePayload: []byte("created"),
		headers:         idempotencyReplayHeaders{Location: stringPointer("/orders/42")},
	}
	exact := idempotencyRecord{
		status: "succeeded", responseCode: &code, responseFormat: "bytes",
		responsePayload: []byte("created"), headers: expected.headers, generation: 4,
	}
	if got := classifyIdempotencyCompletionProbe(exact, 4, expected); got != "business_completed" {
		t.Fatalf("exact completion classification = %q", got)
	}
	resourceType, resourceID := "order", "42"
	resource := exact
	resource.responsePayload = []byte("different")
	resource.resourceType, resource.resourceID = &resourceType, &resourceID
	if got := classifyIdempotencyCompletionProbe(resource, 4, expected); got != "resource_bound" {
		t.Fatalf("resource completion classification = %q", got)
	}
	stale := exact
	stale.generation = 5
	if got := classifyIdempotencyCompletionProbe(stale, 4, expected); got != "stale_generation" {
		t.Fatalf("stale completion classification = %q", got)
	}
}

func TestInvalidAuthenticatedActorUUIDFailsBeforeDatabase(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := httpx.WithPrincipal(
		httpx.WithTenantID(context.Background(), "11111111-1111-1111-1111-111111111111"),
		&httpx.Principal{
			Kind: "user", UserID: "not-a-uuid",
			TenantID: "11111111-1111-1111-1111-111111111111",
		},
	)
	req := httptest.NewRequest(http.MethodPost, "/orders", nil).WithContext(ctx)
	req.Header.Set("Idempotency-Key", "invalid-actor")
	w := httptest.NewRecorder()
	Idempotency(nil, "order_create", log)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) { t.Fatal("downstream was called") },
	)).ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("invalid actor status = %d, want 500", w.Code)
	}
}

func TestSucceededReplayWithInvalidStatusFailsClosed(t *testing.T) {
	for name, code := range map[string]*int{
		"NULL":    nil,
		"non-2xx": intPointer(http.StatusBadGateway),
	} {
		t.Run(name, func(t *testing.T) {
			decision, err := decideIdempotencyReplay("succeeded", code, nil)
			if err == nil || decision.replay {
				t.Fatalf("malformed terminal record accepted: %#v / %v", decision, err)
			}
		})
	}
}

func TestNonTerminalIdempotencyStatesAreNotReplay(t *testing.T) {
	lease := time.Now().Add(time.Minute)
	failedCode := http.StatusBadGateway
	resourceType, resourceID := "order", "order-42"
	for name, record := range map[string]idempotencyRecord{
		"in_flight": {
			status: "in_flight", responseFormat: "none", lockedUntil: &lease,
			generation: 1,
		},
		"in_flight resource bound": {
			status: "in_flight", responseFormat: "none", lockedUntil: &lease,
			generation: 1, resourceType: &resourceType, resourceID: &resourceID,
		},
		"failed": {
			status: "failed", responseCode: &failedCode, responseFormat: "bytes",
			responsePayload: []byte{}, generation: 1,
		},
	} {
		decision, err := decideIdempotencyRecord(record)
		if err != nil {
			t.Fatalf("%s decision failed: %v", name, err)
		}
		if decision.replay {
			t.Fatalf("%s was marked replayable", name)
		}
	}
}

func TestIdempotencyRequestHashIncludesMethodEscapedPathQueryAndBody(t *testing.T) {
	body := []byte(`{"reason":"same body"}`)
	base := idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", body)

	for name, got := range map[string][32]byte{
		"method":       idempotencyRequestHash("PATCH", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", body),
		"escaped path": idempotencyRequestHash("POST", "/v1/revenue/adjustments/a/b/reverse?mode=full", body),
		"raw query":    idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=partial", body),
		"body":         idempotencyRequestHash("POST", "/v1/revenue/adjustments/a%2Fb/reverse?mode=full", []byte(`{"reason":"different"}`)),
	} {
		if got == base {
			t.Fatalf("%s did not affect request hash", name)
		}
	}
}

func TestLegacyLookupSQLContract(t *testing.T) {
	for _, required := range []string{
		"SELECT 1", "app.lookup_legacy_idempotency_key($1, $2, $3)",
	} {
		if !strings.Contains(legacyIdempotencyLookupSQL, required) {
			t.Fatalf("legacy lookup missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"FROM idempotency_keys", "FOR SHARE", "created_at", "completed_at",
		"CURRENT_TIMESTAMP", "interval '30 days'",
	} {
		if strings.Contains(legacyIdempotencyLookupSQL, forbidden) {
			t.Fatalf("legacy lookup unexpectedly contains %q", forbidden)
		}
	}
}

func TestIdempotencyBaseScopeFailsFastAtConstruction(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, valid := range []string{"a", "order_create", strings.Repeat("a", 64)} {
		t.Run("valid_"+valid[:1], func(t *testing.T) {
			_ = Idempotency(nil, valid, log)
		})
	}
	for name, invalid := range map[string]string{
		"empty":          "",
		"too long":       strings.Repeat("a", 65),
		"actor suffix":   "order_create:actor:0123456789abcdef01234567",
		"uppercase":      "Order_create",
		"dash":           "order-create",
		"space":          "order create",
		"non ascii":      "order_创建",
		"embedded colon": "order:create",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered == nil {
					t.Fatalf("invalid scope %q did not panic", invalid)
				}
			}()
			_ = Idempotency(nil, invalid, log)
		})
	}
}

func TestIdempotencyRequestTargetPreservesEscapedPathAndRawQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/items/a%2Fb?mode=full&x=%2F", nil)
	if got, want := idempotencyRequestTarget(req.URL), "/items/a%2Fb?mode=full&x=%2F"; got != want {
		t.Fatalf("request target = %q, want %q", got, want)
	}
}

func TestIdempotencyScopeIsActorBound(t *testing.T) {
	a := idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-a"})
	b := idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-b"})
	if a == b {
		t.Fatal("two actors share an idempotency uniqueness domain")
	}
	if a != idempotencyActorScope("order_create", &httpx.Principal{UserID: "actor-a"}) {
		t.Fatal("same actor scope is unstable")
	}
	if idempotencyActorScope("order_create", nil) !=
		idempotencyActorScope("order_create", &httpx.Principal{}) {
		t.Fatal("anonymous scope is unstable")
	}
}

type networkResponse struct {
	status           int
	header           http.Header
	body             []byte
	transferEncoding []string
}

func exerciseRecorderNetwork(
	t *testing.T, downstream http.Handler,
) (networkResponse, networkResponse, []int) {
	t.Helper()
	var evidence struct {
		sync.Mutex
		status int
		stored []byte
		err    error
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			recorder := newIdempotencyRecorder(w)
			downstream.ServeHTTP(recorder, r)
			recorder.finish()
			stored, err := encodeStoredIdempotencyResponse(recorder)
			evidence.Lock()
			evidence.status = recorder.status
			evidence.stored = append([]byte(nil), stored...)
			evidence.err = err
			evidence.Unlock()
		case "/replay":
			evidence.Lock()
			status := evidence.status
			stored := append([]byte(nil), evidence.stored...)
			captureErr := evidence.err
			evidence.Unlock()
			if captureErr != nil || status == 0 {
				http.Error(w, "capture unavailable", http.StatusInternalServerError)
				return
			}
			decision, err := decideIdempotencyReplay("succeeded", &status, stored)
			if err != nil || !decision.replay {
				http.Error(w, "capture unavailable", http.StatusInternalServerError)
				return
			}
			if err := serveSucceededIdempotencyReplay(
				w, &decision.status, decision.body, decision.contentType,
				decision.contentTypePresent,
			); err != nil {
				http.Error(w, "replay unavailable", http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	transport := &http.Transport{DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var informational []int
	fetch := func(path string, traceInformational bool) networkResponse {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if traceInformational {
			trace := &httptrace.ClientTrace{
				Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
					informational = append(informational, code)
					return nil
				},
			}
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return networkResponse{
			status: response.StatusCode, header: response.Header.Clone(), body: body,
			transferEncoding: append([]string(nil), response.TransferEncoding...),
		}
	}

	first := fetch("/first", true)
	replay := fetch("/replay", false)
	return first, replay, informational
}

type commitTrackingWriter struct {
	header           http.Header
	committedHeader  http.Header
	status           int
	writeHeaderCalls int
	informational    []int
	body             bytes.Buffer
}

func newCommitTrackingWriter() *commitTrackingWriter {
	return &commitTrackingWriter{header: make(http.Header)}
}

func (w *commitTrackingWriter) Header() http.Header { return w.header }

func (w *commitTrackingWriter) WriteHeader(code int) {
	if code >= 100 && code <= 199 && code != http.StatusSwitchingProtocols {
		w.informational = append(w.informational, code)
		return
	}
	w.writeHeaderCalls++
	if w.status != 0 {
		return
	}
	w.status = code
	w.committedHeader = w.header.Clone()
}

func (w *commitTrackingWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(b)
}

type shortResponseWriter struct {
	header http.Header
}

func newShortResponseWriter() *shortResponseWriter {
	return &shortResponseWriter{header: make(http.Header)}
}

func (w *shortResponseWriter) Header() http.Header { return w.header }

func (*shortResponseWriter) WriteHeader(int) {}

func (*shortResponseWriter) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	return len(b) - 1, nil
}

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*failingResponseWriter) WriteHeader(int) {}

func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func intPointer(v int) *int { return &v }
