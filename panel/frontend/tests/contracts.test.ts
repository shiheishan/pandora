import { describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { ApiClient, ApiFailure } from "../src/core/api";
import { parseMinor, legacyInteger, money } from "../src/core/numbers";
import { apiUrl, resolveApiBase } from "../src/core/runtime";
import { principalSchema, principalSubject } from "../src/core/data";
import { Operation } from "../src/core/operations";
import { protocolField } from "../src/core/protocol";

const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
const adjustment = (id: string, amount = "100") => ({
  operation_id: id,
  amount_minor: amount,
  user_id: "u",
  actor_id: "admin",
  currency: "CNY",
  reason: "测试财务调账理由",
  balance_before_minor: "0",
  balance_after_minor: "100",
  ledger_transaction_id: "ledger",
  created_at: "2026-09-05T00:00:00Z",
});
const client = (reauth = vi.fn(async () => true), expired = vi.fn()) =>
  new ApiClient(
    "http://localhost/secret/",
    () => "test-token",
    expired,
    reauth,
  );
describe("exact amounts and gateway routing", () => {
  it("honors per-protocol TLS restrictions and nested secret markers from the API schema", () => {
    const schema = {
      property_types: { tls: "number" },
      enums: { tls: ["0", "2"], method: ["aes-128-gcm"] },
      sensitive_properties: ["private_key"],
    };
    expect(protocolField(schema, "tls").enums).toEqual(["0", "2"]);
    expect(protocolField(schema, "method").enums).toEqual(["aes-128-gcm"]);
    expect(
      protocolField(schema, "reality_settings.private_key").sensitive,
    ).toBe(true);
    expect(protocolField(schema, "reality_settings.public_key").sensitive).toBe(
      false,
    );
  });
  it("uses immutable user identity before email or alternate id", () => {
    expect(
      principalSubject(
        principalSchema.parse({
          user_id: "stable-user",
          id: "alternate",
          email: "rename@example.test",
        }),
      ),
    ).toBe("stable-user");
    expect(
      principalSubject(
        principalSchema.parse({ user_id: "admin-without-email" }),
      ),
    ).toBe("admin-without-email");
  });
  it("converts decimals exactly without binary floats", () => {
    expect(parseMinor("1.01")).toBe("101");
    expect(parseMinor("-0.01", { signed: true })).toBe("-1");
    expect(parseMinor("90071992547409.93")).toBe("9007199254740993");
    expect(money("9007199254740993")).toBe("CNY 90,071,992,547,409.93");
  });
  it("rejects rounding, exponent notation, overflow and unsafe legacy integers", () => {
    for (const value of [
      "1.001",
      "1e3",
      "Infinity",
      "92233720368547758.08",
      "-1",
    ])
      expect(() => parseMinor(value)).toThrow();
    expect(() => legacyInteger("9007199254740993")).toThrow();
    expect(() => parseMinor("0", { zero: false })).toThrow();
  });
  it("keeps random admin prefix regardless of hash route", () => {
    const base = resolveApiBase(
      "https://example.test/random/app/#/users/u?page=2",
      "../",
    );
    expect(apiUrl(base, "v1/users?q=a")).toBe(
      "https://example.test/random/v1/users?q=a",
    );
    expect(() =>
      resolveApiBase("https://a.test/app/", "https://b.test/"),
    ).toThrow();
    expect(() => apiUrl(base, "../v1/users")).toThrow();
  });
  it("accepts the actual admin /me shape without an email field", () => {
    expect(
      principalSchema.parse({
        user_id: "admin-id",
        kind: "user",
        permissions: ["node.read"],
      }).user_id,
    ).toBe("admin-id");
    expect(() =>
      principalSchema.parse({ email: "only@example.test" }),
    ).toThrow();
  });
});
describe("API authentication and response uncertainty", () => {
  it("handles reauth_required 401 before expiration and preserves request identity", async () => {
    const expired = vi.fn();
    const reauth = vi.fn(async () => true);
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(
        response(
          { error: { code: "reauth_required", message: "Confirm" } },
          401,
        ),
      )
      .mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    await client(reauth, expired).write(
      "v1/users/u/balance",
      { amount_minor: "100" },
      { idempotencyKey: "same-key" },
    );
    expect(reauth).toHaveBeenCalledOnce();
    expect(expired).not.toHaveBeenCalled();
    expect(fetcher.mock.calls[0]?.[1].body).toBe(
      fetcher.mock.calls[1]?.[1].body,
    );
    expect(fetcher.mock.calls[1]?.[1].headers["Idempotency-Key"]).toBe(
      "same-key",
    );
  });
  it("clears only ordinary 401 and never upgrades ordinary 403 to reauth", async () => {
    const expired = vi.fn();
    const reauth = vi.fn(async () => true);
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(response({ error: { message: "Denied" } }, 403))
        .mockResolvedValueOnce(
          response({ error: { message: "Expired" } }, 401),
        ),
    );
    const api = client(reauth, expired);
    await expect(api.get("v1/users")).rejects.toMatchObject({
      kind: "forbidden",
    });
    expect(reauth).not.toHaveBeenCalled();
    await expect(api.get("v1/users")).rejects.toMatchObject({
      kind: "unauthenticated",
    });
    expect(expired).toHaveBeenCalledOnce();
  });
  it("successful malformed write response remains uncertain", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () => new Response("<html>proxy error</html>", { status: 200 }),
      ),
    );
    try {
      await client().write("v1/users/u/balance", {});
      throw new Error("expected rejection");
    } catch (error) {
      expect(error).toBeInstanceOf(ApiFailure);
      expect((error as ApiFailure).uncertain).toBe(true);
    }
  });
  it("isolates late results from a replaced account", async () => {
    let generation = 0;
    let finish!: (value: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const api = new ApiClient(
      "http://localhost/",
      () => "token",
      vi.fn(),
      async () => false,
      () => generation,
    );
    const pending = api.get("v1/me");
    generation++;
    finish(response({ user_id: "old-admin" }));
    await expect(pending).rejects.toMatchObject({ kind: "cancelled" });
  });
  it("validates required response fields with a schema", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => response({ wrong: [] })),
    );
    await expect(
      client().request("v1/users", z.object({ users: z.array(z.unknown()) })),
    ).rejects.toMatchObject({ kind: "contract" });
  });
});
describe("stable operations", () => {
  it("shares double clicks and retries an uncertain balance with the same ID and frozen payload", async () => {
    let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetcher);
    const api = client();
    const operation = new Operation("balance-adjustment", "admin-1", true);
    const id = operation.id;
    const payload = {
      operation_id: id,
      amount_minor: "100",
      currency: "CNY",
      reason: "测试财务调账理由",
    };
    const first = operation.send(api, "v1/users/u/balance", payload);
    const second = operation.send(api, "v1/users/u/balance", payload);
    expect(first).toBe(second);
    finish(response({ error: { message: "Unavailable" } }, 503));
    await expect(first).rejects.toMatchObject({ kind: "server" });
    expect(operation.state).toBe("unknown");
    expect(operation.id).toBe(id);
    await expect(
      operation.send(api, "v1/users/u/balance", {
        ...payload,
        amount_minor: "200",
      }),
    ).rejects.toMatchObject({ kind: "conflict" });
    fetcher.mockResolvedValueOnce(response(adjustment(id)));
    await operation.send(api, "v1/users/u/balance", payload);
    expect(fetcher.mock.calls[0]?.[1].headers["Idempotency-Key"]).toBe(
      fetcher.mock.calls[1]?.[1].headers["Idempotency-Key"],
    );
    expect(sessionStorage.length).toBe(0);
  });
  it("restores only the same subject and replays after reload with identical payload", async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new TypeError("offline"));
    vi.stubGlobal("fetch", fetcher);
    const operation = new Operation("balance-adjustment", "admin-A", true);
    const payload = {
      operation_id: operation.id,
      amount_minor: "123",
      currency: "CNY",
      reason: "恢复同一个调账",
    };
    await expect(
      operation.send(client(), "v1/users/user-A/balance", payload),
    ).rejects.toThrow("结果待核实");
    expect(Operation.restore("admin-B")).toHaveLength(0);
    const restored = Operation.restore("admin-A");
    expect(restored).toHaveLength(1);
    expect(restored[0]?.id).toBe(operation.id);
    fetcher.mockResolvedValueOnce(response(adjustment(operation.id, "123")));
    await restored[0]!.replay(client());
    expect(fetcher.mock.calls[1]?.[1].body).toBe(JSON.stringify(payload));
  });
  it("query 404 leaves intent unresolved, then an immutable result resolves it", async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce(
        response({ error: { message: "Not found" } }, 404),
      );
    vi.stubGlobal("fetch", fetcher);
    const operation = new Operation("balance-adjustment", "admin-1", true);
    const payload = { operation_id: operation.id, amount_minor: "100" };
    await expect(
      operation.send(client(), "v1/users/u/balance", payload),
    ).rejects.toThrow();
    const restored = Operation.restore("admin-1")[0]!;
    await expect(restored.verify(client())).rejects.toMatchObject({
      status: 404,
    });
    expect(Operation.restore("admin-1")).toHaveLength(1);
    fetcher.mockResolvedValueOnce(response(adjustment(operation.id)));
    await restored.verify(client());
    expect(Operation.restore("admin-1")).toHaveLength(0);
  });
});
