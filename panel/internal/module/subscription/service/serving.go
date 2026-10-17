package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ServingSubscription 是一份订阅侧可服务的订阅：生效中（status=active 且未到期），流量没耗尽（本期窗口里
// 没有任何一行限量流量配额用满，剩余含本期送的量；一次性套餐流量用完后订阅仍 active，但不进名单，直到期末；
// 与扣量同一口径，窗口见 repo.TrafficQuotaGrace）。
// 账号状态、套餐绑池、池的用户组限定不在这里判（属主分别是 identity、catalog、node）。
type ServingSubscription struct {
	ID     uuid.UUID
	UserID uuid.UUID
	// PlanID 是这一份的套餐：节点按套餐取线路（catalog.PlanPools）
	PlanID        uuid.UUID
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
			PlanID:        r.PlanID,
			PlanVersionID: r.PlanVersionID,
			NodeUID:       r.NodeUID,
			ProxyUUID:     r.ProxyUUID,
			DeviceLimit:   r.DeviceLimit,
			PeriodEnd:     r.PeriodEnd,
		}
	}
	return out, nil
}
