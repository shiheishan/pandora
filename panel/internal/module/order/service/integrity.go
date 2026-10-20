package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 数据巡检（report:integrity 每 30 分钟调一次，只读）：库里约束拦不住、出现就说明有 bug 或半截状态的订单。

// CheckPaidUnfulfilled 是「已付未落地」：停在 paid 的订单。结算事务里 markPaid 之后紧接着 fulfil（开通、续期、叠加、换套餐、
// 流量重置、充值直接转 fulfilled），同一事务提交，所以提交后的 paid 都说明履约那一步被绕过或写了一半。样例是订单 id。
func (s *Service) CheckPaidUnfulfilled(ctx context.Context) (integrity.Result, error) {
	rows, err := s.repo.PaidUnfulfilledRows(ctx)
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}

// CheckReservations 是「预留与订单脱节」：held 预留挂在已不是待付的订单上（券与余额冻结没放出来），
// 或待付订单没有 held 预留。建单、结算（captureReservation）、取消与超时（releaseOrder）都在同一事务里同时改两边。样例是订单 id。
func (s *Service) CheckReservations(ctx context.Context) (integrity.Result, error) {
	rows, err := s.repo.ReservationMismatchRows(ctx)
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}

// UnsettledAmong 返回 ids 里状态不是 paid / fulfilled（即没收到钱）的订单 id，不存在的 id 不返回。
// 给 report 的跨模块巡检用：payment 的收款、commission 的佣金条目各自给出订单 id，在这里核对订单状态。
func (s *Service) UnsettledAmong(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	return s.repo.UnsettledAmong(ctx, ids)
}
