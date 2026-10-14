package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// HandleWebhook 处理渠道回调：解析验签、入账、按渠道要求出回执。只有渠道不存在、停用或报文无法解析时返回 error（handler 走通用错误响应）。
func (s *Service) HandleWebhook(ctx context.Context, code string, in provider.Inbound) (provider.AckOutput, error) {
	p, ad, err := s.adapterFor(ctx, code, false)
	if err != nil {
		return provider.AckOutput{}, err
	}

	n, err := ad.ParseNotification(ctx, in)
	if err != nil {
		plog.From(ctx).Warn("支付回调无法解析", zap.String("provider", code), zap.Error(err))
		return provider.AckOutput{}, errs.Newf(errs.BadRequest, "回调格式无法解析")
	}

	res, recErr := s.recordPayment(ctx, recordInput{
		Provider:          p,
		EventID:           n.EventID,
		EventType:         eventTypeOf(n.Status),
		PaymentRef:        n.PaymentRef,
		OutTradeNo:        n.OutTradeNo,
		Amount:            n.Amount,
		Fee:               n.Fee,
		Currency:          n.Currency,
		Method:            n.Method,
		Raw:               n.Raw,
		SignatureVerified: n.SignatureVerified,
	}, nil)

	ack := provider.AckInput{
		SignatureValid: n.SignatureVerified,
		HandlerError:   recErr,
		Processed:      res.Processed,
		AlreadyHandled: res.AlreadyHandled,
	}
	if res.PaymentID != uuid.Nil {
		ack.PaymentID = res.PaymentID.String()
	}
	if res.SignatureFailed {
		ack.HandlerError = errs.New(errs.Unauthorized)
	}

	log := plog.From(ctx)
	if recErr != nil {
		log.Error("支付回调入账失败",
			zap.String("provider", code),
			zap.String("out_trade_no", n.OutTradeNo),
			zap.Error(recErr),
		)
	} else {
		log.Info("支付回调已处理",
			zap.String("provider", code),
			zap.String("out_trade_no", n.OutTradeNo),
			zap.Bool("processed", res.Processed),
			zap.Bool("already_handled", res.AlreadyHandled),
			zap.String("quarantine_kind", string(res.QuarantineKind)),
		)
	}

	return ad.NotificationAck(ack), nil
}
