package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/giftcard/model"
)

// CodeCounts 是一个模板下的码数与已兑换数。
type CodeCounts struct{ Total, Used int64 }

// CreateTemplate 插入模板，再插奖池与限定套餐。
func (r *Repo) CreateTemplate(ctx context.Context, t *model.Template, prizes []model.Prize, planIDs []uuid.UUID) error {
	if err := checkTemplateMoney(t, prizes); err != nil {
		return err
	}
	conn := r.d.Conn(ctx)
	if err := conn.Create(t).Error; err != nil {
		return mapErr(err)
	}
	if err := conn.Raw("SELECT * FROM gift_card_templates WHERE id = ?", t.ID).Scan(t).Error; err != nil {
		return mapErr(err)
	}
	if len(prizes) > 0 {
		for i := range prizes {
			prizes[i].TemplateID = t.ID
		}
		if err := conn.Create(&prizes).Error; err != nil {
			return mapErr(err)
		}
	}
	if len(planIDs) > 0 {
		rows := make([]model.TemplatePlan, len(planIDs))
		for i, pid := range planIDs {
			rows[i] = model.TemplatePlan{TemplateID: t.ID, PlanID: pid}
		}
		if err := conn.Create(&rows).Error; err != nil {
			return mapErr(err)
		}
	}
	return nil
}

// UpdateTemplate 按 id 更新模板可改列，并整池替换奖池与限定套餐。
func (r *Repo) UpdateTemplate(ctx context.Context, t *model.Template, prizes []model.Prize, planIDs []uuid.UUID) (bool, error) {
	if err := checkTemplateMoney(t, prizes); err != nil {
		return false, err
	}
	conn := r.d.Conn(ctx)
	cols := map[string]any{
		"name":             t.Name,
		"description":      t.Description,
		"status":           t.Status,
		"balance_amount":   t.BalanceAmount,
		"traffic_bytes":    t.TrafficBytes,
		"expire_days":      t.ExpireDays,
		"reset_quota":      t.ResetQuota,
		"plan_id":          t.PlanID,
		"price_id":         t.PriceID,
		"new_user_only":    t.NewUserOnly,
		"paid_user_only":   t.PaidUserOnly,
		"require_invite":   t.RequireInvite,
		"max_use_per_user": t.MaxUsePerUser,
		"cooldown_hours":   t.CooldownHours,
		"theme_color":      t.ThemeColor,
		"updated_at":       now(),
	}
	res := conn.Model(&model.Template{}).Where("id = ?", t.ID).Updates(cols)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	if res.RowsAffected != 1 {
		return false, nil
	}
	if err := conn.Exec("DELETE FROM gift_card_prizes WHERE template_id = ?", t.ID).Error; err != nil {
		return false, mapErr(err)
	}
	if err := conn.Exec("DELETE FROM gift_card_template_plans WHERE template_id = ?", t.ID).Error; err != nil {
		return false, mapErr(err)
	}
	if len(prizes) > 0 {
		for i := range prizes {
			prizes[i].TemplateID = t.ID
		}
		if err := conn.Create(&prizes).Error; err != nil {
			return false, mapErr(err)
		}
	}
	if len(planIDs) > 0 {
		rows := make([]model.TemplatePlan, len(planIDs))
		for i, pid := range planIDs {
			rows[i] = model.TemplatePlan{TemplateID: t.ID, PlanID: pid}
		}
		if err := conn.Create(&rows).Error; err != nil {
			return false, mapErr(err)
		}
	}
	return true, nil
}

// TemplateByID 按 id 读模板；forUpdate 为真加 FOR UPDATE。
func (r *Repo) TemplateByID(ctx context.Context, id uuid.UUID, forUpdate bool) (model.Template, bool, error) {
	var t model.Template
	err := r.d.Conn(ctx).Raw("SELECT * FROM gift_card_templates WHERE id = ?"+forUpdateSuffix(forUpdate), id).Scan(&t).Error
	if err != nil {
		return model.Template{}, false, mapErr(err)
	}
	if t.ID == uuid.Nil {
		return model.Template{}, false, nil
	}
	return t, true, nil
}

// ListTemplates 列出未归档的模板。
func (r *Repo) ListTemplates(ctx context.Context) ([]model.Template, error) {
	var out []model.Template
	err := r.d.Conn(ctx).Where("status <> ?", "archived").
		Order("created_at DESC, id DESC").Find(&out).Error
	return out, mapErr(err)
}

// PrizesByTemplates 批量取奖池并按模板分组。
func (r *Repo) PrizesByTemplates(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]model.Prize, error) {
	out := make(map[uuid.UUID][]model.Prize)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []model.Prize
	if err := r.d.Conn(ctx).Where("template_id IN ?", ids).
		Order("template_id, position").Find(&rows).Error; err != nil {
		return nil, mapErr(err)
	}
	for _, p := range rows {
		out[p.TemplateID] = append(out[p.TemplateID], p)
	}
	return out, nil
}

// PlanIDsByTemplates 批量取限定套餐并按模板分组。
func (r *Repo) PlanIDsByTemplates(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]uuid.UUID, error) {
	out := make(map[uuid.UUID][]uuid.UUID)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.d.Conn(ctx).Raw(
		"SELECT template_id, plan_id FROM gift_card_template_plans WHERE template_id IN ? ORDER BY template_id, plan_id", ids,
	).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tid, pid uuid.UUID
		if err := rows.Scan(&tid, &pid); err != nil {
			return nil, err
		}
		out[tid] = append(out[tid], pid)
	}
	return out, mapErr(rows.Err())
}

// CodeCountsByTemplates 批量统计各模板下的码数与已兑换数。
func (r *Repo) CodeCountsByTemplates(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]CodeCounts, error) {
	out := make(map[uuid.UUID]CodeCounts)
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.d.Conn(ctx).Raw(`
		SELECT template_id,
		       count(*) AS total,
		       count(*) FILTER (WHERE status = 'used') AS used
		FROM gift_card_codes WHERE template_id IN ? GROUP BY template_id`, ids).Rows()
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var tid uuid.UUID
		var cc CodeCounts
		if err := rows.Scan(&tid, &cc.Total, &cc.Used); err != nil {
			return nil, err
		}
		out[tid] = cc
	}
	return out, mapErr(rows.Err())
}

func checkTemplateMoney(t *model.Template, prizes []model.Prize) error {
	if err := checkMoney(t.BalanceAmount); err != nil {
		return err
	}
	for _, p := range prizes {
		if err := checkMoney(p.BalanceAmount); err != nil {
			return err
		}
	}
	return nil
}
