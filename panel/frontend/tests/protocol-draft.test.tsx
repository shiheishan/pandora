import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import FormDialog from "../src/core/FormDialog";
import { protocolChanges } from "../src/features/admin/ProtocolEditor";
function setup(required = false) {
  const settle = vi.fn(), submit = vi.fn().mockResolvedValue(undefined);
  const initial = { network: "tcp", "network_settings.path": "/old", "reality_settings.private_key": "" };
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><FormDialog active settle={settle} entry={{ id: 1, resolve: vi.fn(), title: "协议配置", protectDraft: true, initial, fields: [
    { name: "network", label: "传输协议", group: "基础参数" },
    { name: "network_settings.path", label: "传输路径", group: "传输参数" },
    { name: "reality_settings.private_key", label: "私钥", group: "安全与证书", type: "password", required },
  ], onSubmit: submit }} /></App></ConfigProvider>);
  return { settle, submit, initial };
}
it("keeps values across tabs, asks before discard, and submits only the edited delta", async () => {
  const { settle, submit, initial } = setup();
  await userEvent.click(screen.getByRole("tab", { name: "传输参数" }));
  await userEvent.clear(screen.getByLabelText("传输路径")); await userEvent.type(screen.getByLabelText("传输路径"), "/new");
  await userEvent.click(screen.getByRole("tab", { name: "基础参数" }));
  await userEvent.click(screen.getByRole("button", { name: /取\s*消/ }));
  expect(await screen.findByText("有尚未保存的修改")).toBeVisible(); expect(settle).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "继续编辑" }));
  await userEvent.click(screen.getByRole("tab", { name: "传输参数" })); expect(screen.getByLabelText("传输路径")).toHaveValue("/new");
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(submit).toHaveBeenCalled());
  expect(submit.mock.calls[0]?.[0]).toEqual({ ...initial, "network_settings.path": "/new" });
  expect(protocolChanges({ sensitive_properties: ["private_key"] }, initial, submit.mock.calls[0]![0], {})).toEqual({ network_settings: { path: "/new" } });
  expect(settle).toHaveBeenCalledWith(1, true);
});
it("reveals the tab containing an invalid required field instead of silently blocking save", async () => {
  const { submit } = setup(true);
  await userEvent.click(screen.getByRole("button", { name: /保\s*存/ }));
  await waitFor(() => expect(screen.getByRole("tab", { name: "安全与证书" })).toHaveAttribute("aria-selected", "true"));
  expect(screen.getByText("请填写私钥")).toBeVisible();
  expect(submit).not.toHaveBeenCalled();
});
it("explicit discard does not send a write, while unchanged tabs close without a warning", async () => {
  const { settle, submit } = setup();
  await userEvent.click(screen.getByRole("tab", { name: "安全与证书" }));
  await userEvent.click(screen.getByRole("button", { name: /取\s*消/ })); expect(settle).toHaveBeenCalledWith(1, false);
  settle.mockClear(); await userEvent.type(screen.getByLabelText("私钥"), "fixture-replacement");
  await userEvent.click(screen.getByRole("button", { name: /取\s*消/ }));
  await userEvent.click(screen.getByRole("button", { name: "放弃修改并关闭" })); expect(settle).toHaveBeenCalledWith(1, false); expect(submit).not.toHaveBeenCalled();
});
