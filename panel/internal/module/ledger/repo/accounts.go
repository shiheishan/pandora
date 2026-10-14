package repo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// EnsureAccounts 按传入顺序插入缺失的科目，已存在的不动（一条 INSERT … ON CONFLICT ON CONSTRAINT ledger_accounts_owner_key DO NOTHING）。
// 调用方负责按业务键排好序；用户不存在时返回 ErrForeignKey。
func (r *Repo) EnsureAccounts(ctx context.Context, keys []AccountKey) error {
	if len(keys) == 0 {
		return nil
	}
	rows := make([]model.Account, len(keys))
	for i, k := range keys {
		rows[i] = model.Account{
			AccountType: k.Type,
			Currency:    k.Currency,
			OwnerUserID: k.OwnerUserID,
			OwnerRef:    k.OwnerRef,
			Balance:     decimal.Zero,
		}
	}
	err := r.d.Conn(ctx).Clauses(clause.OnConflict{
		OnConstraint: "ledger_accounts_owner_key",
		DoNothing:    true,
	}).Create(&rows).Error
	return mapErr(err)
}

// LockAccounts 一条语句取出并按 id 升序锁住这些科目（FOR UPDATE），返回行按 id 升序。找不到的键不在结果里。
func (r *Repo) LockAccounts(ctx context.Context, keys []AccountKey) ([]model.Account, error) {
	if len(keys) == 0 {
		return []model.Account{}, nil
	}
	var b strings.Builder
	b.WriteString("SELECT * FROM ledger_accounts WHERE ")
	args := make([]any, 0, len(keys)*4)
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" OR ")
		}
		if k.OwnerUserID != nil {
			b.WriteString("(account_type = ? AND currency = ? AND owner_user_id = ? AND owner_ref = '')")
			args = append(args, k.Type, k.Currency, *k.OwnerUserID)
		} else {
			b.WriteString("(account_type = ? AND currency = ? AND owner_user_id IS NULL AND owner_ref = ?)")
			args = append(args, k.Type, k.Currency, k.OwnerRef)
		}
	}
	b.WriteString(" ORDER BY id FOR UPDATE")
	var rows []model.Account
	if err := r.d.Conn(ctx).Raw(b.String(), args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.Account{}, nil
	}
	return rows, nil
}

// SetBalances 写回已锁住科目的新余额（按 id 升序逐条 UPDATE ledger_accounts SET balance = ?, updated_at = now() WHERE id = ?），
// 每条影响行数必须是 1，否则报错。写前对每个余额 money.Check。违反不透支 CHECK 时返回 ErrCheck。
func (r *Repo) SetBalances(ctx context.Context, balances map[uuid.UUID]decimal.Decimal) error {
	if len(balances) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(balances))
	for id := range balances {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
	for _, id := range ids {
		bal := balances[id]
		if err := money.Check(bal); err != nil {
			return err
		}
		res := r.d.Conn(ctx).Exec(
			"UPDATE ledger_accounts SET balance = ?, updated_at = now() WHERE id = ?",
			bal, id,
		)
		if err := mapErr(res.Error); err != nil {
			return err
		}
		if res.RowsAffected != 1 {
			return fmt.Errorf("ledger: 写回余额时科目 %s 不存在", id)
		}
	}
	return nil
}

// UserBalances 读一批用户在某币种、某类用户科目上的余额；没有科目的用户不在结果里。ids 为空返回空 map。
func (r *Repo) UserBalances(ctx context.Context, accountType, currency string, userIDs []uuid.UUID) (map[uuid.UUID]decimal.Decimal, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]decimal.Decimal{}, nil
	}
	var rows []model.Account
	err := r.d.Conn(ctx).Model(&model.Account{}).Where(
		"account_type = ? AND currency = ? AND owner_user_id IN ?",
		accountType, currency, userIDs,
	).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]decimal.Decimal, len(rows))
	for _, row := range rows {
		if row.OwnerUserID == nil {
			continue
		}
		out[*row.OwnerUserID] = row.Balance
	}
	return out, nil
}
