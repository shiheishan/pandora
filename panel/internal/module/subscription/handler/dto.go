package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// ---- 门户 ----

// quotaResponse 是一行配额。
type quotaResponse struct {
	Metric      string     `json:"metric"`
	Limit       *int64     `json:"limit"`
	Consumed    int64      `json:"consumed"`
	Remaining   *int64     `json:"remaining"`
	Period      string     `json:"period"`
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end"`
}

// renewalPriceResponse 是续费界面要的当前价格档（金额为分）。
type renewalPriceResponse struct {
	ID              string `json:"id"`
	Currency        string `json:"currency"`
	UnitAmount      int64  `json:"unit_amount"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	Available       bool   `json:"available"`
}

// mySubscriptionResponse 是门户「我的套餐」里的一份。
type mySubscriptionResponse struct {
	ID                 string                `json:"id"`
	PlanID             string                `json:"plan_id"`
	PriceID            string                `json:"price_id"`
	PlanName           string                `json:"plan_name"`
	PlanVersion        int                   `json:"plan_version"`
	Status             string                `json:"status"`
	State              string                `json:"state"`
	PeriodStart        *time.Time            `json:"current_period_start"`
	PeriodEnd          *time.Time            `json:"current_period_end"`
	Currency           string                `json:"currency"`
	Amount             int64                 `json:"amount"`
	Quotas             []quotaResponse       `json:"quotas"`
	DeviceLimit        *int                  `json:"device_limit"`
	OnlineDevices      int                   `json:"online_devices"`
	QuotaResetStrategy string                `json:"quota_reset_strategy"`
	NextResetAt        *time.Time            `json:"next_reset_at"`
	Renewable          bool                  `json:"renewable"`
	RenewalPrice       *renewalPriceResponse `json:"renewal_price"`
	PackRemainingBytes int64                 `json:"pack_remaining_bytes"`
	Label              *string               `json:"label"`
	ClientName         string                `json:"client_name"`
	Changeable         bool                  `json:"changeable"`
	RenewUntil         *time.Time            `json:"renew_until"`
}

// mySubscriptionsResponse 是 GET /me/subscriptions 的响应。
type mySubscriptionsResponse struct {
	Subscriptions       []mySubscriptionResponse `json:"subscriptions"`
	UnattachedPackBytes int64                    `json:"unattached_pack_bytes"`
}

// cents 把金额换成分；超出范围是程序错误。
func cents(d decimal.Decimal) (int64, error) {
	n, err := money.ToCents(d)
	if err != nil {
		return 0, errs.Wrap(errs.Internal, err)
	}
	return n, nil
}

// mySubscriptionsFrom 把 service 结果转成响应。
func mySubscriptionsFrom(in service.MySubscriptions) (mySubscriptionsResponse, error) {
	out := mySubscriptionsResponse{Subscriptions: make([]mySubscriptionResponse, 0, len(in.Subscriptions)), UnattachedPackBytes: in.UnattachedPackBytes}
	for _, s := range in.Subscriptions {
		v, err := mySubscriptionFrom(s)
		if err != nil {
			return mySubscriptionsResponse{}, err
		}
		out.Subscriptions = append(out.Subscriptions, v)
	}
	return out, nil
}

// mySubscriptionFrom 转一份订阅。
func mySubscriptionFrom(s service.MySubscription) (mySubscriptionResponse, error) {
	amount, err := cents(s.Price.Amount)
	if err != nil {
		return mySubscriptionResponse{}, err
	}
	v := mySubscriptionResponse{
		ID:                 s.ID.String(),
		PlanID:             s.PlanID.String(),
		PlanName:           s.PlanName,
		PlanVersion:        s.PlanVersion,
		Status:             s.Status,
		State:              string(s.State),
		PeriodStart:        utcPtr(&s.PeriodStart),
		PeriodEnd:          utcPtr(&s.PeriodEnd),
		Currency:           string(s.Price.Currency),
		Amount:             amount,
		Quotas:             quotasFrom(s.Quotas),
		DeviceLimit:        s.EffectiveDeviceLimit,
		OnlineDevices:      s.OnlineDevices,
		QuotaResetStrategy: s.QuotaResetStrategy,
		NextResetAt:        utcPtr(s.NextResetAt),
		Renewable:          s.Renewable,
		PackRemainingBytes: s.PackRemainingBytes,
		Label:              s.Label,
		ClientName:         s.ClientName,
		Changeable:         s.Changeable,
		RenewUntil:         utcPtr(s.RenewUntil),
	}
	if s.Price.PriceID != nil {
		v.PriceID = s.Price.PriceID.String()
	}
	if p := s.RenewalPrice; p != nil {
		unit, err := cents(p.UnitAmount)
		if err != nil {
			return mySubscriptionResponse{}, err
		}
		v.RenewalPrice = &renewalPriceResponse{
			ID:              p.ID.String(),
			Currency:        string(p.Currency),
			UnitAmount:      unit,
			BillingInterval: p.BillingInterval,
			IntervalCount:   p.IntervalCount,
			Available:       p.Available,
		}
	}
	return v, nil
}

// quotasFrom 转配额行；永不为 nil。
func quotasFrom(qs []service.Quota) []quotaResponse {
	out := make([]quotaResponse, 0, len(qs))
	for _, q := range qs {
		out = append(out, quotaResponse{
			Metric:      q.Metric,
			Limit:       q.Limit,
			Consumed:    q.Consumed,
			Remaining:   q.Remaining,
			Period:      q.Period,
			PeriodStart: utc(q.PeriodStart),
			PeriodEnd:   utcPtr(q.PeriodEnd),
		})
	}
	return out
}

// renameRequest 是 PATCH /me/subscriptions/:id：label 为 null 或空白表示清除名字。
type renameRequest struct {
	Label *string `json:"label" binding:"omitempty,max=256"`
}

// renameResponse 是改名之后这份订阅的名字。
type renameResponse struct {
	Label      *string `json:"label"`
	ClientName string  `json:"client_name"`
}

// packResponse 是用户看到的一笔流量包余额；order_id 只在来源是订单时有。
type packResponse struct {
	ID             string    `json:"id"`
	SubscriptionID *string   `json:"subscription_id"`
	Source         string    `json:"source"`
	OrderID        *string   `json:"order_id"`
	GrantedBytes   int64     `json:"granted_bytes"`
	ConsumedBytes  int64     `json:"consumed_bytes"`
	RemainingBytes int64     `json:"remaining_bytes"`
	CreatedAt      time.Time `json:"created_at"`
}

// packBalanceResponse 是 GET /me/traffic-packs 的响应。
type packBalanceResponse struct {
	RemainingBytesTotal int64          `json:"remaining_bytes_total"`
	Packs               []packResponse `json:"packs"`
}

// packBalanceFrom 转流量包余额。
func packBalanceFrom(in service.PackBalance) packBalanceResponse {
	out := packBalanceResponse{RemainingBytesTotal: in.RemainingBytesTotal, Packs: make([]packResponse, 0, len(in.Packs))}
	for _, p := range in.Packs {
		v := packResponse{
			ID:             p.ID.String(),
			Source:         p.Source,
			GrantedBytes:   p.GrantedBytes,
			ConsumedBytes:  p.ConsumedBytes,
			RemainingBytes: p.RemainingBytes,
			CreatedAt:      utc(p.CreatedAt),
		}
		if p.SubscriptionID != nil {
			id := p.SubscriptionID.String()
			v.SubscriptionID = &id
		}
		if p.Source == service.PackSourceOrder {
			id := p.SourceID.String()
			v.OrderID = &id
		}
		out.Packs = append(out.Packs, v)
	}
	return out
}

// transferRequest：from_subscription_id 为 null 表示「还没加到任何一份」的流量包。
type transferRequest struct {
	FromSubscriptionID *string `json:"from_subscription_id" binding:"omitempty,uuid"`
	ToSubscriptionID   string  `json:"to_subscription_id"`
}

// transferResponse 是转移结果。
type transferResponse struct {
	MovedBytes int64 `json:"moved_bytes"`
}

// ---- 后台 ----

// extendRequest 是后台加时长。
type extendRequest struct {
	Days   int    `json:"days"`
	Reason string `json:"reason"`
}

// extendResponse 是加时长的响应（幂等回放同一份）。
type extendResponse struct {
	SubscriptionID string    `json:"subscription_id"`
	UserEmail      string    `json:"user_email"`
	Days           int       `json:"days"`
	PreviousEnd    time.Time `json:"previous_end"`
	PeriodEnd      time.Time `json:"period_end"`
}

// trafficGrantRequest 是后台加流量包（字节）。
type trafficGrantRequest struct {
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
}

// trafficGrantResponse 是加流量包的响应。
type trafficGrantResponse struct {
	SubscriptionID      string `json:"subscription_id"`
	UserID              string `json:"user_id"`
	UserEmail           string `json:"user_email"`
	GrantID             string `json:"grant_id"`
	GrantedBytes        int64  `json:"granted_bytes"`
	RemainingBytesTotal int64  `json:"remaining_bytes_total"`
}

// trafficResetRequest 是后台手动重置流量。
type trafficResetRequest struct {
	Note string `json:"note"`
}

// trafficResetResponse 是手动重置的响应。
type trafficResetResponse struct {
	Reset      bool  `json:"reset"`
	FreedBytes int64 `json:"freed_bytes"`
}

// deviceLimitRequest：limit 为 null 沿用套餐版本，0 不限。
type deviceLimitRequest struct {
	Limit *int `json:"limit"`
}

// okResponse 是只回成功的响应。
type okResponse struct {
	OK bool `json:"ok"`
}

// resetLogsQuery 是重置日志的筛选参数。
type resetLogsQuery struct {
	Reason string `form:"reason" binding:"omitempty,oneof=renewal cycle_roll manual gift_card plan_change"`
	UserID string `form:"user_id" binding:"omitempty,uuid"`
}

// resetLogResponse 是一条重置日志。
type resetLogResponse struct {
	ID             string    `json:"id"`
	UserEmail      string    `json:"user_email"`
	PlanName       string    `json:"plan_name,omitempty"`
	Metric         string    `json:"metric"`
	Reason         string    `json:"reason"`
	ConsumedBefore int64     `json:"consumed_before"`
	ActorEmail     string    `json:"actor_email,omitempty"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// resetLogsResponse 是重置日志列表。
type resetLogsResponse struct {
	Logs  []resetLogResponse `json:"logs"`
	Total int64              `json:"total"`
}

// resetLogsFrom 转重置日志；永不为 nil。
func resetLogsFrom(rows []service.ResetLogView, total int64) resetLogsResponse {
	out := resetLogsResponse{Logs: make([]resetLogResponse, 0, len(rows)), Total: total}
	for _, r := range rows {
		out.Logs = append(out.Logs, resetLogResponse{
			ID:             r.ID.String(),
			UserEmail:      r.UserEmail,
			PlanName:       r.PlanName,
			Metric:         r.Metric,
			Reason:         r.Reason,
			ConsumedBefore: r.ConsumedBefore,
			ActorEmail:     r.ActorEmail,
			Note:           r.Note,
			CreatedAt:      utc(r.CreatedAt),
		})
	}
	return out
}

// resetStatsResponse 是近 30 天的重置统计。
type resetStatsResponse struct {
	Last30Days  int            `json:"last_30_days"`
	ByReason    map[string]int `json:"by_reason"`
	FreedBytes  int64          `json:"freed_bytes"`
	ManualCount int            `json:"manual_count"`
}
