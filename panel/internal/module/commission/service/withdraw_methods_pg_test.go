package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 虚构地址与哈希，仅用于格式校验用例。
const (
	testTRC20Addr = "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdV"
	testTRC20Bad  = "TBXSw8fM4jpQkGc6zZjsVABFpVN7UvXPdW"         // 校验和不对
	testEVMAddr   = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAed" // EIP-55 规范示例
	testEVMBadSum = "0x5aAeb6053F3E94C9b9A09f33669435E7Ef1BeAeD" // 大小写校验和不对
)

// setWithdraw 以管理员身份设提现开关与开放的提现方式。
func setWithdraw(t *testing.T, e *env, ctx context.Context, admin uuid.UUID, on bool, methods ...string) {
	t.Helper()
	if err := e.settings.SetCommissionWithdraw(adminCtx(ctx, admin), settingssvc.CommissionWithdrawUpdate{
		Enabled: &on,
		Methods: &methods,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertValidationFieldErr(err error, field string) bool {
	if errCode(err) != errs.ValidationFailed {
		return false
	}
	return errs.As(err).Fields[field] != ""
}

func TestWithdrawDisabled(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, false, "alipay")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx := userCtx(ctx, referrer)

	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay", "alipay a@example.test"); !errors.Is(err, ErrWithdrawDisabled) {
		t.Fatalf("提现关闭应拒绝: %v", err)
	}
	var n int
	if err := e.row(ctx, `SELECT count(*) FROM withdrawals WHERE user_id = ?`, referrer).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不应有提现行 %d err=%v", n, err)
	}
}

func TestWithdrawMethodWhitelist(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx := userCtx(ctx, referrer)

	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "wechat", "wechat wx@example.test"); !assertValidationFieldErr(err, "method") {
		t.Fatalf("未开放的 wechat 应有 method 字段: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "bitcoin", "bc1qexample"); !assertValidationFieldErr(err, "method") {
		t.Fatalf("目录外 bitcoin 应有 method 字段: %v", err)
	}

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay", "alipay a@example.test")
	if err != nil {
		t.Fatalf("alipay 应成功: %v", err)
	}
	var method string
	if err := e.row(ctx, `SELECT method FROM withdrawals WHERE id = ?`, id).Scan(&method); err != nil || method != "alipay" {
		t.Fatalf("method=%s err=%v", method, err)
	}
}

func TestWithdrawUSDTAddress(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, true, "alipay", "usdt_trc20", "usdt_erc20")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx := userCtx(ctx, referrer)

	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_trc20", testTRC20Bad); !assertValidationFieldErr(err, "payout_detail") {
		t.Fatalf("TRC20 校验和不对: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_erc20", testEVMBadSum); !assertValidationFieldErr(err, "payout_detail") {
		t.Fatalf("ERC20 EIP-55 不对: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_erc20", "0x123"); !assertValidationFieldErr(err, "payout_detail") {
		t.Fatalf("ERC20 过短: %v", err)
	}
	if _, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_bep20", testEVMAddr); !assertValidationFieldErr(err, "method") {
		t.Fatalf("未开放 bep20: %v", err)
	}

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_trc20", testTRC20Addr)
	if err != nil {
		t.Fatalf("合法 TRC20: %v", err)
	}
	var method string
	if err := e.row(ctx, `SELECT method FROM withdrawals WHERE id = ?`, id).Scan(&method); err != nil || method != "usdt_trc20" {
		t.Fatalf("method=%s err=%v", method, err)
	}
}

func TestMarkPaidUSDT(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, true, "usdt_trc20")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx, actx := userCtx(ctx, referrer), adminCtx(ctx, admin)

	// 可提现 20.00、最低 10.00：两笔各 10.00，第二笔用来测实付数量的小数位
	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.00"), "usdt_trc20", testTRC20Addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, ""); err != nil {
		t.Fatal(err)
	}

	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0001", nil); !assertValidationFieldErr(err, "payout_reference") {
		t.Fatalf("非哈希流水号: %v", err)
	}
	var status string
	if err := e.row(ctx, `SELECT status FROM withdrawals WHERE id = ?`, id).Scan(&status); err != nil || status != WithdrawalApproved {
		t.Fatalf("状态应仍为 approved: %s err=%v", status, err)
	}
	var paidN int
	if err := e.row(ctx, `SELECT count(*) FROM ledger_transactions WHERE kind = 'commission_paid' AND source_id = ?`, id).Scan(&paidN); err != nil || paidN != 0 {
		t.Fatalf("不应有 commission_paid %d err=%v", paidN, err)
	}

	hash := strings.Repeat("ab", 32)
	paid := dec2("2.123456")
	if err := e.svc.MarkWithdrawalPaid(actx, id, hash, &paid); err != nil {
		t.Fatalf("合法打款: %v", err)
	}
	var (
		paidUSDT decimal.Decimal
		ref      string
	)
	if err := e.row(ctx, `SELECT paid_usdt_amount, payout_reference, status FROM withdrawals WHERE id = ?`, id).
		Scan(&paidUSDT, &ref, &status); err != nil {
		t.Fatal(err)
	}
	if !paidUSDT.Equal(dec2("2.123456")) || ref != hash || status != WithdrawalPaid {
		t.Fatalf("打款后行不对: usdt=%s ref=%s status=%s", paidUSDT, ref, status)
	}
	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range mc.Withdrawals {
		if w.ID == id {
			found = true
			if w.TxHash != hash {
				t.Fatalf("MyCommission TxHash %q，应为 %q", w.TxHash, hash)
			}
		}
	}
	if !found {
		t.Fatalf("未找到提现 %s", id)
	}

	id2, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("10.00"), "usdt_trc20", testTRC20Addr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id2, true, ""); err != nil {
		t.Fatal(err)
	}
	mc2, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	var found2 bool
	for _, w := range mc2.Withdrawals {
		if w.ID == id2 {
			found2 = true
			if w.TxHash != "" {
				t.Fatalf("未打款的提现 TxHash 应为空串，得 %q", w.TxHash)
			}
		}
	}
	if !found2 {
		t.Fatalf("未找到提现 %s", id2)
	}
	badAmt := dec2("1.1234567")
	if err := e.svc.MarkWithdrawalPaid(actx, id2, hash, &badAmt); !assertValidationFieldErr(err, "paid_usdt_amount") {
		t.Fatalf("7 位小数应拒绝: %v", err)
	}
}

func TestMarkPaidNonUSDTRejectsAmount(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx, actx := userCtx(ctx, referrer), adminCtx(ctx, admin)

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "alipay", "alipay a@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, ""); err != nil {
		t.Fatal(err)
	}
	one := dec2("1")
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0001", &one); !assertValidationFieldErr(err, "paid_usdt_amount") {
		t.Fatalf("alipay 带实付 USDT 应拒绝: %v", err)
	}
	if err := e.svc.MarkWithdrawalPaid(actx, id, "TX-0001", nil); err != nil {
		t.Fatalf("alipay 无实付应成功: %v", err)
	}
	mc, err := e.svc.MyCommission(ctx, referrer)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range mc.Withdrawals {
		if w.ID == id {
			found = true
			if w.Status != WithdrawalPaid || w.TxHash != "" {
				t.Fatalf("alipay 打款后 status=%s tx_hash=%q，应为 paid 且空串", w.Status, w.TxHash)
			}
		}
	}
	if !found {
		t.Fatalf("未找到提现 %s", id)
	}
	var paidUSDT *decimal.Decimal
	if err := e.row(ctx, `SELECT paid_usdt_amount FROM withdrawals WHERE id = ?`, id).Scan(&paidUSDT); err != nil {
		t.Fatal(err)
	}
	if paidUSDT != nil {
		t.Fatalf("paid_usdt_amount 应为 NULL，得 %v", paidUSDT)
	}
}

func TestMarkPaidEVMHash(t *testing.T) {
	e, ctx := newEnv(t)
	admin := newUser(t, e, ctx)
	setConfig(t, e, ctx, admin, 10, 0, "10.00", "every_order")
	setWithdraw(t, e, ctx, admin, true, "usdt_bep20")
	referrer, buyer := newUser(t, e, ctx), newUser(t, e, ctx)
	accrueAvailable(t, e, ctx, referrer, buyer, "200.00")
	uctx, actx := userCtx(ctx, referrer), adminCtx(ctx, admin)

	id, err := e.svc.RequestWithdrawal(uctx, referrer, dec2("15.00"), "usdt_bep20", testEVMAddr)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ReviewWithdrawal(actx, id, true, ""); err != nil {
		t.Fatal(err)
	}

	h64 := strings.Repeat("cd", 32)
	if err := e.svc.MarkWithdrawalPaid(actx, id, h64, nil); !assertValidationFieldErr(err, "payout_reference") {
		t.Fatalf("无 0x 前缀应拒绝: %v", err)
	}
	evHash := "0x" + h64
	if err := e.svc.MarkWithdrawalPaid(actx, id, evHash, nil); err != nil {
		t.Fatalf("0x 哈希应成功: %v", err)
	}
	var ref, st string
	if err := e.row(ctx, `SELECT payout_reference, status FROM withdrawals WHERE id = ?`, id).Scan(&ref, &st); err != nil {
		t.Fatal(err)
	}
	if ref != evHash || st != WithdrawalPaid {
		t.Fatalf("ref=%s status=%s", ref, st)
	}
}
