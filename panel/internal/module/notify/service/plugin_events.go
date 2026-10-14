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

// scanPlugin 是扫描条目要同时发的插件事件；去重键就是条目的 Key。
type scanPlugin struct {
	Event   string
	Payload map[string]any // 不含 plan：套餐名在写入时取条目已解析好的 Vars["plan"]
}

// emitScanPlugin 发扫描条目附带的插件事件（没有就什么都不做）：复制 payload，补上 "plan" = it.Vars["plan"]，
// 以 it.Key 为去重键。
func (s *Service) emitScanPlugin(ctx context.Context, it scanItem) error {
	if it.Plugin == nil {
		return nil
	}
	pl := make(map[string]any, len(it.Plugin.Payload)+1)
	for k, v := range it.Plugin.Payload {
		pl[k] = v
	}
	pl["plan"] = it.Vars["plan"]
	return s.emitPlugin(ctx, it.Plugin.Event, it.Key, pl)
}
