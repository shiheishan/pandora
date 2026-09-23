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
