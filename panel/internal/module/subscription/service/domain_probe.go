// 门户探测订阅域名：用户在浏览器里定期探测本人所在组的订阅域名可用性并上报。
// 浏览器上报可伪造，所以只收本组域名、按账号限流（handler），且分数下限大于 0，任何域名都不会被压成 0。

package service

import (
	"context"
	"sort"
	"strings"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// ProbeResult 是门户浏览器对一个订阅域名的探测结果。
type ProbeResult struct {
	BaseURL string
	OK      bool
}

// MyProbeDomains 返回当前门户用户所在组的全部订阅域名（按组里顺序），供门户探测；没有组返回空切片（不为 nil）。
func (s *Service) MyProbeDomains(ctx context.Context) ([]string, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	d, err := s.needDelivery()
	if err != nil {
		return nil, err
	}
	now := s.clock()
	ex, err := s.extras.get(ctx, d.Extras, s.repo, now)
	if err != nil {
		return nil, err
	}
	g, err := s.domainGroupFor(ctx, ex, p.UserID)
	if err != nil {
		return nil, err
	}
	if g == nil {
		return []string{}, nil
	}
	urls := baseURLsOf(g)
	if urls == nil {
		return []string{}, nil
	}
	return urls, nil
}

// ReportDomainProbes 收门户浏览器的探测结果：只认本人所在组里登记过的域名（先 NormalizeBaseURL，规范化失败或不在组里的静默忽略，
// 同一域名只算一次），成功 scoreProbeSuccess、失败 scoreProbeFailure，一次 Apply 写 Redis。
// Redis 写失败只打 Warn、不报错（分数只是调权重的参考，不让门户看到错误）。
func (s *Service) ReportDomainProbes(ctx context.Context, results []ProbeResult) error {
	p, err := principal(ctx)
	if err != nil {
		return err
	}
	d, err := s.needDelivery()
	if err != nil {
		return err
	}
	now := s.clock()
	ex, err := s.extras.get(ctx, d.Extras, s.repo, now)
	if err != nil {
		return err
	}
	g, err := s.domainGroupFor(ctx, ex, p.UserID)
	if err != nil {
		return err
	}
	if g == nil || len(g.Domains) == 0 || len(results) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(g.Domains))
	for _, dom := range g.Domains {
		allowed[dom.BaseURL] = struct{}{}
	}
	deltas := make(map[string]float64)
	for _, r := range results {
		norm, err := NormalizeBaseURL(r.BaseURL)
		if err != nil {
			continue
		}
		if _, ok := allowed[norm]; !ok {
			continue
		}
		if _, seen := deltas[norm]; seen {
			continue
		}
		if r.OK {
			deltas[norm] = scoreProbeSuccess
		} else {
			deltas[norm] = scoreProbeFailure
		}
	}
	if len(deltas) > 0 {
		if err := s.scores.Apply(ctx, deltas, now); err != nil {
			plog.From(ctx).Warn("订阅域名探测分数写入失败", zap.Error(err))
		}
	}
	return nil
}

// PortalOrigin 是门户的源（public_base_url 去末尾斜杠），给公开探测接口的 CORS 头用；分发没装配时为空串。
func (s *Service) PortalOrigin() string {
	d, err := s.needDelivery()
	if err != nil {
		return ""
	}
	return strings.TrimRight(d.PublicBaseURL, "/")
}

// ProbeOrigins 是全部组里登记过的订阅域名（去重、字典序），给门户入口页 CSP 的 connect-src 用（门户要能跨源探测它们）。
// 走拉取快照（30 秒）；分发没装配或读失败时返回 nil（读失败打 Warn）。地址都经 NormalizeBaseURL 规范化过，
// 只含 https://、小写字母数字点短横线与可选端口，可以直接放进 CSP。
func (s *Service) ProbeOrigins(ctx context.Context) []string {
	d, err := s.needDelivery()
	if err != nil {
		return nil
	}
	now := s.clock()
	ex, err := s.extras.get(ctx, d.Extras, s.repo, now)
	if err != nil {
		plog.From(ctx).Warn("读取订阅域名快照失败", zap.Error(err))
		return nil
	}
	if len(ex.domains.known) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(ex.domains.known))
	for k := range ex.domains.known {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
