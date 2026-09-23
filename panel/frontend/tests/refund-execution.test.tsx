import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { RefundWorkspace } from "../src/features/admin/Refunds";
import { RefundCommand, type Refund } from "../src/core/refunds";

const actor = "10000000-0000-4000-8000-000000000001";
const order = "20000000-0000-4000-8000-000000000001";
const id = "50000000-0000-4000-8000-000000000001";
const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });

// Multi-step business flow: allow cold module loading and real UI events; assertions remain unchanged.
it("hashes evidence locally, requires actual-payment confirmation, and preserves one external action for local settlement retry", async () => {
  localStorage.setItem("aegis_admin_token", "test-token");
  let current: Refund = {
    operation_id: id,
    order_id: order,
    requested_by: actor,
    currency: "CNY",
    amount_minor: "1000",
    status: "manual_required",
    version: "3",
    entitlement_action: "retain",
    requires_second_reviewer: false,
    manual_reason: "provider_refund_unsupported",
    legs: [
      {
        id: "40000000-0000-4000-8000-000000000001",
        source_kind: "payment",
        source_id: "30000000-0000-4000-8000-000000000001",
        amount_minor: "1000",
        status: "manual_required",
        financial_state: "awaiting_evidence",
      },
    ],
  };
  const writes: RequestInit[] = [];
  const fetcher = vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me"))
      return response({
        user_id: actor,
        permissions: ["billing.ledger.read", "billing.refund.execute"],
      });
    if (path.endsWith("/record-external-result")) {
      writes.push(options!);
      if (writes.length === 1)
        return response(
          {
            error: {
              message: "fixture ledger failure after evidence recorded",
            },
          },
          503,
        );
      current = {
        ...current,
        version: "4",
        status: "processing",
        legs: current.legs.map((leg) => ({
          ...leg,
          financial_state: "provider_succeeded",
          external_result: {
            provider_refund_id: "fixture-channel-refund",
            source_kind: "manual",
            recorded_at: "2026-09-05T00:00:00Z",
          },
        })),
      };
      return response(current);
    }
    if (path.endsWith("/v1/refunds"))
      return response({ items: [current], limit: 100 });
    if (path.endsWith(`/v1/refunds/${id}`)) return response(current);
    throw new Error(`Unexpected fixture route ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(
    [{ path: "/orders/:id", element: <RefundWorkspace orderId={order} /> }],
    { initialEntries: [`/orders/${order}?refund=${id}`] },
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
    await screen.findByRole("button", { name: "登记渠道已退款凭据" }),
  );
  expect(screen.getByRole("button", { name: "提交退款执行" })).toBeDisabled();
  await userEvent.type(
    screen.getByLabelText("渠道退款流水号"),
    "fixture-channel-refund",
  );
  await userEvent.type(
    screen.getByLabelText("可复核的凭证存放位置或渠道记录"),
    "虚构测试归档/fixture-evidence.txt",
  );
  const file = new File(["abc"], "fixture-evidence.txt", {
    type: "text/plain",
  });
  Object.defineProperty(file, "arrayBuffer", {
    value: async () => new TextEncoder().encode("abc").buffer,
  });
  await userEvent.upload(screen.getByLabelText("本机退款凭证文件"), file);
  await waitFor(() =>
    expect(screen.getByLabelText("凭证校验值")).toHaveValue(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    ),
  );
  const submit = screen.getByRole("button", { name: "保存凭据并核对本地入账" });
  await userEvent.click(submit);
  expect(
    await screen.findByText("请先核实原支付渠道已完成此项退款"),
  ).toBeVisible();
  expect(writes).toHaveLength(0);
  await userEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(submit);
  fireEvent.click(submit);
  await waitFor(() =>
    expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("unknown"),
  );
  expect(writes).toHaveLength(1);
  expect(screen.getByLabelText("渠道退款流水号")).toHaveValue(
    "fixture-channel-refund",
  );
  await userEvent.click(
    screen.getByRole("button", { name: "原凭据重试本地结算" }),
  );
  expect(await screen.findByText("渠道已退款，本地入账待核对")).toBeVisible();
  expect(writes).toHaveLength(2);
  expect(writes[0]?.body).toBe(writes[1]?.body);
  expect(JSON.parse(String(writes[0]?.body))).toMatchObject({
    leg_id: current.legs[0]!.id,
    amount_minor: "1000",
    currency: "CNY",
    provider_refund_id: "fixture-channel-refund",
  });
  expect(
    fetcher.mock.calls.some((call) => String(call[0]).endsWith("/execute")),
  ).toBe(false);
  expect(
    screen.queryByText("退款已完成，本地账本已入账"),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "原凭据重试本地结算" }),
  ).toBeEnabled();
});

it("recovers a lost queue acknowledgment by querying approved then replaying the same action, and blocks channel-unknown execution", async () => {
  localStorage.setItem("aegis_admin_token", "test-token");
  let current: Refund = {
    operation_id: id,
    order_id: order,
    requested_by: actor,
    currency: "CNY",
    amount_minor: "1000",
    status: "approved",
    version: "2",
    entitlement_action: "retain",
    legs: [
      {
        id: "40000000-0000-4000-8000-000000000001",
        source_kind: "payment",
        source_id: "30000000-0000-4000-8000-000000000001",
        amount_minor: "1000",
        status: "pending",
        financial_state: "awaiting_evidence",
      },
    ],
  };
  const writes: RequestInit[] = [];
  const fetcher = vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me"))
      return response({
        user_id: actor,
        permissions: ["billing.ledger.read", "billing.refund.execute"],
      });
    if (path.endsWith("/execute")) {
      writes.push(options!);
      if (writes.length === 1)
        return response(
          { error: { message: "fixture lost queue acknowledgment" } },
          503,
        );
      current = { ...current, status: "unknown", version: "3" };
      return response(current, 202);
    }
    if (path.endsWith("/v1/refunds"))
      return response({ items: [current], limit: 100 });
    if (path.endsWith(`/v1/refunds/${id}`)) return response(current);
    throw new Error(`Unexpected fixture route ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(
    [{ path: "/orders/:id", element: <RefundWorkspace orderId={order} /> }],
    { initialEntries: [`/orders/${order}?refund=${id}`] },
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
    await screen.findByRole("button", { name: "提交退款执行" }),
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "确认提交到退款队列" }),
  );
  await waitFor(() =>
    expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("unknown"),
  );
  await userEvent.click(screen.getByRole("button", { name: /取\s*消/ }));
  expect(
    screen.queryByRole("button", { name: "按原操作核对或重试入队" }),
  ).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "按退款号核对" }));
  await userEvent.click(
    await screen.findByRole("button", { name: "按原操作核对或重试入队" }),
  );
  await waitFor(() =>
    expect(RefundCommand.restore(actor)[0]?.saved.state).toBe("confirmed"),
  );
  expect(writes).toHaveLength(2);
  expect(writes[0]?.body).toBe(writes[1]?.body);
  expect(screen.getByRole("button", { name: "提交退款执行" })).toBeDisabled();
  expect(
    screen.queryByRole("button", { name: "按原操作核对或重试入队" }),
  ).not.toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "登记渠道已退款凭据" }),
  ).toBeEnabled();
});
