package app

import (
	"context"
	"errors"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	ordersvc "github.com/aegispanel/aegis/internal/module/order/service"
	promosvc "github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// promoCatalog 把 catalog 的下单快照适配成 promo 要的 CatalogReader。
type promoCatalog struct{ c *catalogsvc.Service }

var _ promosvc.CatalogReader = promoCatalog{}

// MissingPlans 返回 ids 里不存在的套餐（已下架、已归档的也算存在）。
func (a promoCatalog) MissingPlans(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error) {
	found, err := a.c.PlansByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	missing := []uuid.UUID{}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// PriceSubtotal 按「新购」取在售价格：价格须属于 planID，按当前登录用户判断可见性与用户组；买不到一律回 NotFound。
func (a promoCatalog) PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (string, decimal.Decimal, error) {
	req := catalogsvc.OfferRequest{Use: catalogsvc.UseNew, PlanID: &planID}
	if p, ok := authn.From(ctx); ok {
		uid := p.UserID
		req.UserID = &uid
	}
	o, err := a.c.Offer(ctx, priceID, req)
	if err != nil {
		return "", decimal.Zero, asNotFound(err)
	}
	return string(o.Price.Currency), o.Price.UnitAmount, nil
}

// PackSubtotal 取在售流量包的币种与金额；下架或不存在回 NotFound。
func (a promoCatalog) PackSubtotal(ctx context.Context, packID uuid.UUID) (string, decimal.Decimal, error) {
	p, err := a.c.PackOffer(ctx, packID, false)
	if err != nil {
		return "", decimal.Zero, asNotFound(err)
	}
	return string(p.Currency), p.UnitAmount, nil
}

// asNotFound 把 catalog 的业务拒绝（不在售、未发布、下架等）统一成 NotFound，内部错误原样返回。
func asNotFound(err error) error {
	var e *errs.Error
	if errors.As(err, &e) && e.Code != errs.Internal {
		return errs.New(errs.NotFound)
	}
	return err
}

// opsOrderAdapter 把 order 的订单统计适配成 identity 要的 OpsOrders。
type opsOrderAdapter struct{ s *ordersvc.Service }

var _ identitysvc.OpsOrders = opsOrderAdapter{}

func (a opsOrderAdapter) UserOrderStats(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]identitysvc.OrderStat, error) {
	stats, err := a.s.UserOrderStats(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]identitysvc.OrderStat, len(stats))
	for id, st := range stats {
		out[id] = identitysvc.OrderStat{
			Orders: st.Orders,
			Paid:   st.Paid,
		}
	}
	return out, nil
}

