import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
vi.mock("../src/core/runtime", async importOriginal => ({ ...await importOriginal<object>(), runtime: { domain: "portal", apiBase: "https://portal.example/" } }));
const request = vi.hoisted(() => vi.fn());
vi.mock("../src/core/auth", () => ({ useAuth: () => ({ api: { request } }) }));
import { PortalAppearance, PortalSlot, useBranding } from "../src/core/appearance";
function Brand() { const b = useBranding(); return <p>{b.name} / {b.tagline}</p>; }
it("renders only published slot strings and removes content when public configuration changes", async () => {
  request.mockResolvedValue({ slots: { "portal.login.notice": "<p>登录帮助 <a href='/help'>帮助中心</a></p>", "portal.footer": "   ", "portal.home.banner": { content: "invalid" } } });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(<QueryClientProvider client={client}><PortalAppearance><PortalSlot name="portal.login.notice" /><PortalSlot name="portal.footer" /><PortalSlot name="portal.home.banner" /></PortalAppearance></QueryClientProvider>);
  expect(await screen.findByRole("link", { name: "帮助中心" })).toHaveAttribute("href", "/help");
  expect(result.container.querySelectorAll(".portal-slot")).toHaveLength(1);
  request.mockResolvedValue({ slots: {} });
  await client.invalidateQueries({ queryKey: ["public-appearance"] });
  await waitFor(() => expect(result.container.querySelectorAll(".portal-slot")).toHaveLength(0));
});
it("loads anonymous branding and sanitized CSS without blocking login, and cleans up on unmount", async () => {
  const originalTitle = document.title;
  request.mockResolvedValue({ theme: { code: "custom", tokens: {}, branding: { site_name: "测试站点", tagline: "自由连接" }, custom_css: ".fixture{color:red}" } });
  const result = render(<QueryClientProvider client={new QueryClient()}><PortalAppearance><Brand /></PortalAppearance></QueryClientProvider>);
  expect(screen.getByText("PANDORA /", { exact: false })).toBeVisible();
  await screen.findByText("测试站点 / 自由连接");
  await waitFor(() => expect(document.title).toBe("测试站点"));
  expect(request.mock.calls[0]?.[2]).toMatchObject({ anonymous: true });
  expect(document.querySelector("style[data-pandora-theme=custom]")?.textContent).toContain(".fixture{color:red}");
  result.unmount();expect(document.title).toBe(originalTitle);expect(document.querySelector("style[data-pandora-theme]")).toBeNull();
});
it("leaves the default login available when appearance lookup fails", async () => {
  request.mockRejectedValue(new Error("offline"));
  render(<QueryClientProvider client={new QueryClient()}><PortalAppearance><Brand /></PortalAppearance></QueryClientProvider>);
  expect(screen.getByText("PANDORA /", { exact: false })).toBeVisible();
  await waitFor(() => expect(request).toHaveBeenCalled());
});
