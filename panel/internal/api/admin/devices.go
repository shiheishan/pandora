// [INPUT]: 依赖 domain/nodefabric 的 ListOnlineDevices / SetSubscriptionDeviceLimit / SetDeviceLimitPolicy 与 DeviceWindowMinutes，依赖 platform/httpx 的响应与错误
// [OUTPUT]: 对外提供 handlers 的 listOnlineDevices（带 window_minutes）/ setDeviceLimit / setDeviceMode（可改设备识别窗口）三个处理器
// [POS]: api/admin 的设备数限制接口：在线概览、单订阅覆盖、全局判定模式与设备识别窗口（R103，可选值取 nodefabric.DeviceWindowMinutes）；只校验请求与写响应，读写、窗口经库函数读与审计都在 nodefabric 的 device_limit_admin.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

// 设备数限制的管理接口。
//
// 三件事：看谁超了、调某条订阅的额度、切换判定模式。

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// listOnlineDevices 返回当前在线设备概览，超限的排在前面。
func (h *handlers) listOnlineDevices(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())

	out, err := h.d.Node.ListOnlineDevices(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listOnlineDevicesResponse{Devices: out.Devices, Mode: out.Mode, Grace: out.Grace, WindowMinutes: out.WindowMinutes})
}

type listOnlineDevicesResponse struct {
	Devices       []nodefabric.OnlineDevice `json:"devices"`
	Mode          string                    `json:"mode"`
	Grace         int                       `json:"grace"`
	WindowMinutes int                       `json:"window_minutes"`
}

type deviceLimitReq struct {
	// Limit 为 nil 表示恢复成套餐规定，0 表示不限制
	Limit *int `json:"limit"`
}

// setDeviceLimit 调整某条订阅的设备数额度。
func (h *handlers) setDeviceLimit(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	subID := chi.URLParam(r, "id")

	var req deviceLimitReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Limit != nil && (*req.Limit < 0 || *req.Limit > 1000) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "设备数需在 0 到 1000 之间"))
		return
	}
	if _, err := uuid.Parse(subID); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	err := h.d.Node.SetSubscriptionDeviceLimit(r.Context(), tenantID,
		httpx.PrincipalFrom(r.Context()).UserID, subID, req.Limit)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("管理员调整设备数限制", "subscription", subID, "limit", req.Limit)
	httpx.OK(w, setDeviceLimitResponse{OK: true})
}

type setDeviceLimitResponse struct {
	OK bool `json:"ok"`
}

type deviceModeReq struct {
	Mode  string `json:"mode"`
	Grace *int   `json:"grace"`
	// WindowMinutes 是设备识别窗口（R103），省略 = 不改，只收 5 / 10 / 30 / 60
	WindowMinutes *int `json:"window_minutes"`
}

// setDeviceMode 切换判定模式。
func (h *handlers) setDeviceMode(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req deviceModeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Mode != "loose" && req.Mode != "strict" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "模式只能是 loose 或 strict"))
		return
	}
	if req.Grace != nil && (*req.Grace < 0 || *req.Grace > 5) {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed, "宽容值需在 0 到 5 之间"))
		return
	}
	if req.WindowMinutes != nil && !slices.Contains(nodefabric.DeviceWindowMinutes[:], *req.WindowMinutes) {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"window_minutes": "设备识别窗口只能是 5、10、30 或 60 分钟"}))
		return
	}

	err := h.d.Node.SetDeviceLimitPolicy(r.Context(), tenantID, nodefabric.DeviceLimitPolicyInput{
		ActorID: httpx.PrincipalFrom(r.Context()).UserID,
		Mode:    req.Mode, Grace: req.Grace, WindowMinutes: req.WindowMinutes,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.d.Log.Info("管理员切换设备限制模式", "mode", req.Mode, "window_minutes", req.WindowMinutes)
	httpx.OK(w, setDeviceModeResponse{OK: true})
}

type setDeviceModeResponse struct {
	OK bool `json:"ok"`
}
