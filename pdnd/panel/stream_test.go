package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// SSE 的帧解析：心跳注释和空行要跳过，data 行才是事件。
func TestStreamParsesFramesAndSkipsHeartbeat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		// 心跳注释、空行，然后才是真事件——都得正确跳过
		fmt.Fprint(w, ": keepalive\n\n")
		fmt.Fprint(w, "\n")
		fmt.Fprint(w, `data: {"event":"sync.users","data":{"users":[{"id":1,"uuid":"u1","speed_limit":50,"device_limit":2}],"version":"v1"}}`+"\n\n")
		f.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out := make(chan StreamEvent, 4)
	go c.Stream(ctx, out, nil)

	select {
	case e := <-out:
		if e.Type != EventSyncUsers {
			t.Fatalf("事件类型 = %s", e.Type)
		}
		if len(e.Users) != 1 || e.Users[0].UUID != "u1" || e.Users[0].SpeedLimit != 50 {
			t.Errorf("用户解析错了：%+v", e.Users)
		}
		if e.Version != "v1" {
			t.Errorf("版本 = %q", e.Version)
		}
	case <-ctx.Done():
		t.Fatal("没收到事件——心跳或空行把解析卡住了")
	}
}

// 增量事件要带上基准版本，节点端靠它判断能不能打这个补丁。
func TestStreamParsesDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"event":"sync.user.delta","data":{"delta":{"added":[{"id":9,"uuid":"u9"}],"removed":[3]},"from_version":"v1","to_version":"v2"}}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := make(chan StreamEvent, 4)
	go c.Stream(ctx, out, nil)

	select {
	case e := <-out:
		if e.Type != EventSyncUserDelta {
			t.Fatalf("事件类型 = %s", e.Type)
		}
		if len(e.Added) != 1 || e.Added[0].ID != 9 {
			t.Errorf("Added = %+v", e.Added)
		}
		if len(e.Removed) != 1 || e.Removed[0] != 3 {
			t.Errorf("Removed = %v", e.Removed)
		}
		if e.FromVersion != "v1" || e.ToVersion != "v2" {
			t.Errorf("版本区间 = %q → %q", e.FromVersion, e.ToVersion)
		}
	case <-ctx.Done():
		t.Fatal("没收到增量事件")
	}
}

// 不认识的事件类型要跳过，不能断开重连。
//
// 面板加了新事件时，老节点端应当忽略它继续工作，而不是陷入
// 「连上－读到不认识的－断开－重连」的循环。
func TestStreamIgnoresUnknownEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, `data: {"event":"some.future.event","data":{"x":1}}`+"\n\n")
		fmt.Fprint(w, `data: {"event":"sync.config","data":{"etag":"cfg-1"}}`+"\n\n")
		f.Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out := make(chan StreamEvent, 4)
	go c.Stream(ctx, out, nil)

	select {
	case e := <-out:
		// 跳过未知的，第一个收到的应当是后面那条 config
		if e.Type != EventSyncConfig || e.ConfigETag != "cfg-1" {
			t.Errorf("收到 %+v，期望跳过未知事件后拿到 config", e)
		}
	case <-ctx.Done():
		t.Fatal("未知事件把整条流卡死了")
	}
}

// 连不上时要退避重连，而不是死循环猛冲。
func TestStreamRetriesWithBackoff(t *testing.T) {
	var attempts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	errs := make(chan error, 8)
	c.Stream(ctx, make(chan StreamEvent, 1), func(err error) {
		select {
		case errs <- err:
		default:
		}
	})

	if attempts == 0 {
		t.Fatal("一次都没试")
	}
	// 退避从 1 秒起，1.5 秒内不该试很多次——没有退避的话会是几千次
	if attempts > 4 {
		t.Errorf("1.5 秒内试了 %d 次，退避没起作用", attempts)
	}
	if len(errs) == 0 {
		t.Error("失败了却没有回调报错")
	}
}
