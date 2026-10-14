package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/commission/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/shopspring/decimal"
)

// overrideRequest 是后台设置某人单独佣金的请求体：按邮箱找人；rate_percent 与 scope 给 null 或不给表示这一项跟全局走（两项至少给一项）。
type overrideRequest struct {
	Email       string  `json:"email"`
	RatePercent *int    `json:"rate_percent"`
	Scope       *string `json:"scope"`
}

// overrideResponse 是单独佣金设置的一行。
type overrideResponse struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	RatePercent *int      `json:"rate_percent"`
	Scope       *string   `json:"scope"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// listOverridesResponse 是 GET /commission/overrides 的响应。
type listOverridesResponse struct {
	Overrides []overrideResponse `json:"overrides"`
}

// setOverrideResponse 是 POST /commission/overrides 的响应。
type setOverrideResponse struct {
	Override overrideResponse `json:"override"`
}

// overrideFrom 把 service 的一行单独设置转成响应。
func overrideFrom(o service.UserOverride) overrideResponse {
	return overrideResponse{
		UserID:      o.UserID.String(),
		Email:       o.Email,
		RatePercent: o.RatePercent,
		Scope:       o.Scope,
		UpdatedAt:   o.UpdatedAt,
	}
}

// parsePaidUSDT 解析实付 USDT 数量（十进制字符串）；nil 或去空白后为空串回 nil；格式不合法回 Invalid 错误。
func parsePaidUSDT(s *string) (*decimal.Decimal, error) {
	if s == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil, nil
	}
	d, err := decimal.NewFromString(trimmed)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"paid_usdt_amount": "实付 USDT 数量格式不对，填数字，如 100.5"})
	}
	return &d, nil
}
