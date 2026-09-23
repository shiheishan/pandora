import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { TicketsPage } from "../src/features/Tickets";

vi.mock("../src/core/runtime", async (importOriginal) => {
  const original = await importOriginal<typeof import("../src/core/runtime")>();
  return {
    ...original,
    runtime: { ...original.runtime, domain: "portal" },
    tokenKey: "aegis_token",
  };
});
it("keeps the new ticket draft and current route when HTTP200 omits the created ticket ID", async () => {
  localStorage.setItem("aegis_token", "portal-test-token");
  const fetcher = vi.fn(async (input: string, options?: RequestInit) => {
    if (new URL(input).pathname.endsWith("/v1/me"))
      return new Response(
        JSON.stringify({ id: "portal-user", email: "test@example.test" }),
      );
    if (new URL(input).pathname.endsWith("/v1/support/tickets")) {
      if (options?.method === "POST")
        return new Response(JSON.stringify({ tickets: [] }));
      return new Response(JSON.stringify({ tickets: [] }));
    }
    throw new Error("Unexpected route");
  });
  vi.stubGlobal("fetch", fetcher);
  const router = createMemoryRouter(
    [
      { path: "/support", element: <TicketsPage /> },
      { path: "/support/:id", element: <p>错误跳转</p> },
    ],
    { initialEntries: ["/support"] },
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
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
    await screen.findByRole("button", { name: "提交工单" }),
  );
  const body = "无法更新订阅，请保留这段问题说明以便稍后继续处理";
  await userEvent.type(await screen.findByLabelText("问题说明"), body);
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await screen.findByText(/结果待核实/);
  expect(screen.getByLabelText("问题说明")).toHaveValue(body);
  expect(router.state.location.pathname).toBe("/support");
  await waitFor(() =>
    expect(
      Array.from({ length: sessionStorage.length }, (_, index) =>
        sessionStorage.getItem(sessionStorage.key(index)!),
      ).some((value) => value?.includes(body)),
    ).toBe(true),
  );
  expect(screen.queryByText("错误跳转")).not.toBeInTheDocument();
});
