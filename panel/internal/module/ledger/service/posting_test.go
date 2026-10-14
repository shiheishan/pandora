package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func validTwoLegPosting() Posting {
	uid := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	return Posting{
		Kind:     KindBalanceTopup,
		Currency: money.CNY,
		Legs: []Leg{
			{Account: PlatformAccount(ChannelCash, "alipay"), Direction: Debit, Amount: decimal.NewFromInt(10)},
			{Account: UserAccount(UserBalance, uid), Direction: Credit, Amount: decimal.NewFromInt(10)},
		},
	}
}

func TestValidatePosting_valid(t *testing.T) {
	if err := validatePosting(validTwoLegPosting()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	p := validTwoLegPosting()
	p.Kind = KindBalanceAdjusted
	p.Memo = "补偿五个字"
	if err := validatePosting(p); err != nil {
		t.Fatalf("balance_adjusted valid memo: %v", err)
	}
}

func TestValidatePosting_errors(t *testing.T) {
	uid := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	base := validTwoLegPosting()

	cases := []struct {
		name string
		mut  func(*Posting)
	}{
		{"kind 非法", func(p *Posting) { p.Kind = Kind("nope") }},
		{"币种非法", func(p *Posting) { p.Currency = money.Currency("EUR") }},
		{"只有一条分录", func(p *Posting) { p.Legs = p.Legs[:1] }},
		{"金额为 0", func(p *Posting) {
			p.Legs[0].Amount = decimal.Zero
			p.Legs[1].Amount = decimal.Zero
		}},
		{"金额为负", func(p *Posting) {
			p.Legs[0].Amount = decimal.NewFromInt(-1)
			p.Legs[1].Amount = decimal.NewFromInt(-1)
		}},
		{"金额 3 位小数", func(p *Posting) {
			a := decimal.RequireFromString("1.001")
			p.Legs[0].Amount = a
			p.Legs[1].Amount = a
		}},
		{"借贷不平", func(p *Posting) { p.Legs[1].Amount = decimal.NewFromInt(9) }},
		{"用户类科目缺 UserID", func(p *Posting) {
			p.Legs[1].Account = AccountRef{Type: UserBalance, UserID: uuid.Nil, Ref: ""}
		}},
		{"平台类科目缺 Ref", func(p *Posting) {
			p.Legs[0].Account = AccountRef{Type: ChannelCash, UserID: uuid.Nil, Ref: ""}
		}},
		{"平台类科目带了 UserID", func(p *Posting) {
			p.Legs[0].Account = AccountRef{Type: ChannelCash, UserID: uid, Ref: "alipay"}
		}},
		{"方向非法", func(p *Posting) { p.Legs[0].Direction = Direction("sideways") }},
		{"来源只给一半 type", func(p *Posting) {
			p.SourceType = SourceOrder
			p.SourceID = uuid.Nil
		}},
		{"来源只给一半 id", func(p *Posting) {
			p.SourceType = ""
			p.SourceID = uuid.New()
		}},
		{"source_type 含大写", func(p *Posting) {
			p.SourceType = "Order"
			p.SourceID = uuid.New()
		}},
		{"备注含 NUL", func(p *Posting) { p.Memo = "a\x00b" }},
		{"备注 501 字", func(p *Posting) { p.Memo = strings.Repeat("字", 501) }},
		{"人工调账备注过短", func(p *Posting) {
			p.Kind = KindBalanceAdjusted
			p.Memo = "  四个字  "
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			// Legs 是切片，先复制一份，免得用例之间互相改到
			p.Legs = append([]Leg(nil), base.Legs...)
			tc.mut(&p)
			err := validatePosting(p)
			if !errors.Is(err, ErrInvalidPosting) {
				t.Fatalf("errors.Is(ErrInvalidPosting)=false, err=%v", err)
			}
		})
	}
}

func TestSignedDelta(t *testing.T) {
	amt := decimal.NewFromInt(10)
	cases := []struct {
		name string
		typ  AccountType
		dir  Direction
		want string
	}{
		{"借方正常-借", ChannelCash, Debit, "10"},
		{"借方正常-贷", ChannelCash, Credit, "-10"},
		{"贷方正常-贷", UserBalance, Credit, "10"},
		{"贷方正常-借", UserBalance, Debit, "-10"},
	}
	for _, tc := range cases {
		got := signedDelta(tc.typ, tc.dir, amt)
		if got.String() != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}

func TestNextBalances(t *testing.T) {
	uid := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	userRef := UserAccount(UserBalance, uid)
	userID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	cashRef := PlatformAccount(ChannelCash, "alipay")
	cashID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	suspRef := PlatformAccount(Suspense, RefManualAdjust)
	suspID := uuid.MustParse("33333333-3333-4333-8333-333333333333")

	t.Run("user_balance 贷 10", func(t *testing.T) {
		accs := map[AccountRef]lockedAccount{
			userRef: {ID: userID, Type: UserBalance, Balance: decimal.NewFromInt(5)},
			cashRef: {ID: cashID, Type: ChannelCash, Balance: decimal.Zero},
		}
		legs := []Leg{
			{Account: cashRef, Direction: Debit, Amount: decimal.NewFromInt(10)},
			{Account: userRef, Direction: Credit, Amount: decimal.NewFromInt(10)},
		}
		out, err := nextBalances(accs, legs)
		if err != nil {
			t.Fatal(err)
		}
		if !out[userID].Equal(decimal.NewFromInt(15)) {
			t.Fatalf("user balance %s", out[userID])
		}
	})

	t.Run("channel_cash 借 10", func(t *testing.T) {
		accs := map[AccountRef]lockedAccount{
			cashRef: {ID: cashID, Type: ChannelCash, Balance: decimal.NewFromInt(3)},
			userRef: {ID: userID, Type: UserBalance, Balance: decimal.NewFromInt(10)},
		}
		legs := []Leg{
			{Account: cashRef, Direction: Debit, Amount: decimal.NewFromInt(10)},
			{Account: userRef, Direction: Credit, Amount: decimal.NewFromInt(10)},
		}
		out, err := nextBalances(accs, legs)
		if err != nil {
			t.Fatal(err)
		}
		if !out[cashID].Equal(decimal.NewFromInt(13)) {
			t.Fatalf("cash %s", out[cashID])
		}
	})

	t.Run("user_balance 透支", func(t *testing.T) {
		accs := map[AccountRef]lockedAccount{
			userRef: {ID: userID, Type: UserBalance, Balance: decimal.NewFromInt(5)},
			cashRef: {ID: cashID, Type: ChannelCash, Balance: decimal.NewFromInt(100)},
		}
		legs := []Leg{
			{Account: userRef, Direction: Debit, Amount: decimal.NewFromInt(10)},
			{Account: cashRef, Direction: Credit, Amount: decimal.NewFromInt(10)},
		}
		_, err := nextBalances(accs, legs)
		if !errors.Is(err, ErrInsufficientBalance) {
			t.Fatalf("err=%v", err)
		}
	})

	t.Run("suspense 可负", func(t *testing.T) {
		accs := map[AccountRef]lockedAccount{
			suspRef: {ID: suspID, Type: Suspense, Balance: decimal.NewFromInt(1)},
			userRef: {ID: userID, Type: UserBalance, Balance: decimal.NewFromInt(100)},
		}
		legs := []Leg{
			{Account: userRef, Direction: Debit, Amount: decimal.NewFromInt(10)},
			{Account: suspRef, Direction: Credit, Amount: decimal.NewFromInt(10)},
		}
		out, err := nextBalances(accs, legs)
		if err != nil {
			t.Fatal(err)
		}
		if !out[suspID].Equal(decimal.NewFromInt(-9)) {
			t.Fatalf("suspense %s", out[suspID])
		}
	})

	t.Run("同一科目两次净额", func(t *testing.T) {
		accs := map[AccountRef]lockedAccount{
			userRef: {ID: userID, Type: UserBalance, Balance: decimal.NewFromInt(20)},
			cashRef: {ID: cashID, Type: ChannelCash, Balance: decimal.Zero},
		}
		legs := []Leg{
			{Account: userRef, Direction: Debit, Amount: decimal.NewFromInt(5)},
			{Account: userRef, Direction: Credit, Amount: decimal.NewFromInt(3)},
			{Account: cashRef, Direction: Credit, Amount: decimal.NewFromInt(2)},
		}
		out, err := nextBalances(accs, legs)
		if err != nil {
			t.Fatal(err)
		}
		if !out[userID].Equal(decimal.NewFromInt(18)) {
			t.Fatalf("user %s want 18", out[userID])
		}
	})

	t.Run("科目不在 accounts", func(t *testing.T) {
		missing := UserAccount(UserBalance, uuid.New())
		accs := map[AccountRef]lockedAccount{
			cashRef: {ID: cashID, Type: ChannelCash, Balance: decimal.Zero},
		}
		legs := []Leg{
			{Account: missing, Direction: Credit, Amount: decimal.NewFromInt(1)},
			{Account: cashRef, Direction: Debit, Amount: decimal.NewFromInt(1)},
		}
		_, err := nextBalances(accs, legs)
		if !errors.Is(err, ErrInvalidPosting) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestAccountKeys_dedupAndOrder(t *testing.T) {
	u1 := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	u2 := uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	legs := []Leg{
		{Account: PlatformAccount(Suspense, "z"), Direction: Debit, Amount: decimal.NewFromInt(1)},
		{Account: UserAccount(UserBalance, u2), Direction: Credit, Amount: decimal.NewFromInt(1)},
		{Account: PlatformAccount(Suspense, "z"), Direction: Credit, Amount: decimal.NewFromInt(1)},
		{Account: UserAccount(UserBalance, u1), Direction: Debit, Amount: decimal.NewFromInt(1)},
		{Account: PlatformAccount(ChannelCash, "a"), Direction: Debit, Amount: decimal.NewFromInt(1)},
	}
	keys := accountKeys(legs)
	if len(keys) != 4 {
		t.Fatalf("len=%d want 4", len(keys))
	}
	want := []AccountRef{
		PlatformAccount(ChannelCash, "a"),
		PlatformAccount(Suspense, "z"),
		UserAccount(UserBalance, u1),
		UserAccount(UserBalance, u2),
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("[%d] got %+v want %+v", i, keys[i], want[i])
		}
	}
	keys2 := accountKeys(legs)
	for i := range keys {
		if keys[i] != keys2[i] {
			t.Fatal("order not stable")
		}
	}
}

func TestKindDedicated(t *testing.T) {
	for _, k := range []Kind{KindBalanceHold, KindBalanceRelease, KindOrderPaid, KindBalanceAdjusted} {
		if !k.Dedicated() {
			t.Fatalf("%s should be dedicated", k)
		}
	}
	for _, k := range []Kind{KindBalanceTopup, KindCommissionToBalance} {
		if k.Dedicated() {
			t.Fatalf("%s should not be dedicated", k)
		}
	}
}

func TestKindOnePerSource(t *testing.T) {
	for _, k := range []Kind{
		KindBalanceTopup, KindBalanceHold, KindBalanceRelease, KindOrderPaid,
		KindLatePaymentSuspense, KindPlanChangeRefund, KindCommissionSettled,
		KindCommissionPaid, KindGiftCardBalance, KindCommissionVoided,
	} {
		if !k.OnePerSource() {
			t.Fatalf("%s should be one per source", k)
		}
	}
	for _, k := range []Kind{
		KindCommissionAccrued, KindCommissionToBalance, KindBalanceAdjusted, KindLatePaymentApplied,
	} {
		if k.OnePerSource() {
			t.Fatalf("%s should not be one per source", k)
		}
	}
}

func TestSortedRefs(t *testing.T) {
	u1 := uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	u2 := uuid.MustParse("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	refs := []AccountRef{
		PlatformAccount(Suspense, "z"),
		UserAccount(UserBalance, u2),
		PlatformAccount(Suspense, "z"),
		UserAccount(UserBalance, u1),
		PlatformAccount(ChannelCash, "a"),
	}
	got := sortedRefs(refs)
	want := accountKeys([]Leg{
		{Account: PlatformAccount(ChannelCash, "a"), Direction: Debit, Amount: decimal.NewFromInt(1)},
		{Account: PlatformAccount(Suspense, "z"), Direction: Credit, Amount: decimal.NewFromInt(1)},
		{Account: UserAccount(UserBalance, u1), Direction: Debit, Amount: decimal.NewFromInt(1)},
		{Account: UserAccount(UserBalance, u2), Direction: Credit, Amount: decimal.NewFromInt(1)},
	})
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %+v want %+v", i, got[i], want[i])
		}
	}
}

// 写余额撞库里不透支 CHECK：包装成余额不足（调用方 errors.Is 仍认得），同时保留库错误并注明事务已作废；其它错误原样返回。
func TestSetBalancesErrWrapsCheck(t *testing.T) {
	dbErr := fmt.Errorf("%w: 违反 CHECK", repo.ErrCheck)
	got := setBalancesErr(dbErr)
	if !errors.Is(got, ErrInsufficientBalance) {
		t.Fatalf("应能按 ErrInsufficientBalance 识别：%v", got)
	}
	if !errors.Is(got, repo.ErrCheck) {
		t.Fatalf("应保留库约束错误：%v", got)
	}
	if got == ErrInsufficientBalance {
		t.Fatal("库约束兜底不能与写库前查出的余额不足是同一个值")
	}
	if !strings.Contains(got.Error(), "事务已作废") {
		t.Fatalf("错误应注明事务已作废：%q", got.Error())
	}

	other := errors.New("连接断了")
	if setBalancesErr(other) != other {
		t.Fatal("非约束错误应原样返回")
	}
}
