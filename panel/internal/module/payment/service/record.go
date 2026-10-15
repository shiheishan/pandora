package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

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

// ErrProviderRefMismatch 是渠道报来的发票号与意图建单时存下的不一致（同店别的发票、孤儿发票、几套系统共用账户撞单号），整笔拒收。
var ErrProviderRefMismatch = errs.Newf(errs.Conflict, "渠道单号与支付意图不一致")

// settleSavepoint 是调 Settler 前开的保存点名。
const settleSavepoint = "payment_settle"

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
	// FeeUnknown 为 true 时 Fee 不是渠道给的真实值（查单接口不回手续费）：与已入账的那笔比对时不比手续费。
	FeeUnknown bool
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
	if errors.Is(err, ErrProviderRefMismatch) {
		// 拒收的那笔事务连事件行一起回滚了：另开一笔只留证据（原事件不占去重键，渠道重投时照样再核对一次；证据按原事件号去重）
		if evErr := s.recordRefMismatchEvidence(ctx, in); evErr != nil {
			plog.From(ctx).Warn("记录渠道单号不符的证据失败",
				zap.String("provider", in.Provider.Code),
				zap.Error(evErr),
			)
		}
	}
	return out, err
}

// 证据事件号 = 前缀 + 原事件号（截到 180 字节，整体不超过 provider_event_id 的 200 上限）。
const (
	refMismatchEventPrefix = "ref_mismatch:"
	refMismatchEventIDMax  = 180
)

// recordRefMismatchEvidence 落一条已忽略的事件，只存核对用的几个字段；同一原事件只落一条。
func (s *Service) recordRefMismatchEvidence(ctx context.Context, in recordInput) error {
	pr := strings.TrimSpace(in.PaymentRef)
	raw := map[string]string{
		"reject":       "provider_ref_mismatch",
		"event_id":     clip(strings.TrimSpace(in.EventID), 200),
		"out_trade_no": clip(strings.TrimSpace(in.OutTradeNo), 64),
		"channel_ref":  clip(pr, 200),
	}
	ev := &model.Event{
		ID:         uuid.Must(uuid.NewV7()),
		ProviderID: in.Provider.ID,
		// 键由原事件号确定：同一事件重投只留一条证据（InsertEvent 撞键不插），原事件行已随入账事务回滚、不占去重键，每次照样重新核对
		ProviderEventID:   refMismatchEventPrefix + clip(strings.TrimSpace(in.EventID), refMismatchEventIDMax),
		EventType:         in.EventType,
		SignatureVerified: true,
		ProcessingStatus:  eventPending,
		RawPayload:        raw,
	}
	if pr != "" && len(pr) <= 200 {
		ev.ProviderPaymentID = &pr
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		inserted, err := s.repo.InsertEvent(ctx, ev)
		if err != nil || !inserted {
			return err
		}
		msg := "渠道单号与支付意图不一致"
		_, err = s.repo.FinishEvent(ctx, ev.ID, eventIgnored, &msg)
		return err
	})
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
			RawPayload:        unverifiedEvidence(in),
		}
		// 未验签的报文不可信：流水号超长就不存（库里限 200 字）
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
		if intent.ProviderRef != nil && *intent.ProviderRef != pr {
			fields := []zap.Field{
				zap.String("provider", in.Provider.Code),
				zap.String("out_trade_no", strings.TrimSpace(in.OutTradeNo)),
				zap.String("intent_ref", *intent.ProviderRef),
				zap.String("channel_ref", pr),
			}
			if in.Provider.Adapter == AdapterCoinPayments {
				// 临时口径：CoinPayments 建单响应的 invoices[].id 与回调、回查里的 invoice.id 是否同一个标识，
				// 官方文档没写明（都写作系统分配的发票 id），待沙箱核实；核实前只记日志、照常入账，免得全部付款卡住
				log.Error("CoinPayments 渠道单号与支付意图存的发票号不一致（待沙箱核实，暂不拒收）", fields...)
			} else {
				log.Error("渠道单号与支付意图不一致，未入账", fields...)
				return RecordResult{}, ErrProviderRefMismatch
			}
		}
	}

	surcharge := decimal.Zero
	if haveIntent {
		surcharge = intent.SurchargeAmount
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
			(!in.FeeUnknown && !existing.FeeAmount.Equal(in.Fee)) ||
			!existing.SurchargeAmount.Equal(surcharge) {
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
		SurchargeAmount:   surcharge,
		Status:            "succeeded",
		Method:            methodPtr,
	}
	if err := s.repo.InsertPayment(ctx, pay); err != nil {
		return RecordResult{}, err
	}

	if s.settler == nil {
		return RecordResult{}, errors.New("payment: 没有注册 Settler")
	}

	// 保存点：Settler 出任何错都先撤回它这次的写入（写了一半、甚至语句已失败也不会带进挂账或让事务作废）
	if err := s.repo.SavePoint(ctx, settleSavepoint); err != nil {
		return RecordResult{}, err
	}
	settleErr := s.settler.SettlePayment(ctx, SettleInput{
		OrderID:      orderID,
		PaymentID:    paymentID,
		Amount:       in.Amount.Sub(surcharge),
		Fee:          in.Fee,
		Surcharge:    surcharge,
		Currency:     in.Currency,
		ProviderCode: in.Provider.Code,
		Method:       method,
	})
	if settleErr != nil {
		if err := s.repo.RollbackToSavePoint(ctx, settleSavepoint); err != nil {
			return RecordResult{}, errors.Join(settleErr, err)
		}
	}

	var result RecordResult
	var npe *NotPayableError
	switch {
	case settleErr == nil:
		result, err = s.afterSettleSuccess(ctx, evID, in, haveIntent, intent, orderID, paymentID, method, surcharge)
	case errors.As(settleErr, &npe):
		result, err = s.afterSettleQuarantine(ctx, evID, in, npe, haveIntent, intent, orderID, userID, orderNo, paymentID, surcharge)
	default:
		return RecordResult{}, settleErr
	}
	if err != nil {
		return RecordResult{}, err
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
	haveIntent bool, intent model.Intent, orderID, paymentID uuid.UUID, method string, surcharge decimal.Decimal) (RecordResult, error) {

	var except *uuid.UUID
	if haveIntent {
		except = &intent.ID
		if intent.Status == string(IntentRequiresAction) {
			if err := s.markIntentSucceeded(ctx, intent); err != nil {
				return RecordResult{}, err
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
			"surcharge":    surcharge.StringFixed(2),
			"currency":     string(in.Currency),
			"method":       method,
		},
	}); err != nil {
		return RecordResult{}, err
	}

	return RecordResult{Processed: true, PaymentID: paymentID}, nil
}

func (s *Service) afterSettleQuarantine(ctx context.Context, evID uuid.UUID, in recordInput, npe *NotPayableError,
	haveIntent bool, intent model.Intent, orderID, userID uuid.UUID, orderNo string, paymentID uuid.UUID, surcharge decimal.Decimal) (RecordResult, error) {

	if in.Offline != nil {
		return RecordResult{}, errs.Newf(errs.Conflict, "该订单当前状态不可支付")
	}
	if !npe.Kind.Valid() || !orderStatusPattern.MatchString(npe.OrderStatus) {
		return RecordResult{}, errors.New("payment: 挂账参数不合法")
	}

	legs := buildSuspenseLegs(in, surcharge)
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
		Amount:         in.Amount.Sub(surcharge),
		Status:         string(LateSuspense),
		SuspenseTxnID:  txnID,
	}
	if err := s.repo.InsertLateCase(ctx, late); err != nil {
		return RecordResult{}, err
	}

	if haveIntent && intent.Status == string(IntentRequiresAction) {
		if err := s.markIntentSucceeded(ctx, intent); err != nil {
			return RecordResult{}, err
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
			"surcharge":           surcharge.StringFixed(2),
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

// markIntentSucceeded 把付款对应的意图转成 succeeded。意图是读时的快照、没加锁，这期间可能已被「换个方式付」作废：
// 钱已经结算或挂账，意图状态只是记录，所以更新不到时记日志、不让整笔入账回滚（回滚了渠道重推也一样）。
func (s *Service) markIntentSucceeded(ctx context.Context, intent model.Intent) error {
	ok, err := s.repo.SetIntentStatus(ctx, intent.ID, string(IntentRequiresAction), string(IntentSucceeded))
	if err != nil {
		return err
	}
	if !ok {
		plog.From(ctx).Warn("付款对应的支付意图已被并发作废，保留其状态",
			zap.String("intent_id", intent.ID.String()),
			zap.String("order_id", intent.OrderID.String()))
	}
	return nil
}

// unverifiedEvidenceKeys 是验签失败时留证的字段（易支付通知的关键字段），其余一律丢弃。
var unverifiedEvidenceKeys = []string{
	"out_trade_no", "trade_no", "money", "trade_status", "pid", "type",
}

// unverifiedEvidenceValueMax 是验签失败时每个留证字段的上限（字节）；6 个字段加上解析出的 3 个，总量在 2 KiB 以内。
const unverifiedEvidenceValueMax = 64

// unverifiedEvidence 只留白名单字段、每个截到 64 字节：回调是匿名接口，伪造报文不能把任意大小的内容写进禁删的表。
func unverifiedEvidence(in recordInput) map[string]string {
	out := make(map[string]string, len(unverifiedEvidenceKeys)+3)
	for _, k := range unverifiedEvidenceKeys {
		if v, ok := in.Raw[k]; ok {
			out[k] = clip(v, unverifiedEvidenceValueMax)
		}
	}
	out["parsed_event_id"] = clip(in.EventID, unverifiedEvidenceValueMax)
	out["parsed_out_trade_no"] = clip(in.OutTradeNo, unverifiedEvidenceValueMax)
	out["parsed_payment_ref"] = clip(in.PaymentRef, unverifiedEvidenceValueMax)
	return out
}

// clip 把 s 截到不超过 n 字节，不切断 UTF-8 字符，并去掉 NUL（jsonb 不收）。
func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// buildSuspenseLegs 构造迟到支付挂账的分录。
// 迟到的钱里只有订单应付进挂账（以后转余额只转这部分），向用户加收的手续费照收、记手续费收入。
func buildSuspenseLegs(in recordInput, surcharge decimal.Decimal) []ledgersvc.Leg {
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
		Amount:    in.Amount.Sub(surcharge),
	})
	if surcharge.IsPositive() {
		legs = append(legs, ledgersvc.Leg{
			Account:   ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefSurcharge),
			Direction: ledgersvc.Credit,
			Amount:    surcharge,
		})
	}
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
