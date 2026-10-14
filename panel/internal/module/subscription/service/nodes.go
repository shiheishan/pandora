package service

import (
	"context"

	"github.com/google/uuid"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
)

// NodeQuery 是「这份订阅能拿哪些线路」的查询条件。
type NodeQuery struct {
	SubscriptionID, UserID, PlanVersionID uuid.UUID
	// UserGroupID 是订阅主人所在的用户组（节点池可以限定用户组）；没有分组为 nil
	UserGroupID *uuid.UUID
}

// NodeSource 由 node 模块实现：返回可下发的线路，协议配置已是内核字段名，已按新鲜度分层排好序。
// 实现只读，不回调本模块的写方法。
type NodeSource interface {
	DeliverableNodes(ctx context.Context, q NodeQuery) ([]render.Node, error)
}

// UseNodeSource 只在启动装配时调用（不并发安全）。没装配时拉取与线路预览回 ErrDeliveryUnavailable（503）。
func (s *Service) UseNodeSource(n NodeSource) { s.nodes = n }

// DeliverySettings 是订阅分发要的设置读取面；*settingssvc.Service 直接满足。
type DeliverySettings interface {
	SubscriptionDelivery(ctx context.Context) (settingssvc.SubscriptionDelivery, error)
}

// Owners 是拉取时看订阅主人账号状态的读取面；*identitysvc.Service 直接满足。
type Owners interface {
	Get(ctx context.Context, id uuid.UUID) (identitysvc.User, error)
}

// Hasher 对拉取日志里的 IP、UA 做 HMAC；*crypto.Keys 直接满足。
type Hasher interface {
	HMAC(info string, data []byte) []byte
}

// FetchLimiter 是拉取与换链接用的 Redis 限流；*ratelimit.Limiter 直接满足。
type FetchLimiter interface {
	Allow(ctx context.Context, rule ratelimit.Rule, key string) error
}

// Delivery 是订阅分发（拉取、链接列表、换链接）要的依赖。只有 serve 装配；worker 只跑到期、配额滚动与保留期清理，用不到。
type Delivery struct {
	Settings DeliverySettings
	Owners   Owners
	Hasher   Hasher
	Limiter  FetchLimiter
	// PublicBaseURL 是门户与订阅链接的公开地址（配置 public_base_url），不带结尾斜杠也可以
	PublicBaseURL string
	// Render 是渲染的可配项（配置 subscription.singbox），启动时定一次
	Render render.Options
}

// UseDelivery 只在启动装配时调用（不并发安全）。没装配时拉取、链接列表、换链接回 ErrDeliveryUnavailable。
func (s *Service) UseDelivery(d Delivery) {
	dd := d
	s.delivery = &dd
}

// needDelivery 取已装配的分发依赖；没装配回 ErrDeliveryUnavailable。
func (s *Service) needDelivery() (*Delivery, error) {
	if s.delivery == nil || s.delivery.Settings == nil || s.delivery.Owners == nil ||
		s.delivery.Hasher == nil || s.delivery.Limiter == nil {
		return nil, ErrDeliveryUnavailable
	}
	return s.delivery, nil
}
