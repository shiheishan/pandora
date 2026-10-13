package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// 后台入口的文本限制。
const (
	minReasonRunes = 5
	maxReasonRunes = 500
	maxDeviceLimit = 1000
	// maxAdminGrantBytes 是后台一次加流量的上限（10 TiB），只管后台入口；通用发放入口的上限见 maxPackBytes。
	maxAdminGrantBytes int64 = 10 << 40
)

// AdminExtend 是后台给一份订阅加时长（可救回过期 30 天内的），写审计。
func (s *Service) AdminExtend(ctx context.Context, subID uuid.UUID, days int, reason string) (AdminExtendResult, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return AdminExtendResult{}, err
	}
	reason, err = cleanText(reason, minReasonRunes, maxReasonRunes, "reason", "请写清原因，5 到 500 个字")
	if err != nil {
		return AdminExtendResult{}, err
	}
	var out AdminExtendResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		res, err := s.Extend(ctx, Extension{
			SubscriptionID: subID,
			Days:           days,
			Actor:          actorFrom(p),
			Source:         ExtendSourceAdmin,
			Reason:         reason,
		})
		if err != nil {
			return err
		}
		emails, err := s.users.Emails(ctx, []uuid.UUID{res.UserID})
		if err != nil {
			return err
		}
		out = AdminExtendResult{
			SubscriptionID: res.SubscriptionID,
			UserEmail:      emails[res.UserID],
			Days:           days,
			PreviousEnd:    res.PreviousEnd,
			PeriodEnd:      res.PeriodEnd,
		}
		return s.writeAudit(ctx, ActionExtended, subID, map[string]any{
			"user_id":      res.UserID,
			"days":         days,
			"reason":       reason,
			"previous_end": res.PreviousEnd,
			"period_end":   res.PeriodEnd,
			"rescued":      res.Rescued,
		})
	})
	return out, err
}

// AdminGrantTraffic 是后台给一份生效中的订阅加一笔流量包（来源 admin，source_id 为新生成的 id），写审计。
func (s *Service) AdminGrantTraffic(ctx context.Context, subID uuid.UUID, bytes int64, reason string) (AdminTrafficGrantResult, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return AdminTrafficGrantResult{}, err
	}
	if bytes < 1 || bytes > maxAdminGrantBytes {
		return AdminTrafficGrantResult{}, errs.Invalid(map[string]string{"bytes": "流量须在 1 字节到 10 TiB 之间"})
	}
	reason, err := cleanText(reason, minReasonRunes, maxReasonRunes, "reason", "请写清原因，5 到 500 个字")
	if err != nil {
		return AdminTrafficGrantResult{}, err
	}
	sourceID, err := uuid.NewV7()
	if err != nil {
		return AdminTrafficGrantResult{}, errs.Wrap(errs.Internal, err)
	}
	var out AdminTrafficGrantResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, subID, nil)
		if err != nil {
			return err
		}
		pack, err := s.GrantTrafficPack(ctx, PackGrant{
			UserID:         m.UserID,
			SubscriptionID: &m.ID,
			Source:         PackSourceAdmin,
			SourceID:       sourceID,
			Bytes:          bytes,
		})
		if err != nil {
			return err
		}
		left, err := s.repo.PackRemainingBySubscriptions(ctx, []uuid.UUID{m.ID})
		if err != nil {
			return subErr(err)
		}
		emails, err := s.users.Emails(ctx, []uuid.UUID{m.UserID})
		if err != nil {
			return err
		}
		out = AdminTrafficGrantResult{
			SubscriptionID:      m.ID,
			UserID:              m.UserID,
			UserEmail:           emails[m.UserID],
			GrantID:             pack.ID,
			GrantedBytes:        pack.GrantedBytes,
			RemainingBytesTotal: left[m.ID],
		}
		return s.writeAudit(ctx, ActionTrafficGranted, m.ID, map[string]any{
			"user_id":  m.UserID,
			"grant_id": pack.ID,
			"bytes":    bytes,
			"reason":   reason,
		})
	})
	return out, err
}

// AdminResetTraffic 是后台把一份生效中订阅的流量已用量清零（要写清原因），写审计；返回清掉的已用量。
func (s *Service) AdminResetTraffic(ctx context.Context, subID uuid.UUID, note string) (int64, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return 0, err
	}
	note, err = cleanText(note, minReasonRunes, maxReasonRunes, "note", "请写清重置原因，5 到 500 个字")
	if err != nil {
		return 0, err
	}
	actorID := p.UserID
	var freed int64
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, subID, nil)
		if err != nil {
			return err
		}
		n, err := s.ResetTraffic(ctx, TrafficReset{
			SubscriptionID: subID,
			Reason:         ResetManual,
			ActorID:        &actorID,
			Note:           note,
		})
		if err != nil {
			return err
		}
		freed = n
		return s.writeAudit(ctx, ActionManualReset, subID, map[string]any{
			"user_id":     m.UserID,
			"freed_bytes": n,
			"note":        note,
		})
	})
	return freed, err
}

// AdminSetDeviceLimit 是后台改一份订阅的设备数覆盖：nil 沿用套餐版本，0 不限，最多 1000；写审计。
func (s *Service) AdminSetDeviceLimit(ctx context.Context, subID uuid.UUID, limit *int) error {
	if _, err := adminPrincipal(ctx); err != nil {
		return err
	}
	if limit != nil && (*limit < 0 || *limit > maxDeviceLimit) {
		return errs.Invalid(map[string]string{"limit": "设备数须在 0 到 1000 之间，留空沿用套餐"})
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		m, err := s.lockOwned(ctx, subID, nil)
		if err != nil {
			return err
		}
		ok, err := s.repo.UpdateSubscription(ctx, m.ID, m.Status, map[string]any{"device_limit": limit})
		if err != nil {
			return subErr(err)
		}
		if !ok {
			return ErrStateChanged
		}
		s.rtChanged(ctx, tableSubscriptions, realtime.OpUpdate, m.ID, m.UserID)
		s.notifyRoster(ctx, m.ID)
		return s.writeAudit(ctx, ActionDeviceLimitChanged, m.ID, map[string]any{
			"user_id": m.UserID,
			"before":  m.DeviceLimit,
			"after":   limit,
		})
	})
}

// SubscriptionsByIDs 按 id 批量读订阅（不加锁，一条 SQL）；不存在的 id 不在结果里。node 的在线设备总览用。
func (s *Service) SubscriptionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]Subscription, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]Subscription{}, nil
	}
	rows, err := s.repo.SubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil, subErr(err)
	}
	out := make(map[uuid.UUID]Subscription, len(rows))
	for _, m := range rows {
		out[m.ID] = toSubscription(m)
	}
	return out, nil
}
