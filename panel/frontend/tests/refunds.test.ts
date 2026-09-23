import { describe, expect, it, vi } from "vitest";
import { ApiClient } from "../src/core/api";
import {
  RefundCommand,
  canApproveRefund,
  canExecuteRefund,
  canRecordRefundEvidence,
  hashRefundEvidence,
  refundFinancialState,
  refundResultMessage,
  refundPreviewSchema,
  refundSchema,
  validateRefundAllocation,
  type Refund,
} from "../src/core/refunds";

const actor = "10000000-0000-4000-8000-000000000001";
const other = "10000000-0000-4000-8000-000000000002";
const order = "20000000-0000-4000-8000-000000000001";
const payment = "30000000-0000-4000-8000-000000000001";
const balance = "30000000-0000-4000-8000-000000000002";
const preview = refundPreviewSchema.parse({
  order_id: order,
  currency: "CNY",
  paid_amount_minor: "1000",
  refunded_amount_minor: "0",
  reserved_amount_minor: "0",
  available_amount_minor: "1000",
  policy_version: "refund-policy-v1",
  allowed_entitlement_actions: ["retain", "revoke_order_subscription"],
  sources: [
    {
      source_kind: "payment",
      source_id: payment,
      provider_code: "demo_hmac",
      available_amount_minor: "600",
    },
    {
      source_kind: "balance",
      source_id: balance,
      available_amount_minor: "400",
    },
  ],
});

describe("refund execution and known external evidence", () => {
  it("treats 202 as queued and permits exact queue-action replay only after an approved-state query", async () => {
    const refund = {
      ...result(crypto.randomUUID()),
      status: "approved",
      version: "2",
    };
    const command = RefundCommand.execute(other, refund);
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        response({ ...refund, status: "processing", version: "3" }, 202),
      );
    vi.stubGlobal("fetch", fetcher);
    const queued = await command.send(api());
    expect(refundResultMessage(queued)).toContain("进入持久队列");
    expect(refundResultMessage(queued)).not.toContain("已完成");
    await expect(command.send(api())).rejects.toThrow("不能再次自动退款");
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(JSON.parse(fetcher.mock.calls[0]![1].body)).toEqual({
      action_id: command.saved.id,
      expected_version: "2",
    });
    const second = RefundCommand.execute(other, {
      ...refund,
      operation_id: crypto.randomUUID(),
    });
    fetcher.mockRejectedValueOnce(new TypeError("offline"));
    await expect(second.send(api())).rejects.toThrow("执行结果待核实");
    const restored = RefundCommand.restore(other).find(
      (item) => item.saved.id === second.saved.id,
    )!;
    await expect(restored.send(api())).rejects.toThrow("不能再次自动退款");
    expect(canExecuteRefund({ ...refund, status: "unknown" })).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
    const original = { ...refund, operation_id: second.saved.operationId };
    fetcher.mockResolvedValueOnce(response(original));
    const checked = await restored.query(api());
    fetcher.mockResolvedValueOnce(
      response({ ...original, status: "processing", version: "3" }, 202),
    );
    await restored.send(api(), checked);
    expect(fetcher.mock.calls[1]![1].body).toBe(fetcher.mock.calls[3]![1].body);
    expect(restored.saved.state).toBe("confirmed");
  });
  it("records only exact original payment evidence and replays the same local-settlement action after failure", async () => {
    const refund = {
      ...result(crypto.randomUUID()),
      status: "manual_required",
      version: "3",
    };
    const paymentLeg = refund.legs[0]!;
    const values = {
      provider_refund_id: "fixture-refund-01",
      evidence_reference: "虚构测试凭证目录/已完成记录.pdf",
      evidence_sha256: "a".repeat(64),
    };
    expect(canRecordRefundEvidence(refund, refund.legs[1]!)).toBe(false);
    expect(() =>
      RefundCommand.external(other, refund, refund.legs[1]!.id, values),
    ).toThrow("原支付渠道");
    expect(() =>
      RefundCommand.external(other, refund, paymentLeg.id, {
        ...values,
        evidence_sha256: "BAD",
      }),
    ).toThrow();
    const command = RefundCommand.external(
      other,
      refund,
      paymentLeg.id,
      values,
    );
    const known = {
      ...refund,
      status: "processing",
      version: "4",
      legs: refund.legs.map((leg, index) =>
        index
          ? leg
          : {
              ...leg,
              financial_state: "provider_succeeded",
              external_result: {
                provider_refund_id: "fixture-refund-01",
                source_kind: "manual",
                recorded_at: "2026-09-05T00:00:00Z",
              },
            },
      ),
    };
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        response(
          { error: { message: "ledger failed after evidence committed" } },
          503,
        ),
      )
      .mockResolvedValueOnce(response(known));
    vi.stubGlobal("fetch", fetcher);
    await expect(command.send(api())).rejects.toThrow("原凭据");
    const restored = RefundCommand.restore(other).find(
      (item) => item.saved.id === command.saved.id,
    )!;
    const received = await restored.send(api());
    expect(
      fetcher.mock.calls.every((call) =>
        String(call[0]).endsWith("/record-external-result"),
      ),
    ).toBe(true);
    expect(fetcher.mock.calls[0]![1].body).toBe(fetcher.mock.calls[1]![1].body);
    expect(JSON.parse(fetcher.mock.calls[0]![1].body)).toMatchObject({
      action_id: command.saved.id,
      expected_version: "3",
      leg_id: paymentLeg.id,
      amount_minor: "600",
      currency: "CNY",
    });
    expect(refundFinancialState(received.legs[0]!)).toBe(
      "渠道已退款，本地入账待核对",
    );
    expect(refundResultMessage(received)).not.toContain("已完成");
    expect(canRecordRefundEvidence(received, received.legs[0]!)).toBe(false);
  });
  it("calculates a known local SHA256 without upload and refuses oversized files", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    const file = {
      size: 3,
      arrayBuffer: async () => new TextEncoder().encode("abc").buffer,
    } as File;
    expect(await hashRefundEvidence(file)).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
    await expect(
      hashRefundEvidence({ size: 33 * 1024 * 1024 } as File),
    ).rejects.toThrow("32MB");
    expect(fetcher).not.toHaveBeenCalled();
  });
});
const values = () => ({
  ...validateRefundAllocation(preview, "10.00", "retain", {
    [`payment:${payment}`]: "6.00",
    [`balance:${balance}`]: "4.00",
  }),
  reason: "客户申请退还本次订单款项",
});
const result = (id: string): Refund => ({
  operation_id: id,
  order_id: order,
  requested_by: actor,
  currency: "CNY",
  amount_minor: "1000",
  version: "1",
  status: "pending_review",
  entitlement_action: "retain",
  requires_second_reviewer: true,
  legs: [
    {
      id: "40000000-0000-4000-8000-000000000001",
      source_kind: "payment",
      source_id: payment,
      amount_minor: "600",
      status: "pending",
    },
    {
      id: "40000000-0000-4000-8000-000000000002",
      source_kind: "balance",
      source_id: balance,
      amount_minor: "400",
      status: "pending",
    },
  ],
});
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });
const api = () =>
  new ApiClient(
    "http://localhost/test/",
    () => "test-token",
    () => {},
    async () => true,
  );

describe("explicit refund allocation and review policy", () => {
  it("requires exact source totals and full remaining funding for subscription revocation", () => {
    expect(values().allocations.map((item) => item.amount_minor)).toEqual([
      "600",
      "400",
    ]);
    expect(() =>
      validateRefundAllocation(preview, "10.00", "retain", {}),
    ).toThrow("明确分配");
    expect(() =>
      validateRefundAllocation(preview, "10.00", "retain", {
        [`payment:${payment}`]: "10.00",
      }),
    ).toThrow("超过该来源");
    expect(() =>
      validateRefundAllocation(preview, "6.00", "revoke_order_subscription", {
        [`payment:${payment}`]: "6.00",
      }),
    ).toThrow("全部剩余");
    expect(() =>
      refundSchema.parse({
        ...result(crypto.randomUUID()),
        amount_minor: 1000,
      }),
    ).toThrow();
    expect(() =>
      refundSchema.parse({ ...result(crypto.randomUUID()), version: "01" }),
    ).toThrow();
    expect(
      refundSchema.safeParse({
        ...result(crypto.randomUUID()),
        version: "invalid",
      }).success,
    ).toBe(false);
  });
  it("freezes the server second-reviewer policy and conservatively handles absent policy", () => {
    const refund = result(crypto.randomUUID());
    expect(canApproveRefund(refund, actor)).toBe(false);
    expect(canApproveRefund(refund, other)).toBe(true);
    expect(
      canApproveRefund({ ...refund, requires_second_reviewer: false }, actor),
    ).toBe(true);
    expect(
      canApproveRefund(
        { ...refund, requires_second_reviewer: undefined },
        actor,
      ),
    ).toBe(false);
    expect(canApproveRefund({ ...refund, status: "unknown" }, other)).toBe(
      false,
    );
  });
});

describe("durable frozen refund commands", () => {
  it("coalesces double submit, preserves unknown results and replays exactly after refresh", async () => {
    let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const command = RefundCommand.request(actor, order, values(), "CNY");
    expect(Object.isFrozen(command.saved.payload)).toBe(true);
    const first = command.send(api());
    const second = command.send(api());
    expect(first).toBe(second);
    finish(response({ error: { message: "temporary failure" } }, 503));
    await expect(first).rejects.toThrow("结果待核实");
    expect(RefundCommand.restore(other)).toHaveLength(0);
    const restored = RefundCommand.restore(actor)[0]!;
    expect(restored.saved.state).toBe("unknown");
    fetcher.mockResolvedValueOnce(response(result(command.saved.operationId)));
    await restored.send(api());
    expect(fetcher.mock.calls[0]?.[1].body).toBe(
      fetcher.mock.calls[1]?.[1].body,
    );
    expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("confirmed");
  });
  it("does not turn 404 or malformed successful responses into confirmation or replacement IDs", async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(response({ ok: true }))
      .mockResolvedValueOnce(
        response({ error: { message: "not found" } }, 404),
      );
    vi.stubGlobal("fetch", fetcher);
    const command = RefundCommand.request(actor, order, values(), "CNY");
    const id = command.saved.id;
    await expect(command.send(api())).rejects.toThrow("结果待核实");
    await expect(command.query(api())).rejects.toMatchObject({ status: 404 });
    expect(command.saved.id).toBe(id);
    expect(command.saved.state).toBe("unknown");
    fetcher.mockResolvedValueOnce(response({ ...result(id), currency: "USD" }));
    await expect(command.query(api())).rejects.toMatchObject({
      kind: "contract",
    });
    expect(command.saved.state).toBe("unknown");
    fetcher.mockResolvedValueOnce(response(result(id)));
    await command.query(api());
    expect(command.saved.state).toBe("confirmed");
  });
  it("preserves the exact review action and version; GET alone cannot prove that action was applied", async () => {
    const current = result(crypto.randomUUID());
    const command = RefundCommand.review(
      other,
      current,
      "approve",
      "核对订单与分配来源后批准",
    );
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new TypeError("offline"))
      .mockResolvedValueOnce(
        response({ ...current, version: "2", status: "approved" }),
      )
      .mockResolvedValueOnce(
        response({ ...current, version: "2", status: "approved" }),
      );
    vi.stubGlobal("fetch", fetcher);
    await expect(command.send(api())).rejects.toThrow("结果待核实");
    const restored = RefundCommand.restore(other)[0]!;
    await restored.query(api());
    expect(restored.saved.state).toBe("unknown");
    await restored.send(api());
    expect(fetcher.mock.calls[0]?.[1].body).toBe(
      fetcher.mock.calls[2]?.[1].body,
    );
    expect(restored.saved.state).toBe("confirmed");
  });
  it("does not write if recovery storage cannot be saved and does not let a corrupt record hide other valid records", () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    const save = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementationOnce(() => {
        throw new Error("storage unavailable");
      });
    expect(() => RefundCommand.request(actor, order, values(), "CNY")).toThrow(
      "storage unavailable",
    );
    expect(fetcher).not.toHaveBeenCalled();
    save.mockRestore();
    sessionStorage.setItem(`pandora:refund:${actor}:broken`, "{");
    const command = RefundCommand.request(actor, order, values(), "CNY");
    expect(RefundCommand.restore(actor)[0]?.saved.id).toBe(command.saved.id);
  });
});
