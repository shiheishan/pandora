import { expect, it, vi } from "vitest";
import { ApiClient } from "../src/core/api";
import { recordSchema } from "../src/core/data";

it("keeps public GET results across identity initialization but isolates protected reads and writes", async () => {
  for (const options of [{ anonymous: true }, {}, { anonymous: true, method: "POST" as const }]) {
    let generation = 0;
    const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
      expect(new Headers(init?.headers).has("Authorization")).toBe(!options.anonymous);
      generation++;
      return Response.json({ slots: { "portal.footer": "<p>页脚</p>" } });
    });
    vi.stubGlobal("fetch", fetchMock);
    const api = new ApiClient("https://example.test/", () => "fixture", vi.fn(), async () => false, () => generation);
    const result = api.request("v1/appearance", recordSchema, options);
    if (options.anonymous && !options.method) await expect(result).resolves.toMatchObject({ slots: { "portal.footer": "<p>页脚</p>" } });
    else await expect(result).rejects.toMatchObject({ kind: "cancelled" });
  }
});
