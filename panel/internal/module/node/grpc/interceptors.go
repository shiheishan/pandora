package grpc

import (
	"context"
	"errors"
	"math/big"

	"github.com/aegispanel/aegis/internal/module/node/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// TrailerReenroll 是建流被拒（身份已失效）时附带的 trailer 键，值为 "required"：节点据此停止重连、提示重新接入。
const TrailerReenroll = "x-pandora-reenroll"

// sessionAuthorizer 是鉴权拦截器要的 service 方法。
type sessionAuthorizer interface {
	AuthorizeSession(ctx context.Context, serverID uuid.UUID, certSerial *big.Int) error
}

type interceptors struct {
	log  *zap.Logger
	auth sessionAuthorizer
}

// requiresClientCert：除 Enroll 外的方法都要已验证的客户端证书。
func requiresClientCert(fullMethod string) bool {
	return fullMethod != nodev1.EnrollmentService_Enroll_FullMethodName
}

// wrappedStream 让下游拿到拦截器换过的 ctx。
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context {
	return w.ctx
}

// withReqID 取 metadata 里的 x-request-id（不合法或没有就生成），放进 ctx 与 logger。
func (i *interceptors) withReqID(ctx context.Context) (context.Context, string) {
	var in string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(reqid.MetadataKey); len(v) > 0 {
			in = v[0]
		}
	}
	id := reqid.Normalize(in)
	return plog.WithReqID(ctx, i.log, id), id
}

func (i *interceptors) unaryReqID(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	ctx, id := i.withReqID(ctx)
	_ = grpc.SetHeader(ctx, metadata.Pairs(reqid.MetadataKey, id))
	return handler(ctx, req)
}

func (i *interceptors) streamReqID(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx, id := i.withReqID(ss.Context())
	_ = ss.SetHeader(metadata.Pairs(reqid.MetadataKey, id))
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}

func (i *interceptors) unaryRecover(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			plog.From(ctx).Error("节点 gRPC 处理 panic", zap.String("method", info.FullMethod), zap.Any("panic", r), zap.Stack("stack"))
			err = status.Error(codes.Internal, "internal error")
			resp = nil
		}
	}()
	return handler(ctx, req)
}

func (i *interceptors) streamRecover(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			plog.From(ss.Context()).Error("节点 gRPC 处理 panic", zap.String("method", info.FullMethod), zap.Any("panic", r), zap.Stack("stack"))
			err = status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(srv, ss)
}

func (i *interceptors) unaryAuth(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if !requiresClientCert(info.FullMethod) {
		return handler(ctx, req)
	}
	ctx, err := i.authorize(ctx)
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			_ = grpc.SetTrailer(ctx, metadata.Pairs(TrailerReenroll, "required"))
		}
		return nil, err
	}
	return handler(ctx, req)
}

func (i *interceptors) streamAuth(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if !requiresClientCert(info.FullMethod) {
		return handler(srv, ss)
	}
	ctx, err := i.authorize(ss.Context())
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			ss.SetTrailer(metadata.Pairs(TrailerReenroll, "required"))
		}
		return err
	}
	return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
}

func (i *interceptors) authorize(ctx context.Context) (context.Context, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return ctx, status.Error(codes.Unauthenticated, "需要面板签发的客户端证书")
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
		return ctx, status.Error(codes.Unauthenticated, "需要面板签发的客户端证书")
	}

	leaf := ti.State.VerifiedChains[0][0]
	id, ok := service.ServerIDFromCert(leaf)
	if !ok {
		return ctx, status.Error(codes.Unauthenticated, "需要面板签发的客户端证书")
	}

	if err := i.auth.AuthorizeSession(ctx, id, leaf.SerialNumber); err != nil {
		if errors.Is(err, service.ErrSessionRevoked) {
			return ctx, status.Error(codes.PermissionDenied, "接入身份已失效，需要在后台生成新令牌重新接入")
		}
		plog.From(ctx).Error("校验节点身份失败", zap.Error(err))
		return ctx, status.Error(codes.Unavailable, "暂时无法校验身份")
	}

	ctx = WithServerID(ctx, id)
	ctx = plog.With(ctx, plog.From(ctx).With(zap.String("server_id", id.String())))
	return ctx, nil
}
