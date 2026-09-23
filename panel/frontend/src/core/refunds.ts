import { z } from "zod";
import { ApiClient, ApiFailure, failure } from "./api";
import { enc } from "./data";
import { parseMinor } from "./numbers";

const unsigned = z
  .string()
  .refine(
    (value) =>
      /^(0|[1-9]\d*)$/.test(value) && BigInt(value) <= 9223372036854775807n,
  );
const positive = unsigned.refine((value) => /^[1-9]\d*$/.test(value));
export const entitlementActions = [
  "retain",
  "revoke_order_subscription",
] as const;
const allocationSchema = z.object({
  source_kind: z.enum(["payment", "balance"]),
  source_id: z.uuid(),
  amount_minor: positive,
});
export const refundPreviewSchema = z
  .object({
    order_id: z.uuid(),
    currency: z.string().length(3),
    paid_amount_minor: unsigned,
    refunded_amount_minor: unsigned,
    reserved_amount_minor: unsigned,
    available_amount_minor: unsigned,
    policy_version: z.literal("refund-policy-v1"),
    allowed_entitlement_actions: z.array(z.enum(entitlementActions)),
    manual_reason: z.string().optional(),
    requires_second_reviewer: z.boolean().optional(),
    sources: z.array(
      z.object({
        source_kind: z.enum(["payment", "balance"]),
        source_id: z.uuid(),
        provider_code: z.string().nullable().optional(),
        available_amount_minor: unsigned,
      }),
    ),
  })
  .passthrough();
export type RefundPreview = z.infer<typeof refundPreviewSchema>;
export const refundSchema = z
  .object({
    operation_id: z.uuid(),
    order_id: z.uuid(),
    requested_by: z.uuid(),
    currency: z.string().length(3),
    amount_minor: positive,
    status: z.string().min(1),
    version: positive,
    entitlement_action: z.enum(entitlementActions),
    manual_reason: z.string().nullable().optional(),
    requires_second_reviewer: z.boolean().optional(),
    legs: z.array(
      z.object({
        id: z.uuid(),
        source_kind: z.enum(["payment", "balance"]),
        source_id: z.uuid(),
        amount_minor: positive,
        status: z.string().min(1),
        provider_refund_id: z.string().nullable().optional(),
        ledger_transaction_id: z.string().nullable().optional(),
        financial_state: z
          .enum(["awaiting_evidence", "provider_succeeded", "ledger_succeeded"])
          .optional(),
        external_result: z
          .object({
            provider_refund_id: z.string(),
            source_kind: z.string(),
            recorded_at: z.string(),
          })
          .nullable()
          .optional(),
      }),
    ),
  })
  .passthrough();
export type Refund = z.infer<typeof refundSchema>;
const reasonSchema = z
  .string()
  .trim()
  .refine(
    (value) =>
      Array.from(value).length >= 5 && Array.from(value).length <= 2000,
    "请填写5至2000个字符的原因",
  );
const requestSchema = z.object({
  operation_id: z.uuid(),
  amount_minor: positive,
  entitlement_action: z.enum(entitlementActions),
  reason: reasonSchema,
  allocations: z.array(allocationSchema).min(1).max(32),
});
const reviewSchema = z.object({
  action_id: z.uuid(),
  expected_version: positive,
  decision: z.enum(["approve", "reject"]),
  reason: reasonSchema,
});
const executeSchema = z.object({
  action_id: z.uuid(),
  expected_version: positive,
});
const externalSchema = executeSchema.extend({
  leg_id: z.uuid(),
  provider_refund_id: z.string().trim().min(1).max(256),
  amount_minor: positive,
  currency: z.string().length(3),
  evidence_reference: z
    .string()
    .trim()
    .refine(
      (value) =>
        Array.from(value).length >= 8 && Array.from(value).length <= 2000,
      "请填写8至2000个字符的可复核凭证位置",
    ),
  evidence_sha256: z
    .string()
    .regex(/^[0-9a-f]{64}$/, "请选取凭证文件计算校验值，或填写64位小写SHA256"),
});
const storedSchema = z
  .object({
    id: z.uuid(),
    subject: z.uuid(),
    orderId: z.uuid(),
    currency: z.string().length(3),
    operationId: z.uuid(),
    state: z.enum(["frozen", "unknown", "confirmed", "rejected"]),
    error: z.string().optional(),
    created: z.number(),
  })
  .and(
    z.discriminatedUnion("kind", [
      z.object({ kind: z.literal("request"), payload: requestSchema }),
      z.object({ kind: z.literal("review"), payload: reviewSchema }),
      z.object({ kind: z.literal("execute"), payload: executeSchema }),
      z.object({ kind: z.literal("external"), payload: externalSchema }),
    ]),
  );
type Stored = z.infer<typeof storedSchema>;
const pending = new Map<string, Promise<Refund>>();
const canonicalAllocations = (items: Array<z.infer<typeof allocationSchema>>) =>
  items
    .map((item) => `${item.source_kind}:${item.source_id}:${item.amount_minor}`)
    .sort()
    .join("|");

export const refundPolicyLabels: Record<string, string> = {
  retain: "保留订阅权益",
  revoke_order_subscription: "撤销此订单创建的订阅",
};
export function refundReason(value: unknown): string {
  const labels: Record<string, string> = {
    topup_wallet_recovery_requires_review:
      "充值退款需要核对用户余额回收，须人工处理",
    historical_renewal_entitlement_requires_review:
      "历史续费涉及其他权益来源，须人工核对",
    commission_reversal_requires_review: "此订单包含佣金，须先核对佣金冲回",
    order_subscription_source_missing: "尚未找到此订单创建的订阅",
    subscription_has_other_paid_sources: "订阅还有其他付费来源，不能直接撤销",
    provider_refund_unsupported: "渠道不支持自动退款，需人工处理",
  };
  return typeof value === "string" && value
    ? labels[value] || `需要人工核对（${value}）`
    : "";
}
export function canApproveRefund(refund: Refund, subject: string): boolean {
  return (
    refund.status === "pending_review" &&
    (refund.requested_by !== subject ||
      refund.requires_second_reviewer === false)
  );
}
export function canExecuteRefund(refund: Refund): boolean {
  return refund.status === "approved";
}
export function canRecordRefundEvidence(
  refund: Refund,
  leg: Refund["legs"][number],
): boolean {
  return (
    ["processing", "unknown", "manual_required"].includes(refund.status) &&
    leg.source_kind === "payment" &&
    leg.financial_state !== "ledger_succeeded" &&
    !leg.ledger_transaction_id &&
    !leg.external_result
  );
}
export function refundFinancialState(leg: Refund["legs"][number]): string {
  if (
    leg.source_kind === "balance" &&
    leg.financial_state === "awaiting_evidence"
  )
    return "等待原站内余额账本处理";
  const labels = {
    awaiting_evidence: "尚无渠道到账凭据",
    provider_succeeded: "渠道已退款，本地入账待核对",
    ledger_succeeded: "本地账本已入账",
  };
  return leg.financial_state
    ? labels[leg.financial_state]
    : "尚未返回财务分态，请核对账本";
}
export function refundResultMessage(refund: Refund): string {
  if (
    refund.status === "succeeded" &&
    refund.legs.every((leg) => leg.financial_state === "ledger_succeeded")
  )
    return "退款已完成，本地账本已入账";
  if (refund.legs.some((leg) => leg.financial_state === "provider_succeeded"))
    return "渠道退款凭据已保存，本地入账仍待核对";
  if (refund.status === "processing")
    return "退款已进入持久队列，等待渠道和账本处理";
  if (refund.status === "manual_required")
    return (
      refundReason(refund.manual_reason) || "退款需要人工核对，尚未确认完成"
    );
  return "已收到操作结果，请核对退款详情和账本分态";
}
export async function hashRefundEvidence(file: File): Promise<string> {
  if (file.size > 32 * 1024 * 1024)
    throw new Error(
      "凭证超过32MB，请选取较小的原始凭证文件，或使用高级校验值输入",
    );
  const digest = await crypto.subtle.digest(
    "SHA-256",
    await file.arrayBuffer(),
  );
  return Array.from(new Uint8Array(digest), (byte) =>
    byte.toString(16).padStart(2, "0"),
  ).join("");
}

export function validateRefundAllocation(
  preview: RefundPreview,
  amountText: string,
  policy: string,
  values: Record<string, unknown>,
) {
  const amount = parseMinor(amountText, { zero: false });
  if (
    !preview.allowed_entitlement_actions.includes(
      policy as (typeof entitlementActions)[number],
    )
  )
    throw new Error("退款策略不在本次预览允许范围内");
  if (BigInt(amount) > BigInt(preview.available_amount_minor))
    throw new Error("申请金额超过当前未占用的可退额度");
  if (
    policy === "revoke_order_subscription" &&
    BigInt(amount) !==
      BigInt(preview.paid_amount_minor) - BigInt(preview.refunded_amount_minor)
  )
    throw new Error("撤销订阅必须退还此订单全部剩余已收资金");
  const allocations = preview.sources.flatMap((source) => {
    const raw = values[`${source.source_kind}:${source.source_id}`];
    if (
      raw == null ||
      String(raw).trim() === "" ||
      String(raw).trim() === "0" ||
      String(raw).trim() === "0.00"
    )
      return [];
    const minor = parseMinor(String(raw), { zero: false });
    if (BigInt(minor) > BigInt(source.available_amount_minor))
      throw new Error("某一来源的分配超过该来源可退额度");
    return [
      {
        source_kind: source.source_kind,
        source_id: source.source_id,
        amount_minor: minor,
      },
    ];
  });
  if (
    allocations.length === 0 ||
    allocations.length > 32 ||
    allocations.reduce((sum, item) => sum + BigInt(item.amount_minor), 0n) !==
      BigInt(amount)
  )
    throw new Error("请明确分配退款来源，各项金额之和必须等于申请金额");
  return {
    amount_minor: amount,
    entitlement_action: policy as (typeof entitlementActions)[number],
    allocations,
  };
}

/** Frozen financial commands survive same-tab refresh and never generate a replacement identity implicitly. */
export class RefundCommand {
  private constructor(public saved: Stored) {
    if (saved.kind === "request") {
      saved.payload.allocations.forEach(Object.freeze);
      Object.freeze(saved.payload.allocations);
    }
    Object.freeze(saved.payload);
  }
  private key() {
    return `pandora:refund:${this.saved.subject}:${this.saved.id}`;
  }
  private persist() {
    sessionStorage.setItem(this.key(), JSON.stringify(this.saved));
  }
  private matches(value: Refund): boolean {
    const saved = this.saved;
    if (
      value.operation_id !== saved.operationId ||
      value.order_id !== saved.orderId ||
      value.currency !== saved.currency
    )
      return false;
    if (saved.kind === "external")
      return value.legs.some(
        (leg) =>
          leg.id === saved.payload.leg_id &&
          leg.source_kind === "payment" &&
          leg.amount_minor === saved.payload.amount_minor,
      );
    if (saved.kind !== "request") return true;
    return (
      value.requested_by === saved.subject &&
      value.amount_minor === saved.payload.amount_minor &&
      value.entitlement_action === saved.payload.entitlement_action &&
      canonicalAllocations(value.legs) ===
        canonicalAllocations(saved.payload.allocations)
    );
  }
  static request(
    subject: string,
    orderId: string,
    values: Omit<z.infer<typeof requestSchema>, "operation_id">,
    currency: string,
  ) {
    const id = crypto.randomUUID();
    const saved = storedSchema.parse({
      id,
      subject,
      orderId,
      currency,
      operationId: id,
      kind: "request",
      payload: { ...values, operation_id: id },
      state: "frozen",
      created: Date.now(),
    });
    const command = new RefundCommand(saved);
    command.persist();
    return command;
  }
  static review(
    subject: string,
    refund: Refund,
    decision: "approve" | "reject",
    reason: string,
  ) {
    const id = crypto.randomUUID();
    const saved = storedSchema.parse({
      id,
      subject,
      orderId: refund.order_id,
      currency: refund.currency,
      operationId: refund.operation_id,
      kind: "review",
      payload: {
        action_id: id,
        expected_version: refund.version,
        decision,
        reason,
      },
      state: "frozen",
      created: Date.now(),
    });
    const command = new RefundCommand(saved);
    command.persist();
    return command;
  }
  static restore(subject: string): RefundCommand[] {
    const result: RefundCommand[] = [];
    try {
      for (let index = 0; index < sessionStorage.length; index++) {
        const key = sessionStorage.key(index);
        if (!key?.startsWith(`pandora:refund:${subject}:`)) continue;
        let raw: unknown;
        try {
          raw = JSON.parse(sessionStorage.getItem(key) || "null");
        } catch {
          continue;
        }
        const parsed = storedSchema.safeParse(raw);
        if (!parsed.success) continue;
        const value = parsed.data;
        if (
          value.subject !== subject ||
          (value.kind === "request"
            ? value.id !== value.operationId ||
              value.id !== value.payload.operation_id
            : value.id !== value.payload.action_id)
        )
          continue;
        result.push(new RefundCommand(value));
      }
    } catch {
      /* A storage failure never fabricates a new intent. */
    }
    return result.sort((a, b) => b.saved.created - a.saved.created);
  }
  static execute(subject: string, refund: Refund) {
    if (!canExecuteRefund(refund))
      throw new Error(
        "只有已批准的申请可以进入退款队列；结果不明不能再次自动退款",
      );
    return RefundCommand.action(subject, refund, "execute", {});
  }
  static external(
    subject: string,
    refund: Refund,
    legId: string,
    values: Pick<
      z.infer<typeof externalSchema>,
      "provider_refund_id" | "evidence_reference" | "evidence_sha256"
    >,
  ) {
    const leg = refund.legs.find((item) => item.id === legId);
    if (!leg || !canRecordRefundEvidence(refund, leg))
      throw new Error("只可登记尚待核对的原支付渠道凭证，站内余额须由账本处理");
    return RefundCommand.action(subject, refund, "external", {
      ...values,
      leg_id: leg.id,
      amount_minor: leg.amount_minor,
      currency: refund.currency,
    });
  }
  private static action(
    subject: string,
    refund: Refund,
    kind: "execute" | "external",
    values: Record<string, unknown>,
  ) {
    const id = crypto.randomUUID();
    const saved = storedSchema.parse({
      id,
      subject,
      orderId: refund.order_id,
      currency: refund.currency,
      operationId: refund.operation_id,
      kind,
      payload: { ...values, action_id: id, expected_version: refund.version },
      state: "frozen",
      created: Date.now(),
    });
    const command = new RefundCommand(saved);
    command.persist();
    return command;
  }
  discardRejected() {
    if (this.saved.state !== "rejected")
      throw new Error("只能移除明确拒绝的未完成请求；未知结果必须先核对");
    sessionStorage.removeItem(this.key());
  }
  async query(api: ApiClient) {
    const value = await api.request(
      `v1/refunds/${enc(this.saved.operationId)}`,
      refundSchema.refine(
        (result) => this.matches(result),
        "退款结果与冻结意图不一致",
      ),
    );
    if (this.saved.kind === "request") {
      this.saved.state = "confirmed";
      try {
        this.persist();
      } catch {
        /* The known ID still supports recovery. */
      }
    }
    return value;
  }
  send(api: ApiClient, verifiedQueueState?: Refund): Promise<Refund> {
    const existing = pending.get(this.key());
    if (existing) return existing;
    if (
      this.saved.kind === "execute" &&
      this.saved.state !== "frozen" &&
      !(
        this.saved.state === "unknown" &&
        verifiedQueueState &&
        canExecuteRefund(verifiedQueueState) &&
        this.matches(verifiedQueueState)
      )
    )
      return Promise.reject(
        new Error(
          "执行回执未确认时须先查询；只有查询仍为已批准，才可按原操作重试入队。渠道结果不明不能再次自动退款。",
        ),
      );
    if (this.saved.state === "rejected")
      return Promise.reject(
        new Error("此请求已明确拒绝，请移除记录后重新预览，不复用旧操作号"),
      );
    const previousState = this.saved.state;
    try {
      // Persist uncertainty before execute IO: tab-crash recovery requires a fresh query before exact action replay.
      if (this.saved.kind === "execute") this.saved.state = "unknown";
      this.persist();
    } catch {
      this.saved.state = previousState;
      return Promise.reject(
        new Error("无法保存资金恢复信息，本次尚未发送；请允许会话存储后重试"),
      );
    }
    const path =
      this.saved.kind === "request"
        ? `v1/orders/${enc(this.saved.orderId)}/refunds`
        : `v1/refunds/${enc(this.saved.operationId)}/${this.saved.kind === "external" ? "record-external-result" : this.saved.kind}`;
    const promise = api
      .request(
        path,
        refundSchema.refine(
          (value) => this.matches(value),
          "退款结果与冻结意图不一致",
        ),
        {
          method: "POST",
          body: this.saved.payload,
          idempotencyKey: this.saved.id,
        },
      )
      .then((value) => {
        this.saved.state = "confirmed";
        this.saved.error = undefined;
        try {
          this.persist();
        } catch {
          /* Confirmed server result is retained in memory. */
        }
        return value;
      })
      .catch((error) => {
        const parsed = failure(error);
        const uncertain =
          parsed.uncertain || parsed.code?.includes("idempotency");
        this.saved.state = uncertain ? "unknown" : "rejected";
        this.saved.error = parsed.message;
        try {
          this.persist();
        } catch {
          /* Original frozen record remains. */
        }
        if (uncertain)
          throw new ApiFailure(
            this.saved.kind === "execute"
              ? "执行结果待核实。操作号已保留；先查询，若仍为已批准可按原操作重试入队。渠道结果不明不能再次自动退款。"
              : this.saved.kind === "external"
                ? "凭据登记或本地入账结果待核实。原凭据和操作号已保留；可查询，或以原参数重试本地结算，不会再次请求渠道退款。"
                : "结果待核实。资金意图和操作号已保留；只可查询或重放原请求。",
            parsed.kind,
            parsed.status,
            parsed.code,
          );
        throw error;
      })
      .finally(() => {
        pending.delete(this.key());
      });
    pending.set(this.key(), promise);
    return promise;
  }
}
