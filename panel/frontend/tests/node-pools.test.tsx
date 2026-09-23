/**
 * [INPUT]: 依赖 src/features/admin/Nodes 的 PoolsPage、src/core/dialogs 的 DialogHost、src/core/api 的 ApiFailure，auth 以 vi.mock 替身
 * [OUTPUT]: 对外提供权限组（节点池）页 vitest 用例：本地搜索与筛选、新建、编辑不可改代码、被引用禁删与后端 409 保留行、只读无操作入口
 * [POS]: tests 下节点池管理页的回归守卫；弹窗断言先等 antd Modal 入场动画结束，组件 FormDialog 不为测试让步
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { ApiFailure } from "../src/core/api";
import { DialogHost } from "../src/core/dialogs";
import { PoolsPage } from "../src/features/admin/Nodes";

const fixture = vi.hoisted(() => ({ api: { request: vi.fn(), write: vi.fn() }, writable: true }));
vi.mock("../src/core/auth", () => ({ useAuth: () => ({
  api: fixture.api, scope: "fixture", principal: { id: "fixture" },
  can: (permission: string) => permission === "node.read" || fixture.writable,
}) }));
const pools = [
  { id: "tw", code: "TW-PREMIUM", name: "台湾专线", region: "TW", status: "active", nodes: 2, active_nodes: 2, plans: 1 },
  { id: "jp", code: "JP-EMPTY", name: "日本备用", region: "JP", status: "disabled", nodes: 0, active_nodes: 0, plans: 0 },
];
function mount(entry = "/node-pools", writable = true) {
  fixture.writable = writable;
  fixture.api.request.mockReset().mockImplementation((path, _schema, options) =>
    options?.method && options.method !== "GET" ? fixture.api.write(path, options.body, options) : Promise.resolve({ pools }));
  fixture.api.write.mockReset().mockResolvedValue({ ok: true });
  const router = createMemoryRouter([{ path: "/node-pools", element: <DialogHost><PoolsPage /></DialogHost> }], { initialEntries: [entry] });
  render(<ConfigProvider theme={{ token: { motion: false } }}><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><RouterProvider router={router} /></QueryClientProvider></ConfigProvider>);
  return router;
}

it("searches names and stable codes locally and combines region and state filters", async () => {
  const router = mount("/node-pools?q=tw-premium&region=TW&status=active");
  expect(await screen.findByText("台湾专线")).toBeVisible();
  expect(screen.queryByText("日本备用")).not.toBeInTheDocument();
  expect(fixture.api.request.mock.calls[0]?.[0]).toBe("v1/node-pools?status=active");
  await router.navigate("/node-pools?q=日本&region=TW");
  await waitFor(() => expect(screen.getByText("暂无符合条件的数据")).toBeVisible());
  expect(fixture.api.request.mock.calls.every(call => !String(call[0]).includes("user-groups"))).toBe(true);
});

it("creates with just a name and lets the existing API generate the code", async () => {
  mount();
  await userEvent.click(screen.getByRole("button", { name: "新增权限组" }));
  await userEvent.type(await screen.findByLabelText("名称"), "新权限组");
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(fixture.api.write).toHaveBeenCalledTimes(1));
  expect(fixture.api.write.mock.calls[0]?.[0]).toBe("v1/node-pools");
  expect(fixture.api.write.mock.calls[0]?.[1]).toMatchObject({ name: "新权限组", status: "active" });
  expect(fixture.api.write.mock.calls[0]?.[2].idempotencyKey).toBeTruthy();
});

it("edits the selected node pool and keeps its stable code immutable", async () => {
  mount();
  const row = (await screen.findByText("台湾专线")).closest("tr")!;
  await userEvent.click(within(row).getByRole("button", { name: /编\s*辑/ }));
  expect(await screen.findByLabelText("分组代码")).toBeDisabled();
  await userEvent.clear(screen.getByLabelText("名称"));
  await userEvent.type(screen.getByLabelText("名称"), "台湾新名称");
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(fixture.api.write).toHaveBeenCalled());
  expect(fixture.api.write.mock.calls[0]?.[0]).toBe("v1/node-pools/tw");
  expect(fixture.api.write.mock.calls[0]?.[1]).toMatchObject({ name: "台湾新名称", code: "TW-PREMIUM", region: "TW" });
});

it("guards referenced pool deletion and retains the row when the backend reports a dependency", async () => {
  mount();
  const bound = (await screen.findByText("台湾专线")).closest("tr")!;
  expect(within(bound).getByRole("button", { name: /删\s*除/ })).toBeDisabled();
  const empty = screen.getByText("日本备用").closest("tr")!;
  await userEvent.click(within(empty).getByRole("button", { name: /删\s*除/ }));
  expect(fixture.api.write).not.toHaveBeenCalled();
  fixture.api.write.mockRejectedValueOnce(new ApiFailure("还有节点模板使用这个默认分组", "conflict", 409));
  // antd Modal 的 zoom 入场动画期间 opacity 为 0，先等弹窗真正可见再操作和断言。
  await waitFor(() => expect(screen.getByRole("dialog")).toBeVisible());
  await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /删\s*除/ }));
  await waitFor(() => expect(screen.getByText("还有节点模板使用这个默认分组")).toBeVisible());
  expect(screen.getByText("日本备用")).toBeVisible();
  expect(fixture.api.write.mock.calls[0]?.[0]).toBe("v1/node-pools/jp");
  expect(fixture.api.write.mock.calls[0]?.[2].method).toBe("DELETE");
});

it("does not offer create, edit or delete actions to a read-only node operator", async () => {
  mount("/node-pools", false);
  await screen.findByText("台湾专线");
  expect(screen.queryByRole("button", { name: "新增权限组" })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /编\s*辑|删\s*除/ })).not.toBeInTheDocument();
});
