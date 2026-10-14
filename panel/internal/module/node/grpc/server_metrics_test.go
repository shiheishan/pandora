package grpc

import (
	"context"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// 外加的拦截器排在链最外层（指标要数到每一次调用，包括处理器 panic 的）。
func TestExtraInterceptorsOutermost(t *testing.T) {
	ca := newTestCA(t)
	lis := bufconn.Listen(bufSize)
	var unary []string
	opts := Options{
		PublicHost: "node.example.test",
		CA:         ca,
		Logger:     zap.NewNop(),
		UnaryInterceptors: []grpc.UnaryServerInterceptor{
			func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				unary = append(unary, info.FullMethod)
				return h(ctx, req)
			},
		},
	}
	srv, err := newServer(opts, &fakeEnroller{panic: true}, &fakeAuthorizer{}, &fakeControl{})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeListener(lis) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
	// 处理器 panic 由内层的恢复拦截器接住，外层拦截器照样记到这次调用
	if _, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t"}); err == nil {
		t.Fatal("expected error")
	}
	if len(unary) != 1 || unary[0] != nodev1.EnrollmentService_Enroll_FullMethodName {
		t.Fatalf("外加拦截器调用记录 %v", unary)
	}
}
