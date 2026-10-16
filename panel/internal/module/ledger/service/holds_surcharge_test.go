package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestOrderPaidLegsSurcharge(t *testing.T) {
	orderID := uuid.New()
	userID := uuid.New()
	p := OrderPaid{
		OrderID:   orderID,
		UserID:    userID,
		Total:     decimal.RequireFromString("10.00"),
		Paid:      decimal.RequireFromString("10.00"),
		Fee:       decimal.RequireFromString("0.05"),
		Surcharge: decimal.RequireFromString("0.30"),
		Provider:  "epay1",
	}
	applied := p.Total.Sub(p.Paid) // 0
	legs := orderPaidLegs(p, applied)

	want := []Leg{
		{Account: PlatformAccount(ChannelCash, "epay1"), Direction: Debit, Amount: decimal.RequireFromString("10.25")},
		{Account: PlatformAccount(PlatformFeeExpense, "epay1"), Direction: Debit, Amount: decimal.RequireFromString("0.05")},
		{Account: PlatformAccount(PlatformRevenue, RefMain), Direction: Credit, Amount: decimal.RequireFromString("10.00")},
		{Account: PlatformAccount(PlatformRevenue, RefSurcharge), Direction: Credit, Amount: decimal.RequireFromString("0.30")},
	}

	if len(legs) != len(want) {
		t.Fatalf("legs count mismatch: got %d, want %d", len(legs), len(want))
	}
	for i, l := range legs {
		w := want[i]
		if l.Account != w.Account || l.Direction != w.Direction || !l.Amount.Equal(w.Amount) {
			t.Errorf("[%d] got %+v, want %+v", i, l, w)
		}
	}

	var sumDebit, sumCredit decimal.Decimal
	for _, l := range legs {
		if l.Direction == Debit {
			sumDebit = sumDebit.Add(l.Amount)
		} else {
			sumCredit = sumCredit.Add(l.Amount)
		}
	}
	if !sumDebit.Equal(sumCredit) {
		t.Errorf("debit (%s) != credit (%s)", sumDebit, sumCredit)
	}
}

func TestOrderPaidLegsSurchargeWithBalance(t *testing.T) {
	orderID := uuid.New()
	userID := uuid.New()
	p := OrderPaid{
		OrderID:   orderID,
		UserID:    userID,
		Total:     decimal.RequireFromString("10.00"),
		Paid:      decimal.RequireFromString("6.00"),
		Fee:       decimal.Zero,
		Surcharge: decimal.RequireFromString("0.12"),
		Provider:  "epay1",
	}
	applied := p.Total.Sub(p.Paid) // 4.00
	legs := orderPaidLegs(p, applied)

	want := []Leg{
		{Account: PlatformAccount(ChannelCash, "epay1"), Direction: Debit, Amount: decimal.RequireFromString("6.12")},
		{Account: UserAccount(UserBalanceHold, userID), Direction: Debit, Amount: decimal.RequireFromString("4.00")},
		{Account: PlatformAccount(PlatformRevenue, RefMain), Direction: Credit, Amount: decimal.RequireFromString("10.00")},
		{Account: PlatformAccount(PlatformRevenue, RefSurcharge), Direction: Credit, Amount: decimal.RequireFromString("0.12")},
	}

	if len(legs) != len(want) {
		t.Fatalf("legs count mismatch: got %d, want %d", len(legs), len(want))
	}
	for i, l := range legs {
		w := want[i]
		if l.Account != w.Account || l.Direction != w.Direction || !l.Amount.Equal(w.Amount) {
			t.Errorf("[%d] got %+v, want %+v", i, l, w)
		}
	}

	var sumDebit, sumCredit decimal.Decimal
	for _, l := range legs {
		if l.Direction == Debit {
			sumDebit = sumDebit.Add(l.Amount)
		} else {
			sumCredit = sumCredit.Add(l.Amount)
		}
	}
	if !sumDebit.Equal(sumCredit) {
		t.Errorf("debit (%s) != credit (%s)", sumDebit, sumCredit)
	}
}

func TestOrderPaidLegsNoSurcharge(t *testing.T) {
	orderID := uuid.New()
	userID := uuid.New()
	p := OrderPaid{
		OrderID:   orderID,
		UserID:    userID,
		Total:     decimal.RequireFromString("10.00"),
		Paid:      decimal.RequireFromString("10.00"),
		Fee:       decimal.RequireFromString("0.05"),
		Surcharge: decimal.Zero,
		Provider:  "epay1",
	}
	applied := p.Total.Sub(p.Paid)
	legs := orderPaidLegs(p, applied)

	want := []Leg{
		{Account: PlatformAccount(ChannelCash, "epay1"), Direction: Debit, Amount: decimal.RequireFromString("9.95")},
		{Account: PlatformAccount(PlatformFeeExpense, "epay1"), Direction: Debit, Amount: decimal.RequireFromString("0.05")},
		{Account: PlatformAccount(PlatformRevenue, RefMain), Direction: Credit, Amount: decimal.RequireFromString("10.00")},
	}

	if len(legs) != len(want) {
		t.Fatalf("legs count mismatch: got %d, want %d", len(legs), len(want))
	}
	for i, l := range legs {
		w := want[i]
		if l.Account != w.Account || l.Direction != w.Direction || !l.Amount.Equal(w.Amount) {
			t.Errorf("[%d] got %+v, want %+v", i, l, w)
		}
	}
}

func TestValidateOrderPaidSurcharge(t *testing.T) {
	orderID := uuid.New()
	userID := uuid.New()

	base := OrderPaid{
		OrderID:   orderID,
		UserID:    userID,
		Total:     decimal.RequireFromString("10.00"),
		Paid:      decimal.RequireFromString("10.00"),
		Fee:       decimal.RequireFromString("0.50"),
		Surcharge: decimal.RequireFromString("0.30"),
		Provider:  "epay1",
	}

	// 合法基础用例通过
	if err := validateOrderPaid(base); err != nil {
		t.Fatalf("base should be valid, got: %v", err)
	}

	// Fee = Paid + Surcharge 合法
	atCap := base
	atCap.Fee = atCap.Paid.Add(atCap.Surcharge)
	if err := validateOrderPaid(atCap); err != nil {
		t.Fatalf("Fee = Paid + Surcharge should be valid, got: %v", err)
	}

	// 错误用例
	cases := []struct {
		name string
		mut  func(*OrderPaid)
	}{
		{"Surcharge 为负", func(p *OrderPaid) { p.Surcharge = decimal.RequireFromString("-0.01") }},
		{"Surcharge 三位小数", func(p *OrderPaid) { p.Surcharge = decimal.RequireFromString("0.001") }},
		{"Paid=0 而 Surcharge>0", func(p *OrderPaid) {
			p.Paid = decimal.Zero
			p.Fee = decimal.Zero
			p.Surcharge = decimal.RequireFromString("0.30")
		}},
		{"Fee > Paid+Surcharge", func(p *OrderPaid) {
			p.Fee = p.Paid.Add(p.Surcharge).Add(decimal.RequireFromString("0.01"))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			err := validateOrderPaid(p)
			if !errors.Is(err, ErrInvalidPosting) {
				t.Fatalf("expected ErrInvalidPosting, got: %v", err)
			}
		})
	}
}
