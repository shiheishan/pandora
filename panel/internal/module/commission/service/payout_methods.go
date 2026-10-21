package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf8"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/sha3"
)

// tronBase58Alphabet 是 TRON 地址用的比特币 base58 字母表（无 0/O/I/l）。
const tronBase58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var (
	evmAddressRe  = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	trc20TxHashRe = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	evmTxHashRe   = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

var usdtWithdrawMethods = map[string]struct{}{
	settingssvc.WithdrawMethodUSDTTRC20: {},
	settingssvc.WithdrawMethodUSDTERC20: {},
	settingssvc.WithdrawMethodUSDTBEP20: {},
}

// IsUSDTMethod 判断是否为三种 USDT 链上提现方式之一。
func IsUSDTMethod(m string) bool {
	_, ok := usdtWithdrawMethods[m]
	return ok
}

// ValidTRC20Address 校验 TRON TRC20 收款地址：长度 34、以 T 开头；
// base58 解码得 25 字节，首字节须 0x41（主网），末 4 字节为双 SHA-256 前 21 字节的校验和。
func ValidTRC20Address(s string) bool {
	if len(s) != 34 || s[0] != 'T' {
		return false
	}
	b, ok := decodeTRONBase58(s)
	if !ok || len(b) != 25 {
		return false
	}
	if b[0] != 0x41 {
		return false
	}
	h := sha256.Sum256(b[:21])
	h = sha256.Sum256(h[:])
	return bytes.Equal(h[:4], b[21:25])
}

// decodeTRONBase58 将 TRON 地址从 base58 解码为定长 25 字节（大整数左补零）。
func decodeTRONBase58(s string) ([]byte, bool) {
	n := new(big.Int)
	for _, r := range s {
		idx := strings.IndexRune(tronBase58Alphabet, r)
		if idx < 0 {
			return nil, false
		}
		n.Mul(n, big.NewInt(58))
		n.Add(n, big.NewInt(int64(idx)))
	}
	if n.BitLen() > 200 {
		return nil, false
	}
	out := make([]byte, 25)
	n.FillBytes(out)
	return out, true
}

// ValidEVMAddress 校验 ERC20/BEP20 共用的 EVM 地址：0x 加 40 位十六进制；
// 全小写或全大写直接通过，混合大小写时按 EIP-55（Keccak-256 校验和）核对每一位字母的大小写。
func ValidEVMAddress(s string) bool {
	if !evmAddressRe.MatchString(s) {
		return false
	}
	digits := s[2:]
	lower := strings.ToLower(digits)
	if digits == lower || digits == strings.ToUpper(digits) {
		return true
	}
	h := sha3.NewLegacyKeccak256()
	_, _ = h.Write([]byte(lower))
	hashHex := hex.EncodeToString(h.Sum(nil))
	for i := 0; i < 40; i++ {
		c := digits[i]
		if c >= '0' && c <= '9' {
			continue
		}
		// 哈希这一位 ≥ 8 时字母必须大写，否则必须小写
		if wantUpper := hashHex[i] >= '8'; wantUpper != (c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// ValidTxHash 按提现方式校验链上交易哈希格式；非 USDT 方式不校验格式。
func ValidTxHash(method, ref string) bool {
	switch method {
	case settingssvc.WithdrawMethodUSDTTRC20:
		return trc20TxHashRe.MatchString(ref)
	case settingssvc.WithdrawMethodUSDTERC20, settingssvc.WithdrawMethodUSDTBEP20:
		return evmTxHashRe.MatchString(ref)
	default:
		return true
	}
}

var maxUSDTAmount = decimal.RequireFromString("1000000000000000000") // 1e18，与 numeric(24,6) 上限一致

// ValidUSDTAmount 校验实付 USDT：大于 0、最多 6 位小数、小于 1e18。
func ValidUSDTAmount(d decimal.Decimal) bool {
	if !d.IsPositive() {
		return false
	}
	if !d.Equal(d.Truncate(6)) {
		return false
	}
	return d.LessThan(maxUSDTAmount)
}

// ValidatePayoutAccount 校验用户填写的收款账号，返回去首尾空白后的账号与字段错误（无错为 nil）。
func ValidatePayoutAccount(method, account string) (string, map[string]string) {
	acct := strings.TrimSpace(account)
	fields := map[string]string{}
	switch {
	case acct == "":
		fields["payout_detail"] = "请填写收款账号"
	case strings.ContainsRune(acct, 0):
		fields["payout_detail"] = "收款账号含有非法字符"
	case utf8.RuneCountInString(acct) > MaxPayoutDetailRunes:
		fields["payout_detail"] = "收款账号最多 500 个字"
	}
	if len(fields) > 0 {
		return acct, fields
	}
	switch method {
	case settingssvc.WithdrawMethodUSDTTRC20:
		if !ValidTRC20Address(acct) {
			fields["payout_detail"] = "TRC20 地址格式不对，应为 T 开头的 34 位地址，请核对后重填"
		}
	case settingssvc.WithdrawMethodUSDTERC20, settingssvc.WithdrawMethodUSDTBEP20:
		if !ValidEVMAddress(acct) {
			fields["payout_detail"] = "地址格式不对，应为 0x 开头的 42 位地址（大小写需与钱包里一致），请核对后重填"
		}
	}
	if len(fields) == 0 {
		return acct, nil
	}
	return acct, fields
}

// ValidatePaidUSDT 在标记已打款时校验 USDT 交易哈希与实付数量；无错返回 nil。
func ValidatePaidUSDT(method string, ref string, amount *decimal.Decimal) map[string]string {
	fields := map[string]string{}
	if IsUSDTMethod(method) {
		if !ValidTxHash(method, ref) {
			switch method {
			case settingssvc.WithdrawMethodUSDTTRC20:
				fields["payout_reference"] = "TRC20 交易哈希应为 64 位十六进制"
			default:
				fields["payout_reference"] = "交易哈希应为 0x 开头加 64 位十六进制"
			}
		}
		if amount != nil && !ValidUSDTAmount(*amount) {
			fields["paid_usdt_amount"] = "实付 USDT 数量需大于 0，最多 6 位小数"
		}
	} else if amount != nil {
		fields["paid_usdt_amount"] = "只有 USDT 提现才记录实付 USDT 数量"
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}
