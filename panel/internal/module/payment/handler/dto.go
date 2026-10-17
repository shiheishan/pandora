package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// paymentMethodLabels 是常见渠道内方式的中文名。
var paymentMethodLabels = map[string]string{
	"alipay": "支付宝",
	"wxpay":  "微信支付",
	"qqpay":  "QQ 钱包",
}

// paymentMethodsResponse 对应 GET /v1/payment-methods。
type paymentMethodsResponse struct {
	Methods []paymentMethodResponse `json:"methods"`
}

// paymentMethodResponse 是门户可选的一种支付方式。
type paymentMethodResponse struct {
	Provider   string `json:"provider"`
	Method     string `json:"method"`
	Label      string `json:"label"`
	FeeFixed   int64  `json:"fee_fixed"`
	FeePercent string `json:"fee_percent"`
	Fee        *int64 `json:"fee,omitempty"`
	Total      *int64 `json:"total,omitempty"`
}

func paymentMethodFrom(m service.PaymentMethod) (paymentMethodResponse, error) {
	label := m.DisplayName
	if l, ok := paymentMethodLabels[m.Method]; ok {
		label = l
	}
	feeFixed, err := money.ToCents(m.FeeFixed)
	if err != nil {
		return paymentMethodResponse{}, err
	}
	out := paymentMethodResponse{
		Provider: m.Provider, Method: m.Method, Label: label,
		FeeFixed: feeFixed, FeePercent: m.FeePercent.StringFixed(2),
	}
	if m.Fee != nil {
		c, err := money.ToCents(*m.Fee)
		if err != nil {
			return paymentMethodResponse{}, err
		}
		out.Fee = &c
	}
	if m.Total != nil {
		c, err := money.ToCents(*m.Total)
		if err != nil {
			return paymentMethodResponse{}, err
		}
		out.Total = &c
	}
	return out, nil
}

// providerSettingsRequest 是渠道可编辑配置（新建内嵌、编辑单独使用）。
type providerSettingsRequest struct {
	DisplayName      string   `json:"display_name"`
	BaseURL          string   `json:"base_url"`
	SubmitPath       string   `json:"submit_path"`
	APIPath          string   `json:"api_path"`
	Methods          []string `json:"methods"`
	DefaultMethod    string   `json:"default_method"`
	AllowPrivateHost bool     `json:"allow_private_host"`
	MinAmount        int64    `json:"min_amount"`
	FeeFixed         *int64   `json:"fee_fixed"`
	FeePercent       *string  `json:"fee_percent"`
	MerchantID       string   `json:"merchant_id"`
	Key              string   `json:"key"`
	WebhookSecret    string   `json:"webhook_secret"`
}

func (r providerSettingsRequest) settings() (service.ProviderSettings, error) {
	out := service.ProviderSettings{
		DisplayName: r.DisplayName, BaseURL: r.BaseURL,
		SubmitPath: r.SubmitPath, APIPath: r.APIPath,
		Methods: r.Methods, DefaultMethod: r.DefaultMethod,
		AllowPrivateHost: r.AllowPrivateHost,
		MinAmount:        money.FromCents(r.MinAmount),
		MerchantID:       r.MerchantID, Key: r.Key,
		WebhookSecret: r.WebhookSecret,
	}
	if r.FeeFixed != nil {
		if *r.FeeFixed < 0 {
			return service.ProviderSettings{}, errs.Invalid(map[string]string{
				"fee_fixed": "固定手续费不能为负",
			})
		}
		d := money.FromCents(*r.FeeFixed)
		out.FeeFixed = &d
	}
	if r.FeePercent != nil {
		s := strings.TrimSpace(*r.FeePercent)
		if strings.ContainsAny(s, "eE") {
			return service.ProviderSettings{}, errs.Invalid(map[string]string{
				"fee_percent": "比例手续费需是数字，如 2.5",
			})
		}
		d, err := decimal.NewFromString(s)
		if err != nil {
			return service.ProviderSettings{}, errs.Invalid(map[string]string{
				"fee_percent": "比例手续费需是数字，如 2.5",
			})
		}
		out.FeePercent = &d
	}
	return out, nil
}

// createProviderRequest 是新建渠道请求体。
type createProviderRequest struct {
	Code    string `json:"code"`
	Adapter string `json:"adapter"`
	providerSettingsRequest
}

// providerWriteResponse 是建、改渠道成功响应。
type providerWriteResponse struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	CredentialsChanged bool   `json:"credentials_changed"`
}

func providerWriteFrom(r service.ProviderWriteResult) providerWriteResponse {
	return providerWriteResponse{
		ID: r.ID.String(), Code: r.Code, CredentialsChanged: r.CredentialsChanged,
	}
}

// listProvidersResponse 对应 GET /v1/payment-providers。
type listProvidersResponse struct {
	Providers []providerResponse `json:"providers"`
}

// providerResponse 是后台渠道卡一行。
type providerResponse struct {
	ID               string     `json:"id"`
	Code             string     `json:"code"`
	Adapter          string     `json:"adapter"`
	DisplayName      string     `json:"display_name"`
	Enabled          bool       `json:"enabled"`
	AcceptingNew     bool       `json:"accepting_new"`
	HasCredentials   bool       `json:"has_credentials"`
	BaseURL          string     `json:"base_url"`
	SubmitPath       string     `json:"submit_path"`
	APIPath          string     `json:"api_path"`
	Methods          []string   `json:"methods"`
	DefaultMethod    string     `json:"default_method"`
	AllowPrivateHost bool       `json:"allow_private_host"`
	MinAmount        int64      `json:"min_amount"`
	FeeFixed         int64      `json:"fee_fixed"`
	FeePercent       string     `json:"fee_percent"`
	HasWebhookSecret bool       `json:"has_webhook_secret"`
	NotifyURL        string     `json:"notify_url"`
	Today            int64      `json:"today"`
	SuccessRate24h   *float64   `json:"success_rate_24h"`
	LastCallbackAt   *time.Time `json:"last_callback_at"`
}

func providerFrom(v service.ProviderView) (providerResponse, error) {
	minAmt, err := money.ToCents(v.MinAmount)
	if err != nil {
		return providerResponse{}, err
	}
	methods := v.Methods
	if methods == nil {
		methods = []string{}
	}
	today, err := money.ToCents(v.Today)
	if err != nil {
		return providerResponse{}, err
	}
	feeFixed, err := money.ToCents(v.FeeFixed)
	if err != nil {
		return providerResponse{}, err
	}
	return providerResponse{
		ID: v.ID.String(), Code: v.Code, Adapter: v.Adapter, DisplayName: v.DisplayName,
		Enabled: v.Enabled, AcceptingNew: v.AcceptingNew, HasCredentials: v.HasCredentials,
		BaseURL:    v.BaseURL,
		SubmitPath: v.SubmitPath, APIPath: v.APIPath, Methods: methods,
		DefaultMethod: v.DefaultMethod, AllowPrivateHost: v.AllowPrivateHost,
		MinAmount: minAmt, FeeFixed: feeFixed, FeePercent: v.FeePercent.StringFixed(2),
		HasWebhookSecret: v.HasWebhookSecret, NotifyURL: v.NotifyURL,
		Today:          today,
		SuccessRate24h: v.SuccessRate24h, LastCallbackAt: v.LastCallbackAt,
	}, nil
}

// toggleProviderRequest 是渠道开关请求体。
type toggleProviderRequest struct {
	Enabled      bool `json:"enabled"`
	AcceptingNew bool `json:"accepting_new"`
}

// toggleProviderResponse 对应 POST .../toggle 成功响应。
type toggleProviderResponse struct {
	OK bool `json:"ok"`
}

// listLatePaymentsResponse 对应 GET /v1/late-payments。
type listLatePaymentsResponse struct {
	Cases         []lateCaseResponse `json:"cases"`
	Total         int64              `json:"total"`
	PendingAmount int64              `json:"pending_amount"`
}

// lateCaseResponse 是挂账列表一行。
type lateCaseResponse struct {
	ID               string     `json:"id"`
	CaseKind         string     `json:"case_kind"`
	Status           string     `json:"status"`
	Amount           int64      `json:"amount"`
	OrderNo          string     `json:"order_no"`
	OrderStatus      string     `json:"order_status"`
	UserID           string     `json:"user_id"`
	UserEmail        string     `json:"user_email"`
	ReceivedAt       time.Time  `json:"received_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	ResolutionReason *string    `json:"resolution_reason,omitempty"`
}

func lateCaseFrom(c service.LateCase) (lateCaseResponse, error) {
	amt, err := money.ToCents(c.Amount)
	if err != nil {
		return lateCaseResponse{}, err
	}
	return lateCaseResponse{
		ID: c.ID.String(), CaseKind: string(c.Kind), Status: string(c.Status),
		Amount:  amt,
		OrderNo: c.OrderNo, OrderStatus: c.OrderStatus,
		UserID: c.UserID.String(), UserEmail: c.UserEmail,
		ReceivedAt: c.ReceivedAt,
		ResolvedAt: c.ResolvedAt, ResolutionReason: c.ResolutionReason,
	}, nil
}

// applyLatePaymentRequest 是挂账转入余额请求体。
type applyLatePaymentRequest struct {
	Reason string `json:"reason"`
}

// applyLatePaymentResponse 对应转入余额成功响应。
type applyLatePaymentResponse struct {
	LedgerTxnID string `json:"ledger_txn_id"`
}

// paymentSettingsResponse 对应 GET/PUT /v1/payment-settings。
type paymentSettingsResponse struct {
	NotifyBaseURL string `json:"notify_base_url"` // 存下的回调地址前缀；空 = 用站点地址
	EffectiveBase string `json:"effective_base"`  // 实际生效的前缀
	PublicBaseURL string `json:"public_base_url"` // 站点地址
}

// paymentSettingsRequest 是 PUT /v1/payment-settings 请求体。
type paymentSettingsRequest struct {
	NotifyBaseURL string `json:"notify_base_url"`
}
