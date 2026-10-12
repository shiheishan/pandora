package repo

import (
	"context"
	"errors"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
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
