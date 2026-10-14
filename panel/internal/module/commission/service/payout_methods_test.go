package service

import (
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

func TestValidTRC20Address(t *testing.T) {
	ok := []string{
		"TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV",
		"TD5gsCwxykWsLN9aPrq2TAfNjByuZKYp4E",
	}
	for _, addr := range ok {
		if !ValidTRC20Address(addr) {
			t.Errorf("期望通过: %q", addr)
		}
	}
	bad := []string{
		"TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdW",
		"Tas3vExdmvHHZhkC1z5ByHT3Szd491AdKz",
		"TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPd",
		"TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPd0",
		"",
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
	}
	for _, addr := range bad {
		if ValidTRC20Address(addr) {
			t.Errorf("期望不通过: %q", addr)
		}
	}
}

func TestValidEVMAddress(t *testing.T) {
	ok := []string{
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0xfB6916095ca1df60bB79Ce92cE3Ea74c37c5d359",
		"0xdbF03B407c01E7cD3CBea99509d93f8DDDC8C6FB",
		"0xD1220A0cf47c7B9Be7A2E6BA89F429762e7b9aDb",
		"0x5aaeb6053f3e94c9b9a09f33669435e7ef1beaed",
		"0x5AAEB6053F3E94C9B9A09F33669435E7EF1BEAED",
	}
	for _, addr := range ok {
		if !ValidEVMAddress(addr) {
			t.Errorf("期望通过: %q", addr)
		}
	}
	bad := []string{
		"0x5AAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAe",
		"5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed",
		"0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeg",
	}
	for _, addr := range bad {
		if ValidEVMAddress(addr) {
			t.Errorf("期望不通过: %q", addr)
		}
	}
}

func TestValidTxHash(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	if !ValidTxHash(settingssvc.WithdrawMethodUSDTTRC20, h64) {
		t.Fatal("TRC20 64 位十六进制应通过")
	}
	if ValidTxHash(settingssvc.WithdrawMethodUSDTTRC20, "0x"+h64) {
		t.Fatal("TRC20 带 0x 应不通过")
	}
	if ValidTxHash(settingssvc.WithdrawMethodUSDTTRC20, strings.Repeat("a", 63)) {
		t.Fatal("TRC20 63 位应不通过")
	}
	evmHash := "0x" + h64
	if !ValidTxHash(settingssvc.WithdrawMethodUSDTERC20, evmHash) {
		t.Fatal("ERC20 0x+64 应通过")
	}
	if ValidTxHash(settingssvc.WithdrawMethodUSDTERC20, h64) {
		t.Fatal("ERC20 不带 0x 应不通过")
	}
	if !ValidTxHash(settingssvc.WithdrawMethodAlipay, "任意流水号") {
		t.Fatal("alipay 不按哈希格式校验")
	}
}

func TestValidUSDTAmount(t *testing.T) {
	ok := []string{"100.5", "0.000001"}
	for _, s := range ok {
		if !ValidUSDTAmount(dec(s)) {
			t.Errorf("期望通过: %s", s)
		}
	}
	bad := []string{"0", "-1", "0.0000001", "1000000000000000000"}
	for _, s := range bad {
		if ValidUSDTAmount(dec(s)) {
			t.Errorf("期望不通过: %s", s)
		}
	}
}

func TestValidatePayoutAccount(t *testing.T) {
	validTRC := "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV"
	acct, errFields := ValidatePayoutAccount(settingssvc.WithdrawMethodUSDTTRC20, "  "+validTRC+"  ")
	if acct != validTRC || errFields != nil {
		t.Fatalf("TRC20 合法地址去空白: acct=%q fields=%v", acct, errFields)
	}
	_, errFields = ValidatePayoutAccount(settingssvc.WithdrawMethodUSDTTRC20, "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed")
	if errFields == nil || errFields["payout_detail"] == "" {
		t.Fatal("TRC20 填 EVM 地址应有 payout_detail 错误")
	}
	acct, errFields = ValidatePayoutAccount(settingssvc.WithdrawMethodAlipay, "pay@example.test")
	if acct != "pay@example.test" || errFields != nil {
		t.Fatalf("alipay: acct=%q fields=%v", acct, errFields)
	}
	_, errFields = ValidatePayoutAccount(settingssvc.WithdrawMethodAlipay, "")
	if errFields == nil {
		t.Fatal("空串应有错误")
	}
}

func TestValidatePaidUSDT(t *testing.T) {
	h64 := strings.Repeat("ab", 32)
	if ValidatePaidUSDT(settingssvc.WithdrawMethodUSDTTRC20, h64, nil) != nil {
		t.Fatal("TRC20 合法哈希 + nil 数量应无错误")
	}
	fields := ValidatePaidUSDT(settingssvc.WithdrawMethodUSDTERC20, h64, nil)
	if fields == nil || fields["payout_reference"] == "" {
		t.Fatal("ERC20 不带 0x 应有 payout_reference")
	}
	amt := dec("1")
	fields = ValidatePaidUSDT(settingssvc.WithdrawMethodAlipay, "ref", &amt)
	if fields == nil || fields["paid_usdt_amount"] == "" {
		t.Fatal("alipay + 非 nil 数量应有 paid_usdt_amount")
	}
}

func TestIsUSDTMethodAndKnownWithdrawMethod(t *testing.T) {
	if !IsUSDTMethod(settingssvc.WithdrawMethodUSDTTRC20) || IsUSDTMethod(settingssvc.WithdrawMethodAlipay) {
		t.Fatal("IsUSDTMethod")
	}
	if !KnownWithdrawMethod(WithdrawMethodOther) || !KnownWithdrawMethod(settingssvc.WithdrawMethodBank) {
		t.Fatal("KnownWithdrawMethod")
	}
	if KnownWithdrawMethod("bogus") {
		t.Fatal("未知方式应 false")
	}
}
