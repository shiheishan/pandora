package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func startTestServer(t *testing.T, handler http.Handler, grace time.Duration) (addr string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runContextWithListener(ctx, Options{
			Handler:         handler,
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			ShutdownTimeout: grace,
		}, listener)
	}()
	t.Cleanup(cancel)
	return listener.Addr().String(), cancel, serverDone
}

// SIGTERM（信号 ctx 取消）时，在途的普通请求不被取消，能在宽限期内正常跑完并返回
// 200；Shutdown 等它跑完才返回。旧实现把信号 ctx 当 BaseContext，请求 ctx 在信号
// 到达的瞬间就被取消，这条用例会拿到 handler 观察到的 context canceled。
func TestRunContextLetsInFlightRequestFinish(t *testing.T) {
	const work = 300 * time.Millisecond
	started := make(chan struct{})
	ctxErr := make(chan error, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		// 模拟一段事务：期间请求 ctx 不应被取消
		select {
		case <-time.After(work):
		case <-r.Context().Done():
		}
		ctxErr <- r.Context().Err()
		_, _ = io.WriteString(w, "committed")
	})
	addr, cancel, serverDone := startTestServer(t, handler, 5*time.Second)

	type result struct {
		status int
		body   string
		err    error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + addr)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{status: resp.StatusCode, body: string(b), err: err}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	signalAt := time.Now()
	cancel() // 相当于收到 SIGTERM

	select {
	case err := <-ctxErr:
		if err != nil {
			t.Fatalf("in-flight request ctx was cancelled by the shutdown signal: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not finish")
	}
	r := <-got
	if r.err != nil || r.status != http.StatusOK || r.body != "committed" {
		t.Fatalf("in-flight request did not complete: status=%d body=%q err=%v", r.status, r.body, r.err)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server returned an error: %v", err)
		}
		if elapsed := time.Since(signalAt); elapsed < work/2 {
			t.Fatalf("server returned after %v, before the in-flight request finished", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not return after the in-flight request finished")
	}

	// 停机后不再接新连接
	if _, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		t.Fatal("listener must be closed after shutdown")
	}
}

// 宽限期耗尽还没跑完的请求：此时才取消它的 ctx，并强制关连接。
func TestRunContextCancelsStuckRequestAfterGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	started := make(chan struct{})
	cancelledAt := make(chan time.Time, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		cancelledAt <- time.Now()
	})
	addr, cancel, serverDone := startTestServer(t, handler, grace)
	go func() {
		resp, err := http.Get("http://" + addr)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}
	signalAt := time.Now()
	cancel()

	select {
	case at := <-cancelledAt:
		if d := at.Sub(signalAt); d < grace*8/10 {
			t.Fatalf("stuck request was cancelled after %v, before the %v grace period", d, grace)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stuck request was never cancelled")
	}
	select {
	case <-serverDone:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not return after forcing close")
	}
}

// 事件流端点经包装后仍能断言 Flusher、用 ResponseController 解除超时。
func TestStreamWriterKeepsFlusherAndUnwrap(t *testing.T) {
	var flusherOK bool
	var deadlineErr error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, flusherOK = w.(http.Flusher)
		deadlineErr = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		w.WriteHeader(http.StatusNoContent)
	})
	addr, cancel, serverDone := startTestServer(t, handler, time.Second)
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cancel()
	<-serverDone
	if !flusherOK {
		t.Fatal("wrapped writer must implement http.Flusher")
	}
	if deadlineErr != nil {
		t.Fatalf("ResponseController must reach the underlying writer: %v", deadlineErr)
	}
}
