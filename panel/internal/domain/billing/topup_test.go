package billing

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/middleware"
)

func TestBuildTopupPaidEntrySpecs(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		fee    int64
		want   []topupPaidEntrySpec
	}{
		{
			name:   "zero fee",
			amount: 1000,
			want: []topupPaidEntrySpec{
				{AccountType: AccountChannelCash, Direction: Debit, Amount: 1000, Description: "渠道净收款"},
				{AccountType: AccountUserBalance, Direction: Credit, Amount: 1000, Description: "用户余额增加"},
			},
		},
		{
			name:   "positive fee",
			amount: 1000,
			fee:    30,
			want: []topupPaidEntrySpec{
				{AccountType: AccountChannelCash, Direction: Debit, Amount: 970, Description: "渠道净收款"},
				{AccountType: AccountPlatformFeeExpense, Direction: Debit, Amount: 30, Description: "渠道手续费"},
				{AccountType: AccountUserBalance, Direction: Credit, Amount: 1000, Description: "用户余额增加"},
			},
		},
		{
			name:   "fee equals amount",
			amount: 1000,
			fee:    1000,
			want: []topupPaidEntrySpec{
				{AccountType: AccountPlatformFeeExpense, Direction: Debit, Amount: 1000, Description: "渠道手续费"},
				{AccountType: AccountUserBalance, Direction: Credit, Amount: 1000, Description: "用户余额增加"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildTopupPaidEntrySpecs(tt.amount, tt.fee)
			if err != nil {
				t.Fatalf("buildTopupPaidEntrySpecs() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildTopupPaidEntrySpecs() = %#v, want %#v", got, tt.want)
			}

			var debits, credits int64
			for _, entry := range got {
				if entry.AccountType == AccountPlatformRevenue {
					t.Fatal("topup posting must not include platform revenue")
				}
				switch entry.Direction {
				case Debit:
					debits += entry.Amount
				case Credit:
					credits += entry.Amount
				default:
					t.Fatalf("unexpected direction %q", entry.Direction)
				}
			}
			if debits != credits {
				t.Fatalf("posting is unbalanced: debits=%d credits=%d", debits, credits)
			}
		})
	}
}

func TestBuildTopupPaidEntrySpecsRejectsInvalidAmounts(t *testing.T) {
	tests := []struct {
		name   string
		amount int64
		fee    int64
	}{
		{name: "zero amount", amount: 0},
		{name: "negative amount", amount: -1},
		{name: "negative fee", amount: 1000, fee: -1},
		{name: "fee exceeds amount", amount: 1000, fee: 1001},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildTopupPaidEntrySpecs(tt.amount, tt.fee); err == nil {
				t.Fatal("buildTopupPaidEntrySpecs() error = nil, want error")
			}
		})
	}
}

func TestValidateTopupAmountBoundaries(t *testing.T) {
	tests := []struct {
		amount  int64
		wantErr bool
	}{
		{99, true},
		{100, false},
		{5000000, false},
		{5000001, true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.amount), func(t *testing.T) {
			err := validateTopupAmount(test.amount)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateTopupAmount(%d) error = %v", test.amount, err)
			}
		})
	}
}

func TestNormalizeTopupCurrencyIsStrict(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"", "CNY", false},
		{"CNY", "CNY", false},
		{"USD", "USD", false},
		{"cny", "", true},
		{"usd", "", true},
		{" CNY", "", true},
		{"USD ", "", true},
		{"EUR", "", true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%q", test.input), func(t *testing.T) {
			got, err := normalizeTopupCurrency(test.input)
			if got != test.want || (err != nil) != test.wantErr {
				t.Fatalf("normalizeTopupCurrency(%q) = %q/%v", test.input, got, err)
			}
		})
	}
}

func TestCreateTopupRejectsMissingClaimBeforeDatabaseAccess(t *testing.T) {
	service := &Service{}
	if _, err := service.CreateTopup(context.Background(),
		"91000000-0000-7000-8000-000000000001", CreateTopupInput{
			UserID: "91000000-0000-7000-8000-000000000011",
			Amount: 100,
		}); err == nil {
		t.Fatal("CreateTopup accepted a missing claim")
	}
}

func TestCreateTopupRejectsInvalidCurrencyBeforeDatabaseAccess(t *testing.T) {
	tenantID := "91000000-0000-7000-8000-000000000001"
	actorID := "91000000-0000-7000-8000-000000000011"
	actorHash := sha256.Sum256([]byte(actorID))
	requestHash := sha256.Sum256([]byte("topup-request"))
	claim := middleware.IdempotencyClaim{
		ID:           "91000000-0000-7000-8000-000000000101",
		TenantID:     tenantID,
		ActorID:      actorID,
		Scope:        TopupIdempotencyScope,
		StorageScope: fmt.Sprintf("%s:actor:%x", TopupIdempotencyScope, actorHash[:12]),
		Key:          "topup-key",
		Generation:   1,
		LockedUntil:  time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		RequestHash:  requestHash,
	}
	service := &Service{}
	if _, err := service.CreateTopup(context.Background(), tenantID, CreateTopupInput{
		UserID: actorID, Amount: 100, Currency: "usd", Claim: claim,
	}); err == nil {
		t.Fatal("CreateTopup accepted a non-canonical currency")
	}
}
