import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { RefundWorkspace } from "../src/features/admin/Refunds";
import { RefundCommand } from "../src/core/refunds";

const actor = "10000000-0000-4000-8000-000000000001";
const order = "20000000-0000-4000-8000-000000000001";
const source = "30000000-0000-4000-8000-000000000001";
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });

it("requires explicit allocation, prevents duplicate submission, restores an unknown request and enforces returned reviewer policy", async () => {
  localStorage.setItem("aegis_admin_token", "test-token");
  let finish!: (value: Response) => void;
  let sent: Record<string, unknown> | undefined;
  const fetcher = vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me"))
      return response({
        user_id: actor,
        permissions: [
          "billing.ledger.read",
          "billing.refund.request",
          "billing.refund.approve",
        ],
      });
    if (path.endsWith("/refund-preview"))
      return response({
        order_id: order,
        currency: "CNY",
        paid_amount_minor: "1000",
        refunded_amount_minor: "0",
        reserved_amount_minor: "0",
        available_amount_minor: "1000",
        policy_version: "refund-policy-v1",
        allowed_entitlement_actions: ["retain", "revoke_order_subscription"],
        requires_second_reviewer: true,
        sources: [
          {
            source_kind: "payment",
            source_id: source,
            provider_code: "demo_hmac",
            available_amount_minor: "1000",
          },
        ],
      });
    if (
      path.endsWith(`/orders/${order}/refunds`) &&
      options?.method === "POST"
    ) {
      sent = JSON.parse(String(options.body)) as Record<string, unknown>;
      return new Promise<Response>((resolve) => {
        finish = resolve;
      });
    }
    if (path.endsWith("/v1/refunds"))
      return response({ items: [], limit: 100 });
    if (sent && path.endsWith(`/v1/refunds/${String(sent.operation_id)}`))
      return response({
        operation_id: sent.operation_id,
        order_id: order,
        requested_by: actor,
        currency: "CNY",
        amount_minor: "1000",
        status: "pending_review",
        version: "1",
        entitlement_action: "retain",
        requires_second_reviewer: true,
        legs: [
          {
            id: "40000000-0000-4000-8000-000000000001",
            source_kind: "payment",
            source_id: source,
            amount_minor: "1000",
            status: "pending",
          },
        ],
      });
    throw new Error(`Unexpected route ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(
    [{ path: "/orders/:id", element: <RefundWorkspace orderId={order} /> }],
    { initialEntries: [`/orders/${order}`] },
  );
  render(
    <ConfigProvider theme={{ token: { motion: false } }}>
      <App>
        <QueryClientProvider client={client}>
          <DialogHost>
            <AuthProvider>
              <RouterProvider router={router} />
            </AuthProvider>
          </DialogHost>
        </QueryClientProvider>
      </App>
    </ConfigProvider>,
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "发起退款申请" }),
  );
  await userEvent.type(screen.getByLabelText("申请金额（元）"), "10.00");
  await userEvent.click(screen.getByRole("button", { name: "读取可退款来源" }));
  const allocation = await screen.findByPlaceholderText("本来源退款金额（元）");
  expect(allocation).toHaveValue("");
  await userEvent.type(
    screen.getByLabelText("申请原因"),
    "客户主动申请退还本次订单款项",
  );
  await userEvent.click(
    screen.getByRole("button", { name: "冻结参数并提交申请" }),
  );
  expect(await screen.findByText(/请明确分配退款来源/)).toBeVisible();
  expect(sent).toBeUndefined();
  await userEvent.type(allocation, "10.00");
  const submit = screen.getByRole("button", { name: "冻结参数并提交申请" });
  fireEvent.click(submit);
  fireEvent.click(submit);
  await waitFor(() => expect(sent).toBeDefined());
  expect(
    fetcher.mock.calls.filter((call) =>
      String(call[0]).endsWith(`/orders/${order}/refunds`),
    ),
  ).toHaveLength(1);
  await act(async () => {
    finish(response({ error: { message: "temporary unavailable" } }, 503));
  });
  expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("unknown");
  expect(screen.getByRole("button", { name: "发起退款申请" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "按退款号核对" }));
  expect(await screen.findByText("本申请需要另一位管理员批准")).toBeVisible();
  expect(screen.getByRole("button", { name: "批准申请" })).toBeDisabled();
  expect(screen.getByRole("button", { name: "撤回申请" })).toBeEnabled();
  expect(
    screen.queryByRole("button", { name: "提交退款执行" }),
  ).not.toBeInTheDocument();
  expect(screen.getByText("审批通过后才能执行退款。")).toBeVisible();
  expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("confirmed");
  expect(router.state.location.search).toContain(String(sent?.operation_id));
});
