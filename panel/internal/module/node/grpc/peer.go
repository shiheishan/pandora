// Package grpc 是节点通道的 gRPC 传输层（与 handler 同层）：TLS 与拦截器、proto 与 service 类型互转、
// 流的收发循环。不碰数据库（depguard 钉住），业务都经 node service。
//
// 分工：server.go、enroll.go（p5-enroll）；control.go（p5-session）；本文件（主 Agent）。
package grpc

import (
	"context"

	"github.com/google/uuid"
)

type serverIDKey struct{}

// WithServerID 把经客户端证书认证过的服务器 id 放进 ctx（只由鉴权拦截器调用）。
func WithServerID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, serverIDKey{}, id)
}

// ServerIDFrom 取鉴权拦截器放进 ctx 的服务器 id；没有（未经拦截器）返回 false。
func ServerIDFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(serverIDKey{}).(uuid.UUID)
	return id, ok
}
