package service

// 到期扫描（subscription:expire，每分钟）。停发不靠它：节点名单与拉取按 current_period_end 现算。
//
//  1. 到期：到期的 active 改成 expired（带 WHERE status = 'active'），写 expired 事件与审计，提交后通知名单。
//     只改状态、不动凭据：凭据一动，续费时链接就救不回来了。
//  2. 关窗：过期满 RenewalWindow 的写 renewal_closed_at、吊销有效凭据（旧链接作废，之后只能新购），
//     写 renewal_closed 事件与审计。
//
// 两步都分批、每批一个短事务、按到期时间 SKIP LOCKED：正被续费、延期锁着的这一轮跳过，下一轮再看。
// 连跑两轮，第二轮零改动。到期的插件事件与到期通知归别的模块，这里不做。

import (
	"context"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/google/uuid"
)

// 到期扫描的分批参数：每批一个短事务、至多 expireBatchSize 份，一轮至多 expireMaxBatches 批。
const (
	expireBatchSize  = 200
	expireMaxBatches = 50
)

// 到期扫描写的审计动作（名字与插件事件类型同口径）。
const (
	ActionExpired       = "subscription.expired"
	ActionRenewalClosed = "subscription.renewal_closed"
)

// RevokedRenewalClosed 是关窗时吊销凭据的原因。
const RevokedRenewalClosed = "renewal_window_closed"

// ExpireStats 是一轮到期扫描的结果。
type ExpireStats struct {
	Expired            int
	Closed             int
	RevokedCredentials int
}

// ExpireDue 跑一轮到期扫描（见文件头）。
func (s *Service) ExpireDue(ctx context.Context) (ExpireStats, error) {
	var st ExpireStats
	for batch := 0; batch < expireMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		selected, expired, err := s.expireBatch(ctx)
		if err != nil {
			return st, fmt.Errorf("到期扫描: %w", err)
		}
		st.Expired += expired
		if selected < expireBatchSize {
			break
		}
	}
	for batch := 0; batch < expireMaxBatches; batch++ {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		selected, closed, revoked, err := s.closeBatch(ctx)
		if err != nil {
			return st, fmt.Errorf("关闭续费窗口: %w", err)
		}
		st.Closed += closed
		st.RevokedCredentials += revoked
		if selected < expireBatchSize {
			break
		}
	}
	return st, nil
}

// expireBatch 在一个事务里锁一批到期的 active 订阅并改成 expired；返回选中数与改到的数（出错时整批回滚，计数作废）。
func (s *Service) expireBatch(ctx context.Context) (selected, expired int, err error) {
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		now := s.clock()
		subs, err := s.repo.LockDueExpiries(ctx, now, expireBatchSize)
		if err != nil {
			return subErr(err)
		}
		selected = len(subs)
		if selected == 0 {
			return nil
		}
		if !CanTransition(StatusActive, StatusExpired) {
			return errs.Wrap(errs.Internal, fmt.Errorf("订阅状态转换表缺少 active→expired"))
		}
		ids := make([]uuid.UUID, len(subs))
		for i, m := range subs {
			ids[i] = m.ID
		}
		changed, err := s.repo.TransitionStatus(ctx, ids, StatusActive, StatusExpired, now)
		if err != nil {
			return subErr(err)
		}
		changedSet := make(map[uuid.UUID]struct{}, len(changed))
		for _, id := range changed {
			changedSet[id] = struct{}{}
		}
		for _, m := range subs {
			if _, ok := changedSet[m.ID]; !ok {
				continue
			}
			s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
			from := StatusActive
			if err := s.writeEvent(ctx, m.ID, EventExpired, &from, StatusExpired, Actor{Kind: ActorScheduler}, nil, map[string]any{"period_end": m.CurrentPeriodEnd}); err != nil {
				return err
			}
			if err := s.writeAudit(ctx, ActionExpired, m.ID, map[string]any{"user_id": m.UserID.String(), "period_end": m.CurrentPeriodEnd}); err != nil {
				return err
			}
		}
		if err := s.emitExpired(ctx, subs, changedSet); err != nil {
			return err
		}
		s.notifyRoster(ctx, changed...)
		expired = len(changed)
		return nil
	})
	return selected, expired, err
}

// closeBatch 在一个事务里锁一批过期满窗口的订阅，关窗并吊销有效凭据；返回选中、关窗、吊销数。
func (s *Service) closeBatch(ctx context.Context) (selected, closed, revoked int, err error) {
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		now := s.clock()
		subs, err := s.repo.LockDueRenewalCloses(ctx, now.Add(-RenewalWindow), expireBatchSize)
		if err != nil {
			return subErr(err)
		}
		selected = len(subs)
		if selected == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(subs))
		for i, m := range subs {
			ids[i] = m.ID
		}
		closedIDs, err := s.repo.CloseRenewalWindows(ctx, ids, now)
		if err != nil {
			return subErr(err)
		}
		revokedCreds, err := s.repo.RevokeCredentialsOnClose(ctx, closedIDs, now, RevokedRenewalClosed)
		if err != nil {
			return subErr(err)
		}
		counts := make(map[uuid.UUID]int, len(revokedCreds))
		for _, c := range revokedCreds {
			counts[c.SubscriptionID]++
			s.rtChanged(ctx, tableCredentials, realtime.OpUpdate, c.ID, c.UserID)
		}
		closedSet := make(map[uuid.UUID]struct{}, len(closedIDs))
		for _, id := range closedIDs {
			closedSet[id] = struct{}{}
		}
		windowDays := int(RenewalWindow / (24 * time.Hour))
		for _, m := range subs {
			if _, ok := closedSet[m.ID]; !ok {
				continue
			}
			s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
			from := StatusExpired
			if err := s.writeEvent(ctx, m.ID, EventRenewalClosed, &from, StatusExpired, Actor{Kind: ActorScheduler}, nil, map[string]any{
				"window_days":         windowDays,
				"revoked_credentials": counts[m.ID],
			}); err != nil {
				return err
			}
			if err := s.writeAudit(ctx, ActionRenewalClosed, m.ID, map[string]any{
				"user_id":             m.UserID.String(),
				"period_end":          m.CurrentPeriodEnd,
				"revoked_credentials": counts[m.ID],
			}); err != nil {
				return err
			}
			revoked += counts[m.ID]
		}
		// 关窗不调 notifyRoster：已过期的订阅本来就不在名单里。
		closed = len(closedIDs)
		return nil
	})
	return selected, closed, revoked, err
}
