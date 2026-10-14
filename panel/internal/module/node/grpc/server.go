package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// CertAuthority 是 gRPC 服务端要的 CA 能力（*service.CA 实现）。
type CertAuthority interface {
	Certificate() *x509.Certificate
	IssueServerCert(host string, now time.Time) (*tls.Certificate, error)
}

var _ CertAuthority = (*service.CA)(nil)

// 服务端证书剩余不到这么久就换新。
const serverCertRenewBefore = 10 * 24 * time.Hour

// Options 是节点 gRPC 服务端的装配参数。
type Options struct {
	Addr       string // 监听地址（grpc.node.addr）
	PublicHost string // 服务端证书 SAN（grpc.node.public_host），必填
	CA         CertAuthority
	Service    *service.Service
	// Limiter 为 nil 时 Enroll 不限流（只给测试用）
	Limiter *ratelimit.Limiter
	// EnrollPerMinute 是每个来源 IP 每分钟的接入次数上限，0 用 10
	EnrollPerMinute int
	Logger          *zap.Logger
	// UnaryInterceptors / StreamInterceptors 排在拦截器链最外层（如 Prometheus 指标），可为空
	UnaryInterceptors  []grpc.UnaryServerInterceptor
	StreamInterceptors []grpc.StreamServerInterceptor
}

// Server 是节点 gRPC 服务端。
type Server struct {
	gs   *grpc.Server
	addr string
	log  *zap.Logger
}

// NewServer 构造服务端（不监听）：TLS 1.3，客户端证书可选（只信面板 CA），注册 EnrollmentService 与 ControlService。
func NewServer(o Options) (*Server, error) {
	if o.PublicHost == "" {
		return nil, errors.New("node grpc: public_host 为空")
	}
	if o.CA == nil {
		return nil, errors.New("node grpc: CA 为空")
	}
	if o.Service == nil {
		return nil, errors.New("node grpc: service 为空")
	}
	if o.Logger == nil {
		return nil, errors.New("node grpc: logger 为空")
	}
	return newServer(o, o.Service, o.Service, NewControlServer(o.Service))
}

func newServer(o Options, enr enroller, auth sessionAuthorizer, ctrl nodev1.ControlServiceServer) (*Server, error) {
	certs := &certSource{ca: o.CA, host: o.PublicHost, now: time.Now, log: o.Logger}
	if _, err := certs.get(nil); err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	pool.AddCert(o.CA.Certificate())
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      pool,
		GetCertificate: certs.get,
	}
	creds := credentials.NewTLS(cfg)

	ic := &interceptors{log: o.Logger, auth: auth}
	gs := grpc.NewServer(
		grpc.Creds(creds),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             20 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    2 * time.Minute,
			Timeout: 20 * time.Second,
		}),
		grpc.ConnectionTimeout(20*time.Second),
		grpc.ChainUnaryInterceptor(slices.Concat(o.UnaryInterceptors,
			[]grpc.UnaryServerInterceptor{ic.unaryReqID, ic.unaryRecover, ic.unaryAuth})...),
		grpc.ChainStreamInterceptor(slices.Concat(o.StreamInterceptors,
			[]grpc.StreamServerInterceptor{ic.streamReqID, ic.streamRecover, ic.streamAuth})...),
	)

	nodev1.RegisterEnrollmentServiceServer(gs, newEnrollServer(enr, o.Limiter, o.EnrollPerMinute))
	nodev1.RegisterControlServiceServer(gs, ctrl)

	return &Server{gs: gs, addr: o.Addr, log: o.Logger}, nil
}

// Serve 在 o.Addr 上监听并服务，直到 Shutdown；正常关闭返回 nil。
func (s *Server) Serve(ctx context.Context) error {
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.addr)
	if err != nil {
		return err
	}
	s.log.Info("节点 gRPC 已监听", zap.String("addr", lis.Addr().String()))
	return s.ServeListener(lis)
}

// ServeListener 在给定监听上服务（测试用 bufconn），直到 Shutdown；正常关闭返回 nil。
func (s *Server) ServeListener(lis net.Listener) error {
	err := s.gs.Serve(lis)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

// Shutdown 优雅关闭：等在途调用结束，ctx 到期就强停。会话流是长连接，调用方应先让会话层收尾。
func (s *Server) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.gs.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.gs.Stop()
		<-done
		return ctx.Err()
	}
}

type certSource struct {
	ca   CertAuthority
	host string
	now  func() time.Time
	log  *zap.Logger
	mu   sync.Mutex
	cur  *tls.Certificate
}

func (c *certSource) get(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.cur == nil || now.After(c.cur.Leaf.NotAfter.Add(-serverCertRenewBefore)) {
		nc, err := c.ca.IssueServerCert(c.host, now)
		if err != nil {
			if c.cur != nil && now.Before(c.cur.Leaf.NotAfter) {
				c.log.Warn("续签节点 gRPC 服务端证书失败，继续使用旧证书", zap.Error(err))
			} else {
				return nil, err
			}
		} else {
			c.cur = nc
			c.log.Info("节点 gRPC 服务端证书已更新", zap.Time("not_after", nc.Leaf.NotAfter))
		}
	}
	return c.cur, nil
}
