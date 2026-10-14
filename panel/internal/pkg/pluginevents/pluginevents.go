// Package pluginevents 是插件钩子的出站事件口：发事件的模块（order、notify、identity、subscription、support、giftcard）
// 只依赖这里的接口，由 plugin 模块实现、装配时注入（internal/app 的 wire.go）。
package pluginevents

import "context"

// Emitter 由 plugin 模块实现。Emit 在调用方的事务里（ctx 带事务时）写一条待投递记录，事件与业务变更同生共死；
// payload 是插件收到的 JSON 正文。dedupeKey 相同的事件只投递一次。
type Emitter interface {
	Emit(ctx context.Context, event, dedupeKey string, payload any) error
}

// Emit 经 e 发一个插件事件；e 为 nil（没注入事件口，如单测）时什么都不做。
func Emit(ctx context.Context, e Emitter, event, dedupeKey string, payload map[string]any) error {
	if e == nil {
		return nil
	}
	return e.Emit(ctx, event, dedupeKey, payload)
}
