import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { AppearancePage, themePayload } from "../src/features/admin/Appearance";

it("preserves old extension tokens and branding when editing supported fields", () => {
  expect(themePayload({ tokens: { extension: "keep", brand: "#111111" }, branding: { logo: "/old.svg", site_name: "旧站" } }, { code: "legacy", name: "主题", site_name: "新站", tagline: "标语", brand: "#222222", custom_css: ".old{color:red}" })).toEqual({ code: "legacy", name: "主题", tokens: { extension: "keep", brand: "#222222" }, branding: { logo: "/old.svg", site_name: "新站", tagline: "标语" }, custom_css: ".old{color:red}" });
});
it("copies builtins without overwriting them, preserves extras and reports CSS filtering", async () => {
  localStorage.setItem("aegis_admin_token", "fixture");
  const writes: { body: Record<string, unknown>; key: string | null }[] = [];
  const themes = [{ code: "builtin", name: "内置主题", is_active: true, is_builtin: true, tokens: { brand: "#123456", extension: "keep" }, branding: { site_name: "旧站", extra: "keep" }, custom_css: "a{color:red}" }];
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    if (new URL(input).pathname.endsWith("/v1/me")) return Response.json({ user_id: "admin", permissions: ["*"] });
    if (options?.method === "POST") { writes.push({ body: JSON.parse(String(options.body)), key: new Headers(options.headers).get("Idempotency-Key") }); return Response.json({ saved: true, dropped: ["移除外部 @import"] }); }
    return Response.json({ themes });
  }));
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><DialogHost><AuthProvider><AppearancePage /></AuthProvider></DialogHost></QueryClientProvider></App></ConfigProvider>);
  const row = (await screen.findByText("内置主题")).closest("tr")!;
  expect(within(row).getByRole("button", { name: /删\s*除/ })).toBeDisabled();
  await act(async () => { fireEvent.click(within(row).getByRole("button", { name: "复制编辑" })); await import("../src/core/FormDialog"); });
  const code = await screen.findByLabelText("主题标识");
  expect(code).not.toHaveValue("builtin");
  fireEvent.change(screen.getByLabelText("站点名称"), { target: { value: "新站点" } });
  fireEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(writes[0]?.key).toBeTruthy();
  expect(writes[0]?.body).toMatchObject({ tokens: { extension: "keep" }, branding: { site_name: "新站点", extra: "keep" } });
  await screen.findByText(/服务器已过滤/);
});
