package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 订阅周期末是三处数据共同的口径，必须一起动：
//
//   - subscriptions.current_period_end：节点名单按它判断是否还下发；
//   - 本周期 cycle 配额行的 period_end：上报流量只记到 period_end > now() 的行上，
//     周期末停在旧值的话，过了旧到期日流量既不计也不扣；
//   - active 凭据的 expires_at：订阅拉取与门户链接列表只看它，停在旧值就是 404。
//
// 续费（renewal.go）与变更套餐（plan_change.go）开新周期，会把配额也一并重置，
// 它们自己改订阅与配额行，凭据到期经 syncCredentialExpiryTx 对齐；礼品卡延期与
// 后台加时长只往后推周期末、不开新周期，三处都经 extendSubscriptionTx 一次改完。
// 凭据到期只有 syncCredentialExpiryTx 这一处写，契约测试
// subscription_period_contract_test.go 钉住这一点。

// syncCredentialExpiryTx 把订阅下全部 active 凭据的有效期对齐到新的周期末。
//
// 令牌本身不换：换了等于让用户所有设备重新导入一次订阅。已吊销或已过期的凭据
// 不动 —— 换发链接后旧链接不能因为续费又活过来。返回改到的凭据条数。
func syncCredentialExpiryTx(ctx context.Context, tx pgx.Tx,
	tenantID, subID string, periodEnd time.Time) (int64, error) {

	tag, err := tx.Exec(ctx, `
		UPDATE subscription_credentials
		   SET expires_at = $3
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND status = 'active'`,
		tenantID, subID, periodEnd)
	if err != nil {
		return 0, fmt.Errorf("延长凭据有效期: %w", err)
	}
	return tag.RowsAffected(), nil
}

// subscriptionExtension 描述一次「只往后推周期末、不开新周期」的延期。
type subscriptionExtension struct {
	TenantID       string
	SubscriptionID string
	Days           int
	// ActorKind / ActorID 写进订阅事件：礼品卡是用户自己兑换（user），
	// 后台加时长是管理员（admin）。
	ActorKind string
	ActorID   *string
	// Source 写进事件载荷，区分 gift_card / admin。
	Source string
	// Reason 可空，后台加时长时是管理员填的原因。
	Reason string
}

// subscriptionPeriodChange 是一次延期前后的周期末。
type subscriptionPeriodChange struct {
	Status      string
	PreviousEnd time.Time
	PeriodEnd   time.Time
}

// subscriptionExtendable 是「能不能延期」的唯一判定。
//
// 现在只收 active，与礼品卡原先的 activeSubscription 同口径。试用中、宽限期、
// 已过期的订阅能不能靠礼品卡或后台加时长救回来，是待拍板的产品决定（第 2 波
// 规划第四节第 4 条）；放开时只改这里，并决定是否顺带把状态恢复为 active
// （那要走订阅状态机并写状态变更事件，不能只在这里加一个状态值）。
func subscriptionExtendable(status string) bool {
	return status == "active"
}

// extendSubscriptionTx 把订阅周期末往后推 Days 天，并一次对齐本周期 cycle 配额行与
// active 凭据，写一条 extended 订阅事件。调用方提供事务；本函数自己锁订阅行。
//
// 基准取「现在」和「原到期时间」里更晚的那个：已经走过到期日（状态仍是 active，
// 代码里还没有过期扫描）的订阅从现在起算，未到期的接着原到期日往后 —— 否则给
// 还有 20 天的用户送 7 天，他反而只剩 7 天了。
//
// 配额行只改 period_end 等于旧周期末的 cycle 行：那正是本周期的行。已用量不清零、
// 周期起点不动 —— 延期是给更多时间，不是开新周期发新一轮流量。day / month 行有
// 自己的滚动边界，total 行没有边界，都不碰。
//
// 没有到期时间（current_period_end 为空）的订阅拒绝延期：原先的写法会把它从
// 「永不过期」变成「从今天起 N 天」，等于把时长缩短。
func extendSubscriptionTx(ctx context.Context, tx pgx.Tx,
	ext subscriptionExtension) (subscriptionPeriodChange, error) {

	var change subscriptionPeriodChange
	if ext.Days <= 0 {
		return change, errors.New("subscription extension days must be positive")
	}
	var oldEnd *time.Time
	err := tx.QueryRow(ctx, `
		SELECT status, current_period_end FROM subscriptions
		 WHERE tenant_id = $1 AND id = $2::uuid
		 FOR UPDATE`, ext.TenantID, ext.SubscriptionID).Scan(&change.Status, &oldEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return change, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return change, err
	}
	if !subscriptionExtendable(change.Status) {
		return change, httpx.New(httpx.CodeConflict, "只有生效中的订阅可以延长时长")
	}
	if oldEnd == nil {
		return change, httpx.New(httpx.CodeValidationFailed, "这条订阅没有到期时间，不需要延长")
	}
	change.PreviousEnd = *oldEnd

	// 按 UTC 加天数：一天恒为 24 小时，不随进程时区的夏令时漂移
	now := time.Now().UTC()
	base := now
	if oldEnd.After(now) {
		base = oldEnd.UTC()
	}
	// 库里存到微秒：先截断，三处写入与返回给调用方的是同一个值
	change.PeriodEnd = base.AddDate(0, 0, ext.Days).Truncate(time.Microsecond)

	// 带上旧值做比较：锁住之后不该再变，变了说明有人绕过了行锁
	tag, err := tx.Exec(ctx, `
		UPDATE subscriptions
		   SET current_period_end = $3, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2::uuid AND current_period_end = $4`,
		ext.TenantID, ext.SubscriptionID, change.PeriodEnd, change.PreviousEnd)
	if err != nil {
		return change, fmt.Errorf("延长订阅周期: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return change, errors.New("subscription period extension lost")
	}

	tag, err = tx.Exec(ctx, `
		UPDATE quota_balances
		   SET period_end = $3, updated_at = now()
		 WHERE tenant_id = $1 AND subscription_id = $2::uuid
		   AND period = 'cycle' AND period_end = $4`,
		ext.TenantID, ext.SubscriptionID, change.PeriodEnd, change.PreviousEnd)
	if err != nil {
		return change, fmt.Errorf("延长本周期配额: %w", err)
	}
	quotaRows := tag.RowsAffected()

	credentials, err := syncCredentialExpiryTx(ctx, tx, ext.TenantID, ext.SubscriptionID, change.PeriodEnd)
	if err != nil {
		return change, err
	}

	payload := map[string]any{
		"source": ext.Source, "days": ext.Days,
		"previous_end": change.PreviousEnd, "period_end": change.PeriodEnd,
		"cycle_quota_rows": quotaRows, "credentials": credentials,
	}
	if ext.Reason != "" {
		payload["reason"] = ext.Reason
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status,
			 actor_kind, actor_id, payload)
		VALUES ($1, $2::uuid, 'extended', $3, $3, $4, $5::uuid, $6)`,
		ext.TenantID, ext.SubscriptionID, change.Status, ext.ActorKind, ext.ActorID,
		payload); err != nil {
		return change, fmt.Errorf("记录订阅延期: %w", err)
	}
	return change, nil
}
