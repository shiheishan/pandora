package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// MaxCommissionLevels 是往上找邀请人的最多级数。
const MaxCommissionLevels = 3

// beneficiary 是一单佣金的一位受益人：第几级、分到佣金总额的百分比。
type beneficiary struct {
	UserID       uuid.UUID
	Level        int
	SharePercent int
}

// referrerFunc 取某人的邀请人，没有时为 nil。
type referrerFunc func(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)

// excludedFunc 判断某人是否不拿佣金（计提时是 notEarning：已注销或单独比例为 0）。
type excludedFunc func(ctx context.Context, userID uuid.UUID) (bool, error)

// beneficiaries 列出这一单的受益人。没开多级分销：只有直接邀请人，第 1 级、100%（他被排除时没有受益人）。
// 开了：从直接邀请人起往上最多 3 级，第 N 级分 dist.Levels[N-1]%；份额为 0 的级别、被排除的人（已注销或单独比例 0）都跳过但继续往上找，
// 跳过的人仍占他那一级；遇到已出现过的人（含买家本人）就停，防环。
func beneficiaries(ctx context.Context, buyer, direct uuid.UUID, dist settingssvc.CommissionDistribution,
	next referrerFunc, excluded excludedFunc) ([]beneficiary, error) {
	if !dist.Enabled {
		out, err := excluded(ctx, direct)
		if err != nil || out {
			return nil, err
		}
		return []beneficiary{{UserID: direct, Level: 1, SharePercent: 100}}, nil
	}
	seen := map[uuid.UUID]bool{buyer: true}
	cur := direct
	var out []beneficiary
	for lvl := 1; lvl <= MaxCommissionLevels; lvl++ {
		if seen[cur] {
			break
		}
		seen[cur] = true
		if share := dist.Levels[lvl-1]; share > 0 {
			skip, err := excluded(ctx, cur)
			if err != nil {
				return nil, err
			}
			if !skip {
				out = append(out, beneficiary{UserID: cur, Level: lvl, SharePercent: share})
			}
		}
		if lvl == MaxCommissionLevels {
			break
		}
		up, err := next(ctx, cur)
		if err != nil {
			return nil, err
		}
		if up == nil {
			break
		}
		cur = *up
	}
	return out, nil
}

// LevelAmount 是某一级的佣金：先按比例算出这一单的佣金总额（向零截断到分），再按这一级的份额向零截断到分。
// 与库里 commission_entries_formula 一致；份额 100 时等于 Amount(base, rateBP)。
func LevelAmount(base decimal.Decimal, rateBP int64, sharePercent int) decimal.Decimal {
	return money.ApplyBP(Amount(base, rateBP), int64(sharePercent)*100)
}

// DirectRateBP 是直接邀请人实际拿到的万分比（门户横幅用）：没开多级 = 比例 × 100；开了 = 比例 × 第一级份额。
func DirectRateBP(ratePercent int, dist settingssvc.CommissionDistribution) int64 {
	if !dist.Enabled {
		return RateBP(ratePercent)
	}
	return int64(ratePercent) * int64(dist.Levels[0])
}

// excludedByOverride 判断某人是否被单独设置排除：单独比例设成 0 表示这个人不拿佣金，不论他在第几级（用户 10-14 定）。
func (s *Service) excludedByOverride(ctx context.Context, userID uuid.UUID) (bool, error) {
	ov, ok, err := s.repo.OverrideOf(ctx, userID)
	if err != nil || !ok {
		return false, err
	}
	return ov.RatePercent != nil && *ov.RatePercent == 0, nil
}

// notEarning 是计提时「这个人不拿佣金」的判断：账号已注销（identity 的无锁读 AccountDeleted），或被单独设置排除（单独比例 0）。
// 两种都等同比例 0：跳过、仍占他那一级，下一级不往上挪。只在 OnOrderPaid 里 needRefs 之后用。
func (s *Service) notEarning(ctx context.Context, userID uuid.UUID) (bool, error) {
	gone, err := s.refs.AccountDeleted(ctx, userID)
	if err != nil || gone {
		return gone, err
	}
	return s.excludedByOverride(ctx, userID)
}

// orderTerms 返回这一单佣金总额用的比例与计佣范围：直接邀请人的单独设置决定整单（见 termsFor）；
// 直接邀请人不拿佣金（已注销或单独比例 0，见 notEarning）时他的单独设置整条不作数，按全局算，再由 beneficiaries 跳过他、只给上级分。
func (s *Service) orderTerms(ctx context.Context, direct uuid.UUID, cfg settingssvc.Commission) (ratePercent int, scope string, err error) {
	out, err := s.notEarning(ctx, direct)
	if err != nil {
		return 0, "", err
	}
	if out {
		return cfg.RatePercent, cfg.Scope, nil
	}
	return s.termsFor(ctx, direct, cfg)
}

// termsFor 返回某人作为直接邀请人适用的比例与计佣范围：单独设置里非 nil 的项用它，其余用全局 cfg。
// 单独比例 0 时这里照实返回 0（门户横幅显示「不拿佣金」）；计提整单的比例用 orderTerms。
func (s *Service) termsFor(ctx context.Context, referrer uuid.UUID, cfg settingssvc.Commission) (ratePercent int, scope string, err error) {
	ratePercent = cfg.RatePercent
	scope = cfg.Scope
	ov, ok, err := s.repo.OverrideOf(ctx, referrer)
	if err != nil {
		return 0, "", err
	}
	if !ok {
		return ratePercent, scope, nil
	}
	if ov.RatePercent != nil {
		ratePercent = *ov.RatePercent
	}
	if ov.Scope != nil {
		scope = *ov.Scope
	}
	return ratePercent, scope, nil
}
