package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// OrderHistory 一张订单的支付尝试与收款（给 order 拼 /orders/{id}/payments 与门户订单详情）；订单是否存在由 order 判断。
func (s *Service) OrderHistory(ctx context.Context, orderID uuid.UUID) (OrderHistory, error) {
	intents, err := s.repo.IntentsByOrder(ctx, orderID)
	if err != nil {
		return OrderHistory{}, err
	}
	payments, err := s.repo.PaymentsByOrder(ctx, orderID)
	if err != nil {
		return OrderHistory{}, err
	}
	out := OrderHistory{
		Intents:  make([]IntentView, 0, len(intents)),
		Payments: make([]PaymentView, 0, len(payments)),
	}
	for _, it := range intents {
		out.Intents = append(out.Intents, IntentView{
			ID:           it.ID,
			ProviderCode: it.ProviderCode,
			ProviderName: it.ProviderName,
			OutTradeNo:   it.OutTradeNo,
			ProviderRef:  it.ProviderRef,
			Method:       it.Method,
			Currency:     money.Currency(it.Currency),
			Amount:       it.Amount,
			Surcharge:    it.SurchargeAmount,
			Status:       IntentStatus(it.Status),
			ExpiresAt:    it.ExpiresAt,
			CreatedAt:    it.CreatedAt,
			UpdatedAt:    it.UpdatedAt,
		})
	}
	for _, p := range payments {
		out.Payments = append(out.Payments, PaymentView{
			ID:                p.ID,
			IntentID:          p.PaymentIntentID,
			ProviderCode:      p.ProviderCode,
			ProviderName:      p.ProviderName,
			ProviderPaymentID: p.ProviderPaymentID,
			Currency:          money.Currency(p.Currency),
			Amount:            p.Amount,
			Surcharge:         p.SurchargeAmount,
			Fee:               p.FeeAmount,
			Status:            p.Status,
			Method:            p.Method,
			PaidAt:            p.PaidAt,
		})
	}
	return out, nil
}

// OrderSummaries 一批订单的支付摘要（一条 SQL）；结果里每个请求的 ID 都有值，没有支付记录的是零值。
func (s *Service) OrderSummaries(ctx context.Context, orderIDs []uuid.UUID) (map[uuid.UUID]OrderPaymentSummary, error) {
	out := make(map[uuid.UUID]OrderPaymentSummary, len(orderIDs))
	for _, id := range orderIDs {
		out[id] = OrderPaymentSummary{}
	}
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := s.repo.OrderSummaries(ctx, orderIDs)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.OrderID] = OrderPaymentSummary{
			HasIntent:    r.HasIntent,
			ProviderCode: r.ProviderCode,
			ProviderName: r.ProviderName,
		}
	}
	return out, nil
}
