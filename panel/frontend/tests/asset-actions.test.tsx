import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { DialogHost } from "../src/core/dialogs";
import { AuthProvider } from "../src/core/auth";
import { NodesPage, ServersPage } from "../src/features/admin/Nodes";

function setup(kind: "servers" | "nodes", writable = true, reject = false, legacy = false) {
  localStorage.setItem("aegis_admin_token", "fixture-token");
  let record = { id: "asset-a", name: "测试资产", notes: "原备注", region: "TW", capacity_nodes: 32, row_version: 7, status: "draft", runtime_role: "business", node_type: "vless", display_name: "台湾线路", server_host: "fixture.example", server_port: 443, traffic_rate: 1, protocol_config: { password: "***" } };
  if (legacy) record.node_type = "legacy-fixture";
  const writes: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    let data: unknown = {};
    let status = 200;
    if (path.endsWith("/v1/me")) data = { user_id: "operator", permissions: ["node.read", ...(writable ? ["node.write", "node.provision"] : [])] };
    else if (path.endsWith("/move") && options?.method === "POST") { const payload = JSON.parse(String(options.body)); writes.push(payload); data = {ok:true}; }
    else if (path.endsWith("/v1/node-protocol-schemas")) data = { schemas: [{ node_type: "vless", status: "stable", allowed_properties: ["tls"], property_types: { tls: "number" }, enums: { tls: ["0", "1", "2"] } }] };
    else if (options?.method === "PATCH") {
      const payload = JSON.parse(String(options.body)); writes.push(payload);
      if (reject) { status = 409; data = { error: { code: "conflict", message: "资料已被其他管理员更新，请刷新后重试" } }; }
      else { record = { ...record, ...payload, row_version: record.row_version + 1 }; data = record; }
    } else if (path.endsWith(`/${kind}/asset-a`)) data = record;
    else if (path.endsWith(`/v1/${kind}`)) data = { [kind]: [{ ...record, row_version: 1 }], total: 1 };
    else if (path.endsWith("/nodes")) data = { nodes: [] };
    else if (path.endsWith("/servers")) data = { servers: [{id:"server-other",name:"另一台服务器",status:"ready"}] };
    else if (path.endsWith("/metrics")) data = { points: [] };
    return new Response(JSON.stringify(data), { status });
  }));
  const router = createMemoryRouter([{ path: `/${kind}`, element: kind === "servers" ? <ServersPage /> : <NodesPage /> }], { initialEntries: [`/${kind}`] });
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><DialogHost><AuthProvider><RouterProvider router={router} /></AuthProvider></DialogHost></QueryClientProvider></App></ConfigProvider>);
  return { writes, router };
}

// Multi-step business flow: allow cold module loading and real UI events; assertions remain unchanged.
it("edits a server directly from the list with the fresh version, then reopens persisted notes", async () => {
  const { writes } = setup("servers");
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  const note = await screen.findByLabelText("备注");
  await userEvent.clear(note); await userEvent.type(note, "新的运维备注");
  await userEvent.click(screen.getByRole("button", { name: /更\s*新/ }));
  await waitFor(() => expect(writes).toEqual([{ notes: "新的运维备注", row_version: 7 }]));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  await userEvent.click(screen.getByRole("button", { name: /编\s*辑/ }));
  expect(await screen.findByLabelText("备注")).toHaveValue("新的运维备注");
});

it("opens server details in place and closing retains the list route", async () => {
  const { router, writes } = setup("servers");
  await userEvent.click(await screen.findByRole("button", { name: /详\s*情/ }));
  const dialog = await screen.findByRole("dialog");
  await waitFor(() => expect(within(dialog).getByText("关联节点")).toBeVisible());
  expect(within(dialog).getByText("原备注")).toBeVisible();
  await userEvent.click(within(dialog).getByRole("button", { name: /close/i }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(router.state.location.pathname).toBe("/servers"); expect(writes).toHaveLength(0);
});

it("edits node name without resubmitting redacted secrets or unchanged endpoint fields", async () => {
  const { writes } = setup("nodes");
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  const field = await screen.findByLabelText("节点名称");
  await userEvent.clear(field); await userEvent.type(field, "新的线路名称");
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() => expect(writes).toEqual([{ name: "新的线路名称", row_version: 7 }]));
});

it("keeps an edit draft open after a version conflict", async () => {
  const { writes } = setup("servers", true, true);
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  await userEvent.type(await screen.findByLabelText("备注"), "待保留");
  await userEvent.click(screen.getByRole("button", { name: /更\s*新/ }));
  await waitFor(() => expect(writes).toHaveLength(1));
  expect(screen.getByLabelText("备注")).toHaveValue("原备注待保留");
  expect(screen.getByRole("dialog")).toBeVisible();
});

it("retains base editing for legacy nodes without an editable protocol schema", async () => {
  const { writes } = setup("nodes", true, false, true);
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  await waitFor(() => expect(screen.getByText("旧协议配置将原样保留，可修改节点名称、地址、端口和倍率。")).toBeVisible());
  await userEvent.clear(screen.getByLabelText("节点名称"));
  await userEvent.type(screen.getByLabelText("节点名称"), "旧线路改名");
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() => expect(writes).toEqual([{ name: "旧线路改名", row_version: 7 }]));
});

it("read-only operators can view details but cannot edit", async () => {
  setup("servers", false);
  expect(await screen.findByRole("button", { name: /详\s*情/ })).toBeVisible();
  expect(screen.queryByRole("button", { name: /编\s*辑/ })).not.toBeInTheDocument();
});

it("changes server through the existing move operation from association settings", async () => {
  const { writes } = setup("nodes");
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  await userEvent.click(await screen.findByRole("button", { name: "更换绑定服务器" }));
  const target = await screen.findByLabelText("目标服务器");
  await userEvent.click(target);
  await userEvent.click(await screen.findByText("另一台服务器", {selector:".ant-select-item-option-content"}));
  await userEvent.type(screen.getByLabelText("迁移原因"), "调整归属");
  await userEvent.click(screen.getByRole("button", {name:"确认迁移"}));
  await waitFor(()=>expect(writes).toEqual([{row_version:7,server_id:"server-other",reason:"调整归属"}]));
  await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
});

it("keeps binding separate from unsaved node edits and exposes the node-specific routing entry", async () => {
  const { writes } = setup("nodes");
  await userEvent.click(await screen.findByRole("button", { name: /编\s*辑/ }));
  const association = await screen.findByRole("region", {name:"关联配置"});
  expect(within(association).getByRole("link", {name:"配置路由"})).toHaveAttribute("href", "#/routing?node=asset-a");
  await userEvent.type(screen.getByLabelText("订阅显示名称"), "草稿");
  expect(within(association).getByRole("button", {name:"更换绑定服务器"})).toBeDisabled();
  expect(writes).toHaveLength(0);
});
