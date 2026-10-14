// Package reqid 管请求 ID：HTTP 头、gRPC metadata、asynq 任务载荷里都用同一个值。
package reqid

import (
	"context"

	"github.com/google/uuid"
)

// Header 是 HTTP 请求与响应头名；gRPC metadata 用 MetadataKey。
const (
	Header      = "X-Request-ID"
	MetadataKey = "x-request-id"
)

type key struct{}

// New 生成一个 UUIDv7。
func New() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// Valid 只接受 UUID 形式，避免把任意字符串写进日志。
func Valid(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := uuid.Parse(s)
	return err == nil
}

// Normalize 返回合法的传入值，否则生成新值。
func Normalize(s string) string {
	if Valid(s) {
		return s
	}
	return New()
}

// With 把请求 ID 放进 ctx。
func With(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// From 取 ctx 里的请求 ID，没有时返回空串。
func From(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}
