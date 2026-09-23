import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { TelegramSettings } from "../src/features/admin/TelegramSettings";

it("keeps an existing token when blank and reads saved settings back without sending messages", async () => {
  localStorage.setItem("aegis_admin_token", "fixture-token");
  const writes: unknown[] = [];
  let username = "original_bot";
  let reads = 0;
  vi.stubGlobal("fetch", vi.fn(async (input: string, options?: RequestInit) => {
    const path = new URL(input).pathname;
    if (path.endsWith("/v1/me")) return new Response(JSON.stringify({ user_id: "admin", permissions: ["security.audit.read", "platform.settings.write"] }));
    expect(path).toMatch(/\/settings\/telegram$/);
    if (options?.method === "POST") {
      const body = JSON.parse(String(options.body)); writes.push(body); username = body.bot_username;
      return new Response(JSON.stringify({ ok: true }));
    }
    reads++;
    return new Response(JSON.stringify({ enabled: true, bot_username: username, has_token: true }));
  }));
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><DialogHost><AuthProvider><TelegramSettings /></AuthProvider></DialogHost></QueryClientProvider></App></ConfigProvider>);
  await screen.findByText("original_bot");
  fireEvent.click(screen.getByRole("button", { name: "编辑机器人配置" }));
  const field = await screen.findByLabelText("机器人用户名（可省略 @）");
  fireEvent.change(field, { target: { value: " @updated_bot " } });
  expect(screen.getByLabelText("Bot Token（留空保留）")).toHaveValue("");
  fireEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(writes).toEqual([{ enabled: true, bot_username: "updated_bot", bot_token: "" }]));
  await screen.findByText("updated_bot");
  expect(reads).toBeGreaterThanOrEqual(2);
});
