// [INPUT]: 依赖 server.go 的 runContextWithListener、Options、errNilHandler
// [OUTPUT]: 对外提供 TestRunContextCancelsActiveHandler、TestRunContextRefusesNilHandler
// [POS]: platform/server 的生命周期测试：取消 context 释放长连接处理器后再优雅停机，nil Handler 在开服前即被拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestRunContextCancelsActiveHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	handlerStarted := make(chan struct{})
	handlerStopped := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(handlerStarted)
		<-r.Context().Done()
		close(handlerStopped)
	})

	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- runContextWithListener(ctx, Options{
			Handler:         handler,
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
			ShutdownTimeout: time.Second,
		}, listener)
	}()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		cancel()
		<-serverDone
		t.Fatal(err)
	}
	defer response.Body.Close()

	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("handler did not start")
	}

	startedShutdown := time.Now()
	cancel()

	select {
	case <-handlerStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("active handler did not receive context cancellation")
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server returned an error: %v", err)
		}
		if elapsed := time.Since(startedShutdown); elapsed >= 2*time.Second {
			t.Fatalf("shutdown took %v, want less than 2s", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not return within 2s")
	}
}

func TestRunContextRefusesNilHandler(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	err = runContextWithListener(context.Background(), Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, listener)
	if !errors.Is(err, errNilHandler) {
		t.Fatalf("nil handler must be refused before serving DefaultServeMux, got %v", err)
	}
	if _, err := listener.Accept(); err == nil {
		t.Fatal("listener must be closed when the handler is refused")
	}
}
