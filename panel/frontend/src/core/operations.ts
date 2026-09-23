import { ApiClient, ApiFailure, failure, type RequestOptions } from "./api";
import type { Row } from "./data";
import { z } from "zod";
import { recordSchema } from "./data";

export type OperationState = "editing" | "submitting" | "unknown" | "completed";
type Stored = {
  id: string;
  kind: string;
  subject: string;
  path: string;
  method: string;
  payload: Row;
  fingerprint: string;
  created: number;
  state: OperationState;
};
const storedSchema = z.object({
  id: z.uuid(),
  kind: z.string(),
  subject: z.string(),
  path: z.union([
    z.string().regex(/^v1\/users\/[^/]+\/balance$/),
    z.literal("v1/orders/manual"),
  ]),
  method: z.literal("POST"),
  payload: recordSchema,
  fingerprint: z.string(),
  created: z.number(),
  state: z.enum(["editing", "submitting", "unknown", "completed"]),
});
const manualPayload = z
  .object({
    user_id: z.uuid(),
    plan_id: z.uuid(),
    price_id: z.uuid(),
    reason: z.string().min(5).max(500),
  })
  .strict();
const adjustmentSchema = z
  .object({
    operation_id: z.uuid(),
    amount_minor: z.string().regex(/^-?\d+$/),
    balance_before_minor: z.string().regex(/^\d+$/),
    balance_after_minor: z.string().regex(/^\d+$/),
    user_id: z.string(),
    actor_id: z.string(),
    currency: z.string(),
    reason: z.string(),
    ledger_transaction_id: z.string(),
    created_at: z.string(),
  })
  .passthrough();
function canonical(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
  if (value && typeof value === "object")
    return (
      "{" +
      Object.entries(value)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, item]) => JSON.stringify(key) + ":" + canonical(item))
        .join(",") +
      "}"
    );
  return JSON.stringify(value) ?? "null";
}
export class Operation {
  id: string = crypto.randomUUID();
  state: OperationState = "editing";
  private fingerprint = "";
  private inFlight: Promise<Row> | null = null;
  private result?: Row;
  constructor(
    public kind: string,
    public subject: string,
    private durable = false,
    private restored?: Stored,
  ) {
    if (restored) {
      this.id = restored.id;
      this.state = "unknown";
      this.fingerprint = restored.fingerprint;
    }
  }
  static restore(
    subject: string,
    kind = "balance-adjustment",
    strict = false,
  ): Operation[] {
    const out: Operation[] = [];
    try {
      for (let index = 0; index < sessionStorage.length; index++) {
        const key = sessionStorage.key(index);
        if (!key?.startsWith(`pandora:operation:${subject}:`)) continue;
        const value: unknown = JSON.parse(
          sessionStorage.getItem(key) || "null",
        );
        const parsed = storedSchema.safeParse(value);
        if (!parsed.success) {
          if (strict) throw new Error("invalid recovery record");
          continue;
        }
        const saved = parsed.data;
        if (saved.kind !== kind) continue;
        if (
          strict &&
          (key !== `pandora:operation:${subject}:${saved.id}` ||
            saved.subject !== subject ||
            saved.fingerprint !==
              canonical({
                path: saved.path,
                method: saved.method,
                payload: saved.payload,
              }) ||
            (kind === "manual-order" &&
              (saved.path !== "v1/orders/manual" ||
                !manualPayload.safeParse(saved.payload).success)))
        )
          throw new Error("inconsistent recovery record");
        if (
          saved.subject === subject &&
          saved.kind === kind &&
          (saved.path === "v1/orders/manual"
            ? saved.kind === "manual-order" &&
              manualPayload.safeParse(saved.payload).success
            : saved.kind === "balance-adjustment") &&
          saved.fingerprint ===
            canonical({
              path: saved.path,
              method: saved.method,
              payload: saved.payload,
            }) &&
          saved.state !== "completed"
        )
          out.push(new Operation(saved.kind, subject, true, saved));
      }
    } catch {
      if (strict)
        throw new ApiFailure(
          "操作恢复记录无法核实，请先核对已有订单，暂不能另开人工订单。请保留本站存储并联系管理员处理。",
          "contract",
        );
      /* Compatibility callers retain their existing recovery listing behavior. */
    }
    return out;
  }
  private key() {
    return `pandora:operation:${this.subject}:${this.id}`;
  }
  async verify(api: ApiClient): Promise<Row> {
    const result = await api.request(
      `v1/balance-adjustments/${encodeURIComponent(this.id)}`,
      adjustmentSchema,
    );
    if (
      result.operation_id !== this.id ||
      (this.restored?.payload.amount_minor != null &&
        result.amount_minor !== this.restored.payload.amount_minor)
    )
      throw new ApiFailure("操作结果与本地记录不一致，请人工核对", "conflict");
    this.state = "completed";
    this.result = result;
    try {
      sessionStorage.removeItem(this.key());
    } catch {
      /* An unchanged ID remains safe to query or replay. */
    }
    return result;
  }
  recoveryPayload(): Row | undefined {
    return this.restored ? structuredClone(this.restored.payload) : undefined;
  }
  replay(api: ApiClient, resultSchema: z.ZodType<Row> = recordSchema) {
    if (!this.restored)
      return Promise.reject(
        new ApiFailure("没有可重放的原始操作，请核对账务记录", "conflict"),
      );
    return this.send(
      api,
      this.restored.path,
      this.restored.payload,
      {
        method: this.restored.method as RequestOptions["method"],
      },
      resultSchema,
    );
  }
  send(
    api: ApiClient,
    path: string,
    payload: Row,
    options: Omit<RequestOptions, "body"> = {},
    resultSchema: z.ZodType<Row> = recordSchema,
  ): Promise<Row> {
    const fingerprint = canonical({
      path,
      method: options.method || "POST",
      payload,
    });
    if (this.fingerprint && this.fingerprint !== fingerprint)
      return Promise.reject(
        new ApiFailure(
          "上一操作结果尚未核实，请保持原输入并核实结果",
          "conflict",
        ),
      );
    if (this.inFlight) return this.inFlight;
    if (this.state === "completed" && this.result)
      return Promise.resolve(this.result);
    this.fingerprint = fingerprint;
    this.state = "submitting";
    const stored: Stored = {
      id: this.id,
      kind: this.kind,
      subject: this.subject,
      path,
      method: options.method || "POST",
      payload,
      fingerprint,
      created: this.restored?.created || Date.now(),
      state: "unknown",
    };
    if (this.durable) {
      try {
        sessionStorage.setItem(this.key(), JSON.stringify(stored));
        this.restored = stored;
      } catch {
        this.state = "editing";
        return Promise.reject(
          new ApiFailure(
            "无法保存操作恢复信息，本次尚未发送。请允许本站会话存储后重试",
            "contract",
          ),
        );
      }
    }
    const request =
      this.kind === "balance-adjustment" && payload.amount_minor != null
        ? api.request(
            path,
            adjustmentSchema.refine(
              (value) =>
                value.operation_id === this.id &&
                value.amount_minor === payload.amount_minor,
              "操作结果身份不一致",
            ),
            {
              method: "POST",
              ...options,
              idempotencyKey: this.id,
              body: payload,
            },
          )
        : api.request(path, resultSchema, {
            method: "POST",
            ...options,
            body: payload,
            idempotencyKey: this.id,
          });
    const promise = request
      .then((value) => {
        this.state = "completed";
        this.result = value;
        if (this.durable) {
          try {
            sessionStorage.removeItem(this.key());
          } catch {
            /* The confirmed operation keeps the same ID if its recovery record survives. */
          }
        }
        return value;
      })
      .catch((error) => {
        const parsed = failure(error);
        if (parsed.uncertain) {
          this.state = "unknown";
          throw new ApiFailure(
            `结果待核实。请保持原输入重试同一操作，操作号 ${this.id}`,
            parsed.kind,
            parsed.status,
            parsed.code,
            parsed.fields,
            parsed.requestId,
          );
        }
        this.state = "editing";
        this.fingerprint = "";
        if (this.durable) {
          try {
            sessionStorage.removeItem(this.key());
          } catch {
            /* Retry retains its existing record; never claim storage cleanup succeeded. */
          }
        }
        this.id = crypto.randomUUID();
        this.restored = undefined;
        throw error;
      })
      .finally(() => {
        this.inFlight = null;
      });
    this.inFlight = promise;
    return promise;
  }
}
