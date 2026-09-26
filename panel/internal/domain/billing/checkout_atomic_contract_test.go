// [INPUT]: 依赖 platform/sourcetest 按名取下单、结算、券、预留图与账本科目各声明的源码
// [OUTPUT]: 对外提供结账与结算的源码契约测试（下单原子性、券预留、结算锁序与三条挂账隔离分支）
// [POS]: billing 的源码契约门禁：钉住数据库执行不到本机时也必须成立的锁序与分支顺序，PG18 测试证明它们在真实 SQL 下的效果
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestCreateOrderOutputPreparedJSONContract(t *testing.T) {
	out := CreateOrderOutput{
		DiscountAmount: 25,
		OrderID:        "order-id",
		OrderNo:        "AO-1",
		Currency:       "CNY",
		TotalAmount:    975,
		BalanceApplied: 100,
		PayableAmount:  875,
		Status:         "pending_payment",
	}
	prepared, err := httpx.PrepareJSON(http.StatusCreated, out)
	if err != nil {
		t.Fatal(err)
	}
	out.prepared = prepared
	if out.PreparedResponse().StatusCode() != http.StatusCreated {
		t.Fatalf("status = %d", out.PreparedResponse().StatusCode())
	}
	want := "{\"discount_amount\":25,\"order_id\":\"order-id\",\"order_no\":\"AO-1\",\"currency\":\"CNY\",\"total_amount\":975,\"balance_applied\":100,\"payable_amount\":875,\"status\":\"pending_payment\"}\n"
	if got := string(out.PreparedResponse().BodyBytes()); got != want {
		t.Fatalf("prepared body = %q, want %q", got, want)
	}
}

func TestCheckoutAtomicWriterSourceContract(t *testing.T) {
	// 预留父节点与余额冻结抽到了 order_holds.go，新购、充值、流量包共用。
	s := sourcetest.Load(t, ".").Decls("Service.CreateOrder", "Service.captureZeroPayOrder",
		"insertHeldReservation", "prepareBalanceHold", "postBalanceHold")
	required := []string{
		"middleware.ValidateIdempotencyClaim(",
		"CheckoutIdempotencyScope",
		"idempotency_key_id",
		"INSERT INTO order_reservations",
		"INSERT INTO order_reservation_events",
		"INSERT INTO order_stock_reservations",
		"stock_sold + stock_reserved < stock_total",
		"INSERT INTO order_purchase_limit_reservations",
		"INSERT INTO balance_holds",
		"AccountUserBalanceHold",
		"Kind: \"balance_hold\"",
		"captureZeroPayOrder",
		"Kind: \"order_paid\"",
		"status = 'paid', paid_amount = total_amount, paid_at = now()",
		"s.fulfillOrder(",
		"idempotencybind.BindResource(",
		"idempotencybind.CompleteSuccessJSON(",
		"httpx.PrepareJSON(http.StatusCreated, out)",
		"SET CONSTRAINTS ALL IMMEDIATE",
	}
	for _, needle := range required {
		if !strings.Contains(s, needle) {
			t.Errorf("checkout atomic writer missing %q", needle)
		}
	}
}

func TestCheckoutLedgerAndCouponHoldSourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	ledger := pkg.Decls("AccountUserBalanceHold", "normalBalance")
	if !strings.Contains(ledger, `AccountUserBalanceHold`) ||
		!strings.Contains(ledger, `AccountType = "user_balance_hold"`) ||
		!strings.Contains(ledger, `AccountUserBalanceHold:`) {
		t.Fatal("user balance hold must be a credit-normal account")
	}

	cs := pkg.Decls("applyCoupon", "redeemCoupon")
	for _, needle := range []string{
		"redeemed+reserved >= *maxRedeem",
		"status IN ('held','captured')",
		"reservation_id)",
		"reserved_count = reserved_count + 1",
	} {
		if !strings.Contains(cs, needle) {
			t.Errorf("coupon hold contract missing %q", needle)
		}
	}
	if strings.Contains(pkg.Source(), "reservation_id, status") {
		t.Fatal("coupon insert must rely on the database's held default; app lacks INSERT(status)")
	}
}

func TestSettlementReservationAndLockOrderSourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	// 结算主链：入口与事务体（原窗口从 HandlePaymentWebhook 到 orderPaidPosting）
	handler := pkg.Decls("Service.HandlePaymentWebhook", "Service.settlePaymentTx")
	ordered := []string{
		"INSERT INTO payment_events",
		"FROM orders WHERE tenant_id=$1",
		"subscription-bound order is missing its exact idempotency linkage",
		"lockOrderSubscriptionForSettlement(",
		"ORDER BY id FOR UPDATE",
		"lockOrderReservationGraph(",
		"prepareAndLockLedgerAccounts(",
		"INSERT INTO payments",
		"postOrderPaid(",
		"captureLockedReservation(",
		"AND status IN ('pending_payment','processing')",
		"s.fulfillRenewalLocked(",
		"s.fulfillOrder(",
		"s.fulfillPlanChangeLocked(",
		"audit.Write(",
	}
	last := -1
	for _, needle := range ordered {
		at := strings.Index(handler, needle)
		if at < 0 {
			t.Fatalf("settlement handler missing %q", needle)
		}
		if at <= last {
			t.Fatalf("settlement lock/capture order is not monotonic at %q", needle)
		}
		last = at
	}
	immediateAt := strings.LastIndex(handler, "forceConstraints()")
	if immediateAt <= last {
		t.Fatal("successful settlement must force deferred constraints after audit")
	}
	if strings.Count(handler, "forceConstraints()") < 3 {
		t.Fatal("ordinary settlement exits must force deferred constraints")
	}
	terminal := strings.Index(handler, `status == "cancelled" || status == "expired"`)
	intentWrite := strings.Index(handler, "UPDATE payment_intents")
	paymentWrite := strings.Index(handler, "INSERT INTO payments")
	if terminal < 0 || intentWrite < 0 || paymentWrite < 0 ||
		terminal >= intentWrite || terminal >= paymentWrite {
		t.Fatal("cancelled/expired orders must branch before ordinary intent and payment writes")
	}
	if strings.Count(handler, "quarantineUnexpectedPayment(") != 3 {
		t.Fatal("paid, released and ineligible-subscription payments need explicit quarantine branches")
	}
	// R117：续费 / 变更单锁住订阅后、碰支付意图与预留图之前复核订阅状态，
	// 不合格的钱进挂账而不是让履约撞状态机回滚。
	subLock := strings.Index(handler, "lockOrderSubscriptionForSettlement(")
	recheck := strings.Index(handler, "subscriptionAcceptsPaidChange(subscriptionStatus)")
	ineligible := strings.Index(handler, `"ineligible_subscription", in)`)
	intentLock := strings.Index(handler, "ORDER BY id FOR UPDATE")
	if subLock < 0 || recheck < 0 || ineligible < 0 || intentLock < 0 ||
		!(subLock < recheck && recheck < ineligible && ineligible < intentLock) {
		t.Fatal("subscription eligibility must be rechecked under the subscription lock before intent locks")
	}

	// 原窗口从 postOrderPaid 到 fulfillOrder，中间夹着开通订阅的三个声明
	posting := pkg.Decls("Service.postOrderPaid", "provisionSpec", "Service.provisionSubscription", "initQuotaBalances")
	if !strings.Contains(posting, "AccountID: p.HoldAccountID") {
		t.Fatal("mixed settlement must debit the locked hold account")
	}
	if strings.Contains(posting, "AccountUserBalance,") {
		t.Fatal("mixed settlement must not resolve or debit user_balance again")
	}
}

func TestReservationCaptureSourceContract(t *testing.T) {
	s := sourcetest.Load(t, ".").Decls("lockOrderReservationGraph", "prepareAndLockLedgerAccounts", "captureLockedReservation")
	for _, needle := range []string{
		`case "new", "renewal", "topup", "addon", "upgrade":`,
		`only plan change orders carry a proration credit`,
		`topup reservation graph must contain only its parent`,
		`new-order stock reservation shape is incomplete or inconsistent`,
		`renewal, addon and plan change reservation graphs cannot contain stock or purchase-limit reservations`,
		`(in.Kind == "addon") != (items[0].planID == "" && items[0].trafficPack)`,
		`items[0].lineAmount != items[0].unitAmount*int64(items[0].quantity)`,
		`limited plan is missing its exact purchase-limit reservation`,
		`coupon reservation shape is incomplete or inconsistent`,
		`discount != in.DiscountAmount`,
		`balance hold shape is incomplete or inconsistent`,
		`ON CONFLICT (tenant_id,account_type,owner_user_id,currency)`,
		`sort.Strings(allIDs)`,
		`FOR UPDATE`,
		`status='captured', capture_txn_id=$4::uuid`,
		`AND state='held'`,
		`business_request_id,actor_kind,actor_id`,
		`tag.RowsAffected() != 1`,
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("reservation settlement contract missing %q", needle)
		}
	}
}

// TestEveryPaidOrderKindReachesFulfilled 守住一条不变量：支付成功后，
// 三种订单类型都必须把订单推进到 fulfilled，不能停在 paid。
//
// 这条曾经破过。topup 分支当时只有一句注释「余额已在结算分录里入账」，
// 订单就永远留在 status='paid'、fulfilled_at IS NULL。钱其实到账了，
// 但订单看起来像卡住的：后台列表永远显示「已支付」，而「付了钱没履约」
// 这个排查卡单的标准查询会命中每一张充值单——真有一张入账失败卡在那里，
// 会淹没在假阳性里没人发现。
//
// 用源码扫描而不是跑一遍结算：这个不变量的破坏方式是「某个分支忘了写」，
// 而忘了写的分支在单元测试里通常也没有对应用例。扫源码至少保证下一个
// 新增的 kind 会在这里绊一跤。
func TestEveryPaidOrderKindReachesFulfilled(t *testing.T) {
	// 结算后收尾的 switch 在结算事务体里
	s := sourcetest.Load(t, ".").Decl("Service.settlePaymentTx")

	// checkout.go 里有两处 switch orderKind，认准结算后收尾的那一处：
	// 它是唯一一个分支里会调用 fulfillOrder 的。按出现顺序取第一个会
	// 拿到前面那个校验用的 switch，然后误报一堆缺失分支。
	start := -1
	for i := 0; ; {
		j := strings.Index(s[i:], `switch orderKind {`)
		if j < 0 {
			break
		}
		j += i
		tail := s[j:]
		if k := strings.Index(tail, `default:`); k >= 0 &&
			strings.Contains(tail[:k], `s.fulfillOrder(`) {
			start = j
			break
		}
		i = j + len(`switch orderKind {`)
	}
	if start < 0 {
		t.Fatal("paid-order kind switch not found")
	}
	end := strings.Index(s[start:], `default:`)
	if end < 0 {
		t.Fatal("paid-order kind switch has no default branch")
	}
	sw := s[start : start+end]

	for _, kind := range []string{`case "topup":`, `case "renewal":`, `case "new":`, `case "addon":`, `case "upgrade":`} {
		if !strings.Contains(sw, kind) {
			t.Fatalf("paid-order switch is missing %s", kind)
		}
	}

	// topup 就地收尾，另外两种走 fulfill* 去建/续订阅。
	topup := sw[strings.Index(sw, `case "topup":`):strings.Index(sw, `case "renewal":`)]
	if !strings.Contains(topup, `SET status='fulfilled', fulfilled_at=now()`) {
		t.Error("topup branch must mark the order fulfilled; " +
			"leaving it at status='paid' makes every top-up look like a stuck order")
	}
	if !strings.Contains(topup, `AND status='paid'`) {
		t.Error("topup fulfilment must be guarded on the paid state so a replayed " +
			"callback cannot re-fulfil an order that moved on")
	}

	for _, branch := range []string{`case "renewal":`, `case "new":`, `case "addon":`, `case "upgrade":`} {
		seg := sw[strings.Index(sw, branch):]
		if next := strings.Index(seg[len(branch):], `case "`); next >= 0 {
			seg = seg[:len(branch)+next]
		}
		if !strings.Contains(seg, "fulfill") {
			t.Errorf("%s branch must call a fulfil* helper", branch)
		}
	}
}
