// [INPUT]: 依赖 idempotency_recorder.go 的 newIdempotencyRecorder（WriteHeader / Write / finish）、idempotency_replay.go 的存储编码与重放出口
// [OUTPUT]: 包内提供 commitTrackingWriter / newCommitTrackingWriter（记录下游提交时刻的 ResponseWriter，idempotency_pg18_fixture_test.go 的 pg18ServeTracking 共用）
// [POS]: 录制器的提交语义单测：显式与隐式提交只发生一次、1xx 不提交、状态码校验对齐 net/http、提交后改头不改证据、Content-Type 嗅探与缺省 / 显式空的区分、编码头证据不合规即作废
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
