package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/giftcard/model"
	"github.com/aegispanel/aegis/internal/module/giftcard/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// errTemplateNameTaken 是模板重名。
var errTemplateNameTaken = errs.Invalid(map[string]string{"name": "已经有同名的礼品卡了"})

// ListTemplates 后台模板列表（不含已归档），带每个模板的码数与已兑换数。SQL 条数与模板数无关。
func (s *Service) ListTemplates(ctx context.Context) ([]Template, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, err
	}
	rows, err := s.repo.ListTemplates(ctx)
	if err != nil {
		return nil, gcErr(err)
	}
	return s.assembleTemplates(ctx, rows, true)
}

// SaveTemplate 新建（in.ID 为 nil）或修改一个模板，返回保存后的模板。卡型不能改：已经发出去的码是按原卡型生成的。
// 套餐卡的套餐、价格档与限定套餐经 catalog 校验存在（价格档必须属于该套餐）。
func (s *Service) SaveTemplate(ctx context.Context, in TemplateInput) (Template, error) {
	p, err := adminPrincipal(ctx)
	if err != nil {
		return Template{}, err
	}
	in, err = normalizeTemplate(in)
	if err != nil {
		return Template{}, err
	}
	if err := s.checkCatalogRefs(ctx, in); err != nil {
		return Template{}, err
	}
	var out Template
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		action := ActionTemplateCreated
		var id uuid.UUID
		if in.ID == nil {
			admin := p.UserID
			m := templateModel(in)
			m.ID = uuid.Must(uuid.NewV7())
			m.CreatedBy = &admin
			if err := s.repo.CreateTemplate(ctx, &m, prizeModels(in.Rewards.Pool), in.Conditions.AllowedPlanIDs); err != nil {
				if errors.Is(err, repo.ErrDuplicate) {
					return errTemplateNameTaken
				}
				return gcErr(err)
			}
			id = m.ID
		} else {
			action = ActionTemplateUpdated
			old, found, err := s.repo.TemplateByID(ctx, *in.ID, true)
			if err != nil {
				return gcErr(err)
			}
			if !found {
				return errs.New(errs.NotFound)
			}
			if old.Type != in.Type {
				return errs.Newf(errs.Conflict, "不能修改卡型：已经发出去的码是按原卡型生成的")
			}
			m := templateModel(in)
			m.ID = old.ID
			ok, err := s.repo.UpdateTemplate(ctx, &m, prizeModels(in.Rewards.Pool), in.Conditions.AllowedPlanIDs)
			if err != nil {
				if errors.Is(err, repo.ErrDuplicate) {
					return errTemplateNameTaken
				}
				return gcErr(err)
			}
			if !ok {
				return errs.New(errs.NotFound)
			}
			id = old.ID
		}
		t, err := s.loadTemplate(ctx, id, true)
		if err != nil {
			return err
		}
		out = t
		return s.writeAudit(ctx, action, ResourceTemplate, &id, map[string]any{
			"name":    t.Name,
			"type":    t.Type,
			"status":  t.Status,
			"rewards": auditRewards(t),
		})
	})
	if err != nil {
		return Template{}, err
	}
	return out, nil
}

// checkCatalogRefs 经 catalog 校验模板引用的套餐、价格档与限定套餐都存在，价格档属于该套餐。
func (s *Service) checkCatalogRefs(ctx context.Context, in TemplateInput) error {
	planIDs := append([]uuid.UUID(nil), in.Conditions.AllowedPlanIDs...)
	if in.Type == TypePlan && in.Rewards.PlanID != nil {
		planIDs = append(planIDs, *in.Rewards.PlanID)
	}
	if len(planIDs) > 0 {
		plans, err := s.catalog.PlansByIDs(ctx, dedupeIDs(planIDs))
		if err != nil {
			return err
		}
		if in.Type == TypePlan {
			if _, ok := plans[*in.Rewards.PlanID]; !ok {
				return errs.Invalid(map[string]string{"rewards": "套餐不存在"})
			}
		}
		for _, id := range in.Conditions.AllowedPlanIDs {
			if _, ok := plans[id]; !ok {
				return errs.Invalid(map[string]string{"conditions": "限定的套餐不存在"})
			}
		}
	}
	if in.Type == TypePlan {
		prices, err := s.catalog.PricesByIDs(ctx, []uuid.UUID{*in.Rewards.PriceID})
		if err != nil {
			return err
		}
		pr, ok := prices[*in.Rewards.PriceID]
		if !ok || pr.PlanID != *in.Rewards.PlanID || pr.Status != catalogsvc.PriceActive {
			return errs.Invalid(map[string]string{"rewards": "价格档不存在、已停售或不属于该套餐"})
		}
	}
	return nil
}

// loadTemplate 读一个模板连同奖池、限定套餐（withCounts 时带码数）；不存在回 404。
func (s *Service) loadTemplate(ctx context.Context, id uuid.UUID, withCounts bool) (Template, error) {
	m, found, err := s.repo.TemplateByID(ctx, id, false)
	if err != nil {
		return Template{}, gcErr(err)
	}
	if !found {
		return Template{}, errs.New(errs.NotFound)
	}
	ts, err := s.assembleTemplates(ctx, []model.Template{m}, withCounts)
	if err != nil {
		return Template{}, err
	}
	return ts[0], nil
}

// assembleTemplates 给一组模板补上奖池、限定套餐与（后台视图 withCounts 时）码数和「价格已下架」：每类一条查询。
func (s *Service) assembleTemplates(ctx context.Context, rows []model.Template, withCounts bool) ([]Template, error) {
	out := make([]Template, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, m := range rows {
		ids[i] = m.ID
	}
	prizes, err := s.repo.PrizesByTemplates(ctx, ids)
	if err != nil {
		return nil, gcErr(err)
	}
	plans, err := s.repo.PlanIDsByTemplates(ctx, ids)
	if err != nil {
		return nil, gcErr(err)
	}
	counts := map[uuid.UUID]repo.CodeCounts{}
	archived := map[uuid.UUID]bool{}
	if withCounts {
		if counts, err = s.repo.CodeCountsByTemplates(ctx, ids); err != nil {
			return nil, gcErr(err)
		}
		if archived, err = s.archivedPrices(ctx, rows); err != nil {
			return nil, err
		}
	}
	for _, m := range rows {
		t := templateOf(m, prizes[m.ID], plans[m.ID])
		c := counts[m.ID]
		t.CodeTotal, t.CodeUsed = c.Total, c.Used
		t.PriceArchived = m.PriceID != nil && archived[*m.PriceID]
		out = append(out, t)
	}
	return out, nil
}

// archivedPrices 批量查套餐卡指向的价格档（一次 catalog 查询），回已下架的那些：已归档或已不存在。
// 只在后台视图（withCounts）里查：后台据此标出「价格已下架」，编辑时 checkCatalogRefs 要求重选在售价。
func (s *Service) archivedPrices(ctx context.Context, rows []model.Template) (map[uuid.UUID]bool, error) {
	var priceIDs []uuid.UUID
	for _, m := range rows {
		if m.Type == TypePlan && m.PriceID != nil {
			priceIDs = append(priceIDs, *m.PriceID)
		}
	}
	out := map[uuid.UUID]bool{}
	if len(priceIDs) == 0 {
		return out, nil
	}
	prices, err := s.catalog.PricesByIDs(ctx, dedupeIDs(priceIDs))
	if err != nil {
		return nil, err
	}
	for _, id := range priceIDs {
		if pr, ok := prices[id]; !ok || pr.Status != catalogsvc.PriceActive {
			out[id] = true
		}
	}
	return out, nil
}

// templateOf 把库里的行拼成模板视图。
func templateOf(m model.Template, prizes []model.Prize, planIDs []uuid.UUID) Template {
	t := Template{
		ID:          m.ID,
		Name:        m.Name,
		Description: m.Description,
		Type:        m.Type,
		Status:      m.Status,
		Conditions: Conditions{
			NewUserOnly:    m.NewUserOnly,
			PaidUserOnly:   m.PaidUserOnly,
			AllowedPlanIDs: planIDs,
			RequireInvite:  m.RequireInvite,
		},
		Limits:     Limits{MaxUsePerUser: m.MaxUsePerUser, CooldownHours: m.CooldownHours},
		ThemeColor: m.ThemeColor,
		CreatedAt:  m.CreatedAt,
	}
	if t.Conditions.AllowedPlanIDs == nil {
		t.Conditions.AllowedPlanIDs = []uuid.UUID{}
	}
	r := Rewards{ResetQuota: m.ResetQuota, PlanID: m.PlanID, PriceID: m.PriceID}
	if m.BalanceAmount != nil {
		r.Balance = *m.BalanceAmount
	}
	if m.TrafficBytes != nil {
		r.TrafficBytes = *m.TrafficBytes
	}
	if m.ExpireDays != nil {
		r.ExpireDays = *m.ExpireDays
	}
	for _, p := range prizes {
		prize := Prize{Label: p.Label, Weight: p.Weight}
		if p.BalanceAmount != nil {
			prize.Balance = *p.BalanceAmount
		}
		if p.TrafficBytes != nil {
			prize.TrafficBytes = *p.TrafficBytes
		}
		if p.ExpireDays != nil {
			prize.ExpireDays = *p.ExpireDays
		}
		r.Pool = append(r.Pool, prize)
	}
	t.Rewards = r
	return t
}

// templateModel 把规范化后的输入转成模板行（不含 ID、CreatedBy）。零值奖励写 NULL。
func templateModel(in TemplateInput) model.Template {
	r := in.Rewards
	return model.Template{
		Name:          in.Name,
		Description:   in.Description,
		Type:          in.Type,
		Status:        in.Status,
		BalanceAmount: decPtr(r.Balance),
		TrafficBytes:  int64Ptr(r.TrafficBytes),
		ExpireDays:    intPtr(r.ExpireDays),
		ResetQuota:    r.ResetQuota,
		PlanID:        r.PlanID,
		PriceID:       r.PriceID,
		NewUserOnly:   in.Conditions.NewUserOnly,
		PaidUserOnly:  in.Conditions.PaidUserOnly,
		RequireInvite: in.Conditions.RequireInvite,
		MaxUsePerUser: in.Limits.MaxUsePerUser,
		CooldownHours: in.Limits.CooldownHours,
		ThemeColor:    in.ThemeColor,
	}
}

// prizeModels 把奖池转成奖品行（TemplateID 由 repo 填）。
func prizeModels(pool []Prize) []model.Prize {
	out := make([]model.Prize, 0, len(pool))
	for i, p := range pool {
		out = append(out, model.Prize{
			Position:      i,
			Label:         p.Label,
			Weight:        p.Weight,
			BalanceAmount: decPtr(p.Balance),
			TrafficBytes:  int64Ptr(p.TrafficBytes),
			ExpireDays:    intPtr(p.ExpireDays),
		})
	}
	return out
}

// auditRewards 是审计里记的奖励摘要（金额写成字符串，不记卡密）。
func auditRewards(t Template) map[string]any {
	r := t.Rewards
	out := map[string]any{}
	if r.Balance.IsPositive() {
		out["balance"] = r.Balance.StringFixed(money.Scale)
	}
	if r.TrafficBytes > 0 {
		out["traffic_bytes"] = r.TrafficBytes
	}
	if r.ExpireDays > 0 {
		out["expire_days"] = r.ExpireDays
	}
	if r.ResetQuota {
		out["reset_quota"] = true
	}
	if r.PlanID != nil {
		out["plan_id"] = r.PlanID.String()
	}
	if r.PriceID != nil {
		out["price_id"] = r.PriceID.String()
	}
	if len(r.Pool) > 0 {
		out["pool_size"] = len(r.Pool)
	}
	return out
}

func decPtr(d decimal.Decimal) *decimal.Decimal {
	if !d.IsPositive() {
		return nil
	}
	return &d
}

func int64Ptr(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func intPtr(n int) *int {
	if n <= 0 {
		return nil
	}
	return &n
}
