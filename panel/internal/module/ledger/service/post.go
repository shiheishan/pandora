package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aegispanel/aegis/internal/module/ledger/model"
	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// Post 记一笔交易并同步更新涉及科目的余额缓存，返回交易 ID。在 ctx 的事务里执行（没有就自己开）。
// balance_hold、balance_release、order_paid、balance_adjusted 只能走专门的方法，本方法拒收。
// 参数不合法（ErrInvalidPosting）、余额不足（ErrInsufficientBalance）、按来源唯一的 kind 已记过（ErrDuplicatePosting）
// 都在写库之前查出，调用方事务还能继续用；其余错误（含并发撞唯一索引）说明库里语句已失败，调用方事务必须回滚。
// 例外：撞库约束时返回的错误同时 errors.Is ErrBalanceConstraint 与 ErrInsufficientBalance，调用方先判 ErrBalanceConstraint，判到必须回滚。
func (s *Service) Post(ctx context.Context, p Posting) (uuid.UUID, error) {
	if p.Kind.Dedicated() {
		return uuid.Nil, fmt.Errorf("%w: %s 只能走专门的方法", ErrInvalidPosting, p.Kind)
	}
	var id uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var postErr error
		id, postErr = s.postChecked(ctx, p)
		return postErr
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// postChecked 在已有事务里校验并记账，不开新事务。
func (s *Service) postChecked(ctx context.Context, p Posting) (uuid.UUID, error) {
	if err := validatePosting(p); err != nil {
		return uuid.Nil, err
	}
	return s.post(ctx, p)
}

func (s *Service) post(ctx context.Context, p Posting) (uuid.UUID, error) {
	// 先查再写，已记过时不撞唯一索引、调用方事务还能用；撞索引（repo.ErrDuplicate → ErrDuplicatePosting）保留作并发兜底。
	if p.Kind.OnePerSource() && p.SourceID != uuid.Nil {
		exists, err := s.repo.TransactionExists(ctx, string(p.Kind), p.SourceType, p.SourceID)
		if err != nil {
			return uuid.Nil, err
		}
		if exists {
			return uuid.Nil, ErrDuplicatePosting
		}
	}
	accounts, err := s.lockAccounts(ctx, p.Currency, accountKeys(p.Legs))
	if err != nil {
		return uuid.Nil, err
	}
	newBal, err := nextBalances(accounts, p.Legs)
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.repo.SetBalances(ctx, newBal); err != nil {
		return uuid.Nil, setBalancesErr(err)
	}
	actorKind, actorID := actorOf(ctx)
	var sourceType *string
	var sourceID *uuid.UUID
	if p.SourceType != "" {
		sourceType = &p.SourceType
		if p.SourceID != uuid.Nil {
			sid := p.SourceID
			sourceID = &sid
		}
	}
	txn := &model.Transaction{
		Kind:       string(p.Kind),
		Currency:   string(p.Currency),
		SourceType: sourceType,
		SourceID:   sourceID,
		Memo:       p.Memo,
		ActorKind:  actorKind,
		ActorID:    actorID,
	}
	entries := make([]model.Entry, len(p.Legs))
	for i, leg := range p.Legs {
		acc := accounts[leg.Account]
		entries[i] = model.Entry{
			AccountID: acc.ID,
			Currency:  string(p.Currency),
			Direction: string(leg.Direction),
			Amount:    leg.Amount,
		}
	}
	if err := s.repo.InsertTransaction(ctx, txn, entries); err != nil {
		if errors.Is(err, repo.ErrDuplicate) {
			return uuid.Nil, ErrDuplicatePosting
		}
		return uuid.Nil, err
	}
	return txn.ID, nil
}

// lockAccounts 按稳定顺序确保科目存在并加锁，返回科目引用到已锁行的映射。
func (s *Service) lockAccounts(ctx context.Context, cur money.Currency, refs []AccountRef) (map[AccountRef]lockedAccount, error) {
	refs = sortedRefs(refs)
	keys := make([]repo.AccountKey, len(refs))
	for i, ref := range refs {
		keys[i] = keyOf(ref, cur)
	}
	if err := s.repo.EnsureAccounts(ctx, keys); err != nil {
		if errors.Is(err, repo.ErrForeignKey) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	rows, err := s.repo.LockAccounts(ctx, keys)
	if err != nil {
		return nil, err
	}
	if len(rows) != len(keys) {
		return nil, fmt.Errorf("ledger: 有 %d 个科目没锁到", len(keys)-len(rows))
	}
	accounts := make(map[AccountRef]lockedAccount, len(rows))
	for _, row := range rows {
		ref := refOf(row)
		accounts[ref] = lockedAccount{
			ID:      row.ID,
			Type:    AccountType(row.AccountType),
			Balance: row.Balance,
		}
	}
	return accounts, nil
}

func keyOf(ref AccountRef, cur money.Currency) repo.AccountKey {
	k := repo.AccountKey{
		Type:     string(ref.Type),
		Currency: string(cur),
		OwnerRef: ref.Ref,
	}
	if ref.Type.UserOwned() {
		uid := ref.UserID
		k.OwnerUserID = &uid
		k.OwnerRef = ""
	}
	return k
}

func refOf(a model.Account) AccountRef {
	if a.OwnerUserID != nil {
		return UserAccount(AccountType(a.AccountType), *a.OwnerUserID)
	}
	return PlatformAccount(AccountType(a.AccountType), a.OwnerRef)
}

// setBalancesErr 翻译写余额的错误。撞不透支 CHECK 时包装成 ErrBalanceConstraint 与 ErrInsufficientBalance 并保留库错误：
// 预检（nextBalances）已在锁内查过余额，走到这里说明库里的约束兜住了预检漏掉的情况，语句已失败、事务已作废，
// 与写库前查出的余额不足不同，调用方不能接着用这个事务。
func setBalancesErr(err error) error {
	if errors.Is(err, repo.ErrCheck) {
		return fmt.Errorf("%w: %w: %w", ErrBalanceConstraint, ErrInsufficientBalance, err)
	}
	return err
}
