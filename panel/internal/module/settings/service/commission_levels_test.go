package service

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func newCommissionLevelsService(st *kvStore, al *auditLog) *Service {
	return newCommissionService(st, al)
}

func TestCommissionDistributionWithdrawDefaultsEmptyStore(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newCommissionLevelsService(st, &auditLog{})
	ctx := context.Background()

	d, err := s.CommissionDistribution(ctx)
	if err != nil {
		t.Fatalf("distribution: %v", err)
	}
	if d.Enabled || d.Levels[0] != 0 || d.Levels[1] != 0 || d.Levels[2] != 0 {
		t.Fatalf("distribution defaults: enabled=%v levels=%v", d.Enabled, d.Levels)
	}

	w, err := s.CommissionWithdraw(ctx)
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if !w.Enabled || !reflect.DeepEqual(w.Methods, []string{WithdrawMethodAlipay}) {
		t.Fatalf("withdraw defaults: enabled=%v methods=%v", w.Enabled, w.Methods)
	}
}

func TestDecodeCommissionWithdrawIgnoresUnknownMethods(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionWithdrawMethods: json.RawMessage(`"usdt_trc20,alipay,bogus,alipay"`),
	}}
	s := newCommissionLevelsService(st, &auditLog{})
	w, err := s.CommissionWithdraw(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{WithdrawMethodAlipay, WithdrawMethodUSDTTRC20}
	if !reflect.DeepEqual(w.Methods, want) {
		t.Fatalf("methods: %v want %v", w.Methods, want)
	}
}

func TestCommissionDistributionCorruptL2(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionDistributionL2: json.RawMessage(`101`),
	}}
	s := newCommissionLevelsService(st, &auditLog{})
	if _, err := s.CommissionDistribution(context.Background()); err == nil {
		t.Fatal("l2 101: expected error")
	}
}

func TestCommissionDistributionCorruptSum(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keyCommissionDistributionEnabled: json.RawMessage(`true`),
		keyCommissionDistributionL1:      json.RawMessage(`80`),
		keyCommissionDistributionL2:      json.RawMessage(`80`),
		keyCommissionDistributionL3:      json.RawMessage(`80`),
	}}
	s := newCommissionLevelsService(st, &auditLog{})
	if _, err := s.CommissionDistribution(context.Background()); err == nil {
		t.Fatal("80/80/80: expected error")
	}
	// 关着时计提不用份额，合计不查
	st.kv[keyCommissionDistributionEnabled] = json.RawMessage(`false`)
	if _, err := s.CommissionDistribution(context.Background()); err != nil {
		t.Fatalf("关着时 80/80/80 应能读出: %v", err)
	}
	st.kv[keyCommissionDistributionEnabled] = json.RawMessage(`true`)
	// 合计恰好 100 照常读出
	st.kv[keyCommissionDistributionL1] = json.RawMessage(`50`)
	st.kv[keyCommissionDistributionL2] = json.RawMessage(`30`)
	st.kv[keyCommissionDistributionL3] = json.RawMessage(`20`)
	d, err := s.CommissionDistribution(context.Background())
	if err != nil || d.Levels != [3]int{50, 30, 20} {
		t.Fatalf("50/30/20: %+v err=%v", d, err)
	}
}

func TestSetCommissionDistributionValidation(t *testing.T) {
	s := newCommissionLevelsService(&kvStore{kv: map[string]json.RawMessage{}}, &auditLog{})
	ctx := context.Background()

	l150 := 150
	err := s.SetCommissionDistribution(ctx, CommissionDistributionUpdate{L1: &l150})
	assertFieldValidation(t, err, "distribution_l1")

	enabled := true
	l60, l30, l20 := 60, 30, 20
	err = s.SetCommissionDistribution(ctx, CommissionDistributionUpdate{Enabled: &enabled, L1: &l60, L2: &l30, L3: &l20})
	assertFieldValidation(t, err, "distribution_l1")

	z0, z1, z2 := 0, 0, 0
	err = s.SetCommissionDistribution(ctx, CommissionDistributionUpdate{Enabled: &enabled, L1: &z0, L2: &z1, L3: &z2})
	assertFieldValidation(t, err, "distribution_l1")
}

func TestSetCommissionDistributionPersistsAndAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newCommissionLevelsService(st, al)
	ctx := context.Background()

	enabled := true
	l60, l30, l10 := 60, 30, 10
	if err := s.SetCommissionDistribution(ctx, CommissionDistributionUpdate{
		Enabled: &enabled, L1: &l60, L2: &l30, L3: &l10,
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	d, err := s.CommissionDistribution(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !d.Enabled || d.Levels[0] != 60 || d.Levels[1] != 30 || d.Levels[2] != 10 {
		t.Fatalf("read back: %+v", d)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditCommissionChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit: %s %s", e.action, e.resourceType)
	}

	if err := s.SetCommissionDistribution(ctx, CommissionDistributionUpdate{
		Enabled: &enabled, L1: &l60, L2: &l30, L3: &l10,
	}); err != nil {
		t.Fatalf("noop set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}
}

func TestSetCommissionWithdrawValidation(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newCommissionLevelsService(st, &auditLog{})
	ctx := context.Background()

	enabled := true
	bad := []string{WithdrawMethodAlipay, "foo"}
	err := s.SetCommissionWithdraw(ctx, CommissionWithdrawUpdate{Enabled: &enabled, Methods: &bad})
	assertFieldValidation(t, err, "withdraw_methods")

	empty := []string{}
	err = s.SetCommissionWithdraw(ctx, CommissionWithdrawUpdate{Enabled: &enabled, Methods: &empty})
	assertFieldValidation(t, err, "withdraw_methods")

	// 关着提现也不许全取消：其他币种还按这些方式提现
	disabled := false
	err = s.SetCommissionWithdraw(ctx, CommissionWithdrawUpdate{Enabled: &disabled, Methods: &empty})
	assertFieldValidation(t, err, "withdraw_methods")
	if got := errs.As(err).Fields["withdraw_methods"]; got != "关着提现时其他币种的佣金仍按这里的方式提现，至少要选一种" {
		t.Fatalf("关着提现全取消的提示 %q", got)
	}
	alipay := []string{WithdrawMethodAlipay}
	if err := s.SetCommissionWithdraw(ctx, CommissionWithdrawUpdate{Enabled: &disabled, Methods: &alipay}); err != nil {
		t.Fatalf("关着提现留一种方式应能保存: %v", err)
	}
}

func TestSetCommissionWithdrawMethodsStoredNormalized(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newCommissionLevelsService(st, &auditLog{})
	ctx := context.Background()

	methods := []string{WithdrawMethodUSDTBEP20, WithdrawMethodAlipay, WithdrawMethodUSDTBEP20}
	if err := s.SetCommissionWithdraw(ctx, CommissionWithdrawUpdate{Methods: &methods}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if string(st.kv[keyCommissionWithdrawMethods]) != `"alipay,usdt_bep20"` {
		t.Fatalf("stored: %s", st.kv[keyCommissionWithdrawMethods])
	}
}

func TestNormalizeWithdrawMethods(t *testing.T) {
	cases := []struct {
		in       []string
		want     []string
		allValid bool
	}{
		{[]string{"usdt_bep20", "alipay", "usdt_bep20"}, []string{"alipay", "usdt_bep20"}, true},
		{[]string{"alipay", "other"}, []string{"alipay"}, false},
		{[]string{"other"}, nil, false},
	}
	for _, tc := range cases {
		got, ok := normalizeWithdrawMethods(tc.in)
		if ok != tc.allValid {
			t.Fatalf("in=%v valid: got %v want %v", tc.in, ok, tc.allValid)
		}
		if tc.want == nil {
			if len(got) != 0 {
				t.Fatalf("in=%v: got %v want empty", tc.in, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("in=%v: got %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidWithdrawMethod(t *testing.T) {
	cases := []struct {
		m    string
		want bool
	}{
		{WithdrawMethodAlipay, true},
		{"other", false},
		{"", false},
	}
	for _, tc := range cases {
		if ValidWithdrawMethod(tc.m) != tc.want {
			t.Fatalf("ValidWithdrawMethod(%q)=%v want %v", tc.m, !tc.want, tc.want)
		}
	}
}
