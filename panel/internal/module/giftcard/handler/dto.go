package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 金额一律整数分；JSON 形状对照前端 zod
// （portal/screens/wallet/schemas.ts、admin/screens/marketing/queries.ts）。

// cents 把元换成分，记下第一处换算错误（金额超出 2 位小数是程序错误，对外 500）。
type cents struct{ err error }

func (c *cents) of(d decimal.Decimal) int64 {
	v, err := money.ToCents(d)
	if err != nil && c.err == nil {
		c.err = errs.Wrap(errs.Internal, err)
	}
	return v
}

// ---------------------------------------------------------------------------
// 共用：奖励、条件、限制
// ---------------------------------------------------------------------------

type prizeDTO struct {
	Label        string `json:"label"`
	Weight       int    `json:"weight"`
	Balance      int64  `json:"balance,omitempty"`
	TrafficBytes int64  `json:"traffic_bytes,omitempty"`
	ExpireDays   int    `json:"expire_days,omitempty"`
}

type rewardsDTO struct {
	Balance      int64      `json:"balance,omitempty"`
	TrafficBytes int64      `json:"traffic_bytes,omitempty"`
	ExpireDays   int        `json:"expire_days,omitempty"`
	ResetQuota   bool       `json:"reset_quota,omitempty"`
	PlanID       string     `json:"plan_id,omitempty"`
	PriceID      string     `json:"price_id,omitempty"`
	Pool         []prizeDTO `json:"pool,omitempty"`
}

type conditionsDTO struct {
	NewUserOnly  bool `json:"new_user_only,omitempty"`
	PaidUserOnly bool `json:"paid_user_only,omitempty"`
}

type limitsDTO struct {
	MaxUsePerUser int `json:"max_use_per_user,omitempty"`
}

func rewardsFrom(r service.Rewards, c *cents) rewardsDTO {
	out := rewardsDTO{
		Balance:      c.of(r.Balance),
		TrafficBytes: r.TrafficBytes,
		ExpireDays:   r.ExpireDays,
		ResetQuota:   r.ResetQuota,
	}
	if r.PlanID != nil {
		out.PlanID = r.PlanID.String()
	}
	if r.PriceID != nil {
		out.PriceID = r.PriceID.String()
	}
	for _, p := range r.Pool {
		out.Pool = append(out.Pool, prizeDTO{
			Label: p.Label, Weight: p.Weight, Balance: c.of(p.Balance),
			TrafficBytes: p.TrafficBytes, ExpireDays: p.ExpireDays,
		})
	}
	return out
}

func conditionsFrom(c service.Conditions) conditionsDTO {
	return conditionsDTO{NewUserOnly: c.NewUserOnly, PaidUserOnly: c.PaidUserOnly}
}

func limitsFrom(l service.Limits) limitsDTO {
	return limitsDTO{MaxUsePerUser: l.MaxUsePerUser}
}

// optUUID 解析可空的 id；格式不对回 422（字段名 field）。
func optUUID(field, raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{field: "格式不正确"})
	}
	return &id, nil
}

// ---------------------------------------------------------------------------
// 门户
// ---------------------------------------------------------------------------

type choiceDTO struct {
	Kind           string `json:"kind"`
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// codeRequest 是预览与兑换的请求体；choice 只有兑换要，选项只有一个时可以不传。
type codeRequest struct {
	Code   string     `json:"code"`
	Choice *choiceDTO `json:"choice,omitempty"`
}

// choice 把请求里的选择转成 service 的；subscription_id 格式不对按「这个用法现在不能用了」处理。
func (r codeRequest) choice() (*service.Choice, error) {
	if r.Choice == nil || strings.TrimSpace(r.Choice.Kind) == "" {
		return nil, nil
	}
	out := &service.Choice{Kind: strings.TrimSpace(r.Choice.Kind)}
	if raw := strings.TrimSpace(r.Choice.SubscriptionID); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, service.ErrChoiceStale
		}
		out.SubscriptionID = &id
	}
	return out, nil
}

type placementOptionDTO struct {
	Key            string     `json:"key"`
	Kind           string     `json:"kind"`
	SubscriptionID string     `json:"subscription_id,omitempty"`
	Expired        bool       `json:"expired,omitempty"`
	Badge          string     `json:"badge,omitempty"`
	Label          string     `json:"label,omitempty"`
	PlanID         string     `json:"plan_id,omitempty"`
	PlanName       string     `json:"plan_name,omitempty"`
	State          string     `json:"state,omitempty"`
	PeriodEnd      *time.Time `json:"period_end,omitempty"`
	NewPeriodEnd   *time.Time `json:"new_period_end,omitempty"`
	NewPermanent   bool       `json:"new_permanent,omitempty"`
	Credit         int64      `json:"credit,omitempty"`
	GiftDaysLost   int        `json:"gift_days_lost,omitempty"`
	TrafficUsed    int64      `json:"traffic_used,omitempty"`
	TrafficCap     int64      `json:"traffic_cap,omitempty"`
}

type placementDTO struct {
	Question   string               `json:"question"`
	Options    []placementOptionDTO `json:"options"`
	DefaultKey string               `json:"default_key"`
}

type cardDTO struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Type          string        `json:"type"`
	Status        string        `json:"status"`
	Rewards       rewardsDTO    `json:"rewards"`
	Conditions    conditionsDTO `json:"conditions"`
	Limits        limitsDTO     `json:"limits"`
	CreatedAt     time.Time     `json:"created_at"`
	PlanName      string        `json:"plan_name,omitempty"`
	PlanKind      string        `json:"plan_kind,omitempty"`
	Interval      string        `json:"interval,omitempty"`
	IntervalCount int           `json:"interval_count,omitempty"`
	ValidityDays  *int          `json:"validity_days,omitempty"`
	Placement     *placementDTO `json:"placement"`
}

type previewResponse struct {
	Card cardDTO `json:"card"`
}

func previewFrom(p service.CardPreview) (previewResponse, error) {
	var c cents
	card := cardDTO{
		ID:            p.ID.String(),
		Name:          p.Name,
		Type:          p.Type,
		Status:        p.Status,
		Rewards:       rewardsFrom(p.Rewards, &c),
		Conditions:    conditionsFrom(p.Conditions),
		Limits:        limitsFrom(p.Limits),
		CreatedAt:     p.CreatedAt,
		PlanName:      p.PlanName,
		PlanKind:      p.PlanKind,
		Interval:      p.Interval,
		IntervalCount: p.IntervalCount,
		ValidityDays:  p.ValidityDays,
	}
	if p.Placement != nil {
		pl := &placementDTO{Question: p.Placement.Question, DefaultKey: p.Placement.DefaultKey,
			Options: make([]placementOptionDTO, 0, len(p.Placement.Options))}
		for _, o := range p.Placement.Options {
			v := placementOptionDTO{
				Key: o.Key, Kind: o.Kind, Expired: o.Expired, Badge: o.Badge,
				Label: o.Label, PlanName: o.PlanName, State: o.State,
				PeriodEnd: o.PeriodEnd, NewPeriodEnd: o.NewPeriodEnd, NewPermanent: o.NewPermanent,
				Credit: c.of(o.Credit), GiftDaysLost: o.GiftDaysLost,
				TrafficUsed: o.TrafficUsed, TrafficCap: o.TrafficCap,
			}
			if o.SubscriptionID != nil {
				v.SubscriptionID = o.SubscriptionID.String()
			}
			if o.PlanID != nil {
				v.PlanID = o.PlanID.String()
			}
			pl.Options = append(pl.Options, v)
		}
		card.Placement = pl
	}
	return previewResponse{Card: card}, c.err
}

type redeemResponse struct {
	TemplateName string   `json:"template_name"`
	Type         string   `json:"type"`
	PrizeLabel   string   `json:"prize_label,omitempty"`
	Balance      int64    `json:"balance,omitempty"`
	TrafficBytes int64    `json:"traffic_bytes,omitempty"`
	ExpireDays   int      `json:"expire_days,omitempty"`
	QuotaReset   bool     `json:"quota_reset,omitempty"`
	PlanGranted  string   `json:"plan_granted,omitempty"`
	Summary      []string `json:"summary"`
}

func redeemFrom(r service.RedeemResult) (redeemResponse, error) {
	var c cents
	out := redeemResponse{
		TemplateName: r.TemplateName,
		Type:         r.Type,
		PrizeLabel:   r.PrizeLabel,
		Balance:      c.of(r.Balance),
		TrafficBytes: r.TrafficBytes,
		ExpireDays:   r.ExpireDays,
		QuotaReset:   r.QuotaReset,
		PlanGranted:  r.PlanGranted,
		Summary:      r.Summary,
	}
	if out.Summary == nil {
		out.Summary = []string{}
	}
	return out, c.err
}

// redeemReply 是交给 service 的幂等重放响应：与首发同一个转换、同为 200。
func redeemReply(r service.RedeemResult) (idemsvc.Response, error) {
	out, err := redeemFrom(r)
	if err != nil {
		return idemsvc.Response{}, err
	}
	return idemsvc.JSON(http.StatusOK, out)
}

type myRedemptionDTO struct {
	TemplateName string    `json:"template_name"`
	Type         string    `json:"type"`
	CodeHint     string    `json:"code_hint"`
	PrizeLabel   string    `json:"prize_label,omitempty"`
	Balance      int64     `json:"balance,omitempty"`
	TrafficBytes int64     `json:"traffic_bytes,omitempty"`
	ExpireDays   int       `json:"expire_days,omitempty"`
	RedeemedAt   time.Time `json:"redeemed_at"`
}

type myRedemptionsResponse struct {
	Redemptions []myRedemptionDTO `json:"redemptions"`
}

func myRedemptionsFrom(rows []service.MyRedemption) (myRedemptionsResponse, error) {
	var c cents
	out := myRedemptionsResponse{Redemptions: make([]myRedemptionDTO, 0, len(rows))}
	for _, r := range rows {
		out.Redemptions = append(out.Redemptions, myRedemptionDTO{
			TemplateName: r.TemplateName, Type: r.Type, CodeHint: r.CodeHint, PrizeLabel: r.PrizeLabel,
			Balance: c.of(r.Balance), TrafficBytes: r.TrafficBytes, ExpireDays: r.ExpireDays, RedeemedAt: r.RedeemedAt,
		})
	}
	return out, c.err
}
