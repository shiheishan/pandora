package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/grpc"
)

func (p *panel) startGRPC() error {
	p.mu.Lock()
	if p.grpcSrv != nil {
		p.mu.Unlock()
		return nil
	}
	listen := p.grpcListen
	p.mu.Unlock()

	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := grpc.NewServer(grpc.Creds(p.creds))
	nodev1.RegisterEnrollmentServiceServer(srv, p)
	nodev1.RegisterControlServiceServer(srv, p)
	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Printf("gRPC 服务结束: %v", err)
		}
	}()
	p.mu.Lock()
	p.grpcSrv = srv
	p.grpcLis = lis
	p.mu.Unlock()
	return nil
}

func (p *panel) stopGRPC() {
	p.mu.Lock()
	srv := p.grpcSrv
	lis := p.grpcLis
	p.grpcSrv = nil
	p.grpcLis = nil
	p.mu.Unlock()
	if srv != nil {
		srv.Stop()
	}
	if lis != nil {
		_ = lis.Close()
	}
}

func (p *panel) startGRPCWithRetry(maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	for {
		err := p.startGRPC()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// runControlServer 先同步监听（失败直接返回错误），再在后台服务。
func (p *panel) runControlServer(addr string) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", p.handleStatus)
	mux.HandleFunc("/roster/swap", p.handleRosterSwap)
	mux.HandleFunc("/fault/down", p.handleFaultDown)
	mux.HandleFunc("/fault/up", p.handleFaultUp)
	mux.HandleFunc("/fault/cut", p.handleFaultCut)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.Serve(lis); err != nil && err != http.ErrServerClosed {
			log.Printf("控制口服务结束: %v", err)
		}
	}()
	return srv, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (p *panel) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, p.status())
}

func (p *panel) handleRosterSwap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.hasActiveSession() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "没有活动会话"})
		return
	}
	p.mu.Lock()
	gen, _, toVer, sentMs := p.swapRosterLocked()
	p.mu.Unlock()

	p.activeMu.Lock()
	sess := p.activeSess
	p.activeMu.Unlock()
	if sess == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "没有活动会话"})
		return
	}

	for _, n := range p.nodes {
		delta := buildUsersDelta(p.prof, n.nodeID, gen)
		if err := sess.send(&nodev1.SessionResponse{Body: &nodev1.SessionResponse_UsersDelta{UsersDelta: delta}}); err != nil {
			log.Printf("roster/swap 发送 UsersDelta 失败: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		p.mu.Lock()
		p.usersDeltaSent++
		p.mu.Unlock()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"gen": gen, "version": toVer, "sent_unix_ms": sentMs,
	})
}

func (p *panel) handleFaultDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p.mu.Lock()
	if p.down {
		p.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "已经 down"})
		return
	}
	p.down = true
	p.mu.Unlock()

	p.stopGRPC()
	p.cancelActiveSession()

	now := time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, map[string]int64{"down_unix_ms": now})
}

func (p *panel) handleFaultUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p.mu.Lock()
	if !p.down {
		p.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "未处于 down"})
		return
	}
	p.mu.Unlock()

	if err := p.startGRPCWithRetry(5 * time.Second); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	p.mu.Lock()
	p.down = false
	p.mu.Unlock()

	now := time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, map[string]int64{"up_unix_ms": now})
}

func (p *panel) handleFaultCut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.cancelActiveSession() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "没有活动会话"})
		return
	}
	now := time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, map[string]int64{"cut_unix_ms": now})
}

func shutdownControl(ctx context.Context, srv *http.Server) {
	if srv != nil {
		_ = srv.Shutdown(ctx)
	}
}
