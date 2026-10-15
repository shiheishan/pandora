package service

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestWithMember(t *testing.T) {
	d := func(s string) decimal.Decimal { return decimal.RequireFromString(s) }

	tests := []struct {
		name           string
		subtotal       decimal.Decimal
		discount       decimal.Decimal
		credit         decimal.Decimal
		pct            int
		wantMemberDisc decimal.Decimal
		wantMemberPct  int
		wantTotal      decimal.Decimal
		wantRefund     decimal.Decimal
	}{
		{
			name:           "pct为0：不变、MemberPercent为0",
			subtotal:       d("100"),
			discount:       d("10"),
			credit:         d("0"),
			pct:            0,
			wantMemberDisc: d("0"),
			wantMemberPct:  0,
			wantTotal:      d("90"),
			wantRefund:     d("0"),
		},
		{
			name:           "pct为10：打九折",
			subtotal:       d("100"),
			discount:       d("0"),
			credit:         d("0"),
			pct:            10,
			wantMemberDisc: d("10"),
			wantMemberPct:  10,
			wantTotal:      d("90"),
			wantRefund:     d("0"),
		},
		{
			name:           "pct为10且有券折扣：会员与券相加",
			subtotal:       d("30"),
			discount:       d("5"),
			credit:         d("0"),
			pct:            10,
			wantMemberDisc: d("3"),
			wantMemberPct:  10,
			wantTotal:      d("22"),
			wantRefund:     d("0"),
		},
		{
			name:           "券加会员超过小计被压：会员被压到小计减券",
			subtotal:       d("10"),
			discount:       d("9.50"),
			credit:         d("0"),
			pct:            10,
			wantMemberDisc: d("0.50"),
			wantMemberPct:  10,
			wantTotal:      d("0"),
			wantRefund:     d("0"),
		},
		{
			name:           "券已抵满小计：会员折扣为0、MemberPercent为0",
			subtotal:       d("10"),
			discount:       d("10"),
			credit:         d("0"),
			pct:            10,
			wantMemberDisc: d("0"),
			wantMemberPct:  0,
			wantTotal:      d("0"),
			wantRefund:     d("0"),
		},
		{
			name:           "向零截断：9.99按15%得1.49",
			subtotal:       d("9.99"),
			discount:       d("0"),
			credit:         d("0"),
			pct:            15,
			wantMemberDisc: d("1.49"),
			wantMemberPct:  15,
			wantTotal:      d("8.50"),
			wantRefund:     d("0"),
		},
		{
			name:           "有Credit换套餐降级：Total为0且产生Refund",
			subtotal:       d("30"),
			discount:       d("0"),
			credit:         d("80"),
			pct:            10,
			wantMemberDisc: d("3"),
			wantMemberPct:  10,
			wantTotal:      d("0"),
			wantRefund:     d("53"),
		},
		{
			name:           "有Credit换套餐升级：补差Total为75且Refund为0",
			subtotal:       d("100"),
			discount:       d("0"),
			credit:         d("15"),
			pct:            10,
			wantMemberDisc: d("10"),
			wantMemberPct:  10,
			wantTotal:      d("75"),
			wantRefund:     d("0"),
		},
		{
			name:           "带券加会员加Credit换套餐",
			subtotal:       d("100"),
			discount:       d("20"),
			credit:         d("30"),
			pct:            10,
			wantMemberDisc: d("10"),
			wantMemberPct:  10,
			wantTotal:      d("40"),
			wantRefund:     d("0"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q := Quote{
				Subtotal: tc.subtotal,
				Discount: tc.discount,
				Credit:   tc.credit,
			}
			withMember(&q, tc.pct)

			if !q.MemberDiscount.Equal(tc.wantMemberDisc) {
				t.Fatalf("MemberDiscount: 得到 %s, 期望 %s", q.MemberDiscount, tc.wantMemberDisc)
			}
			if q.MemberPercent != tc.wantMemberPct {
				t.Fatalf("MemberPercent: 得到 %d, 期望 %d", q.MemberPercent, tc.wantMemberPct)
			}
			if !q.Total.Equal(tc.wantTotal) {
				t.Fatalf("Total: 得到 %s, 期望 %s", q.Total, tc.wantTotal)
			}
			if !q.Refund.Equal(tc.wantRefund) {
				t.Fatalf("Refund: 得到 %s, 期望 %s", q.Refund, tc.wantRefund)
			}
		})
	}
}
