package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BalanceEntry 是后台看的一条余额流水：Delta 对用户入账为正、出账为负；BalanceAfter 是这笔之后的可用余额。
type BalanceEntry struct {
	ID           uuid.UUID
	Kind         string
	Delta        decimal.Decimal
	BalanceAfter decimal.Decimal
	Memo         string
	OrderID      *uuid.UUID // 来源是订单时（source_type = order）为订单 id，否则 nil
	At           time.Time
}

// BalanceEntryPage 是一页余额流水与总条数。
type BalanceEntryPage struct {
	Entries []BalanceEntry
	Total   int64
}

// runningBalance 计算截至某分录时的科目余额。借方正常科目为借方累计减贷方累计，贷方正常科目反之。
func runningBalance(t AccountType, credits, debits decimal.Decimal) decimal.Decimal {
	if t.DebitNormal() {
		return debits.Sub(credits)
	}
	return credits.Sub(debits)
}

// UserBalanceEntries 返回某用户可用余额科目（user_balance）的一页流水，新的在前（按分录 id，即真实记账顺序，见 repo.BalanceEntries）；
// 与门户钱包 MyWallet 是同一批分录、同一顺序。
// 后台只读；从没有过余额往来的用户回空页。
func (s *Service) UserBalanceEntries(ctx context.Context, userID uuid.UUID, limit, offset int) (BalanceEntryPage, error) {
	rows, total, err := s.repo.BalanceEntries(ctx, userID, string(UserBalance), limit, offset)
	if err != nil {
		return BalanceEntryPage{}, err
	}

	entries := make([]BalanceEntry, 0, len(rows))
	for _, row := range rows {
		var orderID *uuid.UUID
		if row.SourceType != nil && *row.SourceType == SourceOrder && row.SourceID != nil {
			orderID = row.SourceID
		}

		entries = append(entries, BalanceEntry{
			ID:           row.ID,
			Kind:         row.Kind,
			Delta:        signedDelta(UserBalance, Direction(row.Direction), row.Amount),
			BalanceAfter: runningBalance(UserBalance, row.CreditsThrough, row.DebitsThrough),
			Memo:         row.Memo,
			OrderID:      orderID,
			At:           row.OccurredAt,
		})
	}

	return BalanceEntryPage{
		Entries: entries,
		Total:   total,
	}, nil
}
