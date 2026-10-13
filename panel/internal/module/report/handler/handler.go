// Package handler 是 report 模块的 HTTP 层：后台看板与营收报表调整。全部是后台路由，门户不挂。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/report/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 report service 方法；*service.Service 实现它。
type Service interface {
	Overview(ctx context.Context) (service.Overview, error)
	RevenueTimeseries(ctx context.Context, currency string, days int) (service.RevenueSeries, error)
	ListAdjustments(ctx context.Context, currency string) ([]service.Adjustment, error)
	CreateAdjustment(ctx context.Context, in service.CreateAdjustmentInput) (service.Adjustment, error)
	ReverseAdjustment(ctx context.Context, id uuid.UUID, reason string) (service.Adjustment, error)
	NodeTraffic(ctx context.Context, q service.TrafficQuery) (service.NodeTraffic, error)
	UserTraffic(ctx context.Context, q service.TrafficQuery) (service.UserTraffic, error)
	NotificationBacklog(ctx context.Context) (service.NotificationBacklog, error)
	Tasks(ctx context.Context) (service.Tasks, error)
	SystemStatus(ctx context.Context) (service.SystemStatus, error)
	ActivityTimeseries(ctx context.Context, days int) ([]service.ActivityPoint, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域。
const (
	CreateAdjustmentScope  = "revenue_adjustment_create"
	ReverseAdjustmentScope = "revenue_adjustment_reverse"
)

// Handler 是 report 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic 门户不挂看板与报表接口。
func (h *Handler) RegisterPublic(_ *gin.RouterGroup, _ route.Kit) {}

// RegisterAdmin 注册后台看板与营收报表路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/overview", k.RequireAuth, h.overview)
	v1.GET("/revenue/timeseries", k.RequireAuth, h.revenueTimeseries)
	v1.GET("/revenue/adjustments", k.RequireAuth, h.listAdjustments)
	v1.POST("/revenue/adjustments", k.RequireAuth, k.RecentReauth, k.Idempotency(CreateAdjustmentScope), h.createAdjustment)
	v1.POST("/revenue/adjustments/:id/reverse", k.RequireAuth, k.RecentReauth, k.Idempotency(ReverseAdjustmentScope), h.reverseAdjustment)
	v1.GET("/dashboard/traffic/nodes", k.RequireAuth, h.nodeTraffic)
	v1.GET("/dashboard/traffic/users", k.RequireAuth, h.userTraffic)
	v1.GET("/dashboard/backlog/notifications", k.RequireAuth, h.notificationBacklog)
	v1.GET("/dashboard/tasks", k.RequireAuth, h.tasks)
	v1.GET("/system/status", k.RequireAuth, h.systemStatus)
	v1.GET("/stats/timeseries", k.RequireAuth, h.activityTimeseries)
}

func (h *Handler) overview(c *gin.Context) {
	o, err := h.svc.Overview(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := overviewFrom(o)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) revenueTimeseries(c *gin.Context) {
	var q revenueTimeseriesQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	s, err := h.svc.RevenueTimeseries(c.Request.Context(), q.Currency, q.Days)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := revenueSeriesFrom(s)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) listAdjustments(c *gin.Context) {
	var q currencyQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.ListAdjustments(c.Request.Context(), q.Currency)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := adjustmentsListFrom(rows)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) createAdjustment(c *gin.Context) {
	var req createAdjustmentRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	a, err := h.svc.CreateAdjustment(c.Request.Context(), service.CreateAdjustmentInput{
		Currency:    req.Currency,
		Amount:      money.FromCents(req.Amount),
		Reason:      req.Reason,
		EffectiveOn: req.EffectiveOn,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := adjustmentFrom(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) reverseAdjustment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req reverseAdjustmentRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	a, err := h.svc.ReverseAdjustment(c.Request.Context(), id, req.Reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := adjustmentFrom(a)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) nodeTraffic(c *gin.Context) {
	q, err := bindTrafficQuery(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.NodeTraffic(c.Request.Context(), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeTrafficFrom(t))
}

func (h *Handler) userTraffic(c *gin.Context) {
	if _, ok := c.GetQuery("identity"); ok {
		httpx.Fail(c, errs.Invalid(map[string]string{"identity": "用户排行只给脱敏邮箱，不支持按身份查询"}))
		return
	}
	q, err := bindTrafficQuery(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.UserTraffic(c.Request.Context(), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, userTrafficFrom(t))
}

func (h *Handler) notificationBacklog(c *gin.Context) {
	b, err := h.svc.NotificationBacklog(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, backlogFrom(b))
}

func (h *Handler) tasks(c *gin.Context) {
	t, err := h.svc.Tasks(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp, err := tasksFrom(t)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

func (h *Handler) systemStatus(c *gin.Context) {
	s, err := h.svc.SystemStatus(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, systemStatusFrom(s))
}

func (h *Handler) activityTimeseries(c *gin.Context) {
	var q daysQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	pts, err := h.svc.ActivityTimeseries(c.Request.Context(), q.Days)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, activityTimeseriesFrom(pts))
}

func bindTrafficQuery(c *gin.Context) (service.TrafficQuery, error) {
	var q trafficQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		return service.TrafficQuery{}, err
	}
	return service.TrafficQuery{
		Range:      q.Range,
		Limit:      q.Limit,
		SnapshotAt: q.SnapshotAt,
	}, nil
}
