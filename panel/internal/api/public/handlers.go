// [INPUT]: 依赖 domain 的 billing/identity/payment/support 用例，依赖 platform 的 db/httpx/crypto/realtime 与 middleware
// [OUTPUT]: 对外提供 handlers 的核心门户处理器：探针、注册登录登出、me、改密、站点配置、套餐目录、续费、优惠码试算、下单支付与回调、工单、钱包、邀请佣金与提现、我的公告；包内 isUUID
// [POS]: api/public 的主处理器文件，其余按模块拆在同包兄弟文件里；套餐目录取当前发布版本的额度、限速（R99）与重置策略
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

type handlers struct{ d Deps }

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

// health 是存活探针：只报告进程还在，不碰任何依赖。
// SEC-006 要求「公网无法获得堆栈和详细健康依赖」，所以这里不暴露组件明细。
func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, map[string]string{"status": "ok"})
}

// ready 是就绪探针：检查依赖，但对外只回 ok / unavailable 两种结果。
func (h *handlers) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := timeoutCtx(r, 3*time.Second)
	defer cancel()

	if err := h.d.Pool.Ping(ctx); err != nil {
		h.d.Log.Error("就绪检查失败：数据库不可达", "error", err.Error())
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	if err := h.d.Redis.Ping(ctx).Err(); err != nil {
		h.d.Log.Error("就绪检查失败：缓存不可达", "error", err.Error())
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.OK(w, map[string]string{"status": "ok"})
}

//------------------------------------------------------------------------------
// 认证
//------------------------------------------------------------------------------

type registerStartReq struct {
	Email      string `json:"email"`
	InviteCode string `json:"invite_code"`
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

	resp := map[string]any{
		"registration_token": out.RegistrationToken,
		// 前端据此决定要不要显示验证码输入框，不该自己猜
		"verification_required": out.VerificationRequired,
		"expires_at":            out.ExpiresAt.UTC().Format(time.RFC3339),
		// 注意：无论邮箱是否已注册，响应结构与内容完全一致（IAM-006）
		"message": "若该邮箱可用于注册，验证码已发送",
	}
	if out.DevCode != "" {
		resp["dev_code"] = out.DevCode
	}
	httpx.OK(w, resp)
}

type registerCompleteReq struct {
	RegistrationToken string `json:"registration_token"`
	Code              string `json:"code"`
	Password          string `json:"password"`
	// Deprecated compatibility field. Invite authorization is immutable at
	// registration start and this value is intentionally ignored.
	InviteCode string `json:"invite_code"`
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
	httpx.Created(w, map[string]any{"user_id": out.UserID, "email": out.Email})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
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

	httpx.OK(w, map[string]any{
		"access_token":  out.AccessToken,
		"refresh_token": out.RefreshToken,
		"token_type":    "Bearer",
		"expires_in":    out.ExpiresIn,
		"user_id":       out.UserID,
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

//------------------------------------------------------------------------------
// 目录与账户
//------------------------------------------------------------------------------

func (h *handlers) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenantID := httpx.TenantIDFrom(ctx)
	authed := !httpx.PrincipalFrom(ctx).IsAnonymous()
	// 分组套餐要知道「现在是谁在看」。未登录传 nil，
	// SQL 里那句 $3::uuid IS NOT NULL 会把整个分组分支短路掉
	var viewerID any
	if p := httpx.PrincipalFrom(ctx); p != nil && p.UserID != "" {
		viewerID = p.UserID
	}

	type priceView struct {
		ID            string `json:"id"`
		Currency      string `json:"currency"`
		UnitAmount    int64  `json:"unit_amount"`
		Interval      string `json:"billing_interval"`
		IntervalCount int16  `json:"interval_count"`
		TrialDays     int16  `json:"trial_days"`
	}
	// 套餐能给多少流量、几台设备，是用户选购时唯一真正关心的事。
	// 这些值来自当前 plan_version 的 quota_definitions，
	// 不能在前端写死 —— 换套餐版本时展示必须跟着变（SUB-002）。
	type quotaView struct {
		Metric string `json:"metric"`
		Limit  *int64 `json:"limit"`
		Unit   string `json:"unit"`
		Period string `json:"period"`
	}
	type planView struct {
		ID          string  `json:"id"`
		Code        string  `json:"code"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Version     *int    `json:"version"`
		MaxDevices  *int    `json:"max_devices"`
		// 限速（kbps）全程生效，null = 不限速，与超额策略无关（R99）
		ThrottleKbps *int `json:"throttle_kbps"`
		// 卡片上的「每月 1 日重置」与续费 / 变更按钮要这几项，取当前发布版本与套餐开关
		QuotaResetStrategy string      `json:"quota_reset_strategy"`
		QuotaResetDay      *int16      `json:"quota_reset_day"`
		AllowRenewal       bool        `json:"allow_renewal"`
		AllowUpgrade       bool        `json:"allow_upgrade"`
		Quotas             []quotaView `json:"quotas"`
		Prices             []priceView `json:"prices"`
	}

	out := []planView{}

	err := h.d.Pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// XBD-011 可见性：匿名只看 public，登录后加 authenticated。
		// group / invite_only / hidden 一律不在此列出，
		// 且下单路径会独立复查，列表接口的过滤不是唯一防线。
		visibilities := []string{"public"}
		if authed {
			visibilities = append(visibilities, "authenticated")
		}
		// visibility='group' 的套餐由下面的 SQL 单独判断：
		// 它对组内的人可见，对其他人连存在都不该暴露

		rows, err := tx.Query(ctx, `
			SELECT p.id, p.code, p.name, p.description, pv.version, pv.max_devices, pv.throttle_kbps,
			       pv.quota_reset_strategy, pv.quota_reset_day, p.allow_renewal, p.allow_upgrade
			  FROM plans p
			  JOIN plan_versions pv ON pv.tenant_id = p.tenant_id
			   AND pv.id = p.current_version_id
			   AND pv.status = 'published' AND pv.frozen_at IS NOT NULL
			 WHERE p.tenant_id = $1
			   AND p.status = 'active'
			   AND (
			     p.visibility = ANY($2)
			     OR (p.visibility = 'group' AND $3::uuid IS NOT NULL
			         AND EXISTS (SELECT 1 FROM users u
			                      WHERE u.tenant_id = p.tenant_id
			                        AND u.id = $3::uuid
			                        AND u.user_group_id = ANY(p.visible_group_ids)))
			   )
			   AND p.current_version_id IS NOT NULL
			   AND (p.visible_from  IS NULL OR p.visible_from  <= now())
			   AND (p.visible_until IS NULL OR p.visible_until >  now())
			   AND EXISTS (
			     SELECT 1 FROM prices offer
			      WHERE offer.tenant_id=p.tenant_id AND offer.product_id=p.product_id
			        AND offer.status='active' AND offer.currency IN ('CNY','USD')
			        AND (offer.valid_from IS NULL OR offer.valid_from<=now())
			        AND (offer.valid_until IS NULL OR offer.valid_until>now())
			        AND (offer.user_group_id IS NULL OR ($3::uuid IS NOT NULL AND EXISTS (
			          SELECT 1 FROM users price_viewer
			           WHERE price_viewer.tenant_id=p.tenant_id AND price_viewer.id=$3::uuid
			             AND price_viewer.user_group_id=offer.user_group_id)))
			   )
			 ORDER BY p.sort_order, p.created_at`,
			tenantID, visibilities, viewerID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var pv planView
			if err := rows.Scan(&pv.ID, &pv.Code, &pv.Name, &pv.Description, &pv.Version, &pv.MaxDevices, &pv.ThrottleKbps,
				&pv.QuotaResetStrategy, &pv.QuotaResetDay, &pv.AllowRenewal, &pv.AllowUpgrade); err != nil {
				return err
			}
			pv.Prices = []priceView{}
			pv.Quotas = []quotaView{}
			out = append(out, pv)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		for i := range out {
			// 配额跟随该套餐的当前版本：换版本时展示自动跟着变（SUB-002）
			qrows, err := tx.Query(ctx, `
				SELECT qd.metric, qd.limit_value, qd.unit, qd.period
				  FROM quota_definitions qd
				  JOIN plans pl ON pl.current_version_id = qd.plan_version_id
				 WHERE qd.tenant_id = $1 AND pl.id = $2
				 ORDER BY qd.metric`,
				tenantID, out[i].ID)
			if err != nil {
				return err
			}
			for qrows.Next() {
				var q quotaView
				if err := qrows.Scan(&q.Metric, &q.Limit, &q.Unit, &q.Period); err != nil {
					qrows.Close()
					return err
				}
				out[i].Quotas = append(out[i].Quotas, q)
			}
			qrows.Close()
			if err := qrows.Err(); err != nil {
				return err
			}

			prows, err := tx.Query(ctx, `
				SELECT pr.id, pr.currency, pr.unit_amount, pr.billing_interval,
				       pr.interval_count, pr.trial_days
				  FROM prices pr
				  JOIN plans pl ON pl.product_id = pr.product_id
				 WHERE pr.tenant_id = $1 AND pl.id = $2 AND pr.status = 'active'
				   AND pr.currency IN ('CNY','USD')
				   AND (pr.valid_from  IS NULL OR pr.valid_from  <= now())
				   AND (pr.valid_until IS NULL OR pr.valid_until >  now())
				   AND (
				     pr.user_group_id IS NULL
				     OR ($3::uuid IS NOT NULL AND EXISTS (
				       SELECT 1 FROM users u
				        WHERE u.tenant_id = pr.tenant_id
				          AND u.id = $3::uuid
				          AND u.user_group_id = pr.user_group_id
				     ))
				   )
				 ORDER BY pr.unit_amount`,
				tenantID, out[i].ID, viewerID)
			if err != nil {
				return err
			}
			for prows.Next() {
				var v priceView
				if err := prows.Scan(&v.ID, &v.Currency, &v.UnitAmount, &v.Interval,
					&v.IntervalCount, &v.TrialDays); err != nil {
					prows.Close()
					return err
				}
				out[i].Prices = append(out[i].Prices, v)
			}
			prows.Close()
			if err := prows.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, map[string]any{"plans": out})
}

func (h *handlers) me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.PrincipalFrom(ctx)

	var (
		email       string
		displayName *string
		status      string
		createdAt   time.Time
	)
	err := h.d.Pool.InTx(ctx, db.Scope{TenantID: p.TenantID, ActorID: p.UserID},
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT email, display_name, status, created_at
				   FROM users WHERE tenant_id = $1 AND id = $2`,
				p.TenantID, p.UserID).Scan(&email, &displayName, &status, &createdAt)
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, map[string]any{
		"user_id":      p.UserID,
		"email":        email,
		"display_name": displayName,
		"status":       status,
		"created_at":   createdAt.UTC().Format(time.RFC3339),
		"permissions":  p.Permissions,
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
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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

	httpx.Created(w, map[string]any{
		"intent_id":    out.IntentID,
		"http_method":  out.HTTPMethod,
		"redirect_url": out.RedirectURL,
		"form_fields":  out.FormFields,
		"amount":       out.Amount,
		"currency":     out.Currency,
		"reused":       out.Reused,
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

//------------------------------------------------------------------------------
// 工单（OPS-001）
//------------------------------------------------------------------------------

func (h *handlers) listTicketCategories(w http.ResponseWriter, r *http.Request) {
	type cat struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	// 固定顺序输出：map 遍历顺序随机，会让前端下拉框每次刷新都换位置
	order := []string{"general", "technical", "subscription", "billing", "account", "abuse"}
	out := make([]cat, 0, len(order))
	for _, c := range order {
		if n, ok := support.Categories[c]; ok {
			out = append(out, cat{Code: c, Name: n})
		}
	}
	httpx.OK(w, map[string]any{"categories": out})
}

type createTicketReq struct {
	Subject  string `json:"subject"`
	Category string `json:"category"`
	Body     string `json:"body"`
	OrderID  string `json:"order_id"`
}

func (h *handlers) createTicket(w http.ResponseWriter, r *http.Request) {
	var req createTicketReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support ticket claim missing")))
		return
	}
	out, err := h.d.Support.CreateAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		support.CreateInput{
			UserID:   httpx.PrincipalFrom(r.Context()).UserID,
			Subject:  req.Subject,
			Category: req.Category,
			Body:     req.Body,
			OrderID:  req.OrderID,
		}, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

func (h *handlers) listTickets(w http.ResponseWriter, r *http.Request) {
	ts, err := h.d.Support.ListForUser(r.Context(),
		httpx.TenantIDFrom(r.Context()), httpx.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"tickets": ts})
}

func (h *handlers) getTicket(w http.ResponseWriter, r *http.Request) {
	t, err := h.d.Support.GetForUser(r.Context(),
		httpx.TenantIDFrom(r.Context()), httpx.PrincipalFrom(r.Context()).UserID,
		chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, t)
}

type ticketReplyReq struct {
	Body string `json:"body"`
}

func (h *handlers) replyTicket(w http.ResponseWriter, r *http.Request) {
	var req ticketReplyReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	ticketID := chi.URLParam(r, "id")
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support reply claim missing")))
		return
	}
	out, err := h.d.Support.ReplyAsUserAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		p.UserID, ticketID, req.Body, claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 推给自己：同一个人可能开着多个标签页或换了设备，
	// 一处发言其它几处应当立刻跟上
	h.publishTicket(r.Context(), p.TenantID, p.UserID, ticketID)
	httpx.WritePrepared(w, out.PreparedResponse())
}

func (h *handlers) closeTicket(w http.ResponseWriter, r *http.Request) {
	claim, ok := middleware.IdempotencyClaimFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("support close claim missing")))
		return
	}
	out, err := h.d.Support.CloseByUserAtomic(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, chi.URLParam(r, "id"), claim)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.WritePrepared(w, out.PreparedResponse())
}

// publishTicket 推一条工单变更通知。
//
// 只带 ticket_id，不带内容 —— 前端收到后重新拉该工单，
// 走的是既有的鉴权路径，不必在推送这条链路上再做一遍权限判断。
func (h *handlers) publishTicket(ctx context.Context, tenantID, userID, ticketID string) {
	if h.d.Realtime == nil {
		return
	}
	h.d.Realtime.Publish(ctx, realtime.ChannelUser(tenantID, userID),
		"ticket.updated", map[string]any{"ticket_id": ticketID})
}

// myInviteCode 返回当前用户的邀请码与已邀请人数。
func (h *handlers) myInviteCode(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	sum, err := h.d.Identity.MyInviteCode(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	list, err := h.d.Identity.ListInvitees(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"invite": sum, "invitees": list})
}

// previewCoupon 下单前试算优惠码。
func (h *handlers) previewCoupon(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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

//------------------------------------------------------------------------------
// 分销佣金
//------------------------------------------------------------------------------

func (h *handlers) myCommission(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	sum, err := h.d.Billing.CommissionSummary(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	entries, err := h.d.Billing.ListMyCommissions(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	wds, err := h.d.Billing.ListMyWithdrawals(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	transfers, err := h.d.Billing.ListMyCommissionTransfers(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"summary": sum, "entries": entries, "withdrawals": wds, "transfers": transfers,
	})
}

func (h *handlers) requestWithdrawal(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	var req struct {
		Amount int64  `json:"amount"`
		Payout string `json:"payout_detail"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Payout == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
			"payout_detail": "请填写收款方式"}))
		return
	}
	id, err := h.d.Billing.RequestWithdrawal(r.Context(), p.TenantID, p.UserID,
		req.Amount, req.Payout)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"id": id})
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
	httpx.OK(w, map[string]any{
		"registration_mode":  policy.Mode,
		"email_verification": policy.EmailVerification,
	})
}

// changePassword 让用户自助改密。
//
// 改完会把这个用户的其它会话全部踢掉，所以调用方拿到成功之后
// 手上的令牌仍然有效（当前会话不在吊销范围内），别的设备则需要重新登录。
func (h *handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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
	httpx.OK(w, map[string]any{"ok": true})
}

//------------------------------------------------------------------------------
// 余额
//------------------------------------------------------------------------------

func (h *handlers) myBalance(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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
	httpx.OK(w, map[string]any{
		"balance": amount, "currency": currency, "history": history,
	})
}

// createTopup 建一张充值订单，之后走与买套餐相同的支付流程。
func (h *handlers) createTopup(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
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

// myAnnouncements 返回当前用户可见的公告。
func (h *handlers) myAnnouncements(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	list, err := h.d.Notify.VisibleAnnouncements(r.Context(), p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"announcements": list})
}
