package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/module/payment/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
	pkgtx "github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// intentTTL 是支付意图的有效期上限。
const intentTTL = 30 * time.Minute

// intentCreatingWindow 是占位意图被视为「正在建单」的时长（按库时钟比 created_at）：超过它仍没补上收银台信息（进程中途退出等），再点支付时作废重建。
// 要大于 completeIntentBudget，留出余量。
const intentCreatingWindow = 30 * time.Second

// completeIntentBudget 是 CompleteIntent 的总期限（渠道调用 + 落库）。
// 必须小于反向代理的读超时（deploy/nginx-pandora.conf 的 proxy_read_timeout 20 秒）和服务端请求超时：
// 超过它之前我们自己先结束、作废占位并回 503，用户看到的是可读的错误，而不是 nginx 的 504 与一张悬着的占位。
const completeIntentBudget = 15 * time.Second

// completeIntentPersistTimeout 是 CompleteIntent 落库（回写或作废占位）的期限，从总期限里留出；渠道调用得到其余的 12 秒。
// CoinPayments 冷缓存时要连发两次请求（查法币 ID + 建发票），safehttp 单次超时 6 秒，两次正好在 12 秒内。
const completeIntentPersistTimeout = 3 * time.Second

// IntentDraft 是 BeginIntent 的结果：要么是可直接复用的在途意图，要么是刚占好、等 CompleteIntent 去渠道建单的占位意图。
type IntentDraft struct {
	reused   *IntentResult
	intentID uuid.UUID
	orderID  uuid.UUID
	provider string // 渠道 code，只用于日志
	ad       provider.Provider
	req      provider.CreateRequest
	result   IntentResult // 预填好 IntentID、OutTradeNo、Amount、Surcharge、ExpiresAt
}

// BeginIntent 校验并占住这次支付：在调用方事务里执行（order 已锁订单行），不调渠道。
// 同渠道、同方式、同金额、未过期且已建好收银台的在途意图直接复用；
// 另一张占位意图还在建单（不到 intentCreatingWindow）时回 409「支付正在发起」；其余在途意图作废后插一条新的占位意图。
// 加入调用方事务时不开保存点：返回错误后调用方必须回滚。
func (s *Service) BeginIntent(ctx context.Context, in CreateIntentInput) (IntentDraft, error) {
	if in.OrderID == uuid.Nil || in.UserID == uuid.Nil {
		return IntentDraft{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}
	orderNo := strings.TrimSpace(in.OrderNo)
	if orderNo == "" || len(orderNo) > 64 {
		return IntentDraft{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}
	if !in.Amount.IsPositive() || money.Check(in.Amount) != nil {
		return IntentDraft{}, errs.Newf(errs.BadRequest, "发起支付的参数不完整")
	}

	if s.cfg.PublicBaseURL == "" {
		// 没有对外地址就拼不出回调与回跳地址（回跳前缀还会退化成 "/"，放过 //evil.example）
		return IntentDraft{}, errs.Wrap(errs.Internal, errors.New("payment: 未配置 PublicBaseURL"))
	}
	p, ad, err := s.adapterFor(ctx, in.ProviderCode, true)
	if err != nil {
		return IntentDraft{}, err
	}
	method, err := resolveMethod(p, in.Method)
	if err != nil {
		return IntentDraft{}, err
	}
	if in.Amount.LessThan(p.MinAmount) {
		return IntentDraft{}, ErrBelowMinimum
	}

	surcharge := Surcharge(in.Amount, p.FeeFixed, p.FeePercent)
	gross := in.Amount.Add(surcharge)
	if err := money.Check(gross); err != nil {
		return IntentDraft{}, errs.Wrap(errs.Internal, err)
	}

	base, err := s.notifyBase(ctx)
	if err != nil {
		return IntentDraft{}, err
	}

	now := s.now()
	if in.ExpiresAt != nil && !in.ExpiresAt.After(now) {
		return IntentDraft{}, errs.Newf(errs.OrderLapsed, "这张订单已超过付款期限，请取消后重新下单")
	}
	expiresAt := now.Add(intentTTL)
	if in.ExpiresAt != nil && in.ExpiresAt.Before(expiresAt) {
		expiresAt = *in.ExpiresAt
	}
	expPtr := &expiresAt

	createReq := provider.CreateRequest{
		OutTradeNo: "", // 事务内再拼
		Amount:     gross,
		Subject:    in.Title,
		Method:     method,
		NotifyURL:  notifyURL(base, p.Code),
		ReturnURL:  s.returnURL(in.ReturnURL),
		ClientIP:   in.ClientIP,
	}

	var draft IntentDraft
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		active, ok, err := s.repo.LockActiveIntent(ctx, in.OrderID)
		if err != nil {
			return err
		}
		if ok {
			if active.ProviderID == p.ID && active.Method == method &&
				active.Amount.Equal(gross) && active.SurchargeAmount.Equal(surcharge) &&
				active.RedirectURL != "" &&
				(active.ExpiresAt == nil || active.ExpiresAt.After(now)) {
				draft = IntentDraft{
					reused: &IntentResult{
						IntentID:    active.ID,
						OutTradeNo:  active.OutTradeNo,
						HTTPMethod:  active.HTTPMethod,
						RedirectURL: active.RedirectURL,
						FormFields:  active.FormFields,
						Amount:      active.Amount,
						Surcharge:   active.SurchargeAmount,
						ExpiresAt:   active.ExpiresAt,
						Reused:      true,
					},
				}
				return nil
			}
			if active.RedirectURL == "" {
				creating, err := s.repo.IntentCreatedWithin(ctx, active.ID, intentCreatingWindow)
				if err != nil {
					return err
				}
				if creating {
					return errs.New(errs.PaymentInProgress)
				}
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
		createReq.OutTradeNo = outTradeNo

		intentID := uuid.Must(uuid.NewV7())
		it := &model.Intent{
			ID:              intentID,
			OrderID:         in.OrderID,
			OrderNo:         orderNo,
			UserID:          in.UserID,
			ProviderID:      p.ID,
			OutTradeNo:      outTradeNo,
			ProviderRef:     nil,
			Method:          method,
			Amount:          gross,
			SurchargeAmount: surcharge,
			Status:          string(IntentRequiresAction),
			HTTPMethod:      http.MethodGet,
			RedirectURL:     "",
			FormFields:      map[string]string{},
			ExpiresAt:       expPtr,
		}
		if err := s.repo.InsertIntent(ctx, it); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.New(errs.PaymentInProgress)
			}
			return err
		}

		draft = IntentDraft{
			intentID: intentID,
			orderID:  in.OrderID,
			provider: p.Code,
			ad:       ad,
			req:      createReq,
			result: IntentResult{
				IntentID:   intentID,
				OutTradeNo: outTradeNo,
				Amount:     gross,
				Surcharge:  surcharge,
				ExpiresAt:  expPtr,
			},
		}
		return nil
	})
	if err != nil {
		return IntentDraft{}, err
	}
	return draft, nil
}

// CompleteIntent 去渠道建单并把收银台信息回写到占位意图。必须在 BeginIntent 所在事务提交之后、不在任何事务里调用：
// 渠道调用可能联网，不能占着库连接与订单锁；在事务里调用直接回内部错误。总期限 completeIntentBudget。复用的草稿原样返回。
// 渠道失败时作废占位意图、回 503；占位已被作废（并发再点、订单取消或超时）时回 409，渠道那边留下的发票不会入账到这张单的新意图上。
func (s *Service) CompleteIntent(ctx context.Context, d IntentDraft) (IntentResult, error) {
	if d.reused != nil {
		return *d.reused, nil
	}
	if d.ad == nil || d.intentID == uuid.Nil {
		return IntentResult{}, errs.Wrap(errs.Internal, errors.New("payment: 支付草稿不完整"))
	}
	if pkgtx.InTx(ctx) {
		// 在事务里联网会占着库连接与订单锁（最长 completeIntentBudget），调用方写错了
		plog.From(ctx).Error("CompleteIntent 不能在事务里调用",
			zap.String("provider", d.provider),
			zap.String("order_id", d.orderID.String()),
		)
		return IntentResult{}, errs.Wrap(errs.Internal, errors.New("payment: CompleteIntent 在事务里被调用"))
	}

	gctx, cancelGateway := context.WithTimeout(ctx, completeIntentBudget-completeIntentPersistTimeout)
	resp, err := d.ad.CreatePayment(gctx, d.req)
	cancelGateway()
	// 落库不跟请求一起取消（用户断开也要把结果写下或把占位作废），但有自己的期限，库卡住时不一直挂着
	wctx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), completeIntentPersistTimeout)
	defer cancelPersist()

	badResp := func() bool {
		if resp.HTTPMethod != http.MethodGet && resp.HTTPMethod != http.MethodPost {
			return true
		}
		if resp.RedirectURL == "" || len(resp.RedirectURL) > 4096 {
			return true
		}
		if len(resp.ProviderRef) > 128 {
			return true
		}
		return false
	}

	if err != nil || badResp() {
		logErr := err
		if err == nil {
			logErr = errors.New("payment: 渠道返回的收银台信息不合格")
		}
		plog.From(ctx).Warn("渠道创建支付失败",
			zap.String("provider", d.provider),
			zap.String("order_id", d.orderID.String()),
			zap.Error(logErr),
		)
		if txErr := s.tx.Do(wctx, func(ctx context.Context) error {
			_, cancelErr := s.repo.CancelPlaceholderIntent(ctx, d.intentID)
			return cancelErr
		}); txErr != nil {
			plog.From(wctx).Warn("作废占位意图失败",
				zap.String("provider", d.provider),
				zap.String("order_id", d.orderID.String()),
				zap.Error(txErr),
			)
		}
		return IntentResult{}, errs.Newf(errs.Unavailable, "渠道创建支付失败，请稍后再试或更换付款方式")
	}

	formFields := resp.FormFields
	if formFields == nil {
		formFields = map[string]string{}
	}
	var providerRef *string
	if resp.ProviderRef != "" {
		providerRef = &resp.ProviderRef
	}

	var filled bool
	if txErr := s.tx.Do(wctx, func(ctx context.Context) error {
		ok, fillErr := s.repo.FillPlaceholderIntent(ctx, d.intentID, repo.IntentFill{
			HTTPMethod:  resp.HTTPMethod,
			RedirectURL: resp.RedirectURL,
			FormFields:  formFields,
			ProviderRef: providerRef,
		})
		if fillErr != nil {
			return fillErr
		}
		filled = ok
		return nil
	}); txErr != nil {
		return IntentResult{}, txErr
	}
	if !filled {
		// 占位已被作废：仍把发票号记到它上面，万一这张发票被付了，入账时也能按发票号核对
		if providerRef != nil {
			if refErr := s.tx.Do(wctx, func(ctx context.Context) error {
				_, e := s.repo.SetIntentProviderRef(ctx, d.intentID, *providerRef)
				return e
			}); refErr != nil {
				plog.From(ctx).Warn("给已作废的占位意图记发票号失败",
					zap.String("provider", d.provider),
					zap.String("order_id", d.orderID.String()),
					zap.Error(refErr),
				)
			}
		}
		plog.From(ctx).Warn("占位意图已被作废，渠道建好的发票不再使用",
			zap.String("provider", d.provider),
			zap.String("order_id", d.orderID.String()),
			zap.String("out_trade_no", d.req.OutTradeNo),
			zap.String("provider_ref", resp.ProviderRef),
		)
		return IntentResult{}, errs.Newf(errs.Conflict, "这次支付已被新的发起取代或订单状态已变化，请刷新后重试")
	}

	out := d.result
	out.HTTPMethod = resp.HTTPMethod
	out.RedirectURL = resp.RedirectURL
	out.FormFields = formFields
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

func (s *Service) returnURL(supplied string) string {
	if supplied != "" && strings.HasPrefix(supplied, s.cfg.PublicBaseURL+"/") {
		return supplied
	}
	return s.cfg.PublicBaseURL + "/#orders"
}
