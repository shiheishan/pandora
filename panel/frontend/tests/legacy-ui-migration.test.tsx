import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { UserGroupsPage } from "../src/features/admin/UserGroups";
import { CommissionSettings, commissionPayload } from "../src/features/admin/CommissionSettings";
import { matcherFields, mergeMatcher, routingPayload, RoutingPage } from "../src/features/admin/Routing";

it("preserves old matcher aliases, numeric ports and custom fields while changing one field", () => {
  const original = { domains: ["example.com"], ports: [443, 8443], custom_future: ["preserve"] };
  expect(mergeMatcher(original, matcherFields(original))).toEqual(original);
  expect(mergeMatcher(original, { ...matcherFields(original), domain: "new.example" })).toEqual({ domain: ["new.example"], ports: [443, 8443], custom_future: ["preserve"] });
  expect(original.domains).toEqual(["example.com"]);
});
it("retains custom outbound settings and version, rejects dangling references and shadowed rules", () => {
  const outbounds = [{ tag: "proxy", type: "custom", settings: { nested: { extension: true } } }];
  const draft = { row_version: 12, outbounds, routes: [{ priority: 100, matcher: { domain: ["a"] }, enabled: true, outbound_tag: "proxy" }] };
  expect(routingPayload(draft)).toEqual({ ...draft, routes: [{ ...draft.routes[0], priority: 10 }] });
  expect(() => routingPayload({ ...draft, outbounds: [] })).toThrow("出站不存在");
  expect(() => routingPayload({ ...draft, routes: [{ matcher: {}, enabled: true, outbound_tag: "direct" }, ...draft.routes] })).toThrow("必须放在最后");
});
it("resolves mixed-case outbound references without mutating the editing draft", () => {
  const draft = { row_version: 1, outbounds: [{ tag: " ProxyA ", type: " SOCKS ", settings: { port: 1080 } }], routes: [{ matcher: {}, enabled: true, outbound_tag: " proxya " }] };
  const payload = routingPayload(draft);
  expect(payload.outbounds).toEqual([{ tag: "ProxyA", type: "socks", settings: { port: 1080 } }]);
  expect(payload.routes).toEqual([{ matcher: {}, enabled: true, outbound_tag: "ProxyA", priority: 10 }]);
  expect(draft.routes[0]!.outbound_tag).toBe(" proxya ");
  expect(draft.outbounds[0]!.tag).toBe(" ProxyA ");
});
it("converts CNY amounts exactly and retains existing backend limits", () => {
  expect(commissionPayload({ rate_percent: 20, freeze_days: 3, min_withdraw: "100.01" })).toEqual({ rate_percent: 20, freeze_days: 3, min_withdraw: 10001 });
  expect(() => commissionPayload({ rate_percent: 51, freeze_days: 3, min_withdraw: "100" })).toThrow();
  expect(() => commissionPayload({ rate_percent: 20, freeze_days: 91, min_withdraw: "100" })).toThrow();
  expect(() => commissionPayload({ rate_percent: 20, freeze_days: 3, min_withdraw: "1.001" })).toThrow();
});
function mount(element: React.ReactNode, route = "/") {
  localStorage.setItem("aegis_admin_token", "fixture");
  const router = createMemoryRouter([{ path: "*", element: <DialogHost><AuthProvider>{element}</AuthProvider></DialogHost> }], { initialEntries: [route] });
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><RouterProvider router={router} /></QueryClientProvider></App></ConfigProvider>);
}
it("keeps group code immutable, blocks deletion of referenced groups and preserves rejected edits", async () => {
  const writes: unknown[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    if (new URL(input).pathname.endsWith("/v1/me")) return Response.json({ user_id: "admin", permissions: ["*"] });
    if (options?.method === "POST") { writes.push(JSON.parse(String(options.body))); return Response.json({ error: { code: "conflict", message: "模拟保存冲突" } }, { status: 409 }); }
    return Response.json({ groups: [{ id: "group", code: "legacy", name: "旧分组", description: "原备注", users: 1, plans: 2, prices: 3, coupons: 4 }] });
  }));
  mount(<UserGroupsPage />);
  await screen.findByText("旧分组");
  expect(screen.getByRole("button", { name: /删\s*除/ })).toBeDisabled();
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: /^编\s*辑$/ })); await import("../src/core/FormDialog"); });
  expect(await screen.findByLabelText("分组标识")).toBeDisabled();
  fireEvent.change(screen.getByLabelText("用户组名称"), { target: { value: "新名称" } });
  fireEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(screen.getByLabelText("用户组名称")).toHaveValue("新名称");
  expect(writes[0]).toMatchObject({ code: "legacy", name: "新名称" });
});
it("saves commission form through existing API and verifies authoritative readback", async () => {
  let config = { rate_percent: 20, freeze_days: 3, min_withdraw: 10000 };
  const writes: unknown[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me")) return Response.json({ user_id: "admin", permissions: ["*"] });
    if (options?.method === "POST") { expect(path).toMatch(/commission\/config$/); config = JSON.parse(String(options.body)); writes.push(config); return Response.json({ ok: true }); }
    return Response.json(config);
  }));
  mount(<CommissionSettings />);
  const amount = await screen.findByLabelText("最低提现金额");
  await waitFor(() => expect(amount).toHaveValue("100.00"));
  fireEvent.change(amount, { target: { value: "120.01" } });
  // Xboard-style debounced auto-save, without an extra edit dialog or save click.
  await screen.findByText("佣金设置已保存", {}, { timeout: 5000 });
  expect(writes).toEqual([{ rate_percent: 20, freeze_days: 3, min_withdraw: 12001 }]);
});
it("keeps the routing draft on version conflict and does not publish while editing a rule", async () => {
  const writes: unknown[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    if (new URL(input).pathname.endsWith("/v1/me")) return Response.json({ user_id: "admin", permissions: ["*"] });
    if (options?.method === "PUT") { writes.push(JSON.parse(String(options.body))); return Response.json({ error: { message: "节点已被其他管理员修改，请刷新后重试" } }, { status: 409 }); }
    return Response.json({ row_version: 12, outbounds: [], routes: [{ matcher: { domains: ["example.com"], ports: [443] }, enabled: true, outbound_tag: "block", note: "旧备注" }] });
  }));
  mount(<RoutingPage />, "/routing?node=fixture");
  await screen.findByText("旧备注");
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: /^编\s*辑$/ })); await import("../src/core/FormDialog"); });
  fireEvent.change(await screen.findByLabelText("备注"), { target: { value: "草稿备注" } });
  fireEvent.click(screen.getByRole("button", { name: "应用到草稿" }));
  await screen.findByText("草稿备注");
  expect(writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "保存路由配置" }));
  await screen.findByText("节点已被其他管理员修改，请刷新后重试");
  expect(screen.getByText("草稿备注")).toBeVisible();
  expect(writes).toEqual([{ row_version: 12, outbounds: [], routes: [{ matcher: { domains: ["example.com"], ports: [443] }, enabled: true, outbound_tag: "block", note: "草稿备注", priority: 10 }] }]);
});
