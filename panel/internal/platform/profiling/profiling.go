// [INPUT]: 依赖 net/http/pprof 的 Index/Cmdline/Profile/Symbol/Trace（挂自建 mux，从不经 DefaultServeMux 对外），依赖 log/slog
// [OUTPUT]: 对外提供 Start、Server（Addr、Close）
// [POS]: platform 的 pprof 诊断端口：三个网关按 config.PprofAddrs 各自在独立的回环端口上暴露 /debug/pprof/，与业务网关不共用 listener、路由与中间件；地址校验在 platform/config，这里只对实际绑定的地址再验一次
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package profiling 在独立的回环端口上提供 net/http/pprof。
//
// 为什么不挂到网关路由上：pprof 能导出堆（含解密后的密钥与令牌）、能让进程
// 连续采样几十秒，它不该经过 nginx、不该和用户请求共用限流与超时，更不能因为
// 一条路由写错就出现在公网。独立 listener 只听回环，压测时经 SSH 隧道或在本机取。
package profiling

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// shutdownGrace 是停机时给在途采样的宽限：诊断请求不值得拖慢网关退出，
// 一秒之后直接断开（正在采的 CPU profile 随之作废）。
const shutdownGrace = time.Second

// Server 是一个运行中的 pprof 诊断端口。nil 表示没开，Close 对 nil 是空操作，
// 调用方可以无条件 defer。
type Server struct {
	addr string
	srv  *http.Server
	done chan struct{}
}

// Start 在 addr 上开 pprof；addr 为空即关闭，返回 nil, nil。
//
// 监听失败或实际绑定的不是回环地址都返回错误，网关应当据此拒绝启动——
// 运维显式要了 pprof 却没开成，静默跳过只会让压测数据缺一块而没人察觉。
func Start(addr string, log *slog.Logger) (*Server, error) {
	if addr == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof 监听 %s: %w", addr, err)
	}
	// 第二道闸：config 已只放行回环 IP 字面量，这里再看一次内核实际绑定的地址，
	// 绕过 config 直接调用 Start 的代码也开不出对外的 pprof。
	if err := requireLoopback(ln.Addr()); err != nil {
		_ = ln.Close()
		return nil, err
	}

	s := &Server{
		addr: ln.Addr().String(),
		srv: &http.Server{
			Handler:           newMux(),
			ReadHeaderTimeout: 5 * time.Second,
			// 不设 WriteTimeout：/debug/pprof/profile?seconds=30 与 trace 要占住响应
			// 整个采样窗口，net/http/pprof 发现 WriteTimeout 短于采样时长会直接拒绝。
			IdleTimeout:    60 * time.Second,
			MaxHeaderBytes: 1 << 16,
			ErrorLog:       slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
		done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("pprof 诊断端口异常退出", "addr", s.addr, "error", err.Error())
		}
	}()
	log.Warn("pprof 诊断端口已开启（仅回环，压测与排障用，用完请关）", "addr", s.addr)
	return s, nil
}

// requireLoopback 只放行绑定在回环地址上的 TCP listener。
func requireLoopback(addr net.Addr) error {
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP.IsLoopback() {
		return nil
	}
	return fmt.Errorf("pprof 只能监听回环地址，实际绑定 %s", addr)
}

// newMux 只挂 net/http/pprof 的五个处理器；Index 兼管 heap、goroutine、allocs
// 等按名取的 profile。
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// Addr 是实际绑定的地址（端口写 0 时可从这里拿到真实端口）。
func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	return s.addr
}

// Close 随网关停机关闭诊断端口：先给 shutdownGrace 的宽限，超时即强关，返回前
// 等 Serve 协程退出。
func (s *Server) Close() {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
	}
	<-s.done
}
