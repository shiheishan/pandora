package billing

// 建单与报价共用的金额收尾（设计稿 2.2、2.6）：
//
//	余额   一律经 purchase.ApplyBalance：限制在余额与应付之内；剩下的钱低于支付最低额时
//	       少用一点余额（Kept），或只能全用余额（Forced），或用尽余额仍付不了（Short）
//	几分钱 只有门户换套餐抵扣后的零头（≤ 99 分）按用户 8.1 第 1 题推荐 A 免掉，并进订单折扣；
//	       恒等式 total = 小计 − 折扣 − 剩余价值 不变，审计记 small_due_waived。其余付不了的回 422
//	比对   带了 Expectation 的建单与服务端重算不符就回 409 quote_changed
//
// 报价（checkout_quote.go）与四个建单入口算的是同一组数，确认时才比得上。

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ErrQuoteChanged 是确认下单时金额与报价对不上的 409。前端按码重新报价、在金额旁标「已更新」。
var ErrQuoteChanged = httpx.New(httpx.CodeQuoteChanged, "金额刚变了，请再确认一次")

// defaultEpayMinAmount 是易支付渠道没配 min_amount 时的最低额（分）：支付宝、微信最低 ¥1.00。
const defaultEpayMinAmount = 100

// maxProviderMinAmount 是渠道 min_amount 的上限（分），与后台校验一致。
const maxProviderMinAmount = 100000

// providerMinAmount 读一个渠道配置里的最低付款额（分）；易支付没配时按 ¥1.00。
func providerMinAmount(adapter string, cfg map[string]any) int64 {
	if v, ok := cfg["min_amount"]; ok {
		var n int64
		switch x := v.(type) {
		case float64:
			n = int64(x)
		case int64:
			n = x
		case int:
			n = int64(x)
		case string:
			n, _ = strconv.ParseInt(x, 10, 64)
		}
		if n >= 1 && n <= maxProviderMinAmount {
			return n
		}
	}
	if adapter == "epay" {
		return defaultEpayMinAmount
	}
	return 0
}

// minPaymentSQL 是「能不能在线付」的门槛：所有启用且接单的 CNY 渠道里最小的那个 min_amount
// （站点只要有一个渠道能付就能付；发起支付时再按所选渠道兜底拦，checkProviderMinimum）。
// 易支付没配按 ¥1.00，与 providerMinAmount 同一口径。
const minPaymentSQL = `
	SELECT coalesce(min(CASE
	         WHEN coalesce(pp.config->>'min_amount', '') ~ '^[0-9]{1,6}$'
	              AND (pp.config->>'min_amount')::bigint BETWEEN 1 AND 100000
	           THEN (pp.config->>'min_amount')::bigint
	         WHEN pp.adapter = 'epay' THEN 100
	         ELSE 0 END), 0)::bigint
	  FROM payment_providers pp
	 WHERE pp.tenant_id = $1 AND pp.enabled AND pp.accepting_new
	   AND 'CNY' = ANY(pp.supported_currencies::text[])`

// minPaymentCacheTTL：报价与建单不额外查库，渠道改了最多一分钟后生效（同 subscription 的 siteNames）。
const minPaymentCacheTTL = time.Minute

type minPaymentEntry struct {
	value int64
	at    time.Time
}

var minPaymentCache = struct {
	sync.Mutex
	m map[string]minPaymentEntry
}{m: map[string]minPaymentEntry{}}

// invalidateMinPayment 在渠道写入后清掉这个租户的缓存。
func invalidateMinPayment(tenantID string) {
	minPaymentCache.Lock()
	delete(minPaymentCache.m, tenantID)
	minPaymentCache.Unlock()
}

// minPayment 返回站点在这个币种上的支付最低额（分）；不是 CNY 时没有限制（0）。
func minPayment(ctx context.Context, tx pgx.Tx, tenantID, currency string) (int64, error) {
	if currency != "CNY" {
		return 0, nil
	}
	now := time.Now()
	minPaymentCache.Lock()
	e, ok := minPaymentCache.m[tenantID]
	minPaymentCache.Unlock()
	if ok && now.Sub(e.at) < minPaymentCacheTTL {
		return e.value, nil
	}
	var v int64
	if err := tx.QueryRow(ctx, minPaymentSQL, tenantID).Scan(&v); err != nil {
		return 0, err
	}
	minPaymentCache.Lock()
	minPaymentCache.m[tenantID] = minPaymentEntry{value: v, at: now}
	minPaymentCache.Unlock()
	return v, nil
}

// availableBalance 读用户在这个币种上的可用余额（物化余额，不加锁；建单随后
// prepareBalanceHold 会锁住科目复核够不够扣）。还没有余额科目就是 0。
func availableBalance(ctx context.Context, tx pgx.Tx, tenantID, userID, currency string) (int64, error) {
	var amount int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(-balance_signed, 0)
		  FROM ledger_accounts
		 WHERE tenant_id = $1 AND owner_user_id = $2::uuid
		   AND account_type = 'user_balance' AND currency = $3`,
		tenantID, userID, currency).Scan(&amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return amount, err
}

// balanceOpts 是一单的收尾口径。
type balanceOpts struct {
	// Offline 线下已收款：不走在线支付，不受最低额限制
	Offline bool
	// Manual 后台人工单（待用户支付）：不做 Forced、不免零头，应付低于最低额回 422
	Manual bool
	// AllowWaive 换套餐抵扣后的零头可以免（用户 8.1 第 1 题），其余入口不免
	AllowWaive bool
}

// ErrBelowMinimum 是应付低于能在线付的最低额、用尽余额也付不完的 422（报价里 below_minimum 提前标出）。
var ErrBelowMinimum = httpx.New(httpx.CodeValidationFailed,
	"应付金额低于支付渠道的最低付款额，请先充值或使用余额支付")

// errManualBelowMinimum 是后台待支付单低于最低额：用户付不了，管理员改用赠送或线下已收款。
var errManualBelowMinimum = httpx.New(httpx.CodeValidationFailed,
	"应付金额低于支付渠道的最低付款额，用户无法在线支付，请改用赠送或线下已收款")

// balancePlan 算一单用多少余额：ApplyBalance，再按入口决定零头能不能免（只有门户换套餐能免，
// 最多 99 分）；免不了又付不了就拒绝。
func balancePlan(ctx context.Context, tx pgx.Tx, tenantID, userID, currency string,
	total, requested int64, o balanceOpts) (purchase.Balance, error) {
	if o.Offline || total <= 0 {
		return purchase.ApplyBalance(total, 0, 0, 0), nil
	}
	minPay, err := minPayment(ctx, tx, tenantID, currency)
	if err != nil {
		return purchase.Balance{}, err
	}
	if o.Manual {
		if minPay > 1 && total < minPay {
			return purchase.Balance{}, errManualBelowMinimum
		}
		return purchase.ApplyBalance(total, 0, 0, 0), nil
	}
	available := int64(0)
	// 余额开关打开，或应付低于最低额（Forced / Short 与开关无关）时要知道余额
	if requested > 0 || total < minPay {
		if available, err = availableBalance(ctx, tx, tenantID, userID, currency); err != nil {
			return purchase.Balance{}, err
		}
	}
	b := purchase.WaiveSmallDue(purchase.ApplyBalance(total, available, requested, minPay), o.AllowWaive)
	if b.Short {
		return purchase.Balance{}, ErrBelowMinimum
	}
	return b, nil
}

// checkExpectation 比对确认时带回的报价与服务端重算的结果：total 是免单之前的应付
// （小计 − 折扣 − 剩余价值），后两项是余额收尾之后的数。任一不等回 409 quote_changed。
func checkExpectation(exp *Expectation, total int64, b purchase.Balance) error {
	if exp == nil {
		return nil
	}
	// 报价超过 10 分钟（或时刻在未来）一律重新报价：四个建单入口同一口径
	if _, err := quoteTime(exp, time.Now().UTC()); err != nil {
		return err
	}
	if exp.Total != total || exp.BalanceApplied != b.Applied || exp.Payable != b.Payable {
		return ErrQuoteChanged
	}
	return nil
}

// quoteTime 是剩余价值的时间比例按哪个时刻算：带了 as_of 就用它，只接受
// [now-10min, now]，超出回 409 quote_changed；没带用 now。
func quoteTime(exp *Expectation, now time.Time) (time.Time, error) {
	if exp == nil || exp.AsOf.IsZero() {
		return now, nil
	}
	asOf := exp.AsOf.UTC()
	if asOf.After(now) || now.Sub(asOf) > quoteAsOfWindow {
		return time.Time{}, ErrQuoteChanged
	}
	return asOf, nil
}

// ErrOrderPaymentExpired 是订单已过 30 分钟付款期限还来发起支付的 409。
var ErrOrderPaymentExpired = httpx.New(httpx.CodeConflict, "这张订单已超过付款期限，请取消后重新下单")

// ErrPaymentBelowMinimum 是发起的在线支付低于所选渠道最低额时的 409（建单已按最低额收尾，
// 正常路径到不了这里；渠道自己的报错是英文或者干脆没有，所以面板先拦）。
var ErrPaymentBelowMinimum = httpx.New(httpx.CodeConflict, "支付金额低于该付款方式的最低额")

// checkProviderMinimum 在发起支付前核对渠道最低额。
func checkProviderMinimum(rec *payment.ProviderRecord, payable int64) error {
	if payable < providerMinAmount(rec.Adapter, rec.Config) {
		return ErrPaymentBelowMinimum
	}
	return nil
}
