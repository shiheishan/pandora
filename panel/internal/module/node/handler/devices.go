package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// registerDevices 注册设备数限制的后台路由：在线设备总览要登录；改判定策略影响全站能否连上，另要近期重认证。
// /settings/device-limit 与 settings 模块的 /settings/site、notify 的 /settings/mail 都是静态段，gin 里并存不冲突。
func (h *Handler) registerDevices(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/devices", k.RequireAuth, h.listDevices)
	v1.POST("/settings/device-limit", k.RequireAuth, k.RecentReauth, h.setDevicePolicy)
}

type onlineDeviceResponse struct {
	SubscriptionID string     `json:"subscription_id"`
	UserID         string     `json:"user_id"`
	Email          string     `json:"email"`
	Plan           string     `json:"plan"`
	Limit          int        `json:"limit"`
	Online         int        `json:"online"`
	Nodes          int        `json:"nodes"`
	Overridden     bool       `json:"overridden"`
	Exceeded       bool       `json:"exceeded"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}

type devicesResponse struct {
	Devices       []onlineDeviceResponse `json:"devices"`
	Mode          string                 `json:"mode"`
	Grace         int                    `json:"grace"`
	WindowMinutes int                    `json:"window_minutes"`
}

type devicePolicyRequest struct {
	Mode          string `json:"mode"`
	Grace         *int   `json:"grace"`
	WindowMinutes *int   `json:"window_minutes"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

func devicesResponseFrom(o service.DeviceOverview) devicesResponse {
	devices := make([]onlineDeviceResponse, 0, len(o.Devices))
	for _, d := range o.Devices {
		var lastSeen *time.Time
		if !d.LastSeenAt.IsZero() {
			t := d.LastSeenAt.UTC()
			lastSeen = &t
		}
		devices = append(devices, onlineDeviceResponse{
			SubscriptionID: d.SubscriptionID.String(),
			UserID:         d.UserID.String(),
			Email:          d.Email,
			Plan:           d.Plan,
			Limit:          d.Limit,
			Online:         d.Online,
			Nodes:          d.Nodes,
			Overridden:     d.Overridden,
			Exceeded:       d.Exceeded,
			LastSeenAt:     lastSeen,
		})
	}
	return devicesResponse{
		Devices:       devices,
		Mode:          o.Mode,
		Grace:         o.Grace,
		WindowMinutes: o.WindowMinutes,
	}
}

func (h *Handler) listDevices(c *gin.Context) {
	ctx := c.Request.Context()
	o, err := h.svc.OnlineDevices(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, devicesResponseFrom(o))
}

func (h *Handler) setDevicePolicy(c *gin.Context) {
	var req devicePolicyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := h.svc.SetDevicePolicy(ctx, service.DevicePolicyInput{
		Mode:          req.Mode,
		Grace:         req.Grace,
		WindowMinutes: req.WindowMinutes,
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, okResponse{OK: true})
}
