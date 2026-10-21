package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// LinkRotatedNotice 是一次后台换链接的通知内容（app 适配器从 subscription 的端口转过来）。
type LinkRotatedNotice struct {
	UserID         uuid.UUID
	SubscriptionID uuid.UUID
	PlanID         uuid.UUID
	Label          string    // 这一份的备注名，空 = 没起名
	RotatedAt      time.Time // 换链接的时刻，进去重键
}

// PendingOrderNotice 是一张后台开的待付单的通知内容（app 适配器从 order 的端口转过来）。
type PendingOrderNotice struct {
	UserID    uuid.UUID
	OrderID   uuid.UUID
	PlanID    uuid.UUID
	OrderNo   string
	Amount    decimal.Decimal // 应付金额
	ExpiresAt time.Time       // 付款期限，过了自动取消
}

// NotifyLinkRotated 在调用方事务里给属主发后台换链接通知；写失败时调用方整笔回滚。
func (s *Service) NotifyLinkRotated(ctx context.Context, n LinkRotatedNotice) error {
	if s.plans == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: Plans 未装配"))
	}
	plans, err := s.plans.PlansByIDs(ctx, []uuid.UUID{n.PlanID})
	if err != nil {
		return err
	}
	name := planName(plans, n.PlanID)
	if label := strings.TrimSpace(n.Label); label != "" {
		name = label + " · " + name
	}
	vars := map[string]string{
		"plan":     name,
		"subs_url": s.portalURL(ctx, "subs"),
	}
	key := "link-rotated:" + n.SubscriptionID.String() + ":" + strconv.FormatInt(n.RotatedAt.UnixMicro(), 10)
	return s.NotifyUser(ctx, n.UserID, TemplateLinkRotated, vars, key)
}

// NotifyOrderPending 在调用方事务里给用户发后台待付单通知；写失败时调用方整笔回滚。
func (s *Service) NotifyOrderPending(ctx context.Context, n PendingOrderNotice) error {
	if s.plans == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: Plans 未装配"))
	}
	plans, err := s.plans.PlansByIDs(ctx, []uuid.UUID{n.PlanID})
	if err != nil {
		return err
	}
	loc, err := scanSiteLocation(ctx, s)
	if err != nil {
		return err
	}
	vars := map[string]string{
		"order_no":   n.OrderNo,
		"plan":       planName(plans, n.PlanID),
		"amount":     n.Amount.StringFixed(2) + " 元",
		"expires_at": noticeTime(n.ExpiresAt, loc),
		"order_url":  s.portalURL(ctx, "orders"), // 门户订单页按订单 id 定位，待付卡就在页顶，链到页面即可
	}
	key := "order-pending:" + n.OrderID.String()
	return s.NotifyUser(ctx, n.UserID, TemplateOrderPending, vars, key)
}

// portalURL 按 settings 的 SiteURL（主域名，后台可改）拼用户端 hash 路由链接；base 为空返回空串。
func (s *Service) portalURL(ctx context.Context, path string) string {
	base := strings.TrimRight(s.settings.SiteURL(ctx), "/")
	if base == "" {
		return ""
	}
	return base + "/#/" + path
}
