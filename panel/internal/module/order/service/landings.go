package service

import (
	"context"
	"sort"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// Landings 是门户买 planID 的落点（GET /v1/me/checkout/landings?plan_id=）：用户自己选这单落到哪里。
// 选项：第一项恒为 {Mode: LandingNew}；同款在先（一次性叠加 / 订阅续费），换掉别的未停用套餐在后。
// 不加锁、不写库。
func (s *Service) Landings(ctx context.Context, planID uuid.UUID) (Landings, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return Landings{}, err
	}
	now := s.clock()
	uid := userID
	op, err := s.catalog.PlanOffers(ctx, planID, catalogsvc.OfferRequest{
		Use:    catalogsvc.UseNew,
		UserID: &uid,
		Now:    now,
	})
	if err != nil {
		return Landings{}, err
	}

	us, err := s.subs.ByUser(ctx, userID)
	if err != nil {
		return Landings{}, err
	}

	// 只看未停用的，生效中的在前、再按创建时间新的在前，最多 maxCandidates 份
	var cands []subsvc.Summary
	for _, sum := range us.Subscriptions {
		if sum.State != subsvc.StateEnded {
			cands = append(cands, sum)
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		li, lj := cands[i].State == subsvc.StateLive, cands[j].State == subsvc.StateLive
		if li != lj {
			return li
		}
		if !cands[i].CreatedAt.Equal(cands[j].CreatedAt) {
			return cands[i].CreatedAt.After(cands[j].CreatedAt)
		}
		return cands[i].ID.String() < cands[j].ID.String()
	})
	if len(cands) > maxCandidates {
		cands = cands[:maxCandidates]
	}

	ids := make([]uuid.UUID, 0, len(cands))
	for _, sum := range cands {
		ids = append(ids, sum.ID)
	}
	open, err := s.repo.OpenChangeSubscriptions(ctx, ids)
	if err != nil {
		return Landings{}, err
	}

	opts := []Landing{{Mode: LandingNew}}
	var sameOpts []Landing
	var changeOpts []Landing

	for _, sum := range cands {
		if open[sum.ID] {
			continue // 挂着未完结续费、叠加或换套餐单的那份不列
		}
		subID := sum.ID
		if sum.PlanID == planID {
			if op.Plan.Kind == catalogsvc.KindOneTime {
				sameOpts = append(sameOpts, Landing{
					Mode:           LandingStack,
					SubscriptionID: &subID,
				})
			} else if op.Plan.AllowRenewal && op.Plan.Status != catalogsvc.PlanArchived {
				sameOpts = append(sameOpts, Landing{
					Mode:           LandingRenew,
					SubscriptionID: &subID,
				})
			}
		} else if op.Plan.AllowUpgrade {
			changeOpts = append(changeOpts, Landing{
				Mode:           LandingChange,
				SubscriptionID: &subID,
			})
		}
	}

	opts = append(opts, sameOpts...)
	opts = append(opts, changeOpts...)

	return Landings{Options: opts}, nil
}
