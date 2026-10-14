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

// beneficiaries 列出这一单的受益人。没开多级分销：只有直接邀请人，第 1 级、100%。
// 开了：从直接邀请人起往上最多 3 级，第 N 级分 dist.Levels[N-1]%；比例为 0 的级别跳过但继续往上找；
// 遇到已出现过的人（含买家本人）就停，防环。
func beneficiaries(ctx context.Context, buyer, direct uuid.UUID, dist settingssvc.CommissionDistribution, next referrerFunc) ([]beneficiary, error) {
	if !dist.Enabled {
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
			out = append(out, beneficiary{UserID: cur, Level: lvl, SharePercent: share})
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

// termsFor 返回某人作为直接邀请人适用的比例与计佣范围：单独设置里非 nil 的项用它，其余用全局 cfg。
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
