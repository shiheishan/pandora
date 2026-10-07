package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/domain/payment/demo"
	"github.com/aegispanel/aegis/internal/domain/payment/epay"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// PaymentService 负责渠道适配器的装配与支付意图的生命周期。
//
// 与 settlement.go 的分工：本文件只管「怎么把用户送到收银台」和
// 「怎么把渠道回调翻译成平台事件」；钱一旦确认收到，记账与订阅激活
// 全部交回 HandlePaymentWebhook —— 那条路径不认识任何具体渠道。
type PaymentService struct {
	pool    *db.Pool
	env     *crypto.Envelope
	factory *payment.Factory

	// PublicBaseURL 用于拼 notify_url / return_url，必须是渠道能回访到的公网地址
	publicBaseURL string
	devMode       bool
	masterKey     []byte

	// settle 是进程共用的结算服务：主动查单查到已付，交它走与回调同一条
	// HandlePaymentWebhook。必须是装配时注入的那一个，而不是临时 NewService ——
	// 后者没有挂节点通知，补记履约后节点要等下一轮轮询才知道新用户。
	settle *Service
}

func NewPaymentService(settle *Service, pool *db.Pool, env *crypto.Envelope, masterKey []byte, publicBaseURL string, devMode bool) *PaymentService {
	s := &PaymentService{
		pool:          pool,
		env:           env,
		masterKey:     masterKey,
		publicBaseURL: strings.TrimRight(publicBaseURL, "/"),
		devMode:       devMode,
		settle:        settle,
	}

	s.factory = payment.NewFactory(s.loadProvider, 5*time.Minute)

	// 登记易支付适配器。新增渠道只需在这里多注册一行，
	// 订单、账本、订阅的代码一行都不用动（PAY-002 验收）。
	s.factory.RegisterAdapter("epay", func(rec payment.ProviderRecord) (payment.Provider, error) {
		return epay.New(epay.Config{
			Code:          rec.Code,
			BaseURL:       payment.ConfigString(rec.Config, "base_url"),
			MerchantID:    rec.Credentials.MerchantID,
			Key:           rec.Credentials.Key,
			SubmitPath:    payment.ConfigString(rec.Config, "submit_path"),
			APIPath:       payment.ConfigString(rec.Config, "api_path"),
			DefaultMethod: payment.ConfigString(rec.Config, "default_method"),
			// 仅开发环境允许内网/HTTP 渠道地址，生产恒为 false（SEC-007）
			AllowPrivateHost: devMode && payment.ConfigBool(rec.Config, "allow_private_host"),
		})
	})

	// 参考适配器：HMAC-SHA256 验签 + JSON 回执。
	// 供集成测试与本地联调使用，也是新渠道接入的最小实现范例。
	s.factory.RegisterAdapter("demo_hmac", func(rec payment.ProviderRecord) (payment.Provider, error) {
		secret := []byte(rec.Credentials.Key)
		if len(secret) == 0 {
			// 未单独配置密钥时回落到主密钥，与该渠道的历史行为保持一致
			secret = s.masterKey
		}
		return demo.New(demo.Config{
			Code:        rec.Code,
			Secret:      secret,
			CheckoutURL: payment.ConfigString(rec.Config, "checkout_url"),
		})
	})

	return s
}

func (s *PaymentService) Factory() *payment.Factory { return s.factory }

// providerFor 取渠道实例，并把「渠道被停用」翻译成可展示的业务错误。
// 停用是管理员的有意动作，照普通 error 往上抛会变成 500（缺陷 19）。
func (s *PaymentService) providerFor(ctx context.Context, tenantID, code string) (payment.Provider, *payment.ProviderRecord, error) {
	prov, rec, err := s.factory.Get(ctx, tenantID, code)
	return prov, rec, providerLookupError(err)
}

func providerLookupError(err error) error {
	if errors.Is(err, payment.ErrProviderDisabled) {
		return httpx.New(httpx.CodeUnavailable, "该支付渠道已停用，请更换其他支付方式")
	}
	return err
}

// loadProvider 从库里读渠道并解密凭据。
func (s *PaymentService) loadProvider(ctx context.Context, tenantID, code string) (*payment.ProviderRecord, error) {
	var (
		rec        payment.ProviderRecord
		sealed     []byte
		configRaw  []byte
		currencies []string
	)

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, code, adapter, display_name, enabled, accepting_new,
			       coalesce(supported_currencies, '{}'), coalesce(config, '{}'::jsonb),
			       credentials_encrypted
			  FROM payment_providers
			 WHERE tenant_id = $1 AND code = $2`,
			tenantID, code,
		).Scan(&rec.ID, &rec.Code, &rec.Adapter, &rec.DisplayName,
			&rec.Enabled, &rec.AcceptingNew, &currencies, &configRaw, &sealed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.New(httpx.CodeNotFound, "未知的支付渠道")
	}
	if err != nil {
		return nil, err
	}

	rec.Currencies = currencies
	if len(configRaw) > 0 {
		_ = json.Unmarshal(configRaw, &rec.Config)
	}

	if len(sealed) > 0 {
		// AAD 绑定渠道 ID：密文即使被搬到另一条渠道记录上也解不开
		plain, err := s.env.Open(sealed, []byte("payment_provider:"+rec.ID))
		if err != nil {
			return nil, fmt.Errorf("渠道 %q 凭据解密失败: %w", code, err)
		}
		rec.Credentials, err = payment.DecodeCredentials(plain)
		if err != nil {
			return nil, fmt.Errorf("渠道 %q: %w", code, err)
		}
	}

	return &rec, nil
}

//------------------------------------------------------------------------------
// 创建支付意图
//------------------------------------------------------------------------------

type CreateIntentInput struct {
	OrderID      string
	UserID       string
	ProviderCode string
	Method       string
	ClientIP     string
	ReturnURL    string
}

type CreateIntentOutput struct {
	IntentID    string
	HTTPMethod  string
	RedirectURL string
	FormFields  map[string]string
	Amount      int64
	Currency    string
	// Reused 为 true 表示复用了已存在的在途意图，而不是新建
	Reused bool
}

// CreatePaymentIntent 为订单创建一次支付尝试并返回收银台跳转信息。
func (s *PaymentService) CreatePaymentIntent(ctx context.Context, tenantID string, in CreateIntentInput) (*CreateIntentOutput, error) {
	prov, rec, err := s.providerFor(ctx, tenantID, in.ProviderCode)
	if err != nil {
		return nil, err
	}

	// PAY-009：渠道故障时停止创建新支付，但不影响既有支付的查询与回调
	if !rec.AcceptingNew {
		return nil, httpx.New(httpx.CodeUnavailable, "该支付渠道暂停收单，请稍后再试或更换渠道")
	}

	// 所选方式必须在渠道配置的方式里；没选时规整成渠道默认，
	// 复用在途意图时按规整后的方式比对（见下方「复用在途意图」）
	method, err := resolvePaymentMethod(rec.Config, in.Method)
	if err != nil {
		return nil, err
	}
	// 易支付只出支付宝和微信（用户 2026-10-07 定）：渠道配置里一种可用方式都没有时
	// （例如存量配置只勾了 QQ 钱包），不交给渠道默认值兜底，直接拒绝
	if rec.Adapter == "epay" && method == "" && len(providerMethods(rec.Config)) == 0 &&
		payment.ConfigString(rec.Config, "default_method") != "" {
		return nil, httpx.Invalid(map[string]string{"method": "该渠道没有可用的支付方式"})
	}

	var (
		out       CreateIntentOutput
		orderNo   string
		subject   string
		payable   int64
		currency  string
		expiresAt time.Time
	)

	scope := db.Scope{TenantID: tenantID, ActorID: in.UserID}

	err = s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		var status string
		var ownerID string
		var orderExpires *time.Time

		err := tx.QueryRow(ctx, `
			SELECT order_no, user_id, status, currency, payable_amount, expires_at
			  FROM orders
			 WHERE tenant_id = $1 AND id = $2
			 FOR UPDATE`,
			tenantID, in.OrderID,
		).Scan(&orderNo, &ownerID, &status, &currency, &payable, &orderExpires)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}

		// 越权检查与不存在返回同一种错误，避免用订单 ID 探测他人订单
		if ownerID != in.UserID {
			return httpx.NotFoundOrForbidden()
		}
		if status != "draft" && status != "pending_payment" {
			return httpx.New(httpx.CodeConflict, "该订单当前状态不可支付")
		}
		if payable <= 0 {
			return httpx.New(httpx.CodeConflict, "该订单无需外部支付")
		}

		// --- 复用在途意图 ---
		// payment_intents 上有部分唯一索引保证一个订单只允许一个未终结意图。
		// 用户连点两次「去支付」应该回到同一个收银台，而不是产生两笔待付款。
		var existingID, existingStatus string
		var existingPayload []byte
		var samePro bool
		err = tx.QueryRow(ctx, `
			SELECT id, status, action_payload, provider_id = $3
			  FROM payment_intents
			 WHERE tenant_id = $1 AND order_id = $2
			   AND status IN ('created', 'requires_action', 'processing')
			 LIMIT 1`,
			tenantID, in.OrderID, rec.ID,
		).Scan(&existingID, &existingStatus, &existingPayload, &samePro)

		if err == nil {
			// 同渠道、同方式才复用；换渠道或换方式（先支付宝、退出、再选微信）时
			// 先作废旧意图再重建，否则用户拿到的还是上一次的收银台。
			// 方式记在 action_payload.method；此前建的意图没有这个键，按「方式不同」重建。
			if samePro && len(existingPayload) > 0 {
				var payload struct {
					HTTPMethod  string            `json:"http_method"`
					RedirectURL string            `json:"redirect_url"`
					FormFields  map[string]string `json:"form_fields"`
					Method      *string           `json:"method"`
				}
				if json.Unmarshal(existingPayload, &payload) == nil && payload.RedirectURL != "" &&
					payload.Method != nil && *payload.Method == method {
					out = CreateIntentOutput{
						IntentID:    existingID,
						HTTPMethod:  payload.HTTPMethod,
						RedirectURL: payload.RedirectURL,
						FormFields:  payload.FormFields,
						Amount:      payable,
						Currency:    currency,
						Reused:      true,
					}
					return nil
				}
			}

			// 换渠道、换方式或旧意图无跳转信息：作废后重建
			if _, err := tx.Exec(ctx,
				`UPDATE payment_intents SET status = 'cancelled' WHERE id = $1`,
				existingID); err != nil {
				return err
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// 商品名用订单首行的快照名，保证收银台展示与下单时一致
		_ = tx.QueryRow(ctx, `
			SELECT coalesce(snapshot_plan_name, snapshot_product_name)
			  FROM order_items WHERE order_id = $1 ORDER BY created_at LIMIT 1`,
			in.OrderID).Scan(&subject)
		if subject == "" {
			subject = "订阅服务"
		}

		expiresAt = time.Now().Add(30 * time.Minute)
		if orderExpires != nil && orderExpires.Before(expiresAt) {
			expiresAt = *orderExpires
		}

		// --- 调渠道生成收银台 ---
		// out_trade_no 用对外短单号而非内部 UUID：
		// 渠道后台、对账单、客服沟通全用它，且不泄露内部 ID 规模（DATA-001）。
		// 同一订单再次发起支付（换方式、换渠道、旧意图作废）时用「订单号-序号」：很多易支付
		// 站点拒绝重复的商户单号，而且旧单号那一笔作废后仍可能被付，两笔必须分得开。
		outTradeNo, err := nextOutTradeNo(ctx, tx, tenantID, in.OrderID, orderNo)
		if err != nil {
			return err
		}
		resp, err := prov.CreatePayment(ctx, payment.CreateRequest{
			OutTradeNo: outTradeNo,
			Amount:     payable,
			Currency:   currency,
			Subject:    subject,
			Method:     method,
			NotifyURL:  s.notifyURL(in.ProviderCode),
			ReturnURL:  s.returnURL(in.ReturnURL),
			ClientIP:   in.ClientIP,
		})
		if err != nil {
			return fmt.Errorf("渠道创建支付失败: %w", err)
		}

		payload, _ := json.Marshal(map[string]any{
			"http_method":  resp.HTTPMethod,
			"redirect_url": resp.RedirectURL,
			"form_fields":  resp.FormFields,
			"method":       method,
			"out_trade_no": outTradeNo,
		})
		// provider_ref 记这笔支付在渠道侧的标识：渠道建单时没给（易支付就不给），就记对外单号。
		// 回调与查单先按它定位到意图与订单（(tenant, provider, provider_ref) 唯一）
		providerRef := resp.ProviderRef
		if providerRef == "" {
			providerRef = outTradeNo
		}

		var intentID string
		err = tx.QueryRow(ctx, `
			INSERT INTO payment_intents
				(tenant_id, order_id, provider_id, currency, amount,
				 status, provider_ref, action_payload, expires_at)
			VALUES ($1, $2, $3, $4, $5, 'requires_action', $6, $7, $8)
			RETURNING id`,
			tenantID, in.OrderID, rec.ID, currency, payable,
			providerRef, payload, expiresAt,
		).Scan(&intentID)
		if err != nil {
			return err
		}

		// 订单从 draft 推进到 pending_payment，表示已进入支付流程
		if status == "draft" {
			if _, err := tx.Exec(ctx,
				`UPDATE orders SET status = 'pending_payment' WHERE tenant_id = $1 AND id = $2`,
				tenantID, in.OrderID); err != nil {
				return err
			}
		}

		out = CreateIntentOutput{
			IntentID:    intentID,
			HTTPMethod:  resp.HTTPMethod,
			RedirectURL: resp.RedirectURL,
			FormFields:  resp.FormFields,
			Amount:      payable,
			Currency:    currency,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &out, nil
}

// nextOutTradeNo 给订单的下一次支付取对外单号：第一次就是订单号（与改动之前一致），
// 之后是「订单号-序号」，序号 = 这张订单已有的意图数 + 1。调用方已锁住订单行，同一订单的
// 建意图排成一队，序号不会撞。
func nextOutTradeNo(ctx context.Context, tx pgx.Tx, tenantID, orderID, orderNo string) (string, error) {
	var prior int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM payment_intents WHERE tenant_id = $1 AND order_id = $2`,
		tenantID, orderID).Scan(&prior); err != nil {
		return "", err
	}
	if prior == 0 {
		return orderNo, nil
	}
	return fmt.Sprintf("%s-%d", orderNo, prior+1), nil
}

func (s *PaymentService) notifyURL(providerCode string) string {
	return s.publicBaseURL + "/v1/webhooks/payments/" + providerCode
}

func (s *PaymentService) returnURL(userSupplied string) string {
	// 只接受本站地址，防止把用户重定向到外部站点
	if userSupplied != "" && strings.HasPrefix(userSupplied, s.publicBaseURL+"/") {
		return userSupplied
	}
	// 用 #orders 而不是 /orders：用户门户是单页应用，只在根路径上有页面，
	// /orders 这种路径会走到后端路由，返回一段 JSON 404 ——
	// 而这是用户付完钱看到的最后一个页面。
	return s.publicBaseURL + "/#orders"
}

//------------------------------------------------------------------------------
// 回调解析
//------------------------------------------------------------------------------

// ParsedNotification 是回调经适配器归一化后的结果，交给 HandlePaymentWebhook 消费。
type ParsedNotification struct {
	Provider     payment.Provider
	Notification *payment.Notification
	Input        PaymentWebhookInput
}

// ParseNotification 找到渠道适配器并解析回调。
func (s *PaymentService) ParseNotification(ctx context.Context, tenantID, providerCode string, r *http.Request) (*ParsedNotification, error) {
	prov, rec, err := s.providerFor(ctx, tenantID, providerCode)
	if err != nil {
		return nil, err
	}

	n, err := prov.ParseNotification(ctx, r)
	if err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "回调格式无法解析")
	}
	// 先按 provider_ref 找到发起这笔支付的意图与订单：换过方式的订单，回调带的是
	// 「订单号-序号」，按订单号找不到。找不到（这个改动之前建的意图）再按订单号定位
	orderID := n.OrderID
	if orderID == "" && n.OutTradeNo != "" {
		if orderID, err = s.orderIDByProviderRef(ctx, tenantID, rec.ID, n.OutTradeNo); err != nil {
			return nil, err
		}
	}

	eventType := "payment.unknown"
	switch n.Status {
	case payment.StatusSucceeded:
		eventType = "payment.succeeded"
	case payment.StatusFailed:
		eventType = "payment.failed"
	case payment.StatusRefunded:
		eventType = "payment.refunded"
	}

	raw := make(map[string]any, len(n.Raw))
	for k, v := range n.Raw {
		// 签名值本身不必入库：它对排查无用，却是密钥强度的旁证
		if k == "sign" {
			continue
		}
		raw[k] = v
	}

	return &ParsedNotification{
		Provider:     prov,
		Notification: n,
		Input: PaymentWebhookInput{
			ProviderCode:      providerCode,
			ProviderEventID:   n.EventID,
			ProviderPaymentID: n.PaymentRef,
			EventType:         eventType,
			OrderID:           orderID,
			OrderNo:           n.OutTradeNo,
			Amount:            n.Amount,
			FeeAmount:         n.FeeAmount,
			Currency:          n.Currency,
			RawPayload:        raw,
			SignatureVerified: n.SignatureVerified,
		},
	}, nil
}

// orderIDByProviderRef 按渠道与 provider_ref 找意图所属的订单；没有这样的意图返回空串。
func (s *PaymentService) orderIDByProviderRef(ctx context.Context, tenantID, providerID, ref string) (string, error) {
	var orderID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT order_id::text FROM payment_intents
			 WHERE tenant_id = $1 AND provider_id = $2::uuid AND provider_ref = $3`,
			tenantID, providerID, ref).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return orderID, err
}
