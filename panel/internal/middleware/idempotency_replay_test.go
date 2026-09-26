// [INPUT]: 依赖 idempotency_replay.go 的 decideIdempotencyReplay / decideIdempotencyRecord / serveIdempotencyReplayDecision 与存储编码，依赖 idempotency.go 的 idempotencyRecord / idempotencyCompletion
// [OUTPUT]: 包内提供 intPointer
// [POS]: 重放判定单测：成功记录空正文不再调用处理器、两种旧格式（原始 JSON 与 null）仍可重放、新格式字节精确且不解释旧哨兵、未知 / 不可用 / 资源绑定记录与非法状态、非终态记录一律不当重放
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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

func intPointer(v int) *int { return &v }
