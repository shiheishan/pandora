package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// CreatePlanComplete「一次建成」：套餐、草稿版本、各档公开价，可选直接发布，全部在一个事务里。
func (s *Service) CreatePlanComplete(ctx context.Context, in WizardCreate) (WizardCreated, error) {
	if err := validateWizardCreate(&in); err != nil {
		return WizardCreated{}, err
	}
	vin := wizardVersionInput(in)
	if err := validateVersionInput(&vin); err != nil {
		return WizardCreated{}, err
	}

	var out WizardCreated
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		planID, err := s.createPlanTx(ctx, in.Plan)
		if err != nil {
			return err
		}
		v, err := s.createVersionTx(ctx, planID)
		if err != nil {
			return err
		}
		_, err = s.updateVersionTx(ctx, planID, v.ID, v.RowVersion, vin)
		if err != nil {
			return err
		}

		priceIDs := make([]uuid.UUID, 0, len(in.Prices))
		for _, p := range in.Prices {
			pr, err := s.createPriceTx(ctx, planID, wizardPriceInput(p))
			if err != nil {
				return err
			}
			priceIDs = append(priceIDs, pr.ID)
		}

		published := false
		if in.Publish {
			plan, found, err := s.repo.PlanByID(ctx, planID, repo.NoLock)
			if err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
			ver, found, err := s.repo.VersionByID(ctx, planID, v.ID, repo.NoLock)
			if err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
			_, _, err = s.publishVersionTx(ctx, planID, v.ID, plan.RowVersion, ver.RowVersion)
			if err != nil {
				return err
			}
			published = true
		}

		detail, err := s.loadDetail(ctx, planID)
		if err != nil {
			return err
		}
		out = WizardCreated{
			Plan:      detail,
			VersionID: v.ID,
			PriceIDs:  priceIDs,
			Published: published,
		}
		return nil
	})
	return out, catalogErr(err)
}

// UpdatePlanComplete「一次改完」：资料、公开价、额度变更时滚版本并发布，同一事务。
func (s *Service) UpdatePlanComplete(ctx context.Context, planID uuid.UUID, in WizardUpdate) (WizardUpdated, error) {
	var err error
	if in.ExpectedRowVersion <= 0 {
		return WizardUpdated{}, errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}

	planInput := PlanInput{
		Code:                 in.Code,
		Name:                 in.Name,
		Description:          in.Description,
		Visibility:           in.Visibility,
		VisibleGroupIDs:      in.VisibleGroupIDs,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal,
		SortOrder:            in.SortOrder,
	}
	if in.Highlights != nil {
		planInput.Highlights = *in.Highlights
	}
	if err := preparePlanInput(&planInput); err != nil {
		return WizardUpdated{}, err
	}
	if err := validateWizardQuotaEdits(in); err != nil {
		return WizardUpdated{}, err
	}

	var wanted map[string]WizardPrice
	if in.Prices != nil && len(*in.Prices) > 0 {
		wanted, err = prepareWizardPrices(*in.Prices)
		if err != nil {
			return WizardUpdated{}, err
		}
	}

	var result WizardUpdated
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		changed := []string{}

		_, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		before, err := s.loadDetail(ctx, planID)
		if err != nil {
			return err
		}

		planInput.AllowNewPurchase = boolOr(in.AllowNewPurchase, before.AllowNewPurchase)
		planInput.AllowRenewal = boolOr(in.AllowRenewal, before.AllowRenewal)
		planInput.AllowUpgrade = boolOr(in.AllowUpgrade, before.AllowUpgrade)
		planInput.VisibleFrom = before.VisibleFrom
		planInput.VisibleUntil = before.VisibleUntil
		if in.Highlights == nil {
			planInput.Highlights = before.Highlights
		}
		planInput.Recommended = boolOr(in.Recommended, before.Recommended)

		_, err = s.updatePlanTx(ctx, planID, in.ExpectedRowVersion, planInput)
		if err != nil {
			return err
		}
		changed = append(changed, "套餐资料已更新")

		priceChanges := 0
		if len(wanted) > 0 {
			priceChanges, err = s.syncPublicPricesTx(ctx, planID, wanted)
			if err != nil {
				return err
			}
		}

		cur := currentVersionFromDetail(before)
		if quotaDiffers(cur, in) {
			if err := s.rollVersionTx(ctx, planID, before, in); err != nil {
				return err
			}
			changed = append(changed, "流量、设备数与限速已更新；新购买的用户按新额度，已经买了的用户仍按原额度")
		}

		if priceChanges > 0 {
			changed = append(changed, "价格已更新，只影响之后的新购与续费；已成交的订单不变")
		}

		detail, err := s.loadDetail(ctx, planID)
		if err != nil {
			return err
		}
		result = WizardUpdated{Plan: detail, Changed: changed}
		return nil
	})
	return result, catalogErr(err)
}

func (s *Service) rollVersionTx(ctx context.Context, planID uuid.UUID, before PlanDetail, in WizardUpdate) error {
	cur := currentVersionFromDetail(before)

	var ver Version
	for _, v := range before.Versions {
		if v.Status == VersionDraft {
			ver = v
			break
		}
	}
	if ver.ID == uuid.Nil {
		v, err := s.createVersionTx(ctx, planID)
		if err != nil {
			return err
		}
		ver = v
	}

	vin := inheritVersionInput(cur, in)
	if err := validateVersionInput(&vin); err != nil {
		return err
	}
	_, err := s.updateVersionTx(ctx, planID, ver.ID, ver.RowVersion, vin)
	if err != nil {
		return err
	}

	plan, found, err := s.repo.PlanByID(ctx, planID, repo.NoLock)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	verRow, found, err := s.repo.VersionByID(ctx, planID, ver.ID, repo.NoLock)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	_, _, err = s.publishVersionTx(ctx, planID, ver.ID, plan.RowVersion, verRow.RowVersion)
	return err
}

func currentVersionFromDetail(before PlanDetail) *Version {
	if before.CurrentVersionID == nil {
		return nil
	}
	id := *before.CurrentVersionID
	for i := range before.Versions {
		if before.Versions[i].ID == id {
			return &before.Versions[i]
		}
	}
	return nil
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}
