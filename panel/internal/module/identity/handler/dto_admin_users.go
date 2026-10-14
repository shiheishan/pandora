package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// admCents 把元换成分，记下第一处换算错误（金额超出 2 位小数是程序错误，对外 500）。
type admCents struct{ err error }

func (c *admCents) of(d decimal.Decimal) int64 {
	v, err := money.ToCents(d)
	if err != nil && c.err == nil {
		c.err = errs.Wrap(errs.Internal, err)
	}
	return v
}

const admMetricTraffic = "traffic.bytes"

type admTrafficResponse struct {
	Limit    *int64 `json:"limit"`
	Consumed int64  `json:"consumed"`
}

type admCurrentSubResponse struct {
	ID               string             `json:"id"`
	PlanName         string             `json:"plan_name"`
	Status           string             `json:"status"`
	CurrentPeriodEnd *time.Time         `json:"current_period_end"`
	Traffic          admTrafficResponse `json:"traffic"`
	DeviceLimit      int                `json:"device_limit"`
	OnlineDevices    int                `json:"online_devices"`
}

type admQuotaResponse struct {
	Metric    string `json:"metric"`
	Limit     *int64 `json:"limit"`
	Consumed  int64  `json:"consumed"`
	Remaining *int64 `json:"remaining"`
}

// admSubscriptionResponse 是后台用户详情里的一份订阅。
type admSubscriptionResponse struct {
	ID                  string             `json:"id"`
	Label               *string            `json:"label"`
	PlanName            string             `json:"plan_name"`
	PlanVersion         int                `json:"plan_version"`
	Status              string             `json:"status"`
	CurrentPeriodStart  *time.Time         `json:"current_period_start"`
	CurrentPeriodEnd    *time.Time         `json:"current_period_end"`
	Amount              int64              `json:"amount"`
	Currency            string             `json:"currency"`
	AutoRenew           bool               `json:"auto_renew"` // 新栈不做自动续费，恒 false
	Quotas              []admQuotaResponse `json:"quotas"`
	DeviceLimitOverride *int               `json:"device_limit_override"`
	PlanMaxDevices      *int               `json:"plan_max_devices"`
	OnlineDevices       int                `json:"online_devices"`
	PackRemainingBytes  int64              `json:"pack_remaining_bytes"`
}

// admOrderRowResponse 键与 order 模块 adminOrderRowResponse 完全相同。
type admOrderRowResponse struct {
	ID             string     `json:"id"`
	OrderNo        string     `json:"order_no"`
	UserEmail      string     `json:"user_email"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	Currency       string     `json:"currency"`
	TotalAmount    int64      `json:"total_amount"`
	PayableAmount  int64      `json:"payable_amount"`
	PaidAmount     int64      `json:"paid_amount"`
	RefundedAmount int64      `json:"refunded_amount"`
	BalanceApplied int64      `json:"balance_applied"`
	CreatedAt      time.Time  `json:"created_at"`
	PaidAt         *time.Time `json:"paid_at"`
	ProviderCode   *string    `json:"provider_code"`
	ProviderName   *string    `json:"provider_name"`
	PlanName       string     `json:"plan_name"`
	Interval       string     `json:"interval"`
	IntervalCount  int        `json:"interval_count"`
	ItemCount      int        `json:"item_count"`
	Manual         bool       `json:"manual"`
}

type admPaidTotalResponse struct {
	Currency string `json:"currency"`
	Amount   int64  `json:"amount"`
}

type admTelegramResponse struct {
	Username string    `json:"username"`
	BoundAt  time.Time `json:"bound_at"`
}

type admPoolRefResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func admTrafficFromQuotas(quotas []service.AdminQuota) admTrafficResponse {
	for _, q := range quotas {
		if q.Metric == admMetricTraffic {
			return admTrafficResponse{Limit: q.Limit, Consumed: q.Consumed}
		}
	}
	return admTrafficResponse{Limit: nil, Consumed: 0}
}

func admDeviceLimitFromSub(s *service.AdminSub) int {
	if s == nil {
		return 0
	}
	if s.DeviceLimitOverride != nil {
		return *s.DeviceLimitOverride
	}
	if s.PlanMaxDevices != nil {
		return *s.PlanMaxDevices
	}
	return 0
}

func admCurrentSubFrom(s *service.AdminSub) *admCurrentSubResponse {
	if s == nil {
		return nil
	}
	end := s.PeriodEnd
	return &admCurrentSubResponse{
		ID:               s.ID.String(),
		PlanName:         s.PlanName,
		Status:           s.Status,
		CurrentPeriodEnd: &end,
		Traffic:          admTrafficFromQuotas(s.Quotas),
		DeviceLimit:      admDeviceLimitFromSub(s),
		OnlineDevices:    s.OnlineDevices,
	}
}

func admQuotaFrom(q service.AdminQuota) admQuotaResponse {
	return admQuotaResponse{
		Metric:    q.Metric,
		Limit:     q.Limit,
		Consumed:  q.Consumed,
		Remaining: q.Remaining,
	}
}

func admSubscriptionFrom(cc *admCents, s service.AdminSub) admSubscriptionResponse {
	ps, pe := s.PeriodStart, s.PeriodEnd
	quotas := make([]admQuotaResponse, 0, len(s.Quotas))
	for _, q := range s.Quotas {
		quotas = append(quotas, admQuotaFrom(q))
	}
	return admSubscriptionResponse{
		ID:                  s.ID.String(),
		Label:               s.Label,
		PlanName:            s.PlanName,
		PlanVersion:         s.PlanVersion,
		Status:              s.Status,
		CurrentPeriodStart:  &ps,
		CurrentPeriodEnd:    &pe,
		Amount:              cc.of(s.Amount),
		Currency:            s.Currency,
		AutoRenew:           false,
		Quotas:              quotas,
		DeviceLimitOverride: s.DeviceLimitOverride,
		PlanMaxDevices:      s.PlanMaxDevices,
		OnlineDevices:       s.OnlineDevices,
		PackRemainingBytes:  s.PackRemainingBytes,
	}
}

func admOrderRowFrom(cc *admCents, o service.AdminOrderRow) admOrderRowResponse {
	return admOrderRowResponse{
		ID:             o.ID.String(),
		OrderNo:        o.OrderNo,
		UserEmail:      o.UserEmail,
		Kind:           o.Kind,
		Status:         o.Status,
		Currency:       o.Currency,
		TotalAmount:    cc.of(o.Total),
		PayableAmount:  cc.of(o.Payable),
		PaidAmount:     cc.of(o.Paid),
		RefundedAmount: 0,
		BalanceApplied: cc.of(o.BalanceApplied),
		CreatedAt:      o.CreatedAt,
		PaidAt:         o.PaidAt,
		ProviderCode:   o.ProviderCode,
		ProviderName:   o.ProviderName,
		PlanName:       o.PlanName,
		Interval:       o.Interval,
		IntervalCount:  o.IntervalCount,
		ItemCount:      o.ItemCount,
		Manual:         o.Manual,
	}
}
