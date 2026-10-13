package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// CSVCell 是导出 CSV 的单元格防注入（以 = + - @ 等开头的前面加单引号），口径与审计导出相同。
func CSVCell(s string) string { return auditsvc.CSVSafe(s) }

// formatPaid 把按币种划分的实付金额格式化为展示文本：
// 按币种代码字母序，每个币种 "CNY 12.30"（保留两位小数），多个用 " / " 连接；没有付费订单或实付为零时返回 "0"。
func formatPaid(m map[string]decimal.Decimal) string {
	if len(m) == 0 {
		return "0"
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v.GreaterThan(decimal.Zero) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "0"
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %s", k, m[k].StringFixed(2))
	}
	return strings.Join(parts, " / ")
}

// PreviewBulk 按筛选条件预览命中人数与前 10 个样本。
func (s *Service) PreviewBulk(ctx context.Context, f BulkFilter) (BulkPreview, error) {
	_, q, err := s.bulkQuery(ctx, f)
	if err != nil {
		return BulkPreview{}, err
	}
	total, err := s.repo.CountBulk(ctx, q)
	if err != nil {
		return BulkPreview{}, err
	}
	refs, err := s.repo.BulkUsers(ctx, q, 10)
	if err != nil {
		return BulkPreview{}, err
	}
	op, err := s.needSubs()
	if err != nil {
		return BulkPreview{}, err
	}
	ids := make([]uuid.UUID, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	cur, err := op.CurrentSubs(ctx, ids)
	if err != nil {
		return BulkPreview{}, err
	}
	samples := make([]BulkSample, 0, len(refs))
	for _, r := range refs {
		sm := BulkSample{Email: r.Email}
		if cs, ok := cur[r.ID]; ok {
			pn := cs.PlanName
			sm.PlanName = &pn
			pe := cs.PeriodEnd
			sm.PeriodEnd = &pe
		}
		samples = append(samples, sm)
	}
	return BulkPreview{Total: total, Samples: samples}, nil
}

// ExportUsers 导出筛选命中的用户 CSV 行；审计写不进去则整单失败，不交出名单。
func (s *Service) ExportUsers(ctx context.Context, f BulkFilter, limit int) ([]ExportRow, error) {
	if limit <= 0 || limit > 50000 {
		limit = 10000
	}
	f, q, err := s.bulkQuery(ctx, f)
	if err != nil {
		return nil, err
	}
	_, err = s.needSubs()
	if err != nil {
		return nil, err
	}
	_, err = s.needOrders()
	if err != nil {
		return nil, err
	}
	var out []ExportRow
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.ExportBulk(ctx, q, limit)
		if err != nil {
			return err
		}
		ids := make([]uuid.UUID, len(rows))
		for i, row := range rows {
			ids[i] = row.ID
		}
		op, err := s.needSubs()
		if err != nil {
			return err
		}
		cur, err := op.CurrentSubs(ctx, ids)
		if err != nil {
			return err
		}
		ordOp, err := s.needOrders()
		if err != nil {
			return err
		}
		ordStats, err := ordOp.UserOrderStats(ctx, ids)
		if err != nil {
			return err
		}
		out = make([]ExportRow, 0, len(rows))
		for _, row := range rows {
			r := ExportRow{
				Email:       row.Email,
				Status:      row.Status,
				GroupName:   row.GroupName,
				CreatedAt:   row.CreatedAt,
				LastLoginAt: row.LastLoginAt,
			}
			if cs, ok := cur[row.ID]; ok {
				r.ActiveSubs = cs.LiveCount
			}
			if st, ok := ordStats[row.ID]; ok {
				r.Orders = st.Orders
				r.Paid = formatPaid(st.Paid)
			} else {
				r.Paid = "0"
			}
			out = append(out, r)
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       "user.bulk_exported",
			ResourceType: ResourceUser,
			Data: map[string]any{
				"rows":   len(out),
				"limit":  limit,
				"filter": bulkFilterDigest(f),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []ExportRow{}, nil
	}
	return out, nil
}

// BulkRecipients 供 notify 群发取收件人，与预览、导出共用同一套 BulkFilter 条件。
func (s *Service) BulkRecipients(ctx context.Context, f BulkFilter, max int) ([]BulkRecipient, int64, error) {
	_, q, err := s.bulkQuery(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.repo.CountBulk(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	if total == 0 || total > int64(max) {
		return nil, total, nil
	}
	refs, err := s.repo.BulkUsers(ctx, q, max)
	if err != nil {
		return nil, 0, err
	}
	op, err := s.needSubs()
	if err != nil {
		return nil, 0, err
	}
	ids := make([]uuid.UUID, len(refs))
	for i, r := range refs {
		ids[i] = r.ID
	}
	cur, err := op.CurrentSubs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]BulkRecipient, 0, len(refs))
	for _, r := range refs {
		rec := BulkRecipient{UserID: r.ID, Email: r.Email}
		if cs, ok := cur[r.ID]; ok {
			rec.PlanName = cs.PlanName
			pe := cs.PeriodEnd
			rec.PeriodEnd = &pe
		}
		out = append(out, rec)
	}
	return out, total, nil
}
