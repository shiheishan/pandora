package service

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var sourceTypePattern = regexp.MustCompile("^[a-z_]{1,32}$")

// lockedAccount 是已锁住的科目：ID、类型与当前余额。
type lockedAccount struct {
	ID      uuid.UUID
	Type    AccountType
	Balance decimal.Decimal
}

// validatePosting 校验一笔交易：kind 与币种合法；来源成对且 source_type 匹配 ^[a-z_]{1,32}$；
// 备注是合法 UTF-8、不含 NUL、不超过 500 字（按 rune 计）；至少两条分录；
// 每条分录科目类型合法、归属形状对、方向合法、金额 > 0 且 money.Check 通过；借贷相等。
func validatePosting(p Posting) error {
	if !p.Kind.Valid() {
		return fmt.Errorf("%w: 交易类型不合法", ErrInvalidPosting)
	}
	if !p.Currency.Valid() {
		return fmt.Errorf("%w: 币种不合法", ErrInvalidPosting)
	}
	sourceTypeEmpty := p.SourceType == ""
	sourceIDEmpty := p.SourceID == uuid.Nil
	if sourceTypeEmpty != sourceIDEmpty {
		return fmt.Errorf("%w: 来源类型与来源 ID 必须成对出现", ErrInvalidPosting)
	}
	if !sourceTypeEmpty && !sourceTypePattern.MatchString(p.SourceType) {
		return fmt.Errorf("%w: 来源类型格式不合法", ErrInvalidPosting)
	}
	if !utf8.ValidString(p.Memo) {
		return fmt.Errorf("%w: 备注不是合法 UTF-8", ErrInvalidPosting)
	}
	if strings.ContainsRune(p.Memo, 0) {
		return fmt.Errorf("%w: 备注不能包含 NUL", ErrInvalidPosting)
	}
	if utf8.RuneCountInString(p.Memo) > 500 {
		return fmt.Errorf("%w: 备注超过 500 字", ErrInvalidPosting)
	}
	if len(p.Legs) < 2 {
		return fmt.Errorf("%w: 至少需要两条分录", ErrInvalidPosting)
	}

	var debitSum, creditSum decimal.Decimal
	for i, leg := range p.Legs {
		if err := validateLeg(leg); err != nil {
			return fmt.Errorf("%w: 分录 %d: %v", ErrInvalidPosting, i, err)
		}
		switch leg.Direction {
		case Debit:
			debitSum = debitSum.Add(leg.Amount)
		case Credit:
			creditSum = creditSum.Add(leg.Amount)
		}
	}
	if !debitSum.Equal(creditSum) {
		return fmt.Errorf("%w: 借贷不平", ErrInvalidPosting)
	}
	return nil
}

func validateLeg(leg Leg) error {
	ref := leg.Account
	if !ref.Type.Valid() {
		return fmt.Errorf("科目类型不合法")
	}
	if ref.Type.UserOwned() {
		if ref.UserID == uuid.Nil {
			return fmt.Errorf("用户类科目必须指定用户")
		}
		if ref.Ref != "" {
			return fmt.Errorf("用户类科目不能带 owner_ref")
		}
	} else {
		if ref.UserID != uuid.Nil {
			return fmt.Errorf("平台类科目不能指定用户")
		}
		if ref.Ref == "" {
			return fmt.Errorf("平台类科目必须指定 owner_ref")
		}
		if len(ref.Ref) > 64 {
			return fmt.Errorf("owner_ref 过长")
		}
	}
	if leg.Direction != Debit && leg.Direction != Credit {
		return fmt.Errorf("分录方向不合法")
	}
	if !leg.Amount.IsPositive() {
		return fmt.Errorf("分录金额必须大于 0")
	}
	if err := money.Check(leg.Amount); err != nil {
		return err
	}
	return nil
}

// nextBalances 在已锁住的科目余额上依次应用分录，返回发生变化的科目的新余额（按科目 ID）。
func nextBalances(accounts map[AccountRef]lockedAccount, legs []Leg) (map[uuid.UUID]decimal.Decimal, error) {
	work := make(map[AccountRef]decimal.Decimal)
	for ref, acc := range accounts {
		work[ref] = acc.Balance
	}
	for _, leg := range legs {
		ref := leg.Account
		acc, ok := accounts[ref]
		if !ok {
			return nil, fmt.Errorf("%w: 分录引用了未锁定的科目", ErrInvalidPosting)
		}
		work[ref] = work[ref].Add(signedDelta(acc.Type, leg.Direction, leg.Amount))
	}
	out := make(map[uuid.UUID]decimal.Decimal)
	seen := make(map[AccountRef]struct{})
	for _, leg := range legs {
		ref := leg.Account
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		acc := accounts[ref]
		bal := work[ref]
		if acc.Type.UserOwned() && bal.IsNegative() {
			return nil, ErrInsufficientBalance
		}
		out[acc.ID] = bal
	}
	return out, nil
}

// signedDelta 返回一条分录对其科目余额的有符号影响（正常方向为正）。
func signedDelta(t AccountType, d Direction, amount decimal.Decimal) decimal.Decimal {
	if t.DebitNormal() {
		if d == Debit {
			return amount
		}
		return amount.Neg()
	}
	if d == Credit {
		return amount
	}
	return amount.Neg()
}

// accountKeys 把分录里的科目去重并按 (Type, UserID.String(), Ref) 升序排成稳定顺序。
func accountKeys(legs []Leg) []AccountRef {
	var keys []AccountRef
	seen := make(map[AccountRef]struct{})
	for _, leg := range legs {
		ref := leg.Account
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		keys = append(keys, ref)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Type != b.Type {
			return string(a.Type) < string(b.Type)
		}
		ua, ub := a.UserID.String(), b.UserID.String()
		if ua != ub {
			return ua < ub
		}
		return a.Ref < b.Ref
	})
	return keys
}
