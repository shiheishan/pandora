package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HistoryItem 是余额明细的一行：Delta 对用户是正负号直观的变动（入账为正、出账为负）。
type HistoryItem struct {
	Kind  string
	Delta decimal.Decimal
	Memo  string
	At    time.Time
}

// Wallet 是门户「我的余额」：余额与最近 100 条明细。
type Wallet struct {
	Balance decimal.Decimal
	History []HistoryItem
}

// WalletHistoryLimit 是余额明细的条数上限。
const WalletHistoryLimit = 100

// Balance 返回用户的可用余额；从没有过余额往来时是 0。
func (s *Service) Balance(ctx context.Context, userID uuid.UUID) (decimal.Decimal, error) {
	m, err := s.Balances(ctx, []uuid.UUID{userID})
	if err != nil {
		return decimal.Zero, err
	}
	return m[userID], nil
}

// Balances 批量返回一批用户的可用余额（一条 SQL）；结果里每个请求的 ID 都有值，没有科目的是 0。
func (s *Service) Balances(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	out := make(map[uuid.UUID]decimal.Decimal, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	for _, id := range userIDs {
		out[id] = decimal.Zero
	}
	got, err := s.repo.UserBalances(ctx, string(UserBalance), userIDs)
	if err != nil {
		return nil, err
	}
	for id, bal := range got {
		out[id] = bal
	}
	return out, nil
}

// MyWallet 返回 ctx 登录主体（门户用户）的余额与明细；没有主体回 errs.Unauthorized。
func (s *Service) MyWallet(ctx context.Context) (Wallet, error) {
	p, err := principal(ctx)
	if err != nil {
		return Wallet{}, err
	}
	bal, err := s.Balance(ctx, p.UserID)
	if err != nil {
		return Wallet{}, err
	}
	rows, err := s.repo.History(ctx, p.UserID, string(UserBalance), WalletHistoryLimit)
	if err != nil {
		return Wallet{}, err
	}
	history := make([]HistoryItem, 0, len(rows))
	for _, row := range rows {
		history = append(history, HistoryItem{
			Kind:  row.Kind,
			Delta: signedDelta(UserBalance, Direction(row.Direction), row.Amount),
			Memo:  row.Memo,
			At:    row.OccurredAt,
		})
	}
	return Wallet{
		Balance: bal,
		History: history,
	}, nil
}
