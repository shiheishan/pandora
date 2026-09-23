import { expect, it, vi } from "vitest";
import { ApiClient } from "../src/core/api";

const client = () => new ApiClient("https://example.test/", () => "fixture", vi.fn(), async () => false);

it("loads every picker node beyond one page while table reads remain paginated", async () => {
  const nodes = Array.from({ length: 507 }, (_, id) => ({ id: String(id) }));
  const fetchMock = vi.fn(async (url: string) => {
    const q = new URL(url).searchParams;
    expect(q.get("runtime_role")).toBe("business");
    const offset = Number(q.get("offset"));
    return Response.json({ nodes: nodes.slice(offset, offset + Number(q.get("limit"))), total: 507 });
  });
  vi.stubGlobal("fetch", fetchMock);
  const api = client();
  await expect(api.get("v1/nodes?runtime_role=business")).resolves.toMatchObject({ nodes, total: 507 });
  expect(fetchMock).toHaveBeenCalledTimes(2);
  await expect(api.get("v1/nodes?runtime_role=business&limit=20&offset=500")).resolves.toMatchObject({ nodes: nodes.slice(500), total: 507 });
  expect(fetchMock).toHaveBeenCalledTimes(3);
});

it("rejects a changed or truncated collection instead of hiding missing nodes", async () => {
  for (const second of [{ nodes: [{ id: "a" }], total: 2 }, { nodes: [], total: 2 }, { nodes: [{ id: "b" }], total: 3 }]) {
    const fetchMock = vi.fn().mockResolvedValueOnce(Response.json({ nodes: [{ id: "a" }], total: 2 })).mockResolvedValueOnce(Response.json(second));
    vi.stubGlobal("fetch", fetchMock);
    await expect(client().get("v1/nodes")).rejects.toHaveProperty("kind");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  }
});
