import { it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { Link, MemoryRouter } from "react-router-dom";
import { ConfigProvider } from "antd";
import { Frame } from "../src/app/Frame";
const auth = vi.hoisted(() => ({ permissions: ["*"] }));
beforeEach(() => { auth.permissions = ["*"]; });
vi.mock("../src/core/auth", () => ({ useAuth: () => ({
  principal: { email: "navigation@example.test" }, ready: true,
  can: (permission?: string) => !permission || auth.permissions.includes("*") || auth.permissions.includes(permission), logout: vi.fn(),
}) }));
vi.mock("../src/core/realtime", () => ({ useRealtime: () => "connected" }));
it("shows Xboard-style expanded groups and direct secondary navigation", () => {
  const { container } = render(<ConfigProvider theme={{ token: { motion: false } }}>
    <MemoryRouter initialEntries={["/servers"]}><Frame /></MemoryRouter>
  </ConfigProvider>);
  expect(container.querySelector(".app-layout")).not.toHaveClass("sidebar-collapsed");
  const server = screen.getByText("服务器管理");
  expect(server.closest(".ant-menu-submenu")).toHaveClass("ant-menu-submenu-open");
  expect(server.closest("li")).toHaveClass("ant-menu-item-selected");
  const plan = screen.getAllByText("套餐")[0]!;
  fireEvent.click(plan);
  expect(plan.closest("li")).toHaveClass("ant-menu-item-selected");
  expect(screen.queryByRole("tab")).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: "收起侧栏" }));
  expect(container.querySelector(".app-layout")).toHaveClass("sidebar-collapsed");
  fireEvent.click(screen.getByRole("button", { name: "展开侧栏" }));
  expect(container.querySelector(".app-layout")).not.toHaveClass("sidebar-collapsed");
});

import { adminNav } from "../src/app/navigation";
import { visibleSections } from "../src/app/sections";
it("retains each original destination once and preserves permission-filtered entry points", () => {
  const sections = visibleSections(adminNav);
  expect(sections).toHaveLength(8);
  const paths = sections.flatMap(s => s.items.map(i => i.path));
  expect(paths.sort()).toEqual(adminNav.map(i => i.path).sort());
  expect(new Set(paths).size).toBe(paths.length);
  const restricted = visibleSections(adminNav.filter(i => i.path === "payment-providers"));
  expect(restricted).toHaveLength(1);
  expect(restricted[0]?.items[0]?.path).toBe("payment-providers");
});

it.each(["payment-providers", "plugins", "appearance", "announcements", "content"])("opens %s without duplicate settings navigation", (path) => {
  render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter initialEntries={["/" + path]}><Frame /></MemoryRouter></ConfigProvider>);
  expect(screen.queryByRole("navigation", { name: "设置分类" })).not.toBeInTheDocument();
  expect(screen.queryByRole("heading", { name: "系统配置" })).not.toBeInTheDocument();
});

it("keeps inner navigation for configuration and leaves it when opening an independent page", () => {
  render(<ConfigProvider theme={{ token: { motion: false } }}><MemoryRouter initialEntries={["/notifications"]}><Frame /></MemoryRouter></ConfigProvider>);
  const nav = screen.getByRole("navigation", { name: "设置分类" });
  expect(screen.getByRole("heading", { name: "系统配置" })).toBeInTheDocument();
  expect(within(nav).getByText("通知配置")).toBeInTheDocument();
  expect(within(nav).getByText("安全与运维")).toBeInTheDocument();
  fireEvent.click(within(nav).getByText("安全与访问"));
  expect(within(nav).getByText("安全与访问").closest("li")).toHaveClass("ant-menu-item-selected");
  fireEvent.click(screen.getByText("知识库"));
  expect(screen.queryByRole("navigation", { name: "设置分类" })).not.toBeInTheDocument();
});

import { groupedSidebar, sidebarGroups } from "../src/app/sections";
it("keeps every destination reachable and selects the available configuration entry for restricted accounts", () => {
  expect(["overview", ...sidebarGroups.flatMap(g => g.entries.flatMap(e => e.paths))].sort()).toEqual(adminNav.map(n => n.path).sort());
  const nav = groupedSidebar(adminNav.filter(n => n.path === "audit"), "audit");
  expect(nav.selected).toBe("audit");
  expect(nav.openKeys).toEqual(["group:系统管理"]);
});

it("routes node pools to the real pools page and keeps user groups separate", () => {
  expect(adminNav.find(item => item.path === "node-pools")).toMatchObject({
    title: "权限组", permission: "node.read",
  });
  expect(sidebarGroups.flatMap(g => g.entries).find(e => e.paths.includes("node-pools"))).toMatchObject({ title: "权限组" });
  expect(sidebarGroups.flatMap(g => g.entries).find(e => e.paths.includes("user-groups"))).toMatchObject({ title: "用户" });
});

it("keeps merged user pages clickable and highlights the current page", () => {
  render(<MemoryRouter initialEntries={["/users"]}><Frame /></MemoryRouter>);
  const pages = screen.getByRole("navigation", { name: "相关管理页面" });
  for (const title of ["用户组管理", "设备限制", "批量运营", "用户"]) {
    fireEvent.click(within(pages).getByRole("link", { name: title }));
    expect(within(pages).getByRole("link", { name: title })).toHaveAttribute("aria-current", "page");
  }
});

it("keeps pending payments reachable from the merged order entry", () => {
  render(<MemoryRouter initialEntries={["/orders"]}><Frame /></MemoryRouter>);
  const pages = screen.getByRole("navigation", { name: "相关管理页面" });
  fireEvent.click(within(pages).getByRole("link", { name: "待处理款项" }));
  expect(within(pages).getByRole("link", { name: "待处理款项" })).toHaveAttribute("aria-current", "page");
  expect(screen.getByRole("menuitem", { name: /订单/ })).toHaveClass("ant-menu-item-selected");
});

it("filters related pages by permission and retains the available order destination", () => {
  auth.permissions = ["billing.order.read"];
  render(<MemoryRouter initialEntries={["/orders"]}><Frame /></MemoryRouter>);
  expect(screen.queryByRole("link", { name: "待处理款项" })).not.toBeInTheDocument();
  expect(screen.getByRole("menuitem", { name: /订单/ })).toHaveClass("ant-menu-item-selected");
});

it("allows collapsing groups and opens the destination group when following another page link", () => {
  const { container, rerender } = render(<MemoryRouter initialEntries={["/servers"]}><Frame /></MemoryRouter>);
  const group = screen.getByText("服务器管理").closest(".ant-menu-submenu")!;
  fireEvent.click(group.querySelector(".ant-menu-submenu-title")!);
  expect(group).not.toHaveClass("ant-menu-submenu-open");
  // The sidebar component keeps its expansion state across URL changes.
  rerender(<MemoryRouter initialEntries={["/servers"]}><Frame /><Link to="/node-pools">从业务页面进入权限组</Link></MemoryRouter>);
  fireEvent.click(screen.getByText("从业务页面进入权限组"));
  expect(container.querySelector(".ant-menu-item-selected")).toHaveTextContent("权限组");
  expect(screen.getByText("服务器管理").closest(".ant-menu-submenu")).toHaveClass("ant-menu-submenu-open");
});
