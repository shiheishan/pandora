/**
 * [INPUT]: 依赖 src/features/admin/ManualOrder 的 ManualOrderAction、src/core/operations 的 Operation 恢复记录、auth 与 dialogs 外壳
 * [OUTPUT]: 对外提供人工开单的 vitest 用例：显式选价、回执丢失后重挂载恢复且不重复下单、无权限不加载、损坏恢复记录阻断新单
 * [POS]: tests 下人工开单写路径的回归守卫；恢复记录只由 React 的 Operation 写入，旧单页人工开单不写恢复记录，两者不共享格式
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { DialogHost } from "../src/core/dialogs";
import { AuthProvider } from "../src/core/auth";
import { ManualOrderAction } from "../src/features/admin/ManualOrder";
import { Operation } from "../src/core/operations";
const user = "00000000-0000-4000-8000-000000000001",
  plan = "00000000-0000-4000-8000-000000000002",
  month = "00000000-0000-4000-8000-000000000003",
  year = "00000000-0000-4000-8000-000000000004";
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(
    [{ path: "/orders", element: <ManualOrderAction /> }],
    { initialEntries: ["/orders"] },
  );
  return render(
    <ConfigProvider theme={{ token: { motion: false } }}>
      <AntApp>
        <QueryClientProvider client={client}>
          <DialogHost>
            <AuthProvider>
              <RouterProvider router={router} />
            </AuthProvider>
          </DialogHost>
        </QueryClientProvider>
      </AntApp>
    </ConfigProvider>,
  );
}
it("selects the explicit price and restores an uncertain manual order after remount without duplicate intent", async () => {
  localStorage.setItem("aegis_admin_token", "fixture-token");
  const attempts: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, options?: RequestInit) => {
      const path = new URL(input).pathname;
      if (path.endsWith("/orders/manual")) {
        attempts.push(options!);
        if (attempts.length === 1)
          throw new TypeError("lost committed response");
        return new Response(
          JSON.stringify({
            order_id: crypto.randomUUID(),
            order_no: "QA-ORDER-1",
            status: "fulfilled",
            total_amount: 0,
            payable_amount: 0,
          }),
        );
      }
      return new Response(
        JSON.stringify(
          path.endsWith("/me")
            ? {
                user_id: "admin",
                permissions: [
                  "billing.order.write",
                  "iam.user.read",
                  "catalog.read",
                ],
              }
            : path.endsWith("/users")
              ? {
                  users: [
                    { id: user, email: "qa@example.invalid", status: "active" },
                  ],
                }
              : {
                  plans: [
                    {
                      id: plan,
                      name: "隐藏套餐",
                      status: "active",
                      visibility: "hidden",
                      current_version_id: "v1",
                      prices: [
                        {
                          id: month,
                          status: "active",
                          unit_amount: 100,
                          currency: "CNY",
                          billing_interval: "month",
                          interval_count: 1,
                        },
                        {
                          id: year,
                          status: "active",
                          unit_amount: 1000,
                          currency: "CNY",
                          billing_interval: "year",
                          interval_count: 1,
                        },
                      ],
                    },
                  ],
                },
        ),
      );
    }),
  );
  const view = mount();
  fireEvent.click(await screen.findByRole("button", { name: "人工开单" }));
  fireEvent.mouseDown(await screen.findByLabelText("接收用户"));
  fireEvent.click(await screen.findByText("qa@example.invalid"));
  fireEvent.mouseDown(screen.getByLabelText("赠送套餐"));
  fireEvent.click(await screen.findByText("隐藏套餐 · 隐藏"));
  await waitFor(() =>
    expect(screen.getByLabelText("订阅周期与原价")).toBeEnabled(),
  );
  fireEvent.mouseDown(screen.getByLabelText("订阅周期与原价"));
  fireEvent.click(await screen.findByText(/1 年 ·/));
  fireEvent.change(screen.getByLabelText("赠送原因"), {
    target: { value: "授权客服补偿工单123" },
  });
  fireEvent.click(screen.getByRole("button", { name: "赠送并立即开通" }));
  await waitFor(() => expect(attempts).toHaveLength(1));
  await screen.findByRole("button", { name: "重试原订单" });
  expect(screen.getByLabelText("赠送原因")).toBeDisabled();
  expect(JSON.parse(String(attempts[0]!.body))).toEqual({
    user_id: user,
    plan_id: plan,
    price_id: year,
    reason: "授权客服补偿工单123",
  });
  expect(Operation.restore("admin")).toHaveLength(0);
  expect(Operation.restore("another-admin", "manual-order")).toHaveLength(0);
  expect(Operation.restore("admin", "manual-order")).toHaveLength(1);
  view.unmount();
  mount();
  const retry = await screen.findByRole("button", { name: "核实并重试原订单" });
  expect(screen.getByRole("button", { name: "人工开单" })).toBeDisabled();
  fireEvent.click(retry);
  await waitFor(() => expect(attempts).toHaveLength(2));
  expect(attempts[0]!.body).toBe(attempts[1]!.body);
  expect(
    (attempts[0]!.headers as Record<string, string>)["Idempotency-Key"],
  ).toBe((attempts[1]!.headers as Record<string, string>)["Idempotency-Key"]);
  await waitFor(() =>
    expect(
      screen.queryByRole("button", { name: "核实并重试原订单" }),
    ).not.toBeInTheDocument(),
  );
  expect(sessionStorage.length).toBe(0);
  expect(screen.getByRole("button", { name: "人工开单" })).toBeEnabled();
});
it("hides writes without order permission and never loads users or plans", async () => {
  localStorage.setItem("aegis_admin_token", "fixture-token");
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      calls.push(input);
      return new Response(
        JSON.stringify({
          user_id: "reader",
          permissions: ["billing.order.read"],
        }),
      );
    }),
  );
  mount();
  await waitFor(() => expect(calls.length).toBeGreaterThan(0));
  expect(
    screen.queryByRole("button", { name: "人工开单" }),
  ).not.toBeInTheDocument();
  expect(calls.every((p) => p.endsWith("/v1/me"))).toBe(true);
});

it("blocks replacement manual orders when the current administrator recovery record is corrupt", async () => {
  localStorage.setItem("aegis_admin_token", "fixture-token");
  sessionStorage.setItem("pandora:operation:qa-admin:broken", "{broken");
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string) => {
      calls.push(input);
      return new Response(
        JSON.stringify({ user_id: "qa-admin", permissions: ["*"] }),
      );
    }),
  );
  mount();
  await screen.findByRole("button", { name: "人工开单" });
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "人工开单" })).toBeDisabled(),
  );
  expect(screen.getByText(/操作恢复记录无法核实/)).toBeInTheDocument();
  expect(calls.every((p) => p.endsWith("/v1/me"))).toBe(true);
  expect(sessionStorage.getItem("pandora:operation:qa-admin:broken")).toBe(
    "{broken",
  );
});
