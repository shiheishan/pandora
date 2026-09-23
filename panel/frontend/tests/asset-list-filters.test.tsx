import { render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { ConfigProvider } from "antd";
import userEvent from "@testing-library/user-event";
const api = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("../src/core/auth", () => ({ useAuth: () => ({ api, scope: "fixture", principal: { id: "fixture" } }) }));
import { ResourcePage } from "../src/components/common";

const records = [
  { id: "1", name: "台湾一号", server_host: "TW.example", node_type: "vless" },
  { id: "2", name: "台湾二号", server_host: "tw.example", node_type: "trojan" },
  { id: "3", name: "日本三号", server_host: "jp.example", node_type: "vless" },
];
function mount(entry: string, serverPagination = false) {
  api.request.mockReset().mockResolvedValue({ nodes: records, total: records.length });
  const router = createMemoryRouter([{ path: "/nodes", element: <ResourcePage title="节点" resource="nodes" path="v1/nodes" listKey="nodes" columns={[{ title: "名称", dataIndex: "name" }]} searchable clientSearchFields={["name", "server_host"]} clientFilters={[{ key: "protocol", label: "协议", field: "node_type" }]} serverPagination={serverPagination} selection={{ enabled: row => row.id !== "3", actions: selected => <span data-testid="selected">{selected.map(row => row.id).join(",")}</span> }} /> }], { initialEntries: [entry] });
  render(<ConfigProvider theme={{ token: { motion: false } }}><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><RouterProvider router={router} /></QueryClientProvider></ConfigProvider>);
  return router;
}
it("combines case-insensitive local search and protocol filtering before pagination", async () => {
  mount("/nodes?q=TW.EXAMPLE&protocol=vless&page=8");
  expect(await screen.findByText("台湾一号")).toBeVisible();
  expect(screen.queryByText("台湾二号")).not.toBeInTheDocument();
  expect(screen.queryByText("日本三号")).not.toBeInTheDocument();
  expect(api.request.mock.calls[0]?.[0]).toBe("v1/nodes");
});
it("clears selection across filters and never selects a disabled row", async () => {
  const router = mount("/nodes");
  await screen.findByText("台湾一号");
  await userEvent.click(screen.getAllByRole("checkbox")[0]!);
  expect(screen.getByTestId("selected")).toHaveTextContent("1,2");
  await router.navigate("/nodes?protocol=trojan");
  await waitFor(() => expect(screen.getByTestId("selected")).toBeEmptyDOMElement());
  await router.navigate("/nodes");
  await waitFor(() => expect(screen.getByTestId("selected")).toBeEmptyDOMElement());
});
it("shows an honest empty result when no row matches", async () => {
  mount("/nodes?q=不存在&protocol=vless");
  await waitFor(() => expect(api.request).toHaveBeenCalled());
  expect(await screen.findByText("暂无符合条件的数据")).toBeVisible();
  expect(screen.queryByText("台湾一号")).not.toBeInTheDocument();
});
it("does not apply local filtering to an incomplete server-paginated result", async () => {
  mount("/nodes?q=server-query&protocol=not-local", true);
  expect(await screen.findByText("台湾一号")).toBeVisible();
  expect(api.request.mock.calls[0]?.[0]).toContain("q=server-query");
  expect(api.request.mock.calls[0]?.[0]).toContain("protocol=not-local");
  expect(screen.getByRole("combobox", { name: "协议" })).toBeVisible();
});
