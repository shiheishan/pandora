// Package server provides the shared HTTP server lifecycle for all gateways.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Options struct {
	Addr            string
	Handler         http.Handler
	Log             *slog.Logger
	ShutdownTimeout time.Duration
}

// Run starts the server and waits for an interrupt or SIGTERM. Callers that
// have background workers sharing the process lifetime should create their own
// signal context and call RunContext instead.
func Run(opts Options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, opts)
}

// RunContext starts the server and shuts it down when ctx is cancelled.
//
// ctx is also installed as the base context for every accepted connection.
// Cancelling it therefore releases long-lived handlers (notably SSE) before
// Shutdown waits for active connections to become idle.
func RunContext(ctx context.Context, opts Options) error {
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	return runContextWithListener(ctx, opts, listener)
}

func runContextWithListener(ctx context.Context, opts Options, listener net.Listener) error {
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 20 * time.Second
	}

	srv := &http.Server{
		Addr:    opts.Addr,
		Handler: opts.Handler,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 16,
		ErrorLog:          slog.NewLogLogger(opts.Log.Handler(), slog.LevelWarn),
	}

	errCh := make(chan error, 1)
	go func() {
		opts.Log.Info("HTTP service started", slog.String("addr", listener.Addr().String()))
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		opts.Log.Info("shutdown requested", slog.Duration("grace", opts.ShutdownTimeout))
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), opts.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		opts.Log.Error("graceful shutdown timed out; forcing close", slog.String("error", err.Error()))
		return srv.Close()
	}
	opts.Log.Info("HTTP service stopped")
	return nil
}
