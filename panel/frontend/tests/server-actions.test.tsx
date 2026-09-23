import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { beforeEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ api: { get: vi.fn(), write: vi.fn() }, open: vi.fn(), can: vi.fn() }));
vi.mock("../src/core/auth", () => ({ useAuth: () => ({ api: mocks.api, can: mocks.can }) }));
vi.mock("../src/core/dialogs", () => ({ useDialog: () => mocks.open }));
import { ServerActions } from "../src/features/admin/ServerActions";
beforeEach(() => { vi.resetAllMocks(); mocks.can.mockReturnValue(true); mocks.api.get.mockResolvedValue({ id: "s", name: "台湾测试机", row_version: 12, status: "draft" }); mocks.api.write.mockResolvedValue({ ok: true }); });
function mount(onDeleted = vi.fn()) {
  render(<ConfigProvider theme={{ token: { motion: false } }}><App><ServerActions id="s" onDeleted={onDeleted} /></App></ConfigProvider>);
}
it("requires matching server name and uses the fresh CAS version for deletion", async () => {
  const deleted = vi.fn(); mount(deleted);
  await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "删除服务器" }));
  await waitFor(() => expect(mocks.open).toHaveBeenCalled());
  const dialog = mocks.open.mock.calls[0]![0];
  expect(dialog.description).toContain("吊销身份");
  await expect(dialog.onSubmit({ confirm_name: "其他服务器" })).rejects.toThrow("名称不匹配");
  expect(mocks.api.write).not.toHaveBeenCalled();
  mocks.api.write.mockRejectedValueOnce(new Error("reauth cancelled"));
  await expect(dialog.onSubmit({ confirm_name: "台湾测试机" })).rejects.toThrow("reauth cancelled");
  expect(deleted).not.toHaveBeenCalled();
  await dialog.onSubmit({ confirm_name: "台湾测试机" });
  expect(mocks.api.write).toHaveBeenLastCalledWith("v1/servers/s", { row_version: 12 }, { method: "DELETE" });
  expect(deleted).toHaveBeenCalledOnce();
});
it("refuses deletion while serving and offers only valid lifecycle transitions", async () => {
  mocks.api.get.mockResolvedValue({ id: "s", name: "台湾测试机", row_version: 12, status: "ready" }); mount();
  await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "删除服务器" }));
  await waitFor(() => expect(screen.getByText("服务器仍在服务，请先通过变更状态排空并退役，再删除")).toBeVisible());
  expect(mocks.open).not.toHaveBeenCalled(); expect(mocks.api.write).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "更多操作" }));
  await userEvent.click(await screen.findByRole("menuitem", { name: "变更状态" }));
  await waitFor(() => expect(mocks.open).toHaveBeenCalled());
  const dialog = mocks.open.mock.calls[0]![0];
  expect(dialog.fields[0].options.map((option: {value: string}) => option.value)).toEqual(["draining", "unhealthy", "quarantined"]);
  await dialog.onSubmit({ status: "draining", reason: "计划维护" });
  expect(mocks.api.write).toHaveBeenCalledWith("v1/servers/s/status", { row_version: 12, status: "draining", reason: "计划维护" });
});
it("hides lifecycle actions from a read-only operator", () => {
  mocks.can.mockReturnValue(false); mount(); expect(screen.queryByRole("button")).not.toBeInTheDocument();
});
