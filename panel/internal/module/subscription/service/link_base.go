package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

// linkBase 取生成订阅链接用的地址前缀：按用户组找组 → 组内按有效权重随机；
// 分数按请求者 IP 的地区取最细一级、逐级回落到全国；Telegram 等代发的请求 IP 是服务器的，自然只用全国分。
// Redis 读不到分数时按基础权重；分组走快照，后台改动最多 30 秒生效。
// 没有组或组内没有域名用面板对外地址 fallback。
func (s *Service) linkBase(ctx context.Context, d *Delivery, userID uuid.UUID) (string, error) {
	now := s.clock()
	ex, err := s.extras.get(ctx, d.Extras, extrasSource{s}, now)
	if err != nil {
		return "", err
	}
	g, err := s.domainGroupFor(ctx, ex, userID)
	if err != nil {
		return "", err
	}
	if g == nil || len(g.Domains) == 0 {
		return d.PublicBaseURL, nil
	}
	scores, _ := s.scores.Read(ctx, baseURLsOf(g), s.regionOf(reqmeta.From(ctx).IP), now)
	return pickWeighted(g.Domains, scores), nil
}
