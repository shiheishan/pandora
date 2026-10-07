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

var errNilHandler = errors.New("server: nil Handler would serve http.DefaultServeMux")

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
// 停机顺序（SIGTERM 时）：
//  1. 停止接受新连接，并立即取消所有事件流（SSE）请求的 ctx——它们按设计永不
//     自己结束，不取消就会把 Shutdown 拖满整个宽限期；
//  2. 普通请求的 ctx 不随信号取消，Shutdown 等它们跑完（下单、支付回调的事务
//     能正常提交），最多等 ShutdownTimeout；
//  3. 宽限期到了还没跑完的，才取消它们的 ctx 并强制关连接。
//
// 以前 BaseContext 直接返回信号 ctx，收到 SIGTERM 的瞬间所有在途请求同时被
// 取消，与单元文件里「让在途事务跑完再退」的意图正相反。调用方的后台循环应
// 在 RunContext 返回之后再取消（见各网关 main）。
func RunContext(ctx context.Context, opts Options) error {
	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	return runContextWithListener(ctx, opts, listener)
}

func runContextWithListener(ctx context.Context, opts Options, listener net.Listener) error {
	// nil Handler 会让 http.Server 落到 DefaultServeMux。网关二进制链着
	// net/http/pprof（platform/profiling），它在 init 里往 DefaultServeMux 注册了
	// /debug/pprof/——忘传路由的那一刻，pprof 就挂在了对外端口上。
	if opts.Handler == nil {
		_ = listener.Close()
		return errNilHandler
	}
	if opts.ShutdownTimeout == 0 {
		opts.ShutdownTimeout = 20 * time.Second
	}

	// 请求 ctx 的根：与信号 ctx 脱钩，只在宽限期耗尽时取消（见上面的停机顺序）
	baseCtx, cancelBase := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelBase()
	streams := newStreamTracker()

	srv := &http.Server{
		Addr:    opts.Addr,
		Handler: streams.wrap(opts.Handler),
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
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

	// 事件流先断：浏览器 / 节点会自动重连到新进程。Shutdown 本身会先关监听，
	// 这里先于它调用也不会让新连接漏进来——drain 之后新登记的流会被立即取消。
	if n := streams.drain(); n > 0 {
		opts.Log.Info("closing event streams", slog.Int("streams", n))
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		opts.Log.Error("graceful shutdown timed out; forcing close", slog.String("error", err.Error()))
		cancelBase()
		return srv.Close()
	}
	opts.Log.Info("HTTP service stopped")
	return nil
}
