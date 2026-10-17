package service

import (
	"context"
	"time"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// linkSourceWindow 是链接列表里「近期不同来源数」的统计窗口（门户显示为近 24 小时）。
const linkSourceWindow = 24 * time.Hour

// MyLinks 是门户「订阅链接」：本人每份续费窗口没关的订阅当前的有效链接（过期 30 天内的也列，Expired 为真、只读），按凭据创建时间倒序；永不为 nil。
func (s *Service) MyLinks(ctx context.Context) ([]Link, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.needDelivery()
	if err != nil {
		return nil, err
	}
	prefix, err := s.pathPrefix(ctx, d)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.LinksByUser(ctx, p.UserID, s.clock().Add(-linkSourceWindow))
	if err != nil {
		return nil, subErr(err)
	}
	now := s.clock()
	base, err := s.linkBase(ctx, d, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]Link, 0, len(rows))
	for _, row := range rows {
		plain, err := s.sealer.Open(row.TokenEncrypted, CredentialAAD(row.SubscriptionID))
		if err != nil {
			plog.From(ctx).Warn("订阅链接解封失败，跳过", zap.String("subscription_id", row.SubscriptionID.String()))
			continue
		}
		out = append(out, Link{
			SubscriptionID:     row.SubscriptionID,
			URL:                linkURL(base, prefix, string(plain)),
			FetchCount:         row.FetchCount,
			LastFetchedAt:      row.LastFetchedAt,
			DistinctSources24h: row.DistinctSources,
			Expired:            StateAt(row.Status, row.PeriodEnd, row.RenewalClosedAt, now) != StateLive,
		})
	}
	return out, nil
}
