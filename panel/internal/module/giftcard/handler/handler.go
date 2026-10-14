// Package handler 是 giftcard 模块的 HTTP 层：门户的兑换预览、兑换与我的兑换记录；
// 后台的模板、生码、批次与一次性导出、卡密列表与启停、掩码报表、统计与使用明细。
package handler

import (
	"context"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// Service 是 handler 用到的 giftcard service 方法；*service.Service 实现它。
type Service interface {
	ListTemplates(ctx context.Context) ([]service.Template, error)
	SaveTemplate(ctx context.Context, in service.TemplateInput) (service.Template, error)
	GenerateCodes(ctx context.Context, in service.GenerateInput) (service.Generated, error)
	ListBatches(ctx context.Context, f service.BatchFilter) ([]service.Batch, int64, error)
	ExportBatch(ctx context.Context, batchID uuid.UUID) (service.BatchExport, error)
	ListCodes(ctx context.Context, f service.CodeFilter, limit, offset int) ([]service.Code, int64, error)
	ToggleCode(ctx context.Context, codeID uuid.UUID, disabled bool) error
	ExportCodes(ctx context.Context, f service.CodeFilter) ([]service.CodeReportRow, error)
	Stats(ctx context.Context) (service.Stats, error)
	ListUsages(ctx context.Context, templateID *uuid.UUID) ([]service.Usage, error)
	Preview(ctx context.Context, code string) (service.CardPreview, error)
	Redeem(ctx context.Context, in service.RedeemInput) (service.RedeemResult, error)
	MyRedemptions(ctx context.Context) ([]service.MyRedemption, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// 幂等作用域（沿用旧代码的名字）。
const (
	ScopeRedeem        = "gift_card_redeem"
	ScopeCodesGenerate = "giftcard_codes_generate"
	ScopeBatchExport   = "giftcard_batch_export"
)

// SwitchRedeem 是兑换的功能开关（与 settings 的 SwitchGiftCardRedeem 同值；预览与兑换记录不受它管）。
const SwitchRedeem = "marketing.giftcard.redeem"

// 门户按账号每分钟的上限：预览与兑换都会拿码去撞库，限住爆破速度。
const (
	PreviewPerMinute = 20
	RedeemPerMinute  = 10
)

// Handler 是 giftcard 模块的 HTTP 入口。
type Handler struct {
	svc Service
	lim *ratelimit.Limiter
}

// New 构造 Handler；lim 为 nil 时不限流（只用于测试）。
func New(svc Service, lim *ratelimit.Limiter) *Handler { return &Handler{svc: svc, lim: lim} }

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/gift-cards/preview", k.RequireAuth, h.limit("gift_card_preview", PreviewPerMinute), h.preview)
	// 兑换加幂等键：网络抖动重发拿回同一个结果，而不是一句让人困惑的「卡密无效」
	v1.POST("/gift-cards/redeem", k.RequireAuth, h.limit("gift_card_redeem", RedeemPerMinute),
		k.Switch(SwitchRedeem), k.Idempotency(ScopeRedeem), h.redeem)
	v1.GET("/me/gift-cards", k.RequireAuth, h.myRedemptions)
}

// RegisterAdmin 注册后台路由。生码、导出、改模板、启停卡密都要重认证（卡密是等价现金；恢复一张停用的码
// 等于让它重新生效）。前端遇到 reauth_required 会统一弹重认证框。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/gift-cards", k.RequireAuth, h.listTemplates)
	v1.GET("/gift-cards/stats", k.RequireAuth, h.stats)
	v1.GET("/gift-cards/codes", k.RequireAuth, h.listCodes)
	v1.GET("/gift-cards/codes/report", k.RequireAuth, k.RecentReauth, h.exportCodesReport)
	v1.GET("/gift-cards/batches", k.RequireAuth, h.listBatches)
	v1.POST("/gift-cards/batches/:id/export", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeBatchExport), h.exportBatch)
	v1.GET("/gift-cards/usages", k.RequireAuth, h.listUsages)
	v1.POST("/gift-cards", k.RequireAuth, k.RecentReauth, h.saveTemplate)
	v1.POST("/gift-cards/:id/codes", k.RequireAuth, k.RecentReauth, k.Idempotency(ScopeCodesGenerate), h.generateCodes)
	v1.POST("/gift-cards/codes/:id/toggle", k.RequireAuth, k.RecentReauth, h.toggleCode)
}

// limit 是按登录账号的每分钟限流；排在 RequireAuth 之后。
func (h *Handler) limit(name string, n int) gin.HandlerFunc {
	// Strict：Redis 出故障时拒绝（503）而不是放行 —— 卡密是等价现金，限流是在线爆破的唯一上限
	rule := ratelimit.PerMinute(name, n).StrictMode()
	return func(c *gin.Context) {
		if h.lim == nil {
			c.Next()
			return
		}
		ctx := c.Request.Context()
		p, ok := authn.From(ctx)
		if !ok {
			httpx.Fail(c, errs.New(errs.Unauthorized))
			return
		}
		if err := h.lim.Allow(ctx, rule, p.UserID.String()); err != nil {
			httpx.Fail(c, err)
			return
		}
		c.Next()
	}
}

// pathID 解析路径里的 id；格式不对按不存在处理。
func pathID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return uuid.Nil, false
	}
	return id, true
}

// queryID 解析查询串里可空的 id；格式不对回 400。
func queryID(c *gin.Context, name string) (*uuid.UUID, error) {
	raw := c.Query(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errs.Newf(errs.BadRequest, name+" 格式不正确")
	}
	return &id, nil
}
