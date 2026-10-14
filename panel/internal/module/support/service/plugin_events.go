package service

import "context"

// PluginEvents 是插件钩子的出站事件口，由 plugin 模块实现、装配时注入。
// Emit 在调用方的事务里（ctx 带事务时）写一条待投递记录，事件与业务变更同生共死；
// payload 是插件收到的 JSON 正文。dedupeKey 相同的事件只投递一次。
type PluginEvents interface {
	Emit(ctx context.Context, event, dedupeKey string, payload any) error
}

// UsePluginEvents 注入插件事件口。
func (s *Service) UsePluginEvents(p PluginEvents) { s.events = p }

// PluginEventsWired 报告事件口是否已注入（装配完成后检查）。
func (s *Service) PluginEventsWired() bool { return s.events != nil }

// emitPlugin 发一个插件事件；没注入事件口时什么都不做。
func (s *Service) emitPlugin(ctx context.Context, event, dedupeKey string, payload map[string]any) error {
	if s.events == nil {
		return nil
	}
	return s.events.Emit(ctx, event, dedupeKey, payload)
}
