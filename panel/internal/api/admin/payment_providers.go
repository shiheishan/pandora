package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// 支付渠道：新建与编辑（w2pay）
//
// 写入与加密都在 billing（PaymentService 带信封与 devMode），这里只解请求体。
// 请求体里没有 code / adapter 以外的身份字段：编辑的请求体根本不收这两个键，
// DisallowUnknownFields 会把带了它们的请求拒掉——建后不可改由形状保证。
// 商户号与密钥只写不读：响应里只回「凭据是否变更」。
//------------------------------------------------------------------------------

type providerSettingsReq struct {
	DisplayName      string   `json:"display_name"`
	BaseURL          string   `json:"base_url"`
	SubmitPath       string   `json:"submit_path"`
	APIPath          string   `json:"api_path"`
	Methods          []string `json:"methods"`
	DefaultMethod    string   `json:"default_method"`
	AllowPrivateHost bool     `json:"allow_private_host"`
	// 编辑时留空 = 不改
	MerchantID string `json:"merchant_id"`
	Key        string `json:"key"`
}

func (r providerSettingsReq) settings() billing.ProviderSettings {
	return billing.ProviderSettings{
		DisplayName: r.DisplayName, BaseURL: r.BaseURL,
		SubmitPath: r.SubmitPath, APIPath: r.APIPath,
		Methods: r.Methods, DefaultMethod: r.DefaultMethod,
		AllowPrivateHost: r.AllowPrivateHost,
		MerchantID:       r.MerchantID, Key: r.Key,
	}
}

type createProviderReq struct {
	Code    string `json:"code"`
	Adapter string `json:"adapter"`
	providerSettingsReq
}

type providerWriteResponse struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	CredentialsChanged bool   `json:"credentials_changed"`
}

func providerWriteResponseFrom(out *billing.ProviderWriteResult) providerWriteResponse {
	return providerWriteResponse{ID: out.ID, Code: out.Code, CredentialsChanged: out.CredentialsChanged}
}

// createProvider 新建渠道。建出来是「已启用、暂停收新单」：结账页先看不到，
// 管理员核对无误后在渠道卡上打开收单开关。
func (h *handlers) createProvider(w http.ResponseWriter, r *http.Request) {
	var req createProviderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Payments.CreateProvider(r.Context(), httpx.TenantIDFrom(r.Context()),
		billing.ProviderActor{Kind: "admin", ID: httpx.PrincipalFrom(r.Context()).UserID},
		billing.CreateProviderInput{
			Code: req.Code, Adapter: req.Adapter, ProviderSettings: req.settings(),
			Enabled: true, AcceptingNew: false,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, providerWriteResponseFrom(out))
}

// updateProvider 编辑渠道。code 在路径上，adapter 不收；商户号、密钥留空表示不改。
func (h *handlers) updateProvider(w http.ResponseWriter, r *http.Request) {
	var req providerSettingsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Payments.UpdateProvider(r.Context(), httpx.TenantIDFrom(r.Context()),
		billing.ProviderActor{Kind: "admin", ID: httpx.PrincipalFrom(r.Context()).UserID},
		billing.UpdateProviderInput{Code: chi.URLParam(r, "code"), ProviderSettings: req.settings()})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, providerWriteResponseFrom(out))
}
