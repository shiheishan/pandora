package grpc

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// enroller 是 Enroll 要的 service 方法。
type enroller interface {
	Enroll(ctx context.Context, in service.EnrollInput) (service.EnrollResult, error)
}

const enrollRateRule = "node_enroll_ip"

type enrollServer struct {
	nodev1.UnimplementedEnrollmentServiceServer
	svc     enroller
	limiter *ratelimit.Limiter
	rule    ratelimit.Rule
}

func newEnrollServer(svc enroller, limiter *ratelimit.Limiter, perMinute int) *enrollServer {
	if perMinute <= 0 {
		perMinute = 10
	}
	return &enrollServer{
		svc:     svc,
		limiter: limiter,
		rule:    ratelimit.PerMinute(enrollRateRule, perMinute).StrictMode(),
	}
}

// Enroll 用一次性令牌与 CSR 换客户端证书；错误一律回中性的状态码，不区分令牌不存在与已用。
func (e *enrollServer) Enroll(ctx context.Context, req *nodev1.EnrollRequest) (*nodev1.EnrollResponse, error) {
	if e.limiter != nil {
		if err := e.limiter.Allow(ctx, e.rule, peerIP(ctx)); err != nil {
			var ee *errs.Error
			if errors.As(err, &ee) && ee.Code == errs.RateLimited {
				return nil, status.Error(codes.ResourceExhausted, "接入请求太频繁，稍后再试")
			}
			return nil, status.Error(codes.Unavailable, "暂时无法处理接入")
		}
	}

	in := service.EnrollInput{
		Token:        req.GetToken(),
		CSRDER:       req.GetCsrDer(),
		RequestID:    req.GetRequestId(),
		AgentVersion: req.GetAgentVersion(),
	}
	if h := req.GetHost(); h != nil {
		in.Host = service.EnrollHostInfo{
			Hostname:    h.GetHostname(),
			OS:          h.GetOs(),
			Arch:        h.GetArch(),
			CPUCores:    h.GetCpuCores(),
			MemoryBytes: h.GetMemoryBytes(),
			DiskBytes:   h.GetDiskBytes(),
		}
	}

	res, err := e.svc.Enroll(ctx, in)
	if err != nil {
		if errors.Is(err, service.ErrEnrollInvalid) {
			return nil, status.Error(codes.InvalidArgument, "接入请求不合法")
		}
		if errors.Is(err, service.ErrEnrollDenied) {
			return nil, status.Error(codes.PermissionDenied, "接入被拒绝：令牌无效、已用或已过期")
		}
		var ee *errs.Error
		if errors.As(err, &ee) && ee.Code == errs.Unavailable {
			return nil, status.Error(codes.Unavailable, "面板暂时无法接入节点")
		}
		plog.From(ctx).Error("节点接入失败", zap.Error(err))
		return nil, status.Error(codes.Internal, "internal error")
	}

	return &nodev1.EnrollResponse{
		ServerId:     res.ServerID.String(),
		CertChainPem: res.ChainPEM,
		CaBundlePem:  res.CAPEM,
	}, nil
}

// peerIP 取对端 IP（取不到时用对端地址原文，再取不到用 "unknown"）。
func peerIP(ctx context.Context) string {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return "unknown"
	}
	addr := p.Addr.String()
	if addr == "" {
		return "unknown"
	}
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" {
		return host
	}
	if ip := net.ParseIP(strings.TrimSpace(addr)); ip != nil {
		return ip.String()
	}
	return addr
}
