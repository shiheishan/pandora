// Package handler 是 realtime 模块的 HTTP 层：门户与管理端的 SSE 事件流。
//
// 订阅范围就是鉴权边界：门户只订公共频道、本人频道与会话频道；
// 后台引擎已限定后台令牌，管理端订管理端频道与会话频道。
package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
)

var _ route.Module = (*Handler)(nil)

// Handler 提供 GET /v1/events SSE 端点。
type Handler struct {
	hub     *realtime.Hub
	maxLife time.Duration
}

// New 构造 Handler；maxLife 是单条连接的最长存活时间，传访问令牌有效期；
// 实际按令牌剩余时间再截短，到点断开让客户端带新令牌重连（见 realtime.Stream）。
func New(hub *realtime.Hub, maxLife time.Duration) *Handler {
	return &Handler{hub: hub, maxLife: maxLife}
}

// RegisterPublic 注册门户事件流。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/events", k.RequireAuth, h.publicEvents)
}

// RegisterAdmin 注册管理端事件流。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/events", k.RequireAuth, h.adminEvents)
}

func (h *Handler) publicEvents(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	if h.hub == nil {
		httpx.Fail(c, errs.New(errs.Unavailable))
		return
	}
	realtime.Stream(c, h.hub, p.UserID, []string{
		realtime.ChannelPublic(),
		realtime.ChannelUser(p.UserID),
		realtime.ChannelSession(p.SessionID),
	}, h.lifeFor(p))
}

func (h *Handler) adminEvents(c *gin.Context) {
	p, ok := authn.FromGin(c)
	if !ok {
		httpx.Fail(c, errs.New(errs.Unauthorized))
		return
	}
	if h.hub == nil {
		httpx.Fail(c, errs.New(errs.Unavailable))
		return
	}
	realtime.Stream(c, h.hub, p.UserID, []string{
		realtime.ChannelAdmin(),
		realtime.ChannelSession(p.SessionID),
	}, h.lifeFor(p))
}

// lifeFor 是这条连接最多活多久：不超过 maxLife，也不超过访问令牌剩下的时间。
func (h *Handler) lifeFor(p authn.Principal) time.Duration {
	life := h.maxLife
	if !p.ExpiresAt.IsZero() {
		if left := time.Until(p.ExpiresAt); life <= 0 || left < life {
			life = max(left, time.Second)
		}
	}
	return life
}
