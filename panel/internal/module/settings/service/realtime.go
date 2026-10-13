package service

// 降级开关的实时推送：切换后推 switches.changed，只推管理端（开关是运维信息，不广播到门户公共频道）。
// 与 switches.go 的进程缓存失效（SwitchChannel）是两回事：那条给各进程清缓存，这条给后台页面刷新。

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// UseRealtime 接上实时推送（启动装配时调用，不并发安全）。没接上时不推。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// switchChanged 在当前事务提交后推一条开关变更。
func (s *Service) switchChanged(ctx context.Context, code string, enabled bool) {
	s.rt.Notify(ctx, realtime.TopicSwitches, map[string]any{"code": code, "enabled": enabled}, realtime.ChannelAdmin())
}
