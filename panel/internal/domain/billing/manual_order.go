// [INPUT]: 依赖 checkout.go 的 CreateOrder（人工单复用下单主路径，线下已收款在建单事务里经 settlement.go 的 settlePaymentTx 结清）与 settlement.go 的 HandlePaymentWebhook（标记已支付合成一笔 offline 渠道回调），依赖 middleware 幂等声明、platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 CreateManualOrder、CreateManualOrderInput、ManualSettlement*、OfflineReceipt、MarkOrderPaid、MarkOrderPaidInput、OfflineProviderCode；包内提供 offlinePaymentInput（两条线下收款路径共用的回调形状）与 markPaidQuarantined（钱进挂账时的 409）
// [POS]: billing 的管理员订单动作：人工单（赠送当场履约、建待支付单交给用户付、线下已收款当场结清）与标记线下已收款；都不另起炉灶，履约与记账与用户自己支付完全一致
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	// 先读订单：金额必须由服务端从库里取，不能让调用方传 ——
	// 否则「标记已支付」就成了一个可以任意填金额的入账接口。
	var orderNo, status, currency string
	var payable int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT order_no, status, currency::text, payable_amount
			  FROM orders WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, in.OrderID).Scan(&orderNo, &status, &currency, &payable)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if status != "pending_payment" && status != "processing" {
		return nil, httpx.New(httpx.CodeConflict,
			"只有待支付的订单可以标记为已支付，当前状态："+status)
	}
	if payable <= 0 {
		return nil, httpx.New(httpx.CodeConflict, "这张订单不需要支付")
	}

	out, err := s.HandlePaymentWebhook(ctx, tenantID, offlinePaymentInput(
		in.OrderID, currency, payable, in.ActorID,
		OfflineReceipt{Reference: in.Reference, Reason: in.Reason}))
	if err != nil {
		return nil, err
	}
	// 同一张凭证的事件号已经落过库：上一次这笔钱进了挂账（订单没结，才会又
	// 走到这里），或者并发的另一次标记刚刚结清。这次什么都没有新发生，不写审计。
	if out.AlreadyHandled {
		return nil, httpx.New(httpx.CodeConflict,
			"凭证号 "+in.Reference+" 已经入过账，不能重复标记")
	}

	digest := map[string]any{
		"order_no": orderNo, "amount": payable, "currency": currency,
		"reference": in.Reference, "reason": in.Reason,
		"payment_id": out.PaymentID, "ledger_txn": out.LedgerTxnID,
	}
	if out.QuarantineKind != "" {
		digest["quarantined"] = out.QuarantineKind
	}
	actor := in.ActorID
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "order.marked_paid", ResourceType: "order",
				ResourceID:   &in.OrderID,
				BeforeDigest: map[string]any{"status": status},
				AfterDigest:  digest,
				APIDomain:    "admin", RequestID: httpx.RequestIDFrom(ctx),
			})
		}); err != nil {
		return out, httpx.New(httpx.CodeInternal,
			"订单 "+orderNo+" 已入账，但审计写入失败，请立即联系运维核对："+err.Error())
	}
	// 钱已如实入账，但订单没有结清（R117）：回 409 让管理员知道款项去了挂账，
	// 要在挂账里转入用户余额，而不是以为续费或变更已经生效。
	if out.QuarantineKind != "" {
		return nil, markPaidQuarantined(out.QuarantineKind)
	}
	return out, nil
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
