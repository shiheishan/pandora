package handler

import (
	"encoding/json"

	"github.com/aegispanel/aegis/internal/module/appearance/service"
)

// themeView 是后台与门户共用的主题 JSON 形状。
type themeView struct {
	ID        string          `json:"id"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	IsBuiltin bool            `json:"is_builtin"`
	IsActive  bool            `json:"is_active"`
	Tokens    json.RawMessage `json:"tokens"`
	Branding  json.RawMessage `json:"branding"`
	CustomCSS string          `json:"custom_css"`
}

// slotView 是后台插槽列表的一项。
type slotView struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Where     string `json:"where"`
	Content   string `json:"content"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// publicAppearanceView 是门户一次取齐的外观数据。
type publicAppearanceView struct {
	// 站点名：系统设置 site.name（主题里不再有站点名）
	SiteName string            `json:"site_name"`
	Theme    *themeView        `json:"theme"`
	Slots    map[string]string `json:"slots"`
}

// listThemesResponse 是后台主题列表。
type listThemesResponse struct {
	Themes []themeView `json:"themes"`
}

// listSlotsResponse 是后台插槽列表。
type listSlotsResponse struct {
	Slots []slotView `json:"slots"`
}

// saveThemeRequest 是新建或保存主题的请求体。
type saveThemeRequest struct {
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Create    bool            `json:"create"`
	Tokens    json.RawMessage `json:"tokens"`
	Branding  json.RawMessage `json:"branding"`
	CustomCSS string          `json:"custom_css"`
}

// saveThemeResponse 是保存主题成功响应。
type saveThemeResponse struct {
	Dropped []string `json:"dropped"`
	Saved   bool     `json:"saved"`
}

// activateThemeResponse 是激活主题成功响应。
type activateThemeResponse struct {
	Activated bool `json:"activated"`
}

// deleteThemeResponse 是删除主题成功响应。
type deleteThemeResponse struct {
	Deleted bool `json:"deleted"`
}

// saveSlotRequest 是保存插槽的请求体。
type saveSlotRequest struct {
	Content string `json:"content"`
	Enabled bool   `json:"enabled"`
}

// saveSlotResponse 是保存插槽成功响应。
type saveSlotResponse struct {
	Dropped []string `json:"dropped"`
	Saved   bool     `json:"saved"`
}

func themeFrom(t service.Theme) themeView {
	return themeView{
		ID:        t.ID.String(),
		Code:      t.Code,
		Name:      t.Name,
		IsBuiltin: t.IsBuiltin,
		IsActive:  t.IsActive,
		Tokens:    jsonObjectOrEmpty(t.Tokens),
		Branding:  jsonObjectOrEmpty(t.Branding),
		CustomCSS: "",
	}
}

func slotFrom(s service.Slot) slotView {
	return slotView{
		Key:       s.Key,
		Label:     s.Label,
		Where:     s.Where,
		Content:   s.Content,
		Enabled:   s.Enabled,
		UpdatedAt: s.UpdatedAt,
	}
}

func publicFrom(p service.PublicAppearance) publicAppearanceView {
	siteName := p.SiteName
	if siteName == "" {
		siteName = service.DefaultSiteName
	}
	slots := p.Slots
	if slots == nil {
		slots = map[string]string{}
	}
	var theme *themeView
	if p.Theme != nil {
		tv := themeFrom(*p.Theme)
		theme = &tv
	}
	return publicAppearanceView{
		SiteName: siteName,
		Theme:    theme,
		Slots:    slots,
	}
}

func jsonObjectOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}
