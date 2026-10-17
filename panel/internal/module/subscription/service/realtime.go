package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 实时事件里的表名，与前端 EventsCapsule 认的一致；两张表都走 subscriptions.changed。
// 扣量、配额余额、按日用量、拉取日志与最近拉取时间是高频写，不推。
const (
	tableSubscriptions = "subscriptions"
	tableCredentials   = "subscription_credentials"
)

// UseRealtime 注入实时推送；只在启动装配时调用（不并发安全）。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// rtChanged 在调用方事务里登记「table 里 id 这一行变了」，提交后推给订阅主人 owner 与管理端；回滚不发。
// 必须在写库成功之后、事务函数返回之前调用。
func (s *Service) rtChanged(ctx context.Context, table, op string, id, owner uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicSubscriptions, table, op, id, &owner)
}
