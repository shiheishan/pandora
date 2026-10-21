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
	Bonus       int64      `json:"bonus_bytes"`
	Consumed    int64      `json:"consumed"`
	Remaining   *int64     `json:"remaining"`
	Period      string     `json:"period"`
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   *time.Time `json:"period_end"`
}

// planUsageResponse 是一份订阅按套餐版本给的用量；三项 null 分别表示不限流量、不限台数、不限速。
type planUsageResponse struct {
	TrafficBytes *int64 `json:"traffic_bytes"`
	DeviceLimit  *int   `json:"device_limit"`
	ThrottleKbps *int   `json:"throttle_kbps"`
}

// renewalPriceResponse 是续费界面要的当前价格档（金额为分）。
type renewalPriceResponse struct {
	ID              string `json:"id"`
	UnitAmount      int64  `json:"unit_amount"`
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	Available       bool   `json:"available"`
}

// mySubscriptionResponse 是门户「我的套餐」里的一份。
type mySubscriptionResponse struct {
	ID            string          `json:"id"`
	PlanID        string          `json:"plan_id"`
	PriceID       string          `json:"price_id"`
	PlanName      string          `json:"plan_name"`
	PlanKind      string          `json:"plan_kind"`
	PlanVersion   int             `json:"plan_version"`
	Status        string          `json:"status"`
	State         string          `json:"state"`
	PeriodStart   *time.Time      `json:"current_period_start"`
	PeriodEnd     *time.Time      `json:"current_period_end"`
	Amount        int64           `json:"amount"`
	ValidityDays  *int            `json:"validity_days"`
	Quotas        []quotaResponse `json:"quotas"`
	DeviceLimit   *int            `json:"device_limit"`
	OnlineDevices int             `json:"online_devices"`
	// TrafficLimit、TrafficBonus、TrafficUsed 取自流量配额行；Permanent 为一次性套餐买的永久
	TrafficLimit *int64                `json:"traffic_limit"`
	TrafficBonus int64                 `json:"bonus_bytes"`
	TrafficUsed  int64                 `json:"traffic_used"`
	Permanent    bool                  `json:"permanent"`
	ResetsUsed   int                   `json:"resets_used"`
	ResetLimit   *int                  `json:"reset_limit"`
	ResetPrice   *int64                `json:"reset_price"`
	NextResetAt  *time.Time            `json:"next_reset_at"`
	Renewable    bool                  `json:"renewable"`
	Stackable    bool                  `json:"stackable"`
	RenewalPrice *renewalPriceResponse `json:"renewal_price"`
	Label        *string               `json:"label"`
	ClientName   string                `json:"client_name"`
	Changeable   bool                  `json:"changeable"`
	RenewUntil   *time.Time            `json:"renew_until"`
	Usage        planUsageResponse     `json:"usage"`
	NextUsage    *planUsageResponse    `json:"next_usage"`
	IsTrial      bool                  `json:"is_trial"`
}

// mySubscriptionsResponse 是 GET /me/subscriptions 的响应。
type mySubscriptionsResponse struct {
	Subscriptions []mySubscriptionResponse `json:"subscriptions"`
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
	out := mySubscriptionsResponse{Subscriptions: make([]mySubscriptionResponse, 0, len(in.Subscriptions))}
	for _, s := range in.Subscriptions {
		v, err := mySubscriptionFrom(s)
		if err != nil {
			return mySubscriptionsResponse{}, err
		}
		out.Subscriptions = append(out.Subscriptions, v)
	}
	return out, nil
}

// planUsageFrom 转一份套餐用量。
func planUsageFrom(u service.Usage) planUsageResponse {
	return planUsageResponse{
		TrafficBytes: u.TrafficBytes,
		DeviceLimit:  u.DeviceLimit,
		ThrottleKbps: u.ThrottleKbps,
	}
}

// mySubscriptionFrom 转一份订阅。
func mySubscriptionFrom(s service.MySubscription) (mySubscriptionResponse, error) {
	amount, err := cents(s.Price.Amount)
	if err != nil {
		return mySubscriptionResponse{}, err
	}
	v := mySubscriptionResponse{
		ID:            s.ID.String(),
		PlanID:        s.PlanID.String(),
		PlanName:      s.PlanName,
		PlanKind:      s.PlanKind,
		PlanVersion:   s.PlanVersion,
		Status:        s.Status,
		State:         string(s.State),
		PeriodStart:   utcPtr(&s.PeriodStart),
		PeriodEnd:     utcPtr(&s.PeriodEnd),
		Amount:        amount,
		ValidityDays:  s.Price.ValidityDays,
		Quotas:        quotasFrom(s.Quotas),
		DeviceLimit:   s.EffectiveDeviceLimit,
		OnlineDevices: s.OnlineDevices,
		TrafficLimit:  s.TrafficLimit,
		TrafficBonus:  s.TrafficBonus,
		TrafficUsed:   s.TrafficUsed,
		Permanent:     s.Permanent,
		ResetsUsed:    s.ResetsUsed,
		ResetLimit:    s.ResetLimit,
		NextResetAt:   utcPtr(s.NextResetAt),
		Renewable:     s.Renewable,
		Stackable:     s.Stackable,
		Label:         s.Label,
		ClientName:    s.ClientName,
		Changeable:    s.Changeable,
		RenewUntil:    utcPtr(s.RenewUntil),
		Usage:         planUsageFrom(s.Usage),
		IsTrial:       s.IsTrial,
	}
	if s.NextUsage != nil {
		nu := planUsageFrom(*s.NextUsage)
		v.NextUsage = &nu
	}
	if s.ResetPrice != nil {
		c, err := cents(*s.ResetPrice)
		if err != nil {
			return mySubscriptionResponse{}, err
		}
		v.ResetPrice = &c
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
			Bonus:       q.Bonus,
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

// trafficGrantRequest 是后台加本期流量（字节）。
type trafficGrantRequest struct {
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
}

// trafficGrantResponse 是加本期流量的响应；BonusBytes 是加完后本期送的量合计。
type trafficGrantResponse struct {
	SubscriptionID string `json:"subscription_id"`
	UserID         string `json:"user_id"`
	UserEmail      string `json:"user_email"`
	GrantedBytes   int64  `json:"granted_bytes"`
	BonusBytes     int64  `json:"bonus_bytes"`
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
	Reason string `form:"reason" binding:"omitempty,oneof=renewal cycle_roll manual gift_card plan_change order"`
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
