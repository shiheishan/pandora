package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// 数据巡检（report:integrity 每 30 分钟调一次，只读）：到期扫描 subscription:expire 每分钟一轮，
// 落后超过 IntegrityGrace 就说明任务卡了（worker 没跑、批次一直失败）或有行被长期锁着。
// 停发不靠它（名单与拉取按 current_period_end 现算），但状态、关窗、吊销旧链接、到期事件都停在半截。

// IntegrityGrace 是到期扫描允许落后的时长：十个任务周期，盖过 SKIP LOCKED 跳过的行与单轮上限（50 批 × 200 份）。
const IntegrityGrace = 10 * time.Minute

// CheckExpireOverdue 是「到期未停」：current_period_end 早于 now − IntegrityGrace 仍是 active 的订阅。样例是订阅 id。
func (s *Service) CheckExpireOverdue(ctx context.Context, now time.Time) (integrity.Result, error) {
	rows, err := s.repo.ExpireOverdueRows(ctx, now.Add(-IntegrityGrace))
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}

// CheckCloseOverdue 是「过期满 30 天未关窗」：已过期、renewal_closed_at 为空、current_period_end 早于
// now − RenewalWindow − IntegrityGrace 的订阅（旧链接本该吊销）。样例是订阅 id。
func (s *Service) CheckCloseOverdue(ctx context.Context, now time.Time) (integrity.Result, error) {
	rows, err := s.repo.CloseOverdueRows(ctx, now.Add(-RenewalWindow-IntegrityGrace))
	if err != nil {
		return integrity.Result{}, err
	}
	return integrity.FromRows(rows), nil
}
