package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/giftcard/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 卡密列表与报表的口径（与旧接口相同）。
const (
	CodeListDefault = 50
	CodeListMax     = 5000
	// CodesExportMax 是掩码报表一次导出的行数上限：超过要求缩小筛选，不悄悄截断
	CodesExportMax = 50000
	// usageLimit 是使用明细一次给出的最多行数
	usageLimit = 200
)

// codeTransitions 是卡密状态的转换表：unused 只走一次到 used，或被停用、过期；停用的可以恢复。
// used 与 expired 是终态：已兑换的码不能停用（那等于事后否认一次已经发生的发放）。
var codeTransitions = map[string][]string{
	CodeUnused:   {CodeUsed, CodeDisabled, CodeExpired},
	CodeDisabled: {CodeUnused},
}

// CanTransition 查卡密状态转换表。
func CanTransition(from, to string) bool {
	for _, t := range codeTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// validCodeStatus 报告是不是四种卡密状态之一。
func validCodeStatus(st string) bool {
	switch st {
	case CodeUnused, CodeUsed, CodeDisabled, CodeExpired:
		return true
	}
	return false
}

// codeQuery 校验筛选并转成 repo 的查询；状态走白名单。
func codeQuery(f CodeFilter) (repo.CodeQuery, error) {
	if f.Status != "" && !validCodeStatus(f.Status) {
		return repo.CodeQuery{}, errs.Newf(errs.BadRequest, "不支持的卡密状态")
	}
	return repo.CodeQuery{TemplateID: f.TemplateID, BatchID: f.BatchID, Status: f.Status}, nil
}

// ListCodes 后台卡密列表：只有掩码，按生成时间倒序。
func (s *Service) ListCodes(ctx context.Context, f CodeFilter, limit, offset int) ([]Code, int64, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, 0, err
	}
	q, err := codeQuery(f)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := s.repo.ListCodes(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, gcErr(err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, c := range rows {
		if c.UsedBy != nil {
			ids = append(ids, *c.UsedBy)
		}
	}
	emails, err := s.emails(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Code, 0, len(rows))
	for _, c := range rows {
		v := Code{
			ID:         c.ID,
			CodeMasked: c.CodeMask,
			Status:     c.Status,
			BatchID:    c.BatchID,
			ExpiresAt:  c.ExpiresAt,
			UsedAt:     c.UsedAt,
			CreatedAt:  c.CreatedAt,
			TemplateID: c.TemplateID,
		}
		if e := emailPtr(emails, c.UsedBy); e != nil {
			v.UsedEmail = *e
		}
		out = append(out, v)
	}
	return out, total, nil
}

// ToggleCode 停用（disabled=true）或恢复一个卡密。已兑换、已过期的不能改；状态不需要该操作回 409。
// 停用单个码不要求重认证：那是发现异常时的止血动作，越快越好。
func (s *Service) ToggleCode(ctx context.Context, codeID uuid.UUID, disabled bool) error {
	if _, err := adminPrincipal(ctx); err != nil {
		return err
	}
	from, to := CodeDisabled, CodeUnused
	if disabled {
		from, to = CodeUnused, CodeDisabled
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		c, found, err := s.repo.CodeByID(ctx, codeID, true)
		if err != nil {
			return gcErr(err)
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		switch {
		case c.Status == CodeUsed:
			return errs.Newf(errs.Conflict, "这个码已经被兑换了，不能停用。要收回权益请走调账或工单")
		case c.Status == CodeExpired:
			return errs.Newf(errs.Conflict, "这个码已经过期了")
		case c.Status != from || !CanTransition(from, to):
			return errs.Newf(errs.Conflict, "这个码当前的状态不需要该操作")
		}
		ok, err := s.repo.UpdateCodeStatus(ctx, c.ID, from, to, nil, nil)
		if err != nil {
			return gcErr(err)
		}
		if !ok {
			return ErrStateChanged
		}
		id := c.ID
		return s.writeAudit(ctx, ActionCodeToggled, ResourceCode, &id, map[string]any{
			"from": from, "to": to, "code": c.CodeMask,
		})
	})
}

// ExportCodes 按卡密列表的筛选取出全部匹配行的掩码报表（给运营对账），并写一条审计。
// 先数再取：超过 CodesExportMax 回 422；整批读完、审计写进同一事务之后才交给调用方输出。
func (s *Service) ExportCodes(ctx context.Context, f CodeFilter) ([]CodeReportRow, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, err
	}
	q, err := codeQuery(f)
	if err != nil {
		return nil, err
	}
	var out []CodeReportRow
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		rows, total, err := s.repo.CodeReport(ctx, q, CodesExportMax)
		if err != nil {
			return gcErr(err)
		}
		if total > CodesExportMax {
			return errs.Newf(errs.ValidationFailed, "超过 50000 行，请缩小筛选范围（按模板、批次或状态）")
		}
		ids := make([]uuid.UUID, 0, len(rows))
		for _, r := range rows {
			if r.UsedBy != nil {
				ids = append(ids, *r.UsedBy)
			}
		}
		emails, err := s.emails(ctx, ids)
		if err != nil {
			return err
		}
		out = make([]CodeReportRow, 0, len(rows))
		for _, r := range rows {
			v := CodeReportRow{
				CodeMasked:   r.CodeMask,
				Status:       r.Status,
				TemplateName: r.TemplateName,
				BatchID:      r.BatchID,
				ExpiresAt:    r.ExpiresAt,
				CreatedAt:    r.CreatedAt,
				UsedAt:       r.UsedAt,
			}
			if e := emailPtr(emails, r.UsedBy); e != nil {
				v.UsedEmail = *e
			}
			out = append(out, v)
		}
		// 审计记筛选条件与行数，不记任何码
		return s.writeAudit(ctx, ActionReportExported, ResourceCode, nil, map[string]any{
			"template_id": idString(f.TemplateID), "status": f.Status, "batch_id": idString(f.BatchID), "rows": len(out),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Stats 后台礼品卡统计；金额只计站点币种的部分。
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return Stats{}, err
	}
	cur, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return Stats{}, err
	}
	row, err := s.repo.Stats(ctx)
	if err != nil {
		return Stats{}, gcErr(err)
	}
	return Stats{
		Templates:     row.Templates,
		CodesTotal:    row.CodesTotal,
		CodesUsed:     row.CodesUsed,
		CodesUnused:   row.CodesUnused,
		BalanceOut:    amountIn(row.BalanceOut, cur),
		TrafficOut:    row.TrafficOut,
		BalanceIssued: amountIn(row.BalanceIssued, cur),
		Currency:      cur,
	}, nil
}

// ListUsages 后台使用明细（最近 200 条），templateID 非 nil 时只看这个模板。
func (s *Service) ListUsages(ctx context.Context, templateID *uuid.UUID) ([]Usage, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return nil, err
	}
	rows, err := s.repo.Usages(ctx, templateID, usageLimit)
	if err != nil {
		return nil, gcErr(err)
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	emails, err := s.emails(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Usage, 0, len(rows))
	for _, r := range rows {
		u := Usage{
			TemplateName: r.TemplateName,
			CodeMasked:   r.CodeMask,
			UserEmail:    emails[r.UserID],
			RedeemedAt:   r.RedeemedAt,
			Granted:      Granted{Currency: money.Currency(r.Currency), QuotaReset: r.QuotaReset},
		}
		if r.PrizeLabel != nil {
			u.PrizeLabel = *r.PrizeLabel
		}
		if r.BalanceAmount != nil {
			u.Granted.Balance = *r.BalanceAmount
		}
		if r.TrafficBytes != nil {
			u.Granted.TrafficBytes = *r.TrafficBytes
		}
		if r.ExpireDays != nil {
			u.Granted.ExpireDays = *r.ExpireDays
		}
		out = append(out, u)
	}
	return out, nil
}

// amountIn 取按币种分组的合计里某个币种的那一份；没有为 0。
func amountIn(byCur map[string]decimal.Decimal, cur money.Currency) decimal.Decimal {
	if d, ok := byCur[string(cur)]; ok {
		return d
	}
	return decimal.Zero
}

// idString 把可空 id 写成审计里的字符串（nil 为空串）。
func idString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
