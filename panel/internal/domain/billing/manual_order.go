package billing

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 管理员的两个订单动作（XBD-015）：
//
//   1. 人工单   —— 直接给用户开一张已履约的订单（赠送、补偿、线下成交）
//   2. 标记已支付 —— 用户线下付了钱（银行转账、当面收款），把待支付订单结掉
//
// 两个动作都不另起炉灶：
//   人工单复用 CreateOrder（赠送：全额减免 → payable 0 → 既有零元单捕获直接履约；
//   线下已收款：建单后在同一事务里按 offline 渠道结清）；
//   标记已支付复用 HandlePaymentWebhook（合成一笔 offline 渠道的收款）。
//
// 这样订阅开通、配额、优惠券核销、佣金计提、账本分录的行为，
// 和用户自己在线支付完全一致 —— 不会出现"赠送的订阅少了个字段"这类问题。

// OfflineProviderCode 是线下收款用的渠道标识。
// 它在 payment_providers 里 accepting_new=false，用户结账时选不到，
// 只有管理员标记已支付或人工开单「线下已收款」时才会用到。
const OfflineProviderCode = "offline"

// 人工单的结算方式。
const (
	ManualSettlementGrant   = "grant"   // 赠送：全额减免、当场履约（缺省）
	ManualSettlementPending = "pending" // 待用户支付：建一张待支付单交给用户去付
	ManualSettlementOffline = "offline" // 线下已收款：建单并按线下渠道当场结清
)

// OfflineReceipt 是一笔线下收款的凭据：凭证号与收款说明。
type OfflineReceipt struct {
	Reference string // 线下凭证号：银行流水号、收据编号之类
	Reason    string
}

func validateOfflineReference(ref string) error {
	if ref == "" || utf8.RuneCountInString(ref) > 128 {
		return httpx.Invalid(map[string]string{
			"reference": "请填写线下凭证号（银行流水号、收据编号等），最多 128 字"})
	}
	return nil
}

// offlinePaymentInput 把一笔线下收款合成为 offline 渠道的一次成功回调。
// 标记已支付与人工单「线下已收款」共用它，两条路记账口径因此完全相同。
//
// provider_payment_id 用凭证号：渠道侧的唯一约束因此变成
// 「同一张凭证不能入账两次」，是这个场景真正需要的幂等口径。
// SignatureVerified 直接给 true —— 这条路的「签名」是管理员的 RBAC 权限
// 加近期重认证，不是渠道密钥。
func offlinePaymentInput(orderID, currency string, payable int64, actorID string,
	receipt OfflineReceipt) PaymentWebhookInput {
	return PaymentWebhookInput{
		ProviderCode:      OfflineProviderCode,
		ProviderEventID:   "offline:" + orderID + ":" + receipt.Reference,
		ProviderPaymentID: "offline:" + receipt.Reference,
		EventType:         "payment.succeeded",
		OrderID:           orderID,
		Amount:            payable,
		Currency:          currency,
		FeeAmount:         0,
		SignatureVerified: true,
		// payment_events.raw_payload 非空。真实渠道往里塞的是回调原文；
		// 线下收款没有回调，就把「谁在什么依据下确认了这笔钱」记进去 ——
		// 日后查这笔账时，这是唯一能追到人的线索。
		RawPayload: map[string]any{
			"source":    "admin_offline",
			"actor_id":  actorID,
			"reference": receipt.Reference,
			"reason":    receipt.Reason,
		},
	}
}

type CreateManualOrderInput struct {
	UserID  string
	PlanID  string
	PriceID string
	Reason  string
	ActorID string
	// Settlement 为空按 grant。「线下已收款」（offline）直接记收入并触发
	// 佣金，与标记已支付同门槛：路由挂近期重认证（R64）。「从余额扣除」
	// （balance，D-C-3 未决）暂不接受。
	Settlement string
	// Reference 是线下凭证号，仅 offline 必填，别的结算方式忽略。
	Reference string
	Claim     middleware.IdempotencyClaim
	// Target 是这单落到哪一份（ManualOrderOptions 给出的选项之一）；选项只有一个时可以不传，
	// 多于一个而没带回 422「请选择这单落到哪一份」
	Target *purchase.Choice
	// EntrySubscriptionID 是后台从哪一行订阅点进来的（「给这份开单」），只影响 preview 的默认值
	EntrySubscriptionID string
}

func (s *Service) CreateManualOrder(ctx context.Context, tenantID string,
	in CreateManualOrderInput) (*CreateOrderOutput, error) {

	in.Reason = strings.TrimSpace(in.Reason)
	if tenantID == "" || in.UserID == "" || in.PlanID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "缺少租户、操作人、用户或套餐")
	}
	for _, id := range []string{in.UserID, in.PlanID, in.ActorID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
		}
	}
	// 库里的 CHECK 只要求 5 个字节，这里按字数卡：
	// 「补偿」两个汉字是 6 字节，能过 CHECK 却说明不了任何事。
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.Invalid(map[string]string{
			"reason": "请写清开单原因，5 到 500 个字。这条会进审计，是日后对账的唯一依据"})
	}
	var offline *OfflineReceipt
	switch in.Settlement {
	case "":
		in.Settlement = ManualSettlementGrant
	case ManualSettlementGrant, ManualSettlementPending:
	case ManualSettlementOffline:
		in.Reference = strings.TrimSpace(in.Reference)
		if err := validateOfflineReference(in.Reference); err != nil {
			return nil, err
		}
		offline = &OfflineReceipt{Reference: in.Reference, Reason: in.Reason}
	case "balance":
		return nil, httpx.Invalid(map[string]string{
			"settlement": "暂不支持从余额扣除；需要时请先调账，再用赠送开单"})
	default:
		return nil, httpx.Invalid(map[string]string{"settlement": "结算方式只能是 grant、pending 或 offline"})
	}

	// 落点由人选（购买模型统一，设计稿 2.3）：Target 必须是 ManualOrderOptions 给出的选项之一，
	// 只有一个选项时可以不传。按选项分派：renew 在那一份上开续费单（规则 3，不新开、不换链接）；
	// change 在那一份上开变更单（剩余价值与门户改套餐同一口径，赠送单新价算 0 元、剩余价值全额
	// 退进余额）；new 另开一份。续费与变更单的审计在建单事务里写。
	opt, err := s.manualPlacement(ctx, tenantID, in)
	if err != nil {
		return nil, err
	}
	switch opt.Kind {
	case purchase.KindRenew:
		if in.PriceID == "" {
			return nil, httpx.Invalid(map[string]string{"price_id": "请选择续费的价格档"})
		}
		return s.CreateRenewal(ctx, tenantID, CreateRenewalInput{
			UserID: in.UserID, SubscriptionID: opt.SubscriptionID, PriceID: in.PriceID,
			Claim:       in.Claim,
			ManualGrant: in.Settlement == ManualSettlementGrant, ManualReason: in.Reason,
			ManualActor: in.ActorID, Offline: offline, ManualSettlement: in.Settlement,
		})
	case purchase.KindChange:
		if in.PriceID == "" {
			return nil, httpx.Invalid(map[string]string{"price_id": "请选择新套餐的价格档"})
		}
		changed, err := s.CreatePlanChange(ctx, tenantID, PlanChangeInput{
			UserID: in.UserID, SubscriptionID: opt.SubscriptionID, PlanID: in.PlanID, PriceID: in.PriceID,
			Claim:       in.Claim,
			ManualGrant: in.Settlement == ManualSettlementGrant, ManualReason: in.Reason,
			ManualActor: in.ActorID, Offline: offline, ManualSettlement: in.Settlement,
		})
		if err != nil {
			return nil, err
		}
		// 响应体是变更单那一份（多 proration_credit / balance_refund 两项），已写进幂等记录
		return &changed.CreateOrderOutput, nil
	}

	out, err := s.CreateOrder(ctx, tenantID, CreateOrderInput{
		UserID: in.UserID, PlanID: in.PlanID, PriceID: in.PriceID, Claim: in.Claim,
		ManualGrant:  in.Settlement == ManualSettlementGrant,
		ManualReason: in.Reason, ManualActor: in.ActorID,
		Offline: offline,
	})
	if err != nil {
		return nil, err
	}
	digest := map[string]any{
		"order_no": out.OrderNo, "user_id": in.UserID, "plan_id": in.PlanID,
		"reason": in.Reason, "status": out.Status, "settlement": in.Settlement,
		"subtotal": out.TotalAmount + out.DiscountAmount,
		"granted":  out.DiscountAmount,
	}
	if out.settlement != nil {
		digest["reference"] = in.Reference
		digest["amount"] = out.PayableAmount
		digest["currency"] = out.Currency
		digest["payment_id"] = out.settlement.PaymentID
		digest["ledger_txn"] = out.settlement.LedgerTxnID
	}

	// 审计单独一笔事务：CreateOrder 已经提交，这里失败不该把订单回滚掉，
	// 但要留下痕迹 —— 没有审计的人工单等于没人负责。
	actor := in.ActorID
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "order.manual_created", ResourceType: "order",
				ResourceID:  &out.OrderID,
				AfterDigest: digest,
				APIDomain:   "admin", RequestID: httpx.RequestIDFrom(ctx),
			})
		}); err != nil {
		// 订单已经建好并履约了，这里只能如实上报。
		// 吞掉它等于留下一笔没人负责的赠送记录。
		return out, httpx.New(httpx.CodeInternal,
			"人工单 "+out.OrderNo+" 已创建，但审计写入失败，请立即联系运维核对："+err.Error())
	}
	return out, nil
}

type MarkOrderPaidInput struct {
	OrderID   string
	ActorID   string
	Reason    string
	Reference string // 线下凭证号：银行流水号、收据编号之类
}

// MarkOrderPaid 把一张待支付订单按「线下已收款」结清。
//
// 走的是和真实渠道回调同一条结算链路，所以订阅开通、优惠券核销、
// 余额解冻、佣金计提、账本分录一个不少（回调形状见 offlinePaymentInput）。
// 结算时钱若进了挂账（续费 / 变更单的订阅已结束，或订单刚被释放），
// 入账与审计照写，接口回 409 说明款项去向。
func (s *Service) MarkOrderPaid(ctx context.Context, tenantID string,
	in MarkOrderPaidInput) (*PaymentWebhookOutput, error) {

	in.Reason = strings.TrimSpace(in.Reason)
	in.Reference = strings.TrimSpace(in.Reference)
	if tenantID == "" || in.OrderID == "" || in.ActorID == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "缺少租户、操作人或订单")
	}
	if _, err := uuid.Parse(in.OrderID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.Invalid(map[string]string{
			"reason": "请写清收款说明，5 到 500 个字"})
	}
	if err := validateOfflineReference(in.Reference); err != nil {
		return nil, err
	}

	// 读单、结算、审计在同一个事务里（审计报告 2.3 第 4 条）：原先结算提交之后另开
	// 事务写审计，审计写失败时这笔钱已经入账、却查不到是哪个管理员标记的。现在审计
	// 写不进去，整笔回滚，管理员重试即可（同一凭证号由渠道唯一约束去重）。
	var (
		out             PaymentWebhookOutput
		orderNo, status string
		currency        string
		payable         int64
		alreadyHandled  bool
	)
	actor := in.ActorID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		out = PaymentWebhookOutput{}
		alreadyHandled = false
		// 先读订单：金额必须由服务端从库里取，不能让调用方传 ——
		// 否则「标记已支付」就成了一个可以任意填金额的入账接口。
		err := tx.QueryRow(ctx, `
			SELECT order_no, status, currency::text, payable_amount
			  FROM orders WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, in.OrderID).Scan(&orderNo, &status, &currency, &payable)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if status != "pending_payment" && status != "processing" {
			return httpx.New(httpx.CodeConflict,
				"只有待支付的订单可以标记为已支付，当前状态："+status)
		}
		if payable <= 0 {
			return httpx.New(httpx.CodeConflict, "这张订单不需要支付")
		}

		if err := s.settlePaymentTx(ctx, tx, tenantID, offlinePaymentInput(
			in.OrderID, currency, payable, in.ActorID,
			OfflineReceipt{Reference: in.Reference, Reason: in.Reason}), &out); err != nil {
			return err
		}
		// 同一张凭证的事件号已经落过库：上一次这笔钱进了挂账（订单没结，才会又
		// 走到这里），或者并发的另一次标记刚刚结清。这次什么都没有新发生，不写审计。
		if out.AlreadyHandled {
			alreadyHandled = true
			return nil
		}

		digest := map[string]any{
			"order_no": orderNo, "amount": payable, "currency": currency,
			"reference": in.Reference, "reason": in.Reason,
			"payment_id": out.PaymentID, "ledger_txn": out.LedgerTxnID,
		}
		if out.QuarantineKind != "" {
			digest["quarantined"] = out.QuarantineKind
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "order.marked_paid", ResourceType: "order",
			ResourceID:   &in.OrderID,
			BeforeDigest: map[string]any{"status": status},
			AfterDigest:  digest,
			APIDomain:    "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	if alreadyHandled {
		return nil, httpx.New(httpx.CodeConflict,
			"凭证号 "+in.Reference+" 已经入过账，不能重复标记")
	}
	// 事务已提交：确实开了或续了订阅才通知节点（与 HandlePaymentWebhook 同口径）
	if out.SubscriptionID != "" {
		s.notifyUsersChanged(ctx, tenantID)
	}
	// 钱已如实入账，但订单没有结清（R117）：回 409 让管理员知道款项去了挂账，
	// 要在挂账里转入用户余额，而不是以为续费或变更已经生效。
	if out.QuarantineKind != "" {
		return nil, markPaidQuarantined(out.QuarantineKind)
	}
	return &out, nil
}

// markPaidQuarantined 是标记已付的钱进了挂账时回给后台的 409。
func markPaidQuarantined(caseKind string) *httpx.Error {
	if caseKind == "ineligible_subscription" {
		return httpx.New(httpx.CodeConflict,
			"订阅已结束，款项已转入挂账，可在挂账里转入用户余额")
	}
	return httpx.New(httpx.CodeConflict,
		"订单已不在待支付状态，款项已转入挂账，可在挂账里转入用户余额")
}

// ManualOrderPreview 是后台开单「这单落到哪一份」的选项与默认值（DefaultKey 为空表示不预选，
// 提交按钮置灰「先选落点」）。MinPayment 是站点在这个价格档币种上的最低付款额（分，与报价、
// 建单同一个 minPayment：启用且收新单的 CNY 渠道里最小的 min_amount；不限为 0）——
// 只有开单权限、读不到渠道列表的管理员也能在提交前看到「低于最低额」。
type ManualOrderPreview struct {
	Options    []ManualPlacement `json:"options"`
	DefaultKey string            `json:"default_key"`
	MinPayment int64             `json:"min_payment"`
}

// ManualPlacement 是一个落点选项，外加这单按「待用户支付」开时用户要付多少：Due 是应付（分，
// 续一期与另开一份是价格，换套餐先抵 Credit；preview 时刻的数，建单时以那一刻为准），
// BelowMinimum 是它低于站点最低额、用户没法在线付（与建单 422 同一个判定 manualDueBelowMinimum）。
// 赠送与线下已收款不受限，页面只在选了待用户支付时看这两项。
type ManualPlacement struct {
	purchase.Placement
	Due          int64 `json:"due"`
	BelowMinimum bool  `json:"below_minimum"`
}

// ManualOrderOptions 给后台开单 preview：规则同套餐卡（purchase.Options），从订阅行进来
// （entrySub）时预选那一份。只读。
func (s *Service) ManualOrderOptions(ctx context.Context, tenantID, userID, planID, priceID,
	entrySub string) (*ManualOrderPreview, error) {
	for _, id := range []string{userID, planID} {
		if _, err := uuid.Parse(id); err != nil {
			return nil, httpx.New(httpx.CodeBadRequest, "标识符格式不正确")
		}
	}
	fields := map[string]string{}
	if priceID != "" {
		if _, err := uuid.Parse(priceID); err != nil {
			fields["price_id"] = "价格档标识不正确"
		}
	}
	if entrySub != "" {
		if _, err := uuid.Parse(entrySub); err != nil {
			fields["entry_subscription_id"] = "订阅标识不正确"
		}
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}
	out := ManualOrderPreview{Options: []ManualPlacement{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE tenant_id = $1 AND id = $2::uuid)
			AND EXISTS (SELECT 1 FROM plans WHERE tenant_id = $1 AND id = $3::uuid)`,
			tenantID, userID, planID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.NotFoundOrForbidden()
		}
		views, def, err := placementsTx(ctx, tx, tenantID, userID,
			purchase.Offer{Kind: purchase.OfferPlan, PlanID: planID, PriceID: priceID}, entrySub)
		if err != nil {
			return err
		}
		out.DefaultKey = def
		currency, amount, ok, err := manualPreviewPrice(ctx, tx, tenantID, planID, priceID)
		if err != nil {
			return err
		}
		if ok {
			if out.MinPayment, err = minPayment(ctx, tx, tenantID, currency); err != nil {
				return err
			}
		}
		for _, v := range views {
			mp := ManualPlacement{Placement: v}
			if ok {
				credit := int64(0)
				if v.Kind == purchase.KindChange && (v.Currency == "" || v.Currency == currency) {
					credit = v.Credit
				}
				mp.Due = orderTotal(amount, 0, credit, 0)
				mp.BelowMinimum = manualDueBelowMinimum(mp.Due, out.MinPayment)
			}
			out.Options = append(out.Options, mp)
		}
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

// manualPreviewPrice 读开单 preview 所选价格档的币种与单价（只读、不校验在售：校验在建单时做）。
// 没带价格档或它不属于这个套餐时 ok=false，preview 照常给落点，只是不报应付与最低额。
func manualPreviewPrice(ctx context.Context, tx pgx.Tx, tenantID, planID, priceID string) (string, int64, bool, error) {
	if priceID == "" {
		return "", 0, false, nil
	}
	var currency string
	var amount int64
	err := tx.QueryRow(ctx, `
		SELECT pr.currency::text, pr.unit_amount
		  FROM plans p
		  JOIN prices pr ON pr.tenant_id = p.tenant_id AND pr.product_id = p.product_id
		 WHERE p.tenant_id = $1 AND p.id = $2::uuid AND pr.id = $3::uuid`,
		tenantID, planID, priceID).Scan(&currency, &amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return currency, amount, true, nil
}

// manualPlacement 取开单的落点：在一个只读事务里按当前候选校验 Target。多于一个选项而没带
// Target 回 422「请选择这单落到哪一份」；选项变了回 422 要求刷新。续费与变更的建单会锁住
// 那一份再复核状态。
func (s *Service) manualPlacement(ctx context.Context, tenantID string,
	in CreateManualOrderInput) (purchase.Option, error) {
	var opt purchase.Option
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		cands, err := loadCandidatesTx(ctx, tx, tenantID, in.UserID)
		if err != nil {
			return err
		}
		opts, _ := purchase.Options(purchase.Offer{Kind: purchase.OfferPlan, PlanID: in.PlanID}, cands, "")
		o, _, err := purchase.Resolve(in.Target, opts)
		switch {
		case errors.Is(err, purchase.ErrChoiceRequired):
			return httpx.Invalid(map[string]string{"target": "请选择这单落到哪一份"})
		case errors.Is(err, purchase.ErrChoiceStale):
			return httpx.Invalid(map[string]string{"target": err.Error()})
		case err != nil:
			return err
		}
		opt = o
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return purchase.Option{}, he
		}
		return purchase.Option{}, httpx.Internal(err)
	}
	return opt, nil
}
