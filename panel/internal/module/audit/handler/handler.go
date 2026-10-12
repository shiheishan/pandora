package handler

import (
	"context"
	"io"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Service 是 handler 用到的 audit service 方法（便于测试替换）。
type Service interface {
	List(ctx context.Context, f service.Filter, p service.Page) ([]service.Row, int64, error)
	ExportCSV(ctx context.Context, f service.Filter, open func() io.Writer) error
}

// Handler 注册后台审计 HTTP 路由。
type Handler struct{ svc Service }

// New 构造审计 handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// RegisterPublic 门户无审计接口。
func (h *Handler) RegisterPublic(_ *gin.RouterGroup, _ route.Kit) {}

// RegisterAdmin 挂后台 /v1/audit 路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/audit", k.Permission("security.audit.read"), h.list)
	v1.GET("/audit/export",
		k.Permission("security.audit.read"),
		k.Permission("ops.export"),
		k.RecentReauth,
		h.export,
	)
}

func (h *Handler) list(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 50, 200)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req listAuditRequest
	if err := httpx.BindQuery(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, total, err := h.svc.List(c.Request.Context(), req.filter(), service.Page{Limit: limit, Offset: offset})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	events := make([]auditEventResponse, len(rows))
	for i, r := range rows {
		events[i] = toAuditEvent(r)
	}
	if events == nil {
		events = []auditEventResponse{}
	}
	httpx.OK(c, listAuditResponse{Events: events, Total: total})
}

func (h *Handler) export(c *gin.Context) {
	var req exportAuditRequest
	if err := httpx.BindQuery(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	from, to, err := parseExportRange(req.From, req.To)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	f := req.exportFilter(from, to)

	started := false
	open := func() io.Writer {
		started = true
		name := "audit-" + time.Now().UTC().Format("20060102-150405") + ".csv"
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
		c.Header("Cache-Control", "no-store")
		c.Status(200)
		return c.Writer
	}

	if err := h.svc.ExportCSV(c.Request.Context(), f, open); err != nil {
		if !started {
			httpx.Fail(c, err)
			return
		}
		plog.From(c.Request.Context()).Error("audit export aborted", zap.Error(err))
		c.Abort()
		return
	}
}

var _ route.Module = (*Handler)(nil)
