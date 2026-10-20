// 购买限制设置：仅约束门户用户自助下单（未支付订单数、订阅数上限）。
// 后台人工开单与礼品卡兑换不受限；每次现查库，改了下一单即生效。
package service

import (
	"context"
	"encoding/json"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

const (
	keyPurchaseMaxUnpaidOrders  = "purchase.max_unpaid_orders" // int，缺省 0（不限），0–50
	keyPurchaseMaxSubscriptions = "purchase.max_subscriptions" // int，缺省 0（不限），0–50

	auditPurchaseChanged = "settings.purchase_updated"
	// PurchaseLimitMax 是两项上限的可选上界（0 表示不限）。
	PurchaseLimitMax = 50
)

var purchaseKeys = []string{keyPurchaseMaxUnpaidOrders, keyPurchaseMaxSubscriptions}

// PurchaseLimits 是门户购买限制。
type PurchaseLimits struct {
	MaxUnpaidOrders  int // 每人同时未支付订单上限，0 = 不限
	MaxSubscriptions int // 每人订阅数上限，0 = 不限
}

// PurchaseLimits 读取购买限制。
func (s *Service) PurchaseLimits(ctx context.Context) (PurchaseLimits, error) {
	vals, err := s.store.Values(ctx, purchaseKeys)
	if err != nil {
		return PurchaseLimits{}, err
	}
	return decodePurchaseLimits(ctx, vals), nil
}

// ValidatePurchaseLimits 校验两项均在 0–PurchaseLimitMax。
func ValidatePurchaseLimits(v PurchaseLimits) error {
	fields := map[string]string{}
	if v.MaxUnpaidOrders < 0 || v.MaxUnpaidOrders > PurchaseLimitMax {
		fields["max_unpaid_orders"] = "未支付订单上限要在 0 到 50 之间（0 表示不限）"
	}
	if v.MaxSubscriptions < 0 || v.MaxSubscriptions > PurchaseLimitMax {
		fields["max_subscriptions"] = "订阅数上限要在 0 到 50 之间（0 表示不限）"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// SetPurchaseLimits 在锁住本组各行的事务里整份写入购买限制；有改动才写 settings.purchase_updated 审计。
func (s *Service) SetPurchaseLimits(ctx context.Context, v PurchaseLimits) error {
	if err := ValidatePurchaseLimits(v); err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, purchaseKeys)
		if err != nil {
			return err
		}
		old := decodePurchaseLimits(ctx, vals)
		changes, b, a := diff(purchaseMap(old), purchaseMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditPurchaseChanged, auditResourceSettings, map[string]any{
			"group":  "purchase",
			"before": b,
			"after":  a,
		})
	})
}

func decodePurchaseLimits(ctx context.Context, vals map[string]json.RawMessage) PurchaseLimits {
	maxUnpaid := decodePurchaseItem(ctx, vals, keyPurchaseMaxUnpaidOrders, 0, func(n int) bool {
		return n >= 0 && n <= PurchaseLimitMax
	})
	maxSubs := decodePurchaseItem(ctx, vals, keyPurchaseMaxSubscriptions, 0, func(n int) bool {
		return n >= 0 && n <= PurchaseLimitMax
	})
	return PurchaseLimits{MaxUnpaidOrders: maxUnpaid, MaxSubscriptions: maxSubs}
}

func decodePurchaseItem[T any](ctx context.Context, vals map[string]json.RawMessage, key string, def T, ok func(T) bool) T {
	raw, exists := vals[key]
	if !exists || len(raw) == 0 {
		return def
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		plog.From(ctx).Warn("购买限制设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	if !ok(v) {
		plog.From(ctx).Warn("购买限制设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	return v
}

func purchaseMap(v PurchaseLimits) map[string]any {
	return map[string]any{
		keyPurchaseMaxUnpaidOrders:  v.MaxUnpaidOrders,
		keyPurchaseMaxSubscriptions: v.MaxSubscriptions,
	}
}
