import { StrictMode, type ReactNode } from "react";
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App as AntApp, Button, ConfigProvider } from "antd";
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, Link, RouterProvider } from "react-router-dom";
import { DialogHost, useDialog } from "../src/core/dialogs";
import { AuthProvider, useAuth } from "../src/core/auth";
import { useDraft } from "../src/core/drafts";
import { ResourcePage } from "../src/components/common";
import { NodeDetail } from "../src/features/admin/Nodes";

const response = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status });

it("stops subscriber delivery through the serving endpoint without changing the legacy node lifecycle", async () => {
  localStorage.setItem("aegis_admin_token", "test-token");
  let payload: Record<string, unknown> | undefined;
  const fetcher = vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me"))
      return response({
        user_id: "admin-stable",
        permissions: ["node.read", "node.lifecycle"],
      });
    if (path.endsWith("/v1/nodes/status:batch")) {
      payload = JSON.parse(String(options?.body)) as Record<string, unknown>;
      return response({ ok: true, updated: 1, serving_status: "disabled" });
    }
    if (path.endsWith("/v1/nodes/node-a"))
      return response({
            id: "node-a",
            name: "香港线路",
            row_version: 7,
            status: "active",
            serving_status: payload ? "disabled" : "active",
      });
    if (path.endsWith("/metrics"))
      return response({ latest: null, points: [] });
    throw new Error(`Unexpected request ${path}`);
  });
  vi.stubGlobal("fetch", fetcher);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(
    [{ path: "/nodes/:id", element: <NodeDetail /> }],
    { initialEntries: ["/nodes/node-a"] },
  );
  render(
    <App>
      <QueryClientProvider client={client}>
        <DialogHost>
          <AuthProvider>
            <RouterProvider router={router} />
          </AuthProvider>
        </DialogHost>
      </QueryClientProvider>
    </App>,
  );
  await userEvent.click(
    await screen.findByRole("button", { name: "停止下发" }),
  );
  await userEvent.type(await screen.findByLabelText("原因"), "线路检修，暂时停止下发");
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() =>
    expect(payload).toEqual({
      items: [{ id: "node-a", row_version: 7 }],
      serving_status: "disabled",
      reason: "线路检修，暂时停止下发",
    }),
  );
  expect(
    fetcher.mock.calls.some((call) =>
      String(call[0]).includes("/node-a/status"),
    ),
  ).toBe(false);
});
function App({ children }: { children: ReactNode }) {
  return (
    <ConfigProvider theme={{ token: { motion: false } }}>
      <AntApp>{children}</AntApp>
    </ConfigProvider>
  );
}
function DialogHarness({ done }: { done: (value: boolean) => void }) {
  const open = useDialog();
  return (
    <Button
      onClick={() => {
        void open({
          title: "确认删除",
          fields: [{ name: "reason", label: "原因", required: true }],
          onSubmit: async () => {
            throw new Error("服务器拒绝");
          },
        }).then(done);
      }}
    >
      打开
    </Button>
  );
}
function DraftHarness({
  subject = "A",
  entity = "ticket",
}: {
  subject?: string;
  entity?: string;
}) {
  const draft = useDraft(subject, entity);
  return (
    <input
      aria-label="draft"
      value={draft.value}
      onChange={(event) => draft.setValue(event.target.value)}
    />
  );
}
function ReauthHarness() {
  const { api, token, ready } = useAuth();
  return (
    <>
      <output data-testid="token">{token}</output>
      <Button
        disabled={!ready}
        onClick={() => {
          void api.write("v1/protected", {}).catch(() => {});
        }}
      >
        需要验证的操作
      </Button>
    </>
  );
}
function DoubleSubmit({ submit }: { submit: () => Promise<unknown> }) {
  const open = useDialog();
  return (
    <Button
      onClick={() =>
        void open({
          title: "双击提交",
          fields: [{ name: "reason", label: "理由" }],
          onSubmit: submit,
        })
      }
    >
      打开表单
    </Button>
  );
}
describe("dialog lifecycle", () => {
  it("Escape resolves once under StrictMode and allows another dialog", async () => {
    const done = vi.fn();
    const user = userEvent.setup();
    render(
      <StrictMode>
        <App>
          <DialogHost>
            <DialogHarness done={done} />
          </DialogHost>
        </App>
      </StrictMode>,
    );
    await user.click(screen.getByRole("button", { name: /打.*开/ }));
    await screen.findByRole("dialog");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(done).toHaveBeenCalledExactlyOnceWith(false));
    await user.click(screen.getByRole("button", { name: /打.*开/ }));
    await screen.findByRole("dialog");
    await user.click(screen.getByRole("button", { name: /取.*消/ }));
    await waitFor(() => expect(done).toHaveBeenCalledTimes(2));
  });
  it("hash navigation and unmount settle outstanding callers", async () => {
    const done = vi.fn();
    const user = userEvent.setup();
    const view = render(
      <App>
        <DialogHost>
          <DialogHarness done={done} />
        </DialogHost>
      </App>,
    );
    await user.click(screen.getByRole("button", { name: /打.*开/ }));
    act(() => window.dispatchEvent(new HashChangeEvent("hashchange")));
    await waitFor(() => expect(done).toHaveBeenCalledExactlyOnceWith(false));
    await user.click(screen.getByRole("button", { name: /打.*开/ }));
    view.unmount();
    await waitFor(() => expect(done).toHaveBeenCalledTimes(2));
  });
  it("failed form preserves entered content and is not silently stuck in StrictMode", async () => {
    const user = userEvent.setup();
    render(
      <StrictMode>
        <App>
          <DialogHost>
            <DialogHarness done={vi.fn()} />
          </DialogHost>
        </App>
      </StrictMode>,
    );
    await user.click(screen.getByRole("button", { name: /打.*开/ }));
    await user.type(await screen.findByLabelText("原因"), "不能丢失这个原因");
    await user.click(screen.getByRole("button", { name: /保.*存/ }));
    await waitFor(() => expect(screen.getByText("服务器拒绝")).toBeVisible());
    expect(await screen.findByLabelText("原因")).toHaveValue("不能丢失这个原因");
    expect(screen.getByRole("button", { name: /保.*存/ })).not.toHaveClass(
      "ant-btn-loading",
    );
  });
  it("locks synchronously before async validation so click and Enter cannot submit twice", async () => {
    let finish!: () => void;
    const submit = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    render(
      <App>
        <DialogHost>
          <DoubleSubmit submit={submit} />
        </DialogHost>
      </App>,
    );
    await userEvent.click(screen.getByText("打开表单"));
    const button = screen.getByRole("button", { name: /保.*存/ });
    act(() => {
      fireEvent.click(button);
      fireEvent.click(button);
      fireEvent.submit(screen.getByLabelText("理由").closest("form")!);
    });
    await waitFor(() => expect(submit).toHaveBeenCalledOnce());
    await act(async () => finish());
  });
  it("closing in-flight reauthentication aborts it and ignores a late token, then permits a new attempt", async () => {
    localStorage.setItem("aegis_admin_token", "original-token");
    let finish!: (value: Response) => void;
    let requestSignal: AbortSignal | undefined;
    const fetcher = vi.fn(
      (input: string | URL | Request, options?: RequestInit) => {
        const url = String(input);
        if (url.endsWith("/v1/me"))
          return Promise.resolve(
            response({ user_id: "admin", permissions: ["*"] }),
          );
        if (url.endsWith("/v1/protected"))
          return Promise.resolve(
            response(
              {
                error: { code: "reauth_required", message: "Confirm identity" },
              },
              401,
            ),
          );
        if (url.endsWith("/v1/auth/reauth")) {
          requestSignal = options?.signal as AbortSignal;
          return new Promise<Response>((resolve) => {
            finish = resolve;
          });
        }
        return Promise.reject(new Error(url));
      },
    );
    vi.stubGlobal("fetch", fetcher);
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <App>
        <QueryClientProvider client={qc}>
          <DialogHost>
            <AuthProvider>
              <ReauthHarness />
            </AuthProvider>
          </DialogHost>
        </QueryClientProvider>
      </App>,
    );
    const user = userEvent.setup();
    await waitFor(() =>
      expect(
        screen.getByText("需要验证的操作").closest("button"),
      ).not.toBeDisabled(),
    );
    await user.click(screen.getByText("需要验证的操作"));
    await user.type(screen.getByLabelText("当前密码"), "not-a-real-secret");
    await user.click(screen.getByRole("button", { name: "确认身份" }));
    await waitFor(() => expect(finish).toBeDefined());
    await user.keyboard("{Escape}");
    await waitFor(() => expect(requestSignal?.aborted).toBe(true));
    await act(async () => {
      finish(response({ access_token: "late-token" }));
    });
    expect(screen.getByTestId("token")).toHaveTextContent("original-token");
    expect(localStorage.getItem("aegis_admin_token")).toBe("original-token");
    await user.click(screen.getByText("需要验证的操作"));
    expect(await screen.findByLabelText("当前密码")).toBeInTheDocument();
  });
});
describe("draft ownership", () => {
  it("saves before an immediate navigation and restores only its owner/entity", () => {
    const view = render(<DraftHarness />);
    fireEvent.change(screen.getByLabelText("draft"), {
      target: { value: "写到一半" },
    });
    view.unmount();
    const restored = render(<DraftHarness />);
    expect(screen.getByLabelText("draft")).toHaveValue("写到一半");
    restored.rerender(<DraftHarness subject="B" />);
    expect(screen.getByLabelText("draft")).toHaveValue("");
    restored.rerender(<DraftHarness subject="A" entity="other-ticket" />);
    expect(screen.getByLabelText("draft")).toHaveValue("");
  });
});
describe("scoped query navigation", () => {
  it("a late users response cannot overwrite an orders route; 503 does not render an empty success table", async () => {
    let late!: (value: Response) => void;
    localStorage.setItem("aegis_admin_token", "test");
    const fetcher = vi.fn((input: string | URL | Request) => {
      const url = String(input);
      if (url.endsWith("/v1/me"))
        return Promise.resolve(
          response({ user_id: "admin", permissions: ["*"] }),
        );
      if (url.includes("/v1/users"))
        return new Promise<Response>((resolve) => {
          late = resolve;
        });
      if (url.includes("/v1/orders"))
        return Promise.resolve(
          response({ error: { message: "订单服务临时不可用" } }, 503),
        );
      return Promise.reject(new Error(url));
    });
    vi.stubGlobal("fetch", fetcher);
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const router = createMemoryRouter(
      [
        {
          path: "/users",
          element: (
            <>
              <Link to="/orders">去订单</Link>
              <ResourcePage
                title="用户列表"
                resource="users"
                path="v1/users"
                listKey="users"
                columns={[{ title: "邮箱", dataIndex: "email" }]}
              />
            </>
          ),
        },
        {
          path: "/orders",
          element: (
            <ResourcePage
              title="订单列表"
              resource="orders"
              path="v1/orders"
              listKey="orders"
              columns={[{ title: "单号", dataIndex: "order_no" }]}
            />
          ),
        },
      ],
      { initialEntries: ["/users"] },
    );
    render(
      <App>
        <QueryClientProvider client={qc}>
          <DialogHost>
            <AuthProvider>
              <RouterProvider router={router} />
            </AuthProvider>
          </DialogHost>
        </QueryClientProvider>
      </App>,
    );
    await waitFor(() => expect(late).toBeDefined());
    await userEvent.click(screen.getByText("去订单"));
    expect(await screen.findByText("订单服务临时不可用")).toBeVisible();
    await act(async () => {
      late(response({ users: [{ id: "u1", email: "old-user@example.test" }] }));
    });
    expect(screen.getByText("订单列表")).toBeVisible();
    expect(screen.queryByText("old-user@example.test")).not.toBeInTheDocument();
    expect(screen.queryByText("暂无符合条件的数据")).not.toBeInTheDocument();
  });
});
