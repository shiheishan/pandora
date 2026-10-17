package service

import "github.com/aegispanel/aegis/internal/pkg/pluginevents"

// UsePluginEvents 注入插件事件口（plugin 模块实现，见 internal/pkg/pluginevents）。
func (s *Service) UsePluginEvents(p pluginevents.Emitter) { s.events = p }
