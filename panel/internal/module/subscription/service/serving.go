package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServingSubscription 是一份订阅侧可服务的订阅：生效中（status=active 且未到期），流量没耗尽（最近一期的流量配额
// 不限或还有余量，或挂在这一份上的流量包还有余量，与拉取的用量口径相同）。
// 账号状态、套餐绑池、池的用户组限定不在这里判（属主分别是 identity、catalog、node）。
type ServingSubscription struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	PlanVersionID uuid.UUID
	// NodeUID 是节点协议里的整数用户号
	NodeUID int64
	// ProxyUUID 是协议层的用户标识
	ProxyUUID uuid.UUID
	// DeviceLimit 是管理员在这一份上的设备数覆盖：nil = 沿用套餐版本，0 = 不限
	DeviceLimit *int
	PeriodEnd   time.Time
}

// ServingSubscriptions 一次取全部订阅侧可服务的订阅，按 NodeUID 升序，永不为 nil；一条 SQL。节点名单（node 模块）用。
func (s *Service) ServingSubscriptions(ctx context.Context) ([]ServingSubscription, error) {
	rows, err := s.repo.ServingSubscriptions(ctx, s.clock())
	if err != nil {
		return nil, subErr(err)
	}
	out := make([]ServingSubscription, len(rows))
	for i, r := range rows {
		out[i] = ServingSubscription{
			ID:            r.ID,
			UserID:        r.UserID,
			PlanVersionID: r.PlanVersionID,
			NodeUID:       r.NodeUID,
			ProxyUUID:     r.ProxyUUID,
			DeviceLimit:   r.DeviceLimit,
			PeriodEnd:     r.PeriodEnd,
		}
	}
	return out, nil
}
