package grpc

// 节点会话日志的字段：server_id 只由鉴权拦截器挂一次（走生产同样的拦截器链）。

import (
	"context"
	"strings"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aegispanel/aegis/internal/module/node/service"
)

// fieldCount 是 fields 里键为 key 的字段个数。
func fieldCount(fields []zapcore.Field, key string) int {
	n := 0
	for _, f := range fields {
		if f.Key == key {
			n++
		}
	}
	return n
}

func TestSessionLogsCarryServerIDOnce(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	serverID := uuid.MustParse("0190a000-0000-7000-8000-0000000000b1")

	b := newFakeBackend()
	svc := service.New(nil, nil, nil, nil)
	m := service.NewSessionManager(b, nil, testSessionOpt)
	svc.UseSessions(m)

	ca := newTestCA(t)
	lis := bufconn.Listen(bufSize)
	auth := &fakeAuth{}
	srv, err := newServer(Options{
		PublicHost: "node.example.test",
		CA:         ca,
		Logger:     zap.New(core),
	}, &fakeEnroller{}, auth, newControlServer(svc, auth))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.ServeListener(lis)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	tlsCfg, _ := clientMTLS(t, ca, serverID)
	conn := dial(t, lis, tlsCfg)
	ctx, cancel := streamCtx(t)
	defer cancel()
	stream, err := nodev1.NewControlServiceClient(conn).Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}

	if err := stream.Send(helloMsg(&nodev1.Hello{})); err != nil {
		t.Fatalf("Send hello: %v", err)
	}
	drainWelcome(t, stream)

	if err := stream.Send(&nodev1.SessionRequest{
		Seq:  2,
		Body: &nodev1.SessionRequest_Hello{Hello: &nodev1.Hello{}},
	}); err != nil {
		t.Fatalf("Send mid-stream hello: %v", err)
	}
	if err := stream.Send(&nodev1.SessionRequest{
		Seq: 3,
		Body: &nodev1.SessionRequest_ConfigAck{
			ConfigAck: &nodev1.ConfigAck{
				NodeId:        uuid.New().String(),
				ContentSha256: strings.Repeat("a", 64),
				Phase:         nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED,
			},
		},
	}); err != nil {
		t.Fatalf("Send config ack: %v", err)
	}

	msgs := []string{
		"节点会话已建立",
		"流中途又收到 Hello，忽略",
		"配置回执的摘要不是最近下发的，忽略",
	}
	waitFor(t, 3*time.Second, func() bool {
		for _, msg := range msgs {
			if logs.FilterMessage(msg).Len() < 1 {
				return false
			}
		}
		return true
	}, "三条会话日志")

	wantServerID := serverID.String()
	for _, msg := range msgs {
		for _, e := range logs.FilterMessage(msg).All() {
			if n := fieldCount(e.Context, "server_id"); n != 1 {
				t.Fatalf("日志「%s」：server_id 出现 %d 次，期望 1", msg, n)
			}
			for _, f := range e.Context {
				if f.Key == "server_id" && f.String != wantServerID {
					t.Fatalf("日志「%s」：server_id=%q，期望 %q", msg, f.String, wantServerID)
				}
			}
			if n := fieldCount(e.Context, "req_id"); n != 1 {
				t.Fatalf("日志「%s」：req_id 出现 %d 次，期望 1", msg, n)
			}
			if msg == "节点会话已建立" || msg == "配置回执的摘要不是最近下发的，忽略" {
				if n := fieldCount(e.Context, "session_id"); n != 1 {
					t.Fatalf("日志「%s」：session_id 出现 %d 次，期望 1", msg, n)
				}
			}
		}
	}

	for _, e := range logs.All() {
		if n := fieldCount(e.Context, "server_id"); n > 1 {
			t.Fatalf("日志「%s」：server_id 出现 %d 次，不得超过 1", e.Message, n)
		}
	}
}
