// [INPUT]: 依赖 domain 的 billing/identity/payment 用例（me 的账户行经 identity.PortalProfile），依赖 Deps.Pool 的 Ping（就绪探针）、platform 的 httpx/crypto 与 middleware
// [OUTPUT]: 对外提供 handlers 的核心门户处理器：探针、注册登录登出、me、改密、站点配置、优惠码试算、下单支付与回调、钱包充值、续费、我的公告；包内 isUUID
// [POS]: api/public 的主处理器文件，其余按模块拆在同包兄弟文件里：套餐目录 plans.go、工单 tickets.go、邀请与佣金 referral.go 等
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type handlers struct{ d Deps }

// okResponse 是只回 {"ok": true} 的写操作响应，门户几个文件共用。
type okResponse struct {
	OK bool `json:"ok"`
}

//------------------------------------------------------------------------------
// 前端
//------------------------------------------------------------------------------

// portal 返回用户门户单页。
//
// 这里的响应头与 API 路径刻意不同：
//
//	· API 一律 Cache-Control: no-store（订阅、余额、配额都敏感易变），
//	  而页面是编进二进制的静态资源，可以短时间缓存，用 ETag 让刷新走 304；
//	· 页面需要一条 CSP —— API 不返回 HTML 所以不设 CSP，
//	  但这里返回 HTML，就必须挡住外部脚本与被注入的资源加载。
//------------------------------------------------------------------------------
// 健康检查
//------------------------------------------------------------------------------

// probeResponse 是存活 / 就绪探针的响应：只有 ok / unavailable 两种。
type probeResponse struct {
	Status string `json:"status"`
}

// health 是存活探针：只报告进程还在，不碰任何依赖。
// SEC-006 要求「公网无法获得堆栈和详细健康依赖」，所以这里不暴露组件明细。
func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, probeResponse{Status: "ok"})
}

// ready 是就绪探针：检查依赖，但对外只回 ok / unavailable 两种结果。
func (h *handlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutCtx(r, 3*time.Second)
	defer cancel()

	if err := h.d.Pool.Ping(ctx); err != nil {
		h.d.Log.Error("就绪检查失败：数据库不可达", "error", err.Error())
		httpx.JSON(w, http.StatusServiceUnavailable, probeResponse{Status: "unavailable"})
		return
	}
	if err := h.d.Redis.Ping(ctx).Err(); err != nil {
		h.d.Log.Error("就绪检查失败：缓存不可达", "error", err.Error())
		httpx.JSON(w, http.StatusServiceUnavailable, probeResponse{Status: "unavailable"})
		return
	}
	httpx.OK(w, probeResponse{Status: "ok"})
}

//------------------------------------------------------------------------------
// 认证
//------------------------------------------------------------------------------

type registerStartReq struct {
	Email      string `json:"email"`
	InviteCode string `json:"invite_code"`
}

// registerStartResponse 无论邮箱是否已注册，结构与内容都一致（IAM-006）。
type registerStartResponse struct {
	RegistrationToken string `json:"registration_token"`
	// 前端据此决定要不要显示验证码输入框，不该自己猜
	VerificationRequired bool   `json:"verification_required"`
	ExpiresAt            string `json:"expires_at"`
	Message              string `json:"message"`
	// DevCode 只在开发模式回带验证码时出现，平时没有这个键
	DevCode string `json:"dev_code,omitempty"`
}

func (h *handlers) registerStart(w http.ResponseWriter, r *http.Request) {
	var req registerStartReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	out, err := h.d.Identity.StartRegistration(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.StartRegistrationInput{
			Email:      req.Email,
			InviteCode: req.InviteCode,
			IPHash:     crypto.HashIdentifier(h.d.Cfg.MasterKey, httpx.ClientIP(r)),
			IP:         httpx.ClientIP(r),
			UserAgent:  r.UserAgent(),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, registerStartResponse{
		RegistrationToken:    out.RegistrationToken,
		VerificationRequired: out.VerificationRequired,
		ExpiresAt:            out.ExpiresAt.UTC().Format(time.RFC3339),
		// 注意：无论邮箱是否已注册，响应结构与内容完全一致（IAM-006）
		Message: "若该邮箱可用于注册，验证码已发送",
		DevCode: out.DevCode,
	})
}

type registerCompleteReq struct {
	RegistrationToken string `json:"registration_token"`
	Code              string `json:"code"`
	Password          string `json:"password"`
	// Deprecated compatibility field. Invite authorization is immutable at
	// registration start and this value is intentionally ignored.
	InviteCode string `json:"invite_code"`
}

type registerCompleteResponse struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

func (h *handlers) registerComplete(w http.ResponseWriter, r *http.Request) {
	var req registerCompleteReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	out, err := h.d.Identity.CompleteRegistration(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.CompleteRegistrationInput{
			RegistrationToken: req.RegistrationToken,
			Code:              req.Code,
			Password:          req.Password,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, registerCompleteResponse{UserID: out.UserID, Email: out.Email})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	UserID       string `json:"user_id"`
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	out, err := h.d.Identity.Login(r.Context(),
		httpx.TenantIDFrom(r.Context()), identity.LoginInput{
			Email:     req.Email,
			Password:  req.Password,
			IPHash:    crypto.HashIdentifier(h.d.Cfg.MasterKey, httpx.ClientIP(r)),
			IP:        httpx.ClientIP(r),
			UserAgent: r.UserAgent(),
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.OK(w, loginResponse{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    out.ExpiresIn,
		UserID:       out.UserID,
	})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Identity.LogoutCurrentSession(r.Context(), httpx.TenantIDFrom(r.Context()), identity.LogoutInput{
		UserID:    p.UserID,
		SessionID: p.SessionID,
		Audience:  p.Audience,
	}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.NoContent(w)
}

// meResponse 的 user_id 与权限取自令牌，其余取 users 行。
type meResponse struct {
	UserID      string   `json:"user_id"`
	Email       string   `json:"email"`
	DisplayName *string  `json:"display_name"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	Permissions []string `json:"permissions"`
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.PrincipalFrom(ctx)

	prof, err := h.d.Identity.PortalProfile(ctx, p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, meResponse{
		UserID:      p.UserID,
		Email:       prof.Email,
		DisplayName: prof.DisplayName,
		Status:      prof.Status,
		CreatedAt:   prof.CreatedAt.UTC().Format(time.RFC3339),
		Permissions: p.Permissions,
	})
}

//------------------------------------------------------------------------------
// 下单与支付
//------------------------------------------------------------------------------

type createOrderReq struct {
	PlanID     string `json:"plan_id"`
	PriceID    string `json:"price_id"`
	UseBalance int64  `json:"use_balance"`
	CouponCode string `json:"coupon_code"`
}

func (h *handlers) createOrder(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create order: missing idempotency claim"),
		))
		return
	}
	if err := middleware.ValidateIdempotencyClaim(
		claim, p.TenantID, p.UserID, billing.CheckoutIdempotencyScope,
	); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	var req createOrderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	fields := map[string]string{}
	if req.PlanID == "" {
		fields["plan_id"] = "必填"
	}
	if req.PriceID == "" {
		fields["price_id"] = "必填"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	out, err := h.d.Billing.CreateOrder(r.Context(), p.TenantID, billing.CreateOrderInput{
		UserID:     p.UserID,
		PlanID:     req.PlanID,
		PriceID:    req.PriceID,
		UseBalance: req.UseBalance,
		CouponCode: req.CouponCode,
		Claim:      claim,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.WritePrepared(w, out.PreparedResponse())
}

//------------------------------------------------------------------------------
// 支付
//------------------------------------------------------------------------------

type payOrderReq struct {
	Provider  string `json:"provider"`
	Method    string `json:"method"`
	ReturnURL string `json:"return_url"`
}

// payOrderResponse 是收银台跳转信息：http_method + redirect_url（+ form_fields 表单提交）。
type payOrderResponse struct {
	IntentID    string            `json:"intent_id"`
	HTTPMethod  string            `json:"http_method"`
	RedirectURL string            `json:"redirect_url"`
	FormFields  map[string]string `json:"form_fields"`
	Amount      int64             `json:"amount"`
	Currency    string            `json:"currency"`
	Reused      bool              `json:"reused"`
}

// payOrder 为订单创建支付意图，返回收银台跳转信息。
func (h *handlers) payOrder(w http.ResponseWriter, r *http.Request) {
	orderID := chi.URLParam(r, "id")

	var req payOrderReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Provider == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"provider": "必填"}))
		return
	}

	out, err := h.d.Payments.CreatePaymentIntent(r.Context(),
		httpx.TenantIDFrom(r.Context()), billing.CreateIntentInput{
			OrderID:      orderID,
			UserID:       httpx.PrincipalFrom(r.Context()).UserID,
			ProviderCode: req.Provider,
			Method:       req.Method,
			ClientIP:     httpx.ClientIP(r),
			ReturnURL:    req.ReturnURL,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	httpx.Created(w, payOrderResponse{
		IntentID:    out.IntentID,
		HTTPMethod:  out.HTTPMethod,
		RedirectURL: out.RedirectURL,
		FormFields:  out.FormFields,
		Amount:      out.Amount,
		Currency:    out.Currency,
		Reused:      out.Reused,
	})
}

// paymentWebhook 接收支付渠道回调。
//
// 本函数刻意不认识任何具体渠道：解析与验签委托给该渠道的 Adapter（PAY-002），
// 业务处理统一进 HandlePaymentWebhook（PAY-003 幂等 + PAY-005 记账），
// 回执格式与 HTTP 状态再交回 Adapter 决定 —— 各渠道对此的要求差异极大。
// 新接一个渠道，这里一行都不用改。
func (h *handlers) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerCode := chi.URLParam(r, "provider")
	tenantID := httpx.TenantIDFrom(ctx)
	reqID := httpx.RequestIDFrom(ctx)

	parsed, err := h.d.Payments.ParseNotification(ctx, tenantID, providerCode, r)
	if err != nil {
		// 连适配器都找不到或报文无法解析：没有可用的回执格式，只能走通用错误响应
		h.d.Log.Warn("支付回调无法解析",
			"provider", providerCode, "request_id", reqID, "error", err.Error())
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	ack := payment.AckInput{SignatureValid: parsed.Input.SignatureVerified}

	if !ack.SignatureValid {
		h.d.Log.Warn("支付回调验签失败",
			"provider", providerCode,
			"out_trade_no", parsed.Notification.OutTradeNo,
			"request_id", reqID)
	} else {
		out, herr := h.d.Billing.HandlePaymentWebhook(ctx, tenantID, parsed.Input)
		if herr != nil {
			// 幂等由 payment_events 唯一约束保证，渠道重推不会造成重复发放
			ack.HandlerError = herr
			h.d.Log.Error("支付回调处理失败",
				"provider", providerCode,
				"out_trade_no", parsed.Notification.OutTradeNo,
				"request_id", reqID, "error", herr.Error())
		} else {
			ack.Processed = out.Processed
			ack.AlreadyHandled = out.AlreadyHandled
			ack.PaymentID = out.PaymentID
			ack.SubscriptionID = out.SubscriptionID
			ack.LedgerTxnID = out.LedgerTxnID
			if out.SignatureFailed {
				// 领域层二次验签失败：事件已以 ignored 状态留库，
				// 这里向渠道返回未授权，避免重推。
				ack.HandlerError = httpx.New(httpx.CodeUnauthorized,
					"payment signature verification failed")
			}
			h.d.Log.Info("支付回调已处理",
				"provider", providerCode,
				"out_trade_no", parsed.Notification.OutTradeNo,
				"processed", out.Processed,
				"already_handled", out.AlreadyHandled,
				"signature_failed", out.SignatureFailed,
				"subscription_id", out.SubscriptionID,
				"request_id", reqID)
		}
	}

	resp := parsed.Provider.NotificationAck(ack)
	w.Header().Set("Content-Type", resp.ContentType)
	w.WriteHeader(resp.HTTPStatus)
	_, _ = w.Write(resp.Body)
}

// previewCoupon 下单前试算优惠码。
func (h *handlers) previewCoupon(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req struct {
		PlanID     string `json:"plan_id"`
		PriceID    string `json:"price_id"`
		PackID     string `json:"pack_id"`
		CouponCode string `json:"coupon_code"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 两种互斥形态：{plan_id, price_id} 试算套餐，{pack_id} 试算流量包。
	// 空串或不是 UUID 的值会一路传到 SQL 的 uuid 列上，
	// 在那里报出的是类型错误 —— 对用户显示成「服务暂时不可用」，
	// 而实际上只是参数没填对
	fields := map[string]string{}
	if req.PackID != "" {
		if !isUUID(req.PackID) {
			fields["pack_id"] = "必填"
		}
		if req.PlanID != "" || req.PriceID != "" {
			fields["pack_id"] = "流量包与套餐只能二选一"
		}
	} else {
		if !isUUID(req.PlanID) {
			fields["plan_id"] = "必填"
		}
		if !isUUID(req.PriceID) {
			fields["price_id"] = "必填"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(fields))
		return
	}

	var out map[string]any
	var err error
	if req.PackID != "" {
		out, err = h.d.Billing.PreviewForTrafficPack(r.Context(), p.TenantID, p.UserID,
			req.CouponCode, req.PackID)
	} else {
		out, err = h.d.Billing.PreviewForPrice(r.Context(), p.TenantID, p.UserID,
			req.CouponCode, req.PlanID, req.PriceID)
	}
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// isUUID 粗查一个字符串是否是 UUID 形状。
//
// 不用 uuid 库解析：这里只是想在进 SQL 之前挡掉明显不对的输入，
// 真正的合法性由数据库的 uuid 类型保证。
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

type siteConfigResponse struct {
	RegistrationMode  string `json:"registration_mode"`
	EmailVerification bool   `json:"email_verification"`
}

// siteConfig 返回渲染登录/注册页需要的站点开关。
//
// 前端必须先知道要不要验证邮箱，才能决定注册表单长什么样。
// 靠调用注册接口去试探是本末倒置 —— 那会在用户还没提交时
// 就先建一条注册会话。
func (h *handlers) siteConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	policy, err := h.d.Identity.RegistrationPolicy(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, siteConfigResponse{
		RegistrationMode:  policy.Mode,
		EmailVerification: policy.EmailVerification,
	})
}

// changePassword 让用户自助改密。
//
// 改完会把这个用户的其它会话全部踢掉，所以调用方拿到成功之后
// 手上的令牌仍然有效（当前会话不在吊销范围内），别的设备则需要重新登录。
func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.OldPassword == "" || req.NewPassword == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"old_password": "当前密码必填",
			"new_password": "新密码必填",
		}))
		return
	}

	if err := h.d.Identity.ChangePassword(r.Context(), p.TenantID, identity.ChangePasswordInput{
		UserID:      p.UserID,
		OldPassword: req.OldPassword,
		NewPassword: req.NewPassword,
		APIDomain:   "public",
		// 按设计保留当前会话：其它会话与 refresh 令牌照样吊销
		KeepSessionID: p.SessionID,
		IP:            httpx.ClientIP(r),
		UserAgent:     r.UserAgent(),
	}); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, okResponse{OK: true})
}

//------------------------------------------------------------------------------
// 余额
//------------------------------------------------------------------------------

// balanceResponse 的 history 是 billing 按行拼好的流水，原样透传。
type balanceResponse struct {
	Balance  int64            `json:"balance"`
	Currency string           `json:"currency"`
	History  []map[string]any `json:"history"`
}

func (h *handlers) myBalance(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	const currency = "CNY"
	amount, err := h.d.Billing.BalanceOf(r.Context(), p.TenantID, p.UserID, currency)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	history, err := h.d.Billing.ListBalanceHistory(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, balanceResponse{Balance: amount, Currency: currency, History: history})
}

// createTopup 建一张充值订单，之后走与买套餐相同的支付流程。
func (h *handlers) createTopup(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create topup: missing idempotency claim"),
		))
		return
	}
	if err := middleware.ValidateIdempotencyClaim(
		claim, p.TenantID, p.UserID, billing.TopupIdempotencyScope,
	); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	var req struct {
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Billing.CreateTopup(r.Context(), p.TenantID, billing.CreateTopupInput{
		UserID: p.UserID, Amount: req.Amount, Currency: req.Currency, Claim: claim,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

// createRenewal 为已有订阅续费。
//
// 与新购分开一个接口，是因为两者的输入本来就不同：
// 新购要选套餐，续费只需要指明续哪一条订阅。
func (h *handlers) createRenewal(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(
			errors.New("create renewal: missing idempotency claim"),
		))
		return
	}
	if err := middleware.ValidateIdempotencyClaim(
		claim, p.TenantID, p.UserID, billing.RenewalIdempotencyScope,
	); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	subID := chi.URLParam(r, "id")
	var req struct {
		PriceID    string `json:"price_id"`
		UseBalance int64  `json:"use_balance"`
		CouponCode string `json:"coupon_code"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Billing.CreateRenewal(r.Context(), p.TenantID, billing.CreateRenewalInput{
		UserID: p.UserID, SubscriptionID: subID, PriceID: req.PriceID,
		UseBalance: req.UseBalance, CouponCode: req.CouponCode, Claim: claim,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

type announcementsResponse struct {
	Announcements []notify.Announcement `json:"announcements"`
}

// myAnnouncements 返回当前用户可见的公告。
func (h *handlers) myAnnouncements(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	list, err := h.d.Notify.VisibleAnnouncements(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, announcementsResponse{Announcements: list})
}
