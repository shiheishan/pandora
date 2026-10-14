package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
)

// UsePluginEvents 注入插件事件口（plugin 模块实现，见 internal/pkg/pluginevents）。
func (s *Service) UsePluginEvents(p pluginevents.Emitter) { s.events = p }

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
	return pluginevents.Emit(ctx, s.events, it.Plugin.Event, it.Key, pl)
}
