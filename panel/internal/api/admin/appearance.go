package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 外观（主题 + 插槽）与插件钩子的管理端接口。

//------------------------------------------------------------------------------
// 主题
//------------------------------------------------------------------------------

func (h *handlers) listThemes(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Appearance.ListThemes(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"themes": out})
}

type saveThemeReq struct {
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Tokens    json.RawMessage `json:"tokens"`
	Branding  json.RawMessage `json:"branding"`
	CustomCSS string          `json:"custom_css"`
}

func (h *handlers) saveTheme(w http.ResponseWriter, r *http.Request) {
	var req saveThemeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	notes, err := h.d.Appearance.SaveTheme(r.Context(), httpx.TenantIDFrom(r.Context()),
		appearance.SaveThemeInput{
			Code: req.Code, Name: req.Name, Tokens: req.Tokens,
			Branding: req.Branding, CustomCSS: req.CustomCSS, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 把净化时丢掉的东西如实回给管理员。默默改掉他写的内容，
	// 会让人以为保存失败然后一遍遍重试同一段被过滤的代码。
	httpx.OK(w, map[string]any{"saved": true, "dropped": notes})
}

func (h *handlers) activateTheme(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Appearance.ActivateTheme(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"activated": true})
}

func (h *handlers) deleteTheme(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Appearance.DeleteTheme(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"deleted": true})
}

//------------------------------------------------------------------------------
// 插槽
//------------------------------------------------------------------------------

func (h *handlers) listSlots(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Appearance.ListSlots(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"slots": out})
}

type saveSlotReq struct {
	Content string `json:"content"`
	Enabled bool   `json:"enabled"`
}

func (h *handlers) saveSlot(w http.ResponseWriter, r *http.Request) {
	var req saveSlotReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	notes, err := h.d.Appearance.SaveSlot(r.Context(), httpx.TenantIDFrom(r.Context()),
		appearance.SaveSlotInput{
			Key: chi.URLParam(r, "key"), Content: req.Content,
			Enabled: req.Enabled, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"saved": true, "dropped": notes})
}

//------------------------------------------------------------------------------
// 插件钩子
//------------------------------------------------------------------------------

func (h *handlers) listHooks(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Plugin.List(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"hooks": out, "events": plugin.Events})
}

type saveHookReq struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Events      []string `json:"events"`
	EndpointURL string   `json:"endpoint_url"`
	Secret      string   `json:"secret"` // 空表示不修改
	TimeoutMS   int      `json:"timeout_ms"`
	MaxAttempts int      `json:"max_attempts"`
}

func (h *handlers) saveHook(w http.ResponseWriter, r *http.Request) {
	var req saveHookReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	generated, err := h.d.Plugin.SaveHook(r.Context(), httpx.TenantIDFrom(r.Context()),
		plugin.SaveHookInput{
			Code: req.Code, Name: req.Name, Description: req.Description,
			Enabled: req.Enabled, Events: req.Events, EndpointURL: req.EndpointURL,
			Secret: req.Secret, TimeoutMS: req.TimeoutMS,
			MaxAttempts: req.MaxAttempts, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	resp := map[string]any{"saved": true}
	if generated != "" {
		// 自动生成的签名密钥只在这一次返回：库里存的是加密后的，
		// 之后连管理员也读不回来。
		resp["secret"] = generated
		resp["secret_hint"] = "签名密钥只显示这一次，请立刻填进插件那边的配置"
	}
	httpx.OK(w, resp)
}

func (h *handlers) deleteHook(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Plugin.DeleteHook(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"deleted": true})
}

func (h *handlers) hookDeliveries(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Plugin.Deliveries(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), 50)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"deliveries": out})
}

func (h *handlers) testHook(w http.ResponseWriter, r *http.Request) {
	code, err := h.d.Plugin.TestHook(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"发送失败："+err.Error()))
		return
	}
	if code < 200 || code >= 300 {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"对方返回 HTTP "+itoa64(int64(code))+"，不是 2xx"))
		return
	}
	httpx.OK(w, map[string]any{"sent": true, "response_code": code})
}
