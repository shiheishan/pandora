// [INPUT]: 依赖 ledger.go 的记账、traffic_pack.go 的 GrantTrafficPackTx、traffic_reset.go 的 LogTrafficReset、checkout 的 grantPlanDirect
// [OUTPUT]: 对外提供 GiftGranter 与 Service.GiftGranter：GrantBalance、GrantTraffic、ExtendExpiry、ResetQuota、GrantPlan
// [POS]: billing 实现 giftcard.Granter 的一侧：礼品卡「发什么」由 giftcard 决定，「怎么发」在这里；流量奖励发成用户级流量包余额（D-E-1）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

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

// activeSubscription 取用户当前生效的订阅。
//
// 流量和到期这类奖励只能落在某个订阅上。用户没有生效订阅时不能静默跳过 ——
// 那会让「兑换成功」和「什么都没变」同时发生。
func activeSubscription(ctx context.Context, tx pgx.Tx, tenantID, userID string) (string, error) {
	var subID string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM subscriptions
		 WHERE tenant_id=$1 AND user_id=$2::uuid AND status='active'
		 ORDER BY current_period_end DESC NULLS LAST
		 LIMIT 1 FOR UPDATE`, tenantID, userID).Scan(&subID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.New(httpx.CodeValidationFailed,
			"你当前没有生效中的订阅，这类奖励需要先有一个套餐才能发放")
	}
	return subID, err
}

// GrantTraffic 把礼品卡送的流量发成一笔流量包余额（D-E-1）。
//
// 此前是加在订阅配额行的 granted_addon 上，而配额行跨周期复用、重置与续费
// 都不清它，一次性赠送于是每个周期重新可用（缺陷 16）。现在它和购买的流量包
// 是同一笔余额：挂用户、永不过期、用完为止、每周期先扣套餐额度再扣它。
// 没有生效订阅也能先领着 —— 余额在用户身上，等有了订阅再用。
func (g *GiftGranter) GrantTraffic(ctx context.Context, tx pgx.Tx,
	tenantID, userID, codeID string, bytes int64) error {

	if bytes <= 0 {
		return errors.New("gift traffic must be positive")
	}
	_, err := GrantTrafficPackTx(ctx, tx, tenantID, userID, "gift_card", codeID, bytes)
	return err
}

// ExtendExpiry 把订阅到期时间往后推。
//
// 基准取「现在」和「原到期时间」里更晚的那个：已过期的订阅从今天起算，
// 未过期的接着原到期日往后 —— 否则给还有 20 天的用户送 7 天，
// 他反而只剩 7 天了。
func (g *GiftGranter) ExtendExpiry(ctx context.Context, tx pgx.Tx,
	tenantID, userID string, days int) error {

	if days <= 0 {
		return errors.New("gift expire days must be positive")
	}
	subID, err := activeSubscription(ctx, tx, tenantID, userID)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET current_period_end =
		         greatest(coalesce(current_period_end, now()), now())
		         + make_interval(days => $3),
		       updated_at = now()
		 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, subID, days)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("subscription expiry extension lost")
	}
	return nil
}

// ResetQuota 把本周期已用流量清零。
func (g *GiftGranter) ResetQuota(ctx context.Context, tx pgx.Tx,
	tenantID, userID string) error {

	subID, err := activeSubscription(ctx, tx, tenantID, userID)
	if err != nil {
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

// GrantPlan 兑换套餐卡。
//
// 这里没有复用 CreateManualOrder：那个方法自己开事务，而兑换必须
// 和标记码已用在同一个事务里。硬凑会得到一个「订单建好了但码没作废」
// 的窗口 —— 对卡密来说这等于无限复制。
//
// 所以走的是 fulfillOrder 这条已经把订阅、配额、凭据都做对的路径，
// 只是不建订单：兑换流水本身就是这次发放的凭证。
func (g *GiftGranter) GrantPlan(ctx context.Context, tx pgx.Tx,
	tenantID, userID, planID, priceID, reason string) (string, error) {

	if planID == "" {
		return "", errors.New("gift plan id is required")
	}
	subID, err := g.s.grantPlanDirect(ctx, tx, tenantID, userID, planID, priceID)
	if err != nil {
		return "", err
	}
	return subID, nil
}
