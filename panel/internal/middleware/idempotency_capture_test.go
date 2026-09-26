// [INPUT]: 依赖 idempotency.go 的 Idempotency 与 maxIdempotencyResponseBytes、idempotency_recorder.go 的录制器、idempotency_replay.go 的存储编码与重放
// [OUTPUT]: 包内提供 networkResponse / exerciseRecorderNetwork（同一处理器分别经真实 HTTP 服务器与录制器 + 重放跑一遍）与 shortResponseWriter、failingResponseWriter
// [POS]: 录制器的捕获边界单测：与真实 net/http 服务器逐字节对齐、1 MiB 上限与分块跨界、下游写失败与短写一律作废留存
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"sync"
	"testing"
)

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
