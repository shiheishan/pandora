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
// 返回错误后调用方事务必须回滚（库里语句出错后事务已不可用）。
func (s *Service) Post(ctx context.Context, p Posting) (uuid.UUID, error) {
	if err := validatePosting(p); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var postErr error
		id, postErr = s.post(ctx, p)
		return postErr
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (s *Service) post(ctx context.Context, p Posting) (uuid.UUID, error) {
	refs := accountKeys(p.Legs)
	keys := make([]repo.AccountKey, len(refs))
	for i, ref := range refs {
		keys[i] = keyOf(ref, p.Currency)
	}
	if err := s.repo.EnsureAccounts(ctx, keys); err != nil {
		if errors.Is(err, repo.ErrForeignKey) {
			return uuid.Nil, ErrUserNotFound
		}
		return uuid.Nil, err
	}
	rows, err := s.repo.LockAccounts(ctx, keys)
	if err != nil {
		return uuid.Nil, err
	}
	if len(rows) != len(keys) {
		return uuid.Nil, fmt.Errorf("ledger: 有 %d 个科目没锁到", len(keys)-len(rows))
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
	newBal, err := nextBalances(accounts, p.Legs)
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.repo.SetBalances(ctx, newBal); err != nil {
		if errors.Is(err, repo.ErrCheck) {
			return uuid.Nil, ErrInsufficientBalance
		}
		return uuid.Nil, err
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
