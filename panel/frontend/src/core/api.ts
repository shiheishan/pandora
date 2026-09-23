import { z } from "zod";
import { isRow, recordSchema, type Row } from "./data";
import { apiUrl } from "./runtime";

export type FailureKind =
  | "network"
  | "timeout"
  | "cancelled"
  | "unauthenticated"
  | "reauth-required"
  | "forbidden"
  | "validation"
  | "conflict"
  | "rate-limited"
  | "server"
  | "contract";
export class ApiFailure extends Error {
  constructor(
    message: string,
    public kind: FailureKind,
    public status?: number,
    public code?: string,
    public fields: Record<string, string[]> = {},
    public requestId?: string,
  ) {
    super(message);
    this.name = "ApiFailure";
  }
  get uncertain() {
    return (
      this.kind === "network" ||
      this.kind === "timeout" ||
      this.kind === "server" ||
      this.code === "response_unknown" ||
      this.code === "write_cancelled"
    );
  }
}
export function failure(error: unknown): ApiFailure {
  if (error instanceof ApiFailure) return error;
  return new ApiFailure(
    error instanceof Error ? error.message : "操作失败，请重试",
    "contract",
  );
}
export type RequestOptions = {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  signal?: AbortSignal;
  idempotencyKey?: string;
  skipReauth?: boolean;
  anonymous?: boolean;
  responseType?: "text";
};
export class ApiClient {
  constructor(
    public base: string,
    private token: () => string,
    private expired: () => void,
    private reauth: () => Promise<boolean>,
    private generation: () => number = () => 0,
    private changed: () => void = () => {},
  ) {}
  async request<T>(
    path: string,
    schema: z.ZodType<T>,
    options: RequestOptions = {},
    replayed = false,
  ): Promise<T> {
    const [pathname, query = ""] = path.split("?");
    const params = new URLSearchParams(query);
    if ((!options.method || options.method === "GET") && pathname === "v1/nodes" && !params.has("limit") && !params.has("offset")) {
      const result = schema.safeParse(await this.getNodeCollection(path, options.signal));
      if (!result.success) throw new ApiFailure("节点列表响应格式不符合约定", "contract");
      return result.data;
    }
    const generation = this.generation();
    const controller = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 15_000);
    const abort = () => controller.abort();
    if (options.signal?.aborted) controller.abort();
    options.signal?.addEventListener("abort", abort, { once: true });
    let response: Response;
    let value: unknown;
    try {
      const headers: Record<string, string> = { Accept: options.responseType === "text" ? "text/csv" : "application/json" };
      if (!options.anonymous && this.token())
        headers.Authorization = `Bearer ${this.token()}`;
      if (options.body !== undefined)
        headers["Content-Type"] = "application/json";
      if (options.idempotencyKey)
        headers["Idempotency-Key"] = options.idempotencyKey;
      response = await fetch(apiUrl(this.base, path), {
        cache: "no-store",
        method: options.method || "GET",
        headers,
        body:
          options.body === undefined ? undefined : JSON.stringify(options.body),
        signal: controller.signal,
      });
      const raw = await response.text();
      try {
        value = response.ok && options.responseType === "text" ? raw : raw ? JSON.parse(raw) : {};
      } catch {
        value = null;
      }
    } catch (error) {
      if (controller.signal.aborted)
        throw new ApiFailure(
          timedOut ? "请求超时，请核对操作结果后重试" : "已取消等待",
          timedOut ? "timeout" : "cancelled",
          undefined,
          options.method && options.method !== "GET"
            ? "write_cancelled"
            : undefined,
        );
      throw new ApiFailure(
        "连接失败，请检查网络；已提交的操作可能仍在处理",
        "network",
      );
    } finally {
      clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
    }
    // Public reads carry no account credentials and remain valid while /me resolves.
    // Authenticated reads and every write still reject results from an old session.
    if (generation !== this.generation() && !(options.anonymous && (!options.method || options.method === "GET")))
      throw new ApiFailure(
        "账户身份已变更，旧请求结果已隔离",
        "cancelled",
        undefined,
        options.method && options.method !== "GET"
          ? "write_cancelled"
          : undefined,
      );
    if (!response.ok) {
      const envelope = isRow(value) && isRow(value.error) ? value.error : {};
      const code =
        typeof envelope.code === "string" ? envelope.code : undefined;
      const message =
        typeof envelope.message === "string"
          ? envelope.message
          : `服务暂不可用（HTTP ${response.status}）`;
      const needsReauth =
        code === "reauth_required" ||
        (response.status === 403 && /重新验证身份/.test(message));
      if (needsReauth && !options.skipReauth && !replayed) {
        if (await this.reauth())
          return this.request(path, schema, options, true);
        throw new ApiFailure(
          "已取消身份确认，表单已保留",
          "cancelled",
          response.status,
          code,
        );
      }
      if (response.status === 401 && !needsReauth && !options.anonymous)
        this.expired();
      const kind: FailureKind = needsReauth
        ? "reauth-required"
        : response.status === 401
          ? "unauthenticated"
          : response.status === 403
            ? "forbidden"
            : response.status === 409
              ? "conflict"
              : response.status === 429
                ? "rate-limited"
                : response.status === 400 || response.status === 422
                  ? "validation"
                  : "server";
      const fields: Record<string, string[]> = {};
      if (isRow(envelope.fields))
        for (const [key, item] of Object.entries(envelope.fields))
          fields[key] = Array.isArray(item)
            ? item.filter((x): x is string => typeof x === "string")
            : [String(item)];
      throw new ApiFailure(
        message,
        kind,
        response.status,
        code,
        fields,
        response.headers.get("X-Request-ID") || undefined,
      );
    }
    if (options.method && options.method !== "GET" && !options.anonymous && !path.startsWith("v1/auth/"))
      this.changed();
    const parsed = schema.safeParse(value);
    if (!parsed.success)
      throw new ApiFailure(
        "服务返回的数据不符合约定，请重试或联系管理员",
        "contract",
        response.status,
        options.method && options.method !== "GET"
          ? "response_unknown"
          : undefined,
      );
    return parsed.data;
  }
  get(path: string, signal?: AbortSignal) {
    return this.request(path, recordSchema, { signal });
  }
  // Pickers need all pages; management tables explicitly request just one.
  private async getNodeCollection(path: string, signal?: AbortSignal): Promise<Row> {
    const generation = this.generation();
    const schema = z.object({ nodes: z.array(recordSchema), total: z.number().int().nonnegative() }).passthrough();
    const collected: Row[] = [];
    const ids = new Set<string>();
    let expected: number | undefined;
    for (let page = 0; page < 200; page++) {
      if (generation !== this.generation() || signal?.aborted)
        throw new ApiFailure("已取消等待", "cancelled");
      const result = await this.request(`${path}${path.includes("?") ? "&" : "?"}limit=500&offset=${collected.length}`, schema, { signal });
      if (expected !== undefined && result.total !== expected)
        throw new ApiFailure("节点列表已变更，请刷新后重试", "conflict");
      expected = result.total;
      for (const node of result.nodes) {
        if (typeof node.id !== "string" || ids.has(node.id))
          throw new ApiFailure("节点列表已变更，请刷新后重试", "conflict");
        ids.add(node.id);
        collected.push(node);
      }
      if (collected.length === expected) return { ...result, nodes: collected, offset: 0 };
      if (!result.nodes.length || collected.length > expected) break;
    }
    throw new ApiFailure("节点列表未能完整读取，请刷新后重试", "contract");
  }
  write(
    path: string,
    body: unknown,
    options: Omit<RequestOptions, "body"> = {},
  ): Promise<Row> {
    return this.request(path, recordSchema, {
      method: "POST",
      ...options,
      body,
    });
  }
}
