package service

import (
	"context"
	"errors"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// channelQueryTimeout 是单次渠道查询的上限。
const channelQueryTimeout = 10 * time.Second

// patrolConfig 是巡检的节流参数（各字段含义见下面 defaultPatrol 的注释）。
type patrolConfig struct {
	FirstDelay   time.Duration
	MaxAttempts  int
	BatchLimit   int
	Spacing      time.Duration
	LastCallLead time.Duration
}

// defaultPatrol：5 分钟后首查，最多 6 次，一轮 20 单，间隔 500ms，过期前 90 秒最后一查。
var defaultPatrol = patrolConfig{
	FirstDelay:   5 * time.Minute,
	MaxAttempts:  6,
	BatchLimit:   20,
	Spacing:      500 * time.Millisecond,
	LastCallLead: 90 * time.Second,
}

// queryMinGap 是两次巡检查单的最小间隔。
const queryMinGap = time.Minute

// nextQueryAt 算下一次巡检查单的时间：指数退避，过期前留最后一查。
func nextQueryAt(now time.Time, attempts int, expires *time.Time, p patrolConfig) time.Time {
	if attempts < 1 {
		attempts = 1
	}
	delay := p.FirstDelay
	for i := 1; i < attempts && delay < 24*time.Hour; i++ {
		delay *= 2
	}
	if delay < queryMinGap {
		delay = queryMinGap
	}
	next := now.Add(delay)
	if expires != nil {
		lastCall := expires.Add(-p.LastCallLead)
		if next.After(lastCall) && !lastCall.Before(now.Add(queryMinGap)) {
			next = lastCall
		}
	}
	return next
}

// Query 向订单发起过支付的渠道查单，查到已付就走与回调同一条入账路径补记。
//
// 调用约定（order 的 /orders/{id}/query 调它）：
//   - 不能在事务里、也不能在持有订单锁时调：它要在事务外请求渠道；补记时先拿咨询锁再由 Settler 锁订单，
//     调用方先持有订单锁会与回调的锁序相反。
//   - 本方法不核对订单归属，订单存在且属于当前用户由 order 先判断。
func (s *Service) Query(ctx context.Context, orderID uuid.UUID) (QueryResult, error) {
	princ, isAdmin := authn.From(ctx)
	admin := isAdmin && princ.Audience == authn.AudienceAdmin

	var audited bool
	var hook recordHook
	if admin {
		hook = func(ctx context.Context, _ RecordResult) error {
			audited = true
			return nil
		}
	}

	res, _, err := s.queryOrder(ctx, orderID, hook)
	if admin && !audited {
		if werr := s.tx.Do(ctx, func(ctx context.Context) error {
			return s.audit.Write(ctx, queryAuditEvent(orderID, res, err))
		}); werr != nil {
			return QueryResult{}, errs.Wrap(errs.Internal, werr)
		}
	}
	return res, err
}

// queryOrder 是 Query 的本体；notSupported 为 true 表示所有失败里有「渠道不支持查单」（巡检据此停查）。
func (s *Service) queryOrder(ctx context.Context, orderID uuid.UUID, hook recordHook) (res QueryResult, notSupported bool, err error) {
	refs, err := s.repo.QueryRefs(ctx, orderID)
	if err != nil {
		return QueryResult{}, false, err
	}
	if len(refs) == 0 {
		return QueryResult{}, false, ErrNeverPaid
	}

	var answer *QueryResult
	var firstErr error
	var firstNS bool
	for _, ref := range refs {
		one, ns, qerr := s.queryRef(ctx, orderID, ref, hook)
		if qerr != nil {
			if firstErr == nil {
				firstErr = qerr
				firstNS = ns
			}
			if ns {
				notSupported = true
			}
			continue
		}
		if one.ChannelStatus == ChannelPaid {
			return one, false, nil
		}
		if answer == nil {
			answer = &one
		}
	}
	if answer != nil {
		return *answer, false, nil
	}
	return QueryResult{}, notSupported || firstNS, firstErr
}

func (s *Service) queryRef(ctx context.Context, orderID uuid.UUID, ref repo.QueryRef, hook recordHook) (QueryResult, bool, error) {
	out := QueryResult{
		OrderID:      orderID,
		OrderNo:      ref.OrderNo,
		ProviderCode: ref.ProviderCode,
	}

	p, ad, err := s.adapterFor(ctx, ref.ProviderCode, false)
	if errors.Is(err, ErrProviderDisabled) {
		return out, false, errs.Newf(errs.Unavailable, "该支付渠道已停用，无法向渠道查单")
	}
	if err != nil {
		return out, false, err
	}

	qctx, cancel := context.WithTimeout(ctx, channelQueryTimeout)
	chRes, err := ad.QueryPayment(qctx, ref.OutTradeNo)
	cancel()
	if errors.Is(err, provider.ErrNotSupported) {
		return out, true, errs.Newf(errs.Conflict, "该支付渠道不支持主动查单")
	}
	if err != nil {
		plog.From(ctx).Warn("渠道查单失败",
			zap.String("order_id", orderID.String()),
			zap.String("provider", ref.ProviderCode),
			zap.Error(err),
		)
		return out, false, errs.Newf(errs.Unavailable, "渠道查单失败，请稍后再试")
	}

	switch {
	case !chRes.Found:
		out.ChannelStatus = ChannelNotFound
		return out, false, nil
	case chRes.Status != provider.StatusSucceeded:
		out.ChannelStatus = ChannelUnpaid
		return out, false, nil
	default:
		out.ChannelStatus = ChannelPaid
		if chRes.PaymentRef == "" {
			return out, false, errs.Newf(errs.Unavailable, "渠道查单结果缺少支付流水号，无法补记")
		}
		payHook := hook
		if payHook != nil {
			payHook = func(ctx context.Context, rec RecordResult) error {
				out.Reconciled = rec.Processed
				out.AlreadyRecorded = rec.AlreadyHandled
				out.QuarantineKind = rec.QuarantineKind
				if err := s.audit.Write(ctx, queryAuditEvent(orderID, out, nil)); err != nil {
					return err
				}
				return hook(ctx, rec)
			}
		}
		rec, rerr := s.recordPayment(ctx, recordInput{
			Provider:          p,
			EventID:           chRes.PaymentRef + ":RECONCILED",
			EventType:         "payment.succeeded",
			PaymentRef:        chRes.PaymentRef,
			OutTradeNo:        ref.OutTradeNo,
			Amount:            chRes.Amount,
			Fee:               decimal.Zero,
			Method:            chRes.Method,
			Raw:               map[string]string{"source": "active_query", "method": chRes.Method},
			SignatureVerified: true,
			FeeUnknown:        true,
		}, payHook)
		if errors.Is(rerr, ErrAmountMismatch) || errors.Is(rerr, ErrProviderRefMismatch) {
			// 渠道说已付、金额或渠道单号却和意图对不上：比普通失败严重，要人看
			plog.From(ctx).Error("查单结果与支付意图不符（金额或渠道单号），未入账",
				zap.String("order_id", orderID.String()),
				zap.String("provider", ref.ProviderCode),
				zap.String("out_trade_no", ref.OutTradeNo),
				zap.String("channel_ref", chRes.PaymentRef),
				zap.String("channel_amount", chRes.Amount.StringFixed(2)))
		}
		if rerr != nil {
			return out, false, rerr
		}
		out.Reconciled = rec.Processed
		out.AlreadyRecorded = rec.AlreadyHandled
		out.QuarantineKind = rec.QuarantineKind
		return out, false, nil
	}
}

// ReconcileDue 跑一轮巡检（payment:query_patrol 任务调用）。
func (s *Service) ReconcileDue(ctx context.Context) (PatrolStats, error) {
	p := defaultPatrol
	var stats PatrolStats
	for stats.Queried < p.BatchLimit {
		if stats.Queried > 0 && p.Spacing > 0 {
			select {
			case <-ctx.Done():
				return stats, nil
			case <-time.After(p.Spacing):
			}
		}
		if ctx.Err() != nil {
			return stats, nil
		}

		var due repo.DueIntent
		var claimed bool
		err := s.tx.Do(ctx, func(ctx context.Context) error {
			d, ok, err := s.repo.ClaimDueIntent(ctx, p.MaxAttempts, p.FirstDelay)
			if err != nil {
				return err
			}
			if !ok {
				return nil
			}
			claimed = true
			due = d
			marked, err := s.repo.MarkIntentQueried(ctx, due.IntentID, nextQueryAt(due.Now, due.Attempts+1, due.ExpiresAt, p))
			if err != nil {
				return err
			}
			if !marked {
				return errors.New("payment: 巡检查单认领丢失")
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return stats, nil
			}
			return stats, err
		}
		if !claimed {
			return stats, nil
		}
		stats.Queried++

		res, notSupported, qerr := s.queryOrder(ctx, due.OrderID, nil)
		if qerr != nil {
			stats.Failed++
			if notSupported {
				if stopErr := s.tx.Do(ctx, func(ctx context.Context) error {
					return s.repo.StopIntentQueries(ctx, due.IntentID, p.MaxAttempts)
				}); stopErr != nil {
					plog.From(ctx).Error("停止对不支持查单的渠道巡检失败",
						zap.String("order_id", due.OrderID.String()),
						zap.Error(stopErr),
					)
				}
			}
			plog.From(ctx).Warn("巡检查单未成",
				zap.String("order_id", due.OrderID.String()),
				zap.String("intent_id", due.IntentID.String()),
				zap.Error(qerr),
			)
			continue
		}
		if res.Reconciled {
			stats.Reconciled++
			plog.From(ctx).Warn("巡检查单补记了回调没送到的收款",
				zap.String("order_id", res.OrderID.String()),
				zap.String("order_no", res.OrderNo),
				zap.String("provider", res.ProviderCode),
				zap.String("quarantine_kind", string(res.QuarantineKind)),
			)
		}
	}
	return stats, nil
}

func orderQueryResult(res QueryResult, err error) string {
	switch {
	case err != nil:
		return "failed"
	case res.Reconciled:
		return "reconciled"
	case res.AlreadyRecorded:
		return "already_recorded"
	default:
		return string(res.ChannelStatus)
	}
}

func queryAuditEvent(orderID uuid.UUID, res QueryResult, err error) auditsvc.Event {
	data := map[string]any{
		"result": orderQueryResult(res, err),
	}
	if res.OrderNo != "" {
		data["order_no"] = res.OrderNo
	}
	if res.ProviderCode != "" {
		data["provider_code"] = res.ProviderCode
	}
	if res.ChannelStatus != "" {
		data["channel_status"] = string(res.ChannelStatus)
	}
	if res.QuarantineKind != "" {
		data["quarantine_kind"] = string(res.QuarantineKind)
	}
	e := auditsvc.Event{
		Action:       "order.payment_queried",
		ResourceType: "order",
		ResourceID:   &orderID,
		Data:         data,
	}
	if err != nil {
		e.Outcome = auditsvc.OutcomeFailure
		data["error_code"] = string(errs.As(err).Code)
	} else {
		e.Outcome = auditsvc.OutcomeSuccess
	}
	return e
}
