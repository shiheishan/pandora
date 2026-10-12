// Package handler 是 ledger 模块的 HTTP 层：门户余额查询与后台人工调账。
package handler

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Service 是 handler 用到的 ledger service 方法；*service.Service 实现它。
type Service interface {
	MyWallet(ctx context.Context) (service.Wallet, error)
	AdjustBalance(ctx context.Context, a service.Adjustment) (service.AdjustResult, error)
}

var _ Service = (*service.Service)(nil)
var _ route.Module = (*Handler)(nil)

// Handler 是 ledger 模块的 HTTP 入口。
type Handler struct{ svc Service }

// New 构造 Handler。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// AdjustIdempotencyScope 是后台人工调账的幂等作用域（与旧接口相同）。
const AdjustIdempotencyScope = "admin_user_balance_adjust"

// RegisterPublic 注册门户路由。
func (h *Handler) RegisterPublic(v1 *gin.RouterGroup, k route.Kit) {
	v1.GET("/me/balance", k.RequireAuth, h.myBalance)
}

// RegisterAdmin 注册后台路由。
func (h *Handler) RegisterAdmin(v1 *gin.RouterGroup, k route.Kit) {
	v1.POST("/users/:id/balance", k.RequireAuth, k.RecentReauth, k.Idempotency(AdjustIdempotencyScope), h.adjustBalance)
}

func (h *Handler) myBalance(c *gin.Context) {
	w, err := h.svc.MyWallet(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	bal, err := money.ToCents(w.Balance)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	hist := make([]balanceEntryView, 0, len(w.History))
	for _, item := range w.History {
		delta, err := money.ToCents(item.Delta)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		hist = append(hist, balanceEntryView{
			Kind:  item.Kind,
			Delta: delta,
			Memo:  item.Memo,
			At:    item.At,
		})
	}
	httpx.OK(c, myBalanceResponse{
		Balance:  bal,
		Currency: string(w.Currency),
		History:  hist,
	})
}

func (h *Handler) adjustBalance(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req adjustBalanceRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	res, err := h.svc.AdjustBalance(c.Request.Context(), service.Adjustment{
		UserID:   id,
		Amount:   money.FromCents(req.Amount),
		Currency: req.Currency,
		Reason:   req.Reason,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	bal, err := money.ToCents(res.Balance)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adjustBalanceResponse{Balance: bal})
}
