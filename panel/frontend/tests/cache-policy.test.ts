import { it, expect, vi } from "vitest";
import { QueryClient } from "@tanstack/react-query";
import { cachePolicy } from "../src/core/cachePolicy";
import { ApiClient } from "../src/core/api";
it("uses longer freshness for content but never for money, nodes or credentials", () => {
  expect(cachePolicy("v1/content").staleTime).toBe(600000);
  expect(cachePolicy("v1/plans?page=2").staleTime).toBe(120000);
  for (const p of ["v1/orders/1", "v1/me/balance", "v1/nodes", "v1/refunds"])
    expect(cachePolicy(p)).toMatchObject({ staleTime: 0, refetchInterval: 30000, refetchIntervalInBackground: false });
  expect(cachePolicy("v1/settings/mail")).toMatchObject({ gcTime: 0, staleTime: 0 });
});
it("reuses fresh content, reloads after invalidation, and isolates accounts", async () => {
  const client = new QueryClient(); const fetcher = vi.fn(async () => ({ title: "文章" }));
  const options = { queryKey: ["admin", "account-a", "content"], queryFn: fetcher, ...cachePolicy("v1/content") };
  await client.fetchQuery(options); await client.fetchQuery(options); expect(fetcher).toHaveBeenCalledTimes(1);
  await client.invalidateQueries({ queryKey: ["admin", "account-a"] });
  await client.fetchQuery(options); expect(fetcher).toHaveBeenCalledTimes(2);
  await client.fetchQuery({ ...options, queryKey: ["admin", "account-b", "content"] });
  expect(fetcher).toHaveBeenCalledTimes(3); client.clear(); expect(client.getQueryCache().getAll()).toHaveLength(0);
});
it("bypasses browser caching and invalidates only successful writes", async () => {
  const changed = vi.fn(); const fetcher = vi.fn(async (_input: unknown, _options?: RequestInit) => new Response("{}", { status: 200 })); vi.stubGlobal("fetch", fetcher);
  const api = new ApiClient("https://panel.example/", () => "token", vi.fn(), async () => false, () => 0, changed);
  await api.get("v1/plans"); expect(changed).not.toHaveBeenCalled();
  await api.write("v1/plans", {}); expect(changed).toHaveBeenCalledTimes(1);
  expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ cache: "no-store" });
  fetcher.mockImplementation(async () => new Response("{}", { status: 403 }));
  await expect(api.write("v1/plans", {})).rejects.toThrow(); expect(changed).toHaveBeenCalledTimes(1);
});
