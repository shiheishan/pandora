package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// GiftGranter 是礼品卡兑换时「实际发东西」的那一半。
//
// 礼品卡域负责判断「该不该发、发多少」，这里负责「怎么发才不把账做坏」。
// 之所以放在 billing 而不是 giftcard：往余额记账、改订阅到期、动配额，
// 这些规则本来就归计费域管，复制一份到礼品卡里迟早会和主路径分叉。
//
// 所有方法都接收调用方的 tx —— 兑换必须整体成功或整体失败，
// 不能出现「余额加了但码没标记已用」。
type GiftGranter struct{ s *Service }

func (s *Service) GiftGranter() *GiftGranter { return &GiftGranter{s: s} }

// GrantBalance 走复式记账：借 平台赠送支出 / 贷 用户余额。
//
// 不是直接 UPDATE 一个余额数字 —— 那样账本和余额会对不上，
// 而对不上的第一现场往往在几个月后才被发现。
func (g *GiftGranter) GrantBalance(ctx context.Context, tx pgx.Tx,
	tenantID, userID string, amount int64, currency, memo string) (string, error) {

	if amount <= 0 {
		return "", errors.New("gift balance must be positive")
	}
	if currency == "" {
		currency = "CNY"
	}
	accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, []ledgerAccountSpec{
		{Key: "expense", AccountType: AccountPlatformFeeExpense,
			Currency: currency, OwnerRef: "gift"},
		{Key: "balance", AccountType: AccountUserBalance,
			Currency: currency, UserID: &userID},
	})
	if err != nil {
		return "", err
	}
	return Post(ctx, tx, tenantID, Posting{
		Kind: "gift_card_balance", Currency: currency,
		Memo: memo, ActorKind: "system",
		Entries: []Entry{
			{AccountID: accounts["expense"], Direction: Debit, Amount: amount,
				Description: "gift card balance expense"},
			{AccountID: accounts["balance"], Direction: Credit, Amount: amount,
				Description: "gift card balance to user"},
		},
	})
}

// Placements 给出礼品卡在该用户名下的落点选项与默认 Key（规则 purchase.Options，展示数据见
// placementsTx）。兑换前的 preview 与兑换时都在礼品卡自己的事务里调。
func (g *GiftGranter) Placements(ctx context.Context, tx pgx.Tx, tenantID, userID string,
	offer purchase.Offer) ([]purchase.Placement, string, error) {
	return placementsTx(ctx, tx, tenantID, userID, offer, "")
}

// errNoSubscriptionForReward 是加时长、重置这类奖励没有可落的那一份（原型：先续费再来兑换，卡不会过期）。
var errNoSubscriptionForReward = httpx.New(httpx.CodeValidationFailed,
	"现在没有在用的套餐，这张卡暂时用不了。先续费再来兑换，卡不会过期")

// GrantTraffic 把礼品卡送的流量发成一笔流量包余额（D-E-1）。
//
// 此前是加在订阅配额行的 granted_addon 上，而配额行跨周期复用、重置与续费
// 都不清它，一次性赠送于是每个周期重新可用（缺陷 16）。现在它和购买的流量包
// 是同一笔余额：挂用户、永不过期、用完为止、每周期先扣套餐额度再扣它。
// 没有生效订阅也能先领着 —— 余额在用户身上，等有了订阅再用。
func (g *GiftGranter) GrantTraffic(ctx context.Context, tx pgx.Tx,
	tenantID, userID, subID, codeID string, bytes int64) error {

	if bytes <= 0 {
		return errors.New("gift traffic must be positive")
	}
	var sub *string
	if subID != "" {
		sub = &subID
	}
	_, err := GrantTrafficPackTx(ctx, tx, tenantID, userID, sub, "gift_card", codeID, bytes)
	return err
}

// ExtendExpiry 把用户订阅的到期时间往后推；已过期 30 天内或试用中的订阅会被救回。
//
// 走 extendSubscriptionTx：订阅周期末、本周期 cycle 配额行、active 凭据一起推，
// 救回时状态回到 active、流量按天数折算（规则 2）。基准取「现在」和「原到期时间」
// 里更晚的那个，规则见 extendSubscriptionTx。
func (g *GiftGranter) ExtendExpiry(ctx context.Context, tx pgx.Tx,
	tenantID, userID, subID string, days int) error {

	if days <= 0 {
		return errors.New("gift expire days must be positive")
	}
	if subID == "" {
		return errNoSubscriptionForReward
	}
	err := lockPlacementSubscription(ctx, tx, tenantID, userID,
		purchase.Option{Kind: purchase.KindExtendDays, SubscriptionID: subID})
	if err != nil {
		return err
	}
	_, err = extendSubscriptionTx(ctx, tx, subscriptionExtension{
		TenantID: tenantID, SubscriptionID: subID, Days: days,
		ActorKind: "user", ActorID: &userID, Source: "gift_card",
	})
	return err
}

// ResetQuota 把本周期已用流量清零。
func (g *GiftGranter) ResetQuota(ctx context.Context, tx pgx.Tx,
	tenantID, userID, subID string) error {

	if subID == "" {
		return errNoSubscriptionForReward
	}
	if err := lockPlacementSubscription(ctx, tx, tenantID, userID,
		purchase.Option{Kind: purchase.KindResetTraffic, SubscriptionID: subID}); err != nil {
		return err
	}

	// 清零前先取用量：日志要靠它说明这张卡实际帮用户免掉了多少。
	// UPDATE 之后这个数字就永远查不回来了。
	var before int64
	if err := tx.QueryRow(ctx, `
		SELECT consumed FROM quota_balances
		 WHERE tenant_id=$1 AND subscription_id=$2::uuid AND metric='traffic.bytes'
		 FOR UPDATE`, tenantID, subID).Scan(&before); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.New(httpx.CodeValidationFailed, "当前订阅没有流量配额")
		}
		return err
	}

	// remaining 是生成列，consumed 归零后它自己会跟着涨，不能也不必手写。
	tag, err := tx.Exec(ctx, `
		UPDATE quota_balances
		   SET consumed = 0,
		       notified_thresholds = '{}',
		       updated_at = now()
		 WHERE tenant_id=$1 AND subscription_id=$2::uuid AND metric='traffic.bytes'`,
		tenantID, subID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return httpx.New(httpx.CodeValidationFailed, "当前订阅没有流量配额")
	}
	return LogTrafficReset(ctx, tx, tenantID, subID, userID,
		"traffic.bytes", "gift_card", before, nil, "")
}

// GrantPlan 兑换套餐卡，codeID 是这张卡密，按 choice 落地（用户 2026-10-07 定：落点由人选）：
//
//	renew   在同款那份上续一期（w5expiry 规则 3）：周期与流量和付费续费完全一样
//	        （renewSubscriptionTx），沿用原套餐版本
//	change  把那份换成卡上的套餐（grantPlanChange）：与门户改套餐同一份折算与履约，卡算 0 元，
//	        原套餐的剩余价值全额退进余额（00134 的守卫照常核对）
//	new     开通一份新订阅（grantPlanDirect）
//
// choice 在这个事务里按当前候选重新 Match：选项变了回 422，卡不会被用掉。choice 为零值时
// 只有一个选项就用它（只有一份同款、或一份都没有时的新开），否则要求先选。
//
// 这里没有复用 CreateManualOrder：那个方法自己开事务，而兑换必须
// 和标记码已用在同一个事务里。硬凑会得到一个「订单建好了但码没作废」
// 的窗口 —— 对卡密来说这等于无限复制。
//
// 返回值拆成基本类型（订阅 ID、落地方式 PlanGrant*、退进余额的金额与币种）：giftcard 经
// 自己的 Granter 接口调用，不引用计费域的类型。
func (g *GiftGranter) GrantPlan(ctx context.Context, tx pgx.Tx,
	tenantID, userID, codeID, planID, priceID string, choice purchase.Choice) (string, string, int64, string, error) {
	r, err := g.grantPlan(ctx, tx, tenantID, userID, codeID, planID, priceID, choice)
	if err != nil {
		return "", "", 0, "", err
	}
	return r.SubscriptionID, r.Mode, r.BalanceRefund, r.RefundCurrency, nil
}

func (g *GiftGranter) grantPlan(ctx context.Context, tx pgx.Tx,
	tenantID, userID, codeID, planID, priceID string, choice purchase.Choice) (PlanGrant, error) {
	if planID == "" {
		return PlanGrant{}, errors.New("gift plan id is required")
	}
	opt, ok, err := resolvePlacementTx(ctx, tx, tenantID, userID,
		purchase.Offer{Kind: purchase.OfferPlan, PlanID: planID, PriceID: priceID}, &choice)
	if err != nil {
		return PlanGrant{}, err
	}
	if !ok {
		return PlanGrant{}, errors.New("plan offer has no placement option")
	}
	if opt.Kind != purchase.KindNew {
		if err := lockPlacementSubscription(ctx, tx, tenantID, userID, opt); err != nil {
			return PlanGrant{}, err
		}
	}
	switch opt.Kind {
	case purchase.KindRenew:
		if err := g.s.grantPlanRenewal(ctx, tx, tenantID, userID, opt.SubscriptionID, planID, priceID); err != nil {
			return PlanGrant{}, err
		}
		return PlanGrant{SubscriptionID: opt.SubscriptionID, Mode: PlanGrantRenewed}, nil
	case purchase.KindChange:
		return g.s.grantPlanChange(ctx, tx, tenantID, userID, opt.SubscriptionID, planID, priceID, codeID)
	default:
		subID, err := g.s.grantPlanDirect(ctx, tx, tenantID, userID, planID, priceID)
		if err != nil {
			return PlanGrant{}, err
		}
		return PlanGrant{SubscriptionID: subID, Mode: PlanGrantNew}, nil
	}
}
