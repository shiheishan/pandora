package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// maxNoteRunes 是重置备注的最多字数（与迁移的 CHECK 一致）。
const maxNoteRunes = 500

// ResetTraffic 把一份生效中订阅的流量已用量清零（全部 traffic.bytes 配额行），只清 consumed、不动上限，
// 挂在这一份上的流量包不碰；每行写一条重置日志。返回清掉的已用量合计。
// 不生效回 ErrNotLive；没有流量配额行回 ErrNoTrafficQuota。不写审计（后台入口 AdminResetTraffic 自己写）。
// in.At 非零时按它判生效。
// 在 ctx 的事务里执行，自己锁订阅行；提交后通知名单变化。
func (s *Service) ResetTraffic(ctx context.Context, in TrafficReset) (int64, error) {
	switch in.Reason {
	case ResetManual:
		if in.ActorID == nil || *in.ActorID == uuid.Nil {
			return 0, errs.Wrap(errs.Internal, errMissing("ActorID"))
		}
	case ResetGiftCard:
	case ResetOrder:
		if in.UserID == nil || *in.UserID == uuid.Nil {
			return 0, errs.Wrap(errs.Internal, errMissing("UserID"))
		}
	default:
		return 0, errs.Wrap(errs.Internal, errMissing("Reason"))
	}
	note := ""
	if in.Note != "" {
		n, err := cleanText(in.Note, 1, maxNoteRunes, "note", "备注最多 500 个字，不能含特殊字符")
		if err != nil {
			return 0, err
		}
		note = n
	}
	var freed int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, in.SubscriptionID, in.UserID)
		if err != nil {
			return err
		}
		at := in.At
		if at.IsZero() {
			at = s.clock()
		}
		if StateAt(m.Status, m.CurrentPeriodEnd, m.RenewalClosedAt, at) != StateLive {
			return ErrNotLive
		}
		rows, err := s.repo.QuotasBySubscriptions(ctx, []uuid.UUID{m.ID}, true)
		if err != nil {
			return subErr(err)
		}
		var traffic []model.QuotaBalance
		for _, q := range rows {
			if q.Metric != MetricTraffic {
				continue
			}
			traffic = append(traffic, q)
			freed += q.Consumed
			if err := s.repo.UpdateQuota(ctx, q.ID, map[string]any{"consumed": 0}); err != nil {
				return subErr(err)
			}
		}
		if len(traffic) == 0 {
			return ErrNoTrafficQuota
		}
		if err := s.logResets(ctx, m, traffic, in.Reason, in.ActorID, note); err != nil {
			return err
		}
		s.notifyRoster(ctx, m.ID)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return freed, nil
}
