package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// intentTTL 是支付意图的有效期上限。
const intentTTL = 30 * time.Minute

// CreateIntent 为订单发起一次支付并返回收银台跳转信息；在调用方事务里执行（order 已锁订单行）。
// 加入调用方事务时不开保存点：返回错误后调用方必须回滚，不能吞掉错误继续提交。
func (s *Service) CreateIntent(ctx context.Context, in CreateIntentInput) (IntentResult, error) {
	if in.OrderID == uuid.Nil || in.UserID == uuid.Nil {
		return IntentResult{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}
	orderNo := strings.TrimSpace(in.OrderNo)
	if orderNo == "" || len(orderNo) > 64 {
		return IntentResult{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}
	if !in.Amount.IsPositive() || money.Check(in.Amount) != nil || !in.Currency.Valid() {
		return IntentResult{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}

	if s.cfg.PublicBaseURL == "" {
		// 没有对外地址就拼不出回调与回跳地址（回跳前缀还会退化成 "/"，放过 //evil.example）
		return IntentResult{}, errs.Wrap(errs.Internal, errors.New("payment: 未配置 PublicBaseURL"))
	}
	p, ad, err := s.adapterFor(ctx, in.ProviderCode, true)
	if err != nil {
		return IntentResult{}, err
	}
	if !slices.Contains(p.Currencies, string(in.Currency)) {
		return IntentResult{}, errs.Invalid(map[string]string{"provider": "该支付渠道不支持此币种"})
	}
	method, err := resolveMethod(p, in.Method)
	if err != nil {
		return IntentResult{}, err
	}
	if in.Amount.LessThan(p.MinAmount) {
		return IntentResult{}, ErrBelowMinimum
	}

	now := s.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return IntentResult{}, errs.Newf(errs.OrderLapsed, "这张订单已超过付款期限，请取消后重新下单")
	}
	expiresAt := now.Add(intentTTL)
	if in.ExpiresAt != nil && in.ExpiresAt.Before(expiresAt) {
		expiresAt = *in.ExpiresAt
	}
	expPtr := &expiresAt

	var out IntentResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		active, ok, err := s.repo.LockActiveIntent(ctx, in.OrderID)
		if err != nil {
			return err
		}
		if ok {
			if active.ProviderID == p.ID && active.Method == method &&
				active.Amount.Equal(in.Amount) && active.Currency == string(in.Currency) &&
				active.RedirectURL != "" &&
				(active.ExpiresAt == nil || active.ExpiresAt.After(now)) {
				out = IntentResult{
					IntentID:    active.ID,
					OutTradeNo:  active.OutTradeNo,
					HTTPMethod:  active.HTTPMethod,
					RedirectURL: active.RedirectURL,
					FormFields:  active.FormFields,
					Amount:      active.Amount,
					Currency:    in.Currency,
					ExpiresAt:   active.ExpiresAt,
					Reused:      true,
				}
				return nil
			}
			if !CanTransition(IntentRequiresAction, IntentCancelled) {
				return errors.New("payment: 意图状态机不允许取消在途意图")
			}
			updated, err := s.repo.SetIntentStatus(ctx, active.ID, string(IntentRequiresAction), string(IntentCancelled))
			if err != nil {
				return err
			}
			if !updated {
				return errors.New("payment: 作废在途支付意图失败")
			}
		}

		n, err := s.repo.CountIntents(ctx, in.OrderID)
		if err != nil {
			return err
		}
		outTradeNo := orderNo
		if n > 0 {
			outTradeNo = fmt.Sprintf("%s-%d", orderNo, n+1)
		}
		if len(outTradeNo) > 64 {
			return errors.New("payment: 商户单号过长")
		}

		resp, err := ad.CreatePayment(ctx, provider.CreateRequest{
			OutTradeNo: outTradeNo,
			Amount:     in.Amount,
			Currency:   in.Currency,
			Subject:    in.Title,
			Method:     method,
			NotifyURL:  s.notifyURL(p.Code),
			ReturnURL:  s.returnURL(in.ReturnURL),
			ClientIP:   in.ClientIP,
		})
		if err != nil {
			plog.From(ctx).Warn("渠道创建支付失败",
				zap.String("provider", p.Code),
				zap.String("order_id", in.OrderID.String()),
				zap.Error(err),
			)
			return errs.Newf(errs.Unavailable, "渠道创建支付失败，请稍后再试或更换付款方式")
		}

		intentID := uuid.Must(uuid.NewV7())
		formFields := resp.FormFields
		if formFields == nil {
			formFields = map[string]string{}
		}
		var providerRef *string
		if resp.ProviderRef != "" {
			providerRef = &resp.ProviderRef
		}
		it := &model.Intent{
			ID:          intentID,
			OrderID:     in.OrderID,
			OrderNo:     orderNo,
			UserID:      in.UserID,
			ProviderID:  p.ID,
			OutTradeNo:  outTradeNo,
			ProviderRef: providerRef,
			Method:      method,
			Currency:    string(in.Currency),
			Amount:      in.Amount,
			Status:      string(IntentRequiresAction),
			HTTPMethod:  resp.HTTPMethod,
			RedirectURL: resp.RedirectURL,
			FormFields:  formFields,
			ExpiresAt:   expPtr,
		}
		if err := s.repo.InsertIntent(ctx, it); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "支付正在发起，请稍后再试")
			}
			return err
		}

		out = IntentResult{
			IntentID:    intentID,
			OutTradeNo:  outTradeNo,
			HTTPMethod:  resp.HTTPMethod,
			RedirectURL: resp.RedirectURL,
			FormFields:  formFields,
			Amount:      in.Amount,
			Currency:    in.Currency,
			ExpiresAt:   expPtr,
		}
		return nil
	})
	if err != nil {
		return IntentResult{}, err
	}
	return out, nil
}

// CloseOrderIntents 把订单的在途意图关成 to（cancelled 或 expired）；order 取消、超时释放订单时在同一事务里调用。返回关了几个。
func (s *Service) CloseOrderIntents(ctx context.Context, orderID uuid.UUID, to IntentStatus) (int64, error) {
	if !CanTransition(IntentRequiresAction, to) || to == IntentSucceeded {
		return 0, errors.New("payment: 关闭意图的目标状态不合法")
	}
	var n int64
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		var closeErr error
		n, closeErr = s.repo.CloseOrderIntents(ctx, orderID, nil, string(IntentRequiresAction), string(to))
		return closeErr
	})
	return n, err
}

func (s *Service) notifyURL(code string) string {
	return s.cfg.PublicBaseURL + "/v1/webhooks/payments/" + code
}

func (s *Service) returnURL(supplied string) string {
	if supplied != "" && strings.HasPrefix(supplied, s.cfg.PublicBaseURL+"/") {
		return supplied
	}
	return s.cfg.PublicBaseURL + "/#orders"
}
