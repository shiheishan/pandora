package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	paymentsvc "github.com/aegispanel/aegis/internal/module/payment/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

// defaultPayTitle 是没有订单行（充值单）时收银台的商品名（旧 payments.go 同一兜底）。
const defaultPayTitle = "订阅服务"

// Pay 门户发起支付：锁住自己的待付订单、核对可付，再由 payment 建（或复用）支付意图。
func (s *Service) Pay(ctx context.Context, in PayInput) (paymentsvc.IntentResult, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return paymentsvc.IntentResult{}, err
	}
	for field, v := range map[string]string{"provider": in.Provider, "method": in.Method, "return_url": in.ReturnURL} {
		if err := rejectNUL(field, v); err != nil {
			return paymentsvc.IntentResult{}, err
		}
	}
	var out paymentsvc.IntentResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		o, ok, err := s.repo.OrderForUser(ctx, in.OrderID, userID, repo.ForNoKeyUpdate)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if o.Status != StatusPending {
			return ErrNotPayable
		}
		if !o.PayableAmount.IsPositive() {
			return ErrNoExternalPayment
		}
		if !s.clock().Before(o.ExpiresAt) {
			return ErrPaymentLapsed
		}
		title, err := s.payTitle(ctx, o)
		if err != nil {
			return err
		}
		expires := o.ExpiresAt
		out, err = s.payments.CreateIntent(ctx, paymentsvc.CreateIntentInput{
			OrderID:      o.ID,
			OrderNo:      o.OrderNo,
			UserID:       o.UserID,
			Amount:       o.PayableAmount,
			Currency:     money.Currency(o.Currency),
			ProviderCode: in.Provider,
			Method:       in.Method,
			Title:        title,
			ReturnURL:    in.ReturnURL,
			ClientIP:     reqmeta.From(ctx).IP,
			ExpiresAt:    &expires,
		})
		return err
	})
	if err != nil {
		return paymentsvc.IntentResult{}, err
	}
	return out, nil
}

// payTitle 取收银台商品名：首条订单行的快照名，没有订单行时用兜底名。
func (s *Service) payTitle(ctx context.Context, o model.Order) (string, error) {
	items, err := s.repo.ItemsByOrders(ctx, []uuid.UUID{o.ID})
	if err != nil {
		return "", err
	}
	if len(items) > 0 && items[0].SnapshotName != "" {
		return items[0].SnapshotName, nil
	}
	return defaultPayTitle, nil
}

// Query 门户主动查单（订单须是本人的）。payment.Query 要在事务外、不持订单锁时调。
func (s *Service) Query(ctx context.Context, orderID uuid.UUID) (QueryResult, error) {
	userID, err := portalUser(ctx)
	if err != nil {
		return QueryResult{}, err
	}
	_, ok, err := s.repo.OrderForUser(ctx, orderID, userID, repo.NoLock)
	if err != nil {
		return QueryResult{}, err
	}
	if !ok {
		return QueryResult{}, ErrNotFound
	}
	return s.query(ctx, orderID)
}

// AdminQuery 后台主动查单（payment 自己给后台查单写审计）。
func (s *Service) AdminQuery(ctx context.Context, orderID uuid.UUID) (QueryResult, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return QueryResult{}, err
	}
	_, ok, err := s.repo.OrderByID(ctx, orderID, repo.NoLock)
	if err != nil {
		return QueryResult{}, err
	}
	if !ok {
		return QueryResult{}, ErrNotFound
	}
	return s.query(ctx, orderID)
}

// query 在事务外调 payment.Query，再读一次订单补上最新状态。
func (s *Service) query(ctx context.Context, orderID uuid.UUID) (QueryResult, error) {
	res, err := s.payments.Query(ctx, orderID)
	if err != nil {
		return QueryResult{}, err
	}
	o, ok, err := s.repo.OrderByID(ctx, orderID, repo.NoLock)
	if err != nil {
		return QueryResult{}, err
	}
	if !ok {
		return QueryResult{}, ErrNotFound
	}
	out := QueryResult{
		OrderID:         o.ID,
		OrderNo:         o.OrderNo,
		ProviderCode:    res.ProviderCode,
		ChannelStatus:   string(res.ChannelStatus),
		Reconciled:      res.Reconciled,
		AlreadyRecorded: res.AlreadyRecorded,
		OrderStatus:     o.Status,
	}
	if res.QuarantineKind != "" {
		k := string(res.QuarantineKind)
		out.QuarantineKind = &k
	}
	return out, nil
}

// MarkPaid 后台标记已支付（线下收款）：经 payment.RecordOffline 入账，金额核对与履约由本模块的 Settler 在同一事务里完成。
// 不先锁订单：Settler 会锁，先锁会与回调的锁序相反。订单不可付时 payment 回 409，不开挂账。
func (s *Service) MarkPaid(ctx context.Context, in MarkPaidInput) (MarkPaidResult, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return MarkPaidResult{}, err
	}
	reason, err := checkReason(in.Reason)
	if err != nil {
		return MarkPaidResult{}, err
	}
	reference := strings.TrimSpace(in.Reference)

	var out MarkPaidResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		o, ok, err := s.repo.OrderByID(ctx, in.OrderID, repo.NoLock)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if o.Status != StatusPending {
			return errs.Newf(errs.Conflict, "只有待支付的订单可以标记为已支付，当前状态："+o.Status)
		}
		if !o.PayableAmount.IsPositive() {
			return ErrMarkPaidZero
		}
		rec, err := s.payments.RecordOffline(ctx, paymentsvc.OfflineInput{
			OrderID:   o.ID,
			OrderNo:   o.OrderNo,
			UserID:    o.UserID,
			Amount:    o.PayableAmount,
			Currency:  money.Currency(o.Currency),
			Reference: reference,
			Reason:    reason,
		})
		if err != nil {
			return err
		}
		after, err := s.reload(ctx, o.ID)
		if err != nil {
			return err
		}
		out = MarkPaidResult{
			Processed:      rec.Processed,
			AlreadyHandled: rec.AlreadyHandled,
			PaymentID:      nonNil(rec.PaymentID),
			SubscriptionID: after.SubscriptionID,
			LedgerTxnID:    nonNil(rec.LedgerTxnID),
		}
		data := map[string]any{
			"order_no":  o.OrderNo,
			"amount":    o.PayableAmount.StringFixed(2),
			"currency":  o.Currency,
			"reference": reference,
			"reason":    reason,
		}
		if out.PaymentID != nil {
			data["payment_id"] = out.PaymentID.String()
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionOrderMarkedPaid,
			ResourceType: ResourceOrder,
			ResourceID:   &o.ID,
			Data:         data,
		})
	})
	if err != nil {
		return MarkPaidResult{}, err
	}
	return out, nil
}

// nonNil 把零值 uuid 变成 nil。
func nonNil(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
