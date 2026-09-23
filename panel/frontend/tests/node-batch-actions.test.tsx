import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "antd";
import { beforeEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({ api: { get: vi.fn(), write: vi.fn(), request: vi.fn() }, open: vi.fn(), can: vi.fn() }));
vi.mock("../src/core/auth", () => ({ useAuth: () => ({ api: mocks.api, can: mocks.can, principal: { user_id: "operator" } }) }));
vi.mock("../src/core/dialogs", () => ({ useDialog: () => mocks.open }));
import { ApiFailure } from "../src/core/api";
import { NodeBatchActions } from "../src/features/admin/NodeActions";
const selected = [{ id: "a", name: "A", row_version: 1 }, { id: "b", name: "B", row_version: 1 }];
beforeEach(() => {
  vi.resetAllMocks(); mocks.can.mockReturnValue(true); mocks.api.write.mockResolvedValue({}); mocks.api.request.mockResolvedValue({});
  mocks.api.get.mockImplementation(async (path: string) => ({ id: path.endsWith("a") ? "a" : "b", name: path.endsWith("a") ? "A" : "B", row_version: 8, serving_status: "disabled", runtime_role: "business", sort_order: 5 }));
});
it("uses fresh versions, skips nodes already at the target and retains the exact batch on retry", async () => {
  mocks.api.get.mockImplementation(async (path: string) => ({ id: path.endsWith("a") ? "a" : "b", name: "线路", row_version: 8, serving_status: path.endsWith("a") ? "disabled" : "active", runtime_role: "business" }));
  const clear = vi.fn();
  render(<App><NodeBatchActions selected={selected} clear={clear} /></App>);
  await userEvent.click(screen.getByRole("button", { name: "批量启用" }));
  await waitFor(() => expect(mocks.open).toHaveBeenCalled());
  const dialog = mocks.open.mock.calls[0]![0];
  mocks.api.request.mockRejectedValueOnce(new ApiFailure("timeout", "timeout"));
  await expect(dialog.onSubmit({ reason: "已确认配置" })).rejects.toThrow("结果待核实");
  expect(clear).not.toHaveBeenCalled();
  await dialog.onSubmit({ reason: "已确认配置" });
  expect(mocks.api.request.mock.calls[0]?.[2].body).toEqual({ items: [{ id: "a", row_version: 8 }], serving_status: "active", reason: "已确认配置" });
  expect(mocks.api.request.mock.calls[1]?.[2]).toEqual(mocks.api.request.mock.calls[0]?.[2]);
  expect(clear).toHaveBeenCalledOnce();
});
it("saves only the selected sort values and rejects fractions before writing", async () => {
  render(<App><NodeBatchActions selected={selected} clear={vi.fn()} /></App>);
  await userEvent.click(screen.getByRole("button", { name: "保存排序" }));
  await waitFor(() => expect(mocks.open).toHaveBeenCalled());
  const dialog = mocks.open.mock.calls[0]![0];
  await expect(dialog.onSubmit({ sort_0: 1.5, sort_1: 8 })).rejects.toThrow("整数");
  expect(mocks.api.write).not.toHaveBeenCalled();
  await dialog.onSubmit({ sort_0: 2, sort_1: 8 });
  expect(mocks.api.write).toHaveBeenCalledWith("v1/nodes/order", { items: [{ id: "a", row_version: 8, sort_order: 2 }, { id: "b", row_version: 8, sort_order: 8 }] }, { method: "PUT" });
});
it("does not offer a confirmation if a selected business node has become a probe", async () => {
  mocks.api.get.mockResolvedValue({ id: "a", runtime_role: "probe", row_version: 9 });
  render(<App><NodeBatchActions selected={selected.slice(0, 1)} clear={vi.fn()} /></App>);
  await userEvent.click(screen.getByRole("button", { name: "批量停用" }));
  await waitFor(() => expect(screen.getByText("所选节点已发生变化或不可操作，请刷新后重新选择")).toBeVisible());
  expect(mocks.open).not.toHaveBeenCalled(); expect(mocks.api.write).not.toHaveBeenCalled();
});
