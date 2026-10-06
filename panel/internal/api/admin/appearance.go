// [INPUT]: 依赖 domain/appearance 的主题与插槽服务、domain/plugin 的钩子服务，依赖 platform/httpx
// [OUTPUT]: 对外提供主题（列表 / 保存 / 激活 / 删除）、插槽（列表 / 保存）与 Webhook 钩子（列表 / 保存 / 删除 / 投递记录 / 测试投递）处理器；成功响应为具名 DTO（*Response）
// [POS]: api/admin 的外观与插件处理器（后台-08 主题与插槽、后台-09 Webhook 钩子）；测试投递回 duration_ms

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

type listThemesResponse struct {
	Themes []appearance.Theme `json:"themes"`
}

func (h *handlers) listThemes(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Appearance.ListThemes(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listThemesResponse{Themes: out})
}

type saveThemeReq struct {
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Create    bool            `json:"create"`
	Tokens    json.RawMessage `json:"tokens"`
	Branding  json.RawMessage `json:"branding"`
	CustomCSS string          `json:"custom_css"`
}

type saveThemeResponse struct {
	Dropped []string `json:"dropped"`
	Saved   bool     `json:"saved"`
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
			Code: req.Code, Name: req.Name, Create: req.Create, Tokens: req.Tokens,
			Branding: req.Branding, CustomCSS: req.CustomCSS, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 把净化时丢掉的东西如实回给管理员。默默改掉他写的内容，
	// 会让人以为保存失败然后一遍遍重试同一段被过滤的代码。
	httpx.OK(w, saveThemeResponse{Dropped: notes, Saved: true})
}

type activateThemeResponse struct {
	Activated bool `json:"activated"`
}

func (h *handlers) activateTheme(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Appearance.ActivateTheme(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, activateThemeResponse{Activated: true})
}

type deleteThemeResponse struct {
	Deleted bool `json:"deleted"`
}

func (h *handlers) deleteTheme(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Appearance.DeleteTheme(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, deleteThemeResponse{Deleted: true})
}

//------------------------------------------------------------------------------
// 插槽
//------------------------------------------------------------------------------

type listSlotsResponse struct {
	Slots []appearance.Slot `json:"slots"`
}

func (h *handlers) listSlots(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Appearance.ListSlots(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listSlotsResponse{Slots: out})
}

type saveSlotReq struct {
	Content string `json:"content"`
	Enabled bool   `json:"enabled"`
}

type saveSlotResponse struct {
	Dropped []string `json:"dropped"`
	Saved   bool     `json:"saved"`
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
	httpx.OK(w, saveSlotResponse{Dropped: notes, Saved: true})
}

//------------------------------------------------------------------------------
// 插件钩子
//------------------------------------------------------------------------------

type listHooksResponse struct {
	Events []plugin.EventInfo `json:"events"`
	Hooks  []plugin.Hook      `json:"hooks"`
}

func (h *handlers) listHooks(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Plugin.List(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listHooksResponse{Events: plugin.Events, Hooks: out})
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

// secret 与 secret_hint 只在自动生成了密钥时出现：两者在该分支必非空，omitempty 即可复现缺席。
type saveHookResponse struct {
	Saved      bool   `json:"saved"`
	Secret     string `json:"secret,omitempty"`
	SecretHint string `json:"secret_hint,omitempty"`
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
	resp := saveHookResponse{Saved: true}
	if generated != "" {
		// 自动生成的签名密钥只在这一次返回：库里存的是加密后的，
		// 之后连管理员也读不回来。
		resp.Secret = generated
		resp.SecretHint = "签名密钥只显示这一次，请立刻填进插件那边的配置"
	}
	httpx.OK(w, resp)
}

type deleteHookResponse struct {
	Deleted bool `json:"deleted"`
}

func (h *handlers) deleteHook(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Plugin.DeleteHook(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, deleteHookResponse{Deleted: true})
}

// 投递记录的每一行是领域层按列拼出的 map，原样透传。
type hookDeliveriesResponse struct {
	Deliveries []map[string]any `json:"deliveries"`
}

func (h *handlers) hookDeliveries(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Plugin.Deliveries(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "code"), 50)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, hookDeliveriesResponse{Deliveries: out})
}

type testHookResponse struct {
	DurationMS   int  `json:"duration_ms"`
	ResponseCode int  `json:"response_code"`
	Sent         bool `json:"sent"`
}

func (h *handlers) testHook(w http.ResponseWriter, r *http.Request) {
	code, durationMS, err := h.d.Plugin.TestHook(r.Context(), httpx.TenantIDFrom(r.Context()),
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
	httpx.OK(w, testHookResponse{DurationMS: durationMS, ResponseCode: code, Sent: true})
}
