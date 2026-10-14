package repo

import (
	"context"
	"errors"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// InsertTransaction 插入一笔交易及其分录（交易一条 INSERT … RETURNING，分录一条批量 INSERT），
// 回填 t.ID 与每条分录的 TransactionID。写前对每条分录金额 money.Check。唯一约束冲突返回 ErrDuplicate。
func (r *Repo) InsertTransaction(ctx context.Context, t *model.Transaction, entries []model.Entry) error {
	if len(entries) == 0 {
		return errors.New("ledger: 交易没有分录")
	}
	for _, e := range entries {
		if err := money.Check(e.Amount); err != nil {
			return err
		}
	}
	if err := mapErr(r.d.Conn(ctx).Create(t).Error); err != nil {
		return err
	}
	for i := range entries {
		entries[i].TransactionID = t.ID
	}
	return mapErr(r.d.Conn(ctx).Create(&entries).Error)
}

// TransactionExists 报告某来源是否已有这种 kind 的交易（一条 SELECT EXISTS）。
func (r *Repo) TransactionExists(ctx context.Context, kind, sourceType string, sourceID uuid.UUID) (bool, error) {
	var exists bool
	err := r.d.Conn(ctx).Raw(
		"SELECT EXISTS (SELECT 1 FROM ledger_transactions WHERE kind = ? AND source_type = ? AND source_id = ?)",
		kind, sourceType, sourceID,
	).Row().Scan(&exists)
	return exists, mapErr(err)
}
