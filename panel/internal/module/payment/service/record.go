package service

import (
	"context"
	"errors"
	"regexp"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

var orderStatusPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)

// offlineTarget 是线下收款的订单信息（没有支付意图）。
type offlineTarget struct {
	OrderID uuid.UUID
	UserID  uuid.UUID
	OrderNo string
}

// recordInput 是一笔待入账的渠道事件。
type recordInput struct {
	Provider          providerRecord
	EventID           string
	EventType         string // payment.succeeded / payment.failed / payment.refunded / payment.unknown
	PaymentRef        string // 渠道支付流水号
	OutTradeNo        string // 按它找意图；线下收款为空
	Offline           *offlineTarget
	Amount, Fee       decimal.Decimal
	Currency          money.Currency
	Method            string
	Raw               map[string]string
	SignatureVerified bool
}

// recordHook 在入账事务里、结算之后执行（后台查单写审计用）。
type recordHook func(ctx context.Context, out RecordResult) error

// recordPayment 在 ctx 的事务里（没有就自己开）入账一笔渠道事件。
func (s *Service) recordPayment(ctx context.Context, in recordInput, hook recordHook) (RecordResult, error) {
	var out RecordResult
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var recErr error
		out, recErr = s.recordPaymentTx(ctx, in, hook)
		return recErr
	})
	return out, err
}

func (s *Service) recordPaymentTx(ctx context.Context, in recordInput, hook recordHook) (RecordResult, error) {
	log := plog.From(ctx)

	if !in.SignatureVerified {
		fakeID := "unverified:" + uuid.Must(uuid.NewV7()).String()
		ev := &model.Event{
			ID:                uuid.Must(uuid.NewV7()),
			ProviderID:        in.Provider.ID,
			ProviderEventID:   fakeID,
			EventType:         in.EventType,
			SignatureVerified: false,
			ProcessingStatus:  eventPending,
			RawPayload:        cloneRaw(in.Raw),
		}
		// 未验签的报文不可信：流水号超长就不存（库里限 200 字），整份报文仍留在 raw_payload
		if pr := strings.TrimSpace(in.PaymentRef); pr != "" && len(pr) <= 200 {
			ev.ProviderPaymentID = &pr
		}
		inserted, err := s.repo.InsertEvent(ctx, ev)
		if err != nil {
			return RecordResult{}, err
		}
		if inserted {
			msg := "签名校验失败"
			if _, err := s.repo.FinishEvent(ctx, ev.ID, eventIgnored, &msg); err != nil {
				return RecordResult{}, err
			}
		}
		log.Warn("支付回调验签失败",
			zap.String("provider", in.Provider.Code),
			zap.String("out_trade_no", strings.TrimSpace(in.OutTradeNo)),
		)
		return RecordResult{SignatureFailed: true}, nil
	}

	if err := validateRecordInput(in); err != nil {
		return RecordResult{}, err
	}

	evID := uuid.Must(uuid.NewV7())
	pr := strings.TrimSpace(in.PaymentRef)
	var prPtr *string
	if pr != "" {
		prPtr = &pr
	}
	ev := &model.Event{
		ID:                evID,
		ProviderID:        in.Provider.ID,
		ProviderEventID:   strings.TrimSpace(in.EventID),
		EventType:         in.EventType,
		ProviderPaymentID: prPtr,
		RawPayload:        cloneRaw(in.Raw),
		SignatureVerified: true,
		ProcessingStatus:  eventPending,
	}
	inserted, err := s.repo.InsertEvent(ctx, ev)
	if err != nil {
		return RecordResult{}, err
	}
	if !inserted {
		res := RecordResult{AlreadyHandled: true}
		if pr != "" {
			pay, ok, err := s.repo.PaymentByProviderRef(ctx, in.Provider.ID, pr)
			if err != nil {
				return RecordResult{}, err
			}
			if ok {
				res.PaymentID = pay.ID
			}
		}
		return res, nil
	}

	if in.EventType != "payment.succeeded" {
		if _, err := s.repo.FinishEvent(ctx, evID, eventIgnored, nil); err != nil {
			return RecordResult{}, err
		}
		return RecordResult{Processed: true}, nil
	}

	if err := validateRecordAmounts(in); err != nil {
		return RecordResult{}, err
	}

	var intent model.Intent
	var haveIntent bool
	var orderID, userID uuid.UUID
	var orderNo string

	if in.Offline != nil {
		orderID = in.Offline.OrderID
		userID = in.Offline.UserID
		orderNo = in.Offline.OrderNo
	} else {
		it, ok, err := s.repo.IntentByOutTradeNo(ctx, in.Provider.ID, strings.TrimSpace(in.OutTradeNo))
		if err != nil {
			return RecordResult{}, err
		}
		if !ok {
			return RecordResult{}, errs.Newf(errs.NotFound, "找不到这笔支付")
		}
		intent = it
		haveIntent = true
		orderID = intent.OrderID
		userID = intent.UserID
		orderNo = intent.OrderNo
		if !in.Amount.Equal(intent.Amount) || string(in.Currency) != intent.Currency {
			return RecordResult{}, ErrAmountMismatch
		}
	}

	if pr == "" {
		return RecordResult{}, errs.Newf(errs.BadRequest, "缺少支付事件号或支付流水号")
	}

	if err := s.repo.LockPaymentKey(ctx, in.Provider.ID, pr); err != nil {
		return RecordResult{}, err
	}

	existing, ok, err := s.repo.PaymentByProviderRef(ctx, in.Provider.ID, pr)
	if err != nil {
		return RecordResult{}, err
	}
	if ok {
		if existing.OrderID != orderID {
			return RecordResult{}, ErrPaymentReused
		}
		if !existing.Amount.Equal(in.Amount) || existing.Currency != string(in.Currency) ||
			!existing.FeeAmount.Equal(in.Fee) {
			return RecordResult{}, errs.Newf(errs.Conflict, "重放的渠道收款与已记录的金额不一致")
		}
		msg := "这笔收款此前已入账"
		if _, err := s.repo.FinishEvent(ctx, evID, eventIgnored, &msg); err != nil {
			return RecordResult{}, err
		}
		return RecordResult{AlreadyHandled: true, PaymentID: existing.ID}, nil
	}

	method := normalizeMethod(in.Method)
	if method == "" && haveIntent && intent.Method != "" {
		method = intent.Method
	}
	var methodPtr *string
	if method != "" {
		methodPtr = &method
	}

	paymentID := uuid.Must(uuid.NewV7())
	var intentID *uuid.UUID
	if haveIntent {
		intentID = &intent.ID
	}
	pay := &model.Payment{
		ID:                paymentID,
		OrderID:           orderID,
		UserID:            userID,
		PaymentIntentID:   intentID,
		ProviderID:        in.Provider.ID,
		ProviderPaymentID: pr,
		Currency:          string(in.Currency),
		Amount:            in.Amount,
		FeeAmount:         in.Fee,
		Status:            "succeeded",
		Method:            methodPtr,
	}
	if err := s.repo.InsertPayment(ctx, pay); err != nil {
		return RecordResult{}, err
	}

	if s.settler == nil {
		return RecordResult{}, errors.New("payment: 没有注册 Settler")
	}

	settleErr := s.settler.SettlePayment(ctx, SettleInput{
		OrderID:      orderID,
		PaymentID:    paymentID,
		Amount:       in.Amount,
		Fee:          in.Fee,
		Currency:     in.Currency,
		ProviderCode: in.Provider.Code,
		Method:       method,
	})

	var result RecordResult
	if settleErr == nil {
		result, err = s.afterSettleSuccess(ctx, evID, in, haveIntent, intent, orderID, paymentID, method)
		if err != nil {
			return RecordResult{}, err
		}
	} else {
		var npe *NotPayableError
		if errors.As(settleErr, &npe) {
			result, err = s.afterSettleQuarantine(ctx, evID, in, npe, haveIntent, intent, orderID, userID, orderNo, paymentID)
			if err != nil {
				return RecordResult{}, err
			}
		} else {
			return RecordResult{}, settleErr
		}
	}

	if hook != nil {
		if err := hook(ctx, result); err != nil {
			return RecordResult{}, err
		}
	}
	return result, nil
}

func validateRecordInput(in recordInput) error {
	eid := strings.TrimSpace(in.EventID)
	if eid == "" || len(eid) > 200 {
		return errs.Newf(errs.BadRequest, "缺少支付事件号或支付流水号")
	}
	switch in.EventType {
	case "payment.succeeded", "payment.failed", "payment.refunded", "payment.unknown":
	default:
		return errs.Newf(errs.BadRequest, "缺少支付事件号或支付流水号")
	}
	if in.EventType == "payment.succeeded" {
		pr := strings.TrimSpace(in.PaymentRef)
		if pr == "" || len(pr) > 200 {
			return errs.Newf(errs.BadRequest, "缺少支付事件号或支付流水号")
		}
	}
	return nil
}

func validateRecordAmounts(in recordInput) error {
	if !in.Amount.IsPositive() || money.Check(in.Amount) != nil {
		return errs.Newf(errs.BadRequest, "支付金额或手续费不正确")
	}
	if in.Fee.IsNegative() || money.Check(in.Fee) != nil || in.Fee.GreaterThan(in.Amount) {
		return errs.Newf(errs.BadRequest, "支付金额或手续费不正确")
	}
	if !in.Currency.Valid() {
		return errs.Newf(errs.BadRequest, "支付金额或手续费不正确")
	}
	return nil
}

func (s *Service) afterSettleSuccess(ctx context.Context, evID uuid.UUID, in recordInput,
	haveIntent bool, intent model.Intent, orderID, paymentID uuid.UUID, method string) (RecordResult, error) {

	var except *uuid.UUID
	if haveIntent {
		except = &intent.ID
		if intent.Status == string(IntentRequiresAction) {
			ok, err := s.repo.SetIntentStatus(ctx, intent.ID, string(IntentRequiresAction), string(IntentSucceeded))
			if err != nil {
				return RecordResult{}, err
			}
			if !ok {
				return RecordResult{}, errors.New("payment: 支付意图状态更新失败")
			}
		}
	}
	// 订单已结清：同单别的在途意图（换过渠道、线下收款）一律作废，免得再付一次
	if !CanTransition(IntentRequiresAction, IntentCancelled) {
		return RecordResult{}, errors.New("payment: 意图状态机不允许作废")
	}
	if _, err := s.repo.CloseOrderIntents(ctx, orderID, except, string(IntentRequiresAction), string(IntentCancelled)); err != nil {
		return RecordResult{}, err
	}

	if _, err := s.repo.FinishEvent(ctx, evID, eventProcessed, nil); err != nil {
		return RecordResult{}, err
	}

	oid := orderID
	if err := s.audit.Write(ctx, auditsvc.Event{
		Action:       "payment.succeeded",
		ResourceType: "order",
		ResourceID:   &oid,
		Data: map[string]any{
			"payment_id":   paymentID.String(),
			"provider":     in.Provider.Code,
			"out_trade_no": strings.TrimSpace(in.OutTradeNo),
			"amount":       in.Amount.StringFixed(2),
			"fee":          in.Fee.StringFixed(2),
			"currency":     string(in.Currency),
			"method":       method,
		},
	}); err != nil {
		return RecordResult{}, err
	}

	return RecordResult{Processed: true, PaymentID: paymentID}, nil
}

func (s *Service) afterSettleQuarantine(ctx context.Context, evID uuid.UUID, in recordInput, npe *NotPayableError,
	haveIntent bool, intent model.Intent, orderID, userID uuid.UUID, orderNo string, paymentID uuid.UUID) (RecordResult, error) {

	if in.Offline != nil {
		return RecordResult{}, errs.Newf(errs.Conflict, "该订单当前状态不可支付")
	}
	if !npe.Kind.Valid() || !orderStatusPattern.MatchString(npe.OrderStatus) {
		return RecordResult{}, errors.New("payment: 挂账参数不合法")
	}

	legs := buildSuspenseLegs(in)
	txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
		Kind:       ledgersvc.KindLatePaymentSuspense,
		Currency:   in.Currency,
		SourceType: ledgersvc.SourcePayment,
		SourceID:   paymentID,
		Memo:       "迟到支付挂账：" + string(npe.Kind),
		Legs:       legs,
	})
	if err != nil {
		return RecordResult{}, err
	}

	lateID := uuid.Must(uuid.NewV7())
	late := &model.LateCase{
		ID:             lateID,
		OrderID:        orderID,
		OrderNo:        orderNo,
		OrderStatus:    npe.OrderStatus,
		UserID:         userID,
		PaymentID:      paymentID,
		PaymentEventID: &evID,
		CaseKind:       string(npe.Kind),
		Currency:       string(in.Currency),
		Amount:         in.Amount,
		Status:         string(LateSuspense),
		SuspenseTxnID:  txnID,
	}
	if err := s.repo.InsertLateCase(ctx, late); err != nil {
		return RecordResult{}, err
	}

	if haveIntent && intent.Status == string(IntentRequiresAction) {
		ok, err := s.repo.SetIntentStatus(ctx, intent.ID, string(IntentRequiresAction), string(IntentSucceeded))
		if err != nil {
			return RecordResult{}, err
		}
		if !ok {
			return RecordResult{}, errors.New("payment: 支付意图状态更新失败")
		}
	}

	if _, err := s.repo.FinishEvent(ctx, evID, eventProcessed, nil); err != nil {
		return RecordResult{}, err
	}

	pid := paymentID
	if err := s.audit.Write(ctx, auditsvc.Event{
		Action:       "payment.quarantined",
		ResourceType: "payment",
		ResourceID:   &pid,
		Data: map[string]any{
			"case_kind":           string(npe.Kind),
			"order_id":            orderID.String(),
			"order_no":            orderNo,
			"order_status":        npe.OrderStatus,
			"user_id":             userID.String(),
			"provider_payment_id": strings.TrimSpace(in.PaymentRef),
			"amount":              in.Amount.StringFixed(2),
			"fee":                 in.Fee.StringFixed(2),
			"currency":            string(in.Currency),
			"suspense_txn":        txnID.String(),
		},
	}); err != nil {
		return RecordResult{}, err
	}

	return RecordResult{
		Processed:      true,
		PaymentID:      paymentID,
		QuarantineKind: npe.Kind,
		LedgerTxnID:    txnID,
	}, nil
}

func buildSuspenseLegs(in recordInput) []ledgersvc.Leg {
	net := in.Amount.Sub(in.Fee)
	var legs []ledgersvc.Leg
	if net.IsPositive() {
		legs = append(legs, ledgersvc.Leg{
			Account:   ledgersvc.PlatformAccount(ledgersvc.ChannelCash, in.Provider.Code),
			Direction: ledgersvc.Debit,
			Amount:    net,
		})
	}
	if in.Fee.IsPositive() {
		legs = append(legs, ledgersvc.Leg{
			Account:   ledgersvc.PlatformAccount(ledgersvc.PlatformFeeExpense, in.Provider.Code),
			Direction: ledgersvc.Debit,
			Amount:    in.Fee,
		})
	}
	legs = append(legs, ledgersvc.Leg{
		Account:   ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain),
		Direction: ledgersvc.Credit,
		Amount:    in.Amount,
	})
	return legs
}

func cloneRaw(raw map[string]string) map[string]string {
	if raw == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	return out
}

// eventTypeOf 把归一化状态映射成事件类型。
func eventTypeOf(st provider.Status) string {
	switch st {
	case provider.StatusSucceeded:
		return "payment.succeeded"
	case provider.StatusFailed:
		return "payment.failed"
	case provider.StatusRefunded:
		return "payment.refunded"
	default:
		return "payment.unknown"
	}
}

// normalizeMethod 规整付款方式：小写去空白，只收 ^[a-z0-9_]{1,32}$，不合格回 ""。
func normalizeMethod(raw string) string {
	m := strings.ToLower(strings.TrimSpace(raw))
	if m == "" || len(m) > 32 {
		return ""
	}
	for _, r := range m {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return ""
		}
	}
	return m
}
