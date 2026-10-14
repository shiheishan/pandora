// Package grpc 是节点通道的 gRPC 传输层（与 handler 同层）：TLS 与拦截器、proto 与 service 类型互转、
// 流的收发循环。不碰数据库（depguard 钉住），业务都经 node service。
//
// 分工：server.go、enroll.go；control.go（p5-session）；本文件（主 Agent）。
package grpc

import (
	"context"
	"math/big"

	"github.com/google/uuid"
)

type (
	serverIDKey   struct{}
	certSerialKey struct{}
)

// WithServerID 把经客户端证书认证过的服务器 id 放进 ctx（只由鉴权拦截器调用）。
func WithServerID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, serverIDKey{}, id)
}

// ServerIDFrom 取鉴权拦截器放进 ctx 的服务器 id；没有（未经拦截器）返回 false。
func ServerIDFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(serverIDKey{}).(uuid.UUID)
	return id, ok
}

// WithCertSerial 把建流时出示的客户端证书序列号放进 ctx（只由鉴权拦截器调用）。
// 续期要求它仍是这台服务器 active 那份的序列号，登记后复核身份也用它。
func WithCertSerial(ctx context.Context, serial *big.Int) context.Context {
	return context.WithValue(ctx, certSerialKey{}, new(big.Int).Set(serial))
}

// CertSerialFrom 取鉴权拦截器放进 ctx 的客户端证书序列号；没有返回 false。
func CertSerialFrom(ctx context.Context) (*big.Int, bool) {
	v, ok := ctx.Value(certSerialKey{}).(*big.Int)
	if !ok {
		return nil, false
	}
	return new(big.Int).Set(v), true
}
