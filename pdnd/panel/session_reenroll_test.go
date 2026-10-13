package panel

// 面板以 PermissionDenied + x-pandora-reenroll trailer 结束流时，节点与收到 Tombstone 同样处理。

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func newReenrollSession(t *testing.T, fc *fakeControl) *Session {
	t.Helper()
	pki := newTestPKI(t)
	id := newTestIdentity(t, pki, newServerID(), time.Now().Add(30*24*time.Hour))
	dialer := startFakeGRPC(t, pki.serverTLS(t, "127.0.0.1"), nil, fc)
	s, err := NewSession(SessionOptions{
		Address: "127.0.0.1:9443", Identity: id, AgentVersion: "t", BinarySHA256: "b",
		Applied: func() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) { return nil, nil },
		Logger:  slog.New(slog.DiscardHandler),
		Dialer:  dialer, Rand: func(int64) int64 { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionReenrollTrailerStopsReconnecting(t *testing.T) {
	run := func(t *testing.T, afterWelcome bool) {
		fc := newFakeControl(func(st *fakeStream) error {
			if afterWelcome {
				_ = st.SendWelcome(1, 1)
			}
			st.grpc.SetTrailer(metadata.Pairs(trailerReenroll, "required"))
			return status.Error(codes.PermissionDenied, "接入身份已失效")
		})
		s := newReenrollSession(t, fc)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		errc := make(chan error, 1)
		go func() { errc <- s.Run(ctx) }()
		if afterWelcome {
			nextEvent(t, s, EventWelcome)
		}
		ev := nextEvent(t, s, EventTombstone)
		if ev.Reason != "reenroll_required" {
			t.Fatalf("reason=%q", ev.Reason)
		}
		select {
		case err := <-errc:
			if !errors.Is(err, ErrTombstoned) {
				t.Fatalf("Run err=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Run 未返回 ErrTombstoned")
		}
		n := fc.streams()
		time.Sleep(300 * time.Millisecond)
		if fc.streams() != n {
			t.Fatal("身份失效后不应再建流")
		}
	}
	t.Run("before_welcome", func(t *testing.T) { run(t, false) })
	t.Run("after_welcome", func(t *testing.T) { run(t, true) })
}

func TestSessionPermissionDeniedWithoutTrailerRetries(t *testing.T) {
	fc := newFakeControl(func(*fakeStream) error {
		return status.Error(codes.PermissionDenied, "x")
	})
	s := newReenrollSession(t, fc)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx) }()
	waitStreams(t, fc, 2, 5*time.Second)
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 cancel 后返回")
	}
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == EventTombstone {
				t.Fatal("没有 trailer 的 PermissionDenied 不应当作 Tombstone")
			}
		default:
			return
		}
	}
}
