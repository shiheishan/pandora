import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ConfigProvider } from "antd";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { useState } from "react";
import { expect, it } from "vitest";
import { UnsavedChangesGuard } from "../src/components/UnsavedChangesGuard";

function Editor({ saving = false }: { saving?: boolean }) {
  const [dirty, setDirty] = useState(true);
  const [pending, setPending] = useState(saving);
  return <><UnsavedChangesGuard dirty={dirty} saving={pending} /><button onClick={() => { setDirty(false); setPending(false); }}>模拟保存完成</button></>;
}
function mount(saving = false) {
  const router = createMemoryRouter([{ path: "/routing", element: <Editor saving={saving} /> }, { path: "/nodes", element: <p>节点列表</p> }], { initialEntries: ["/routing?node=one"] });
  render(<ConfigProvider theme={{ token: { motion: false } }}><RouterProvider router={router} /></ConfigProvider>);
  return router;
}
it("protects node switches and page exits, retains edits on cancel, and permits explicit discard", async () => {
  const router = mount();
  await act(async () => { await router.navigate("/routing?node=two"); });
  await waitFor(() => expect(screen.getByText("有尚未保存的修改")).toBeVisible());
  expect(router.state.location.search).toBe("?node=one");
  fireEvent.click(screen.getByRole("button", { name: "继续编辑" }));
  expect(router.state.location.search).toBe("?node=one");
  await act(async () => { await router.navigate("/nodes"); });
  fireEvent.click(await screen.findByRole("button", { name: "放弃修改并离开" }));
  expect(await screen.findByText("节点列表")).toBeVisible();
});
it("warns on browser close only while dirty and lets a saved page leave directly", async () => {
  const router = mount();
  const before = new Event("beforeunload", { cancelable: true });window.dispatchEvent(before);expect(before.defaultPrevented).toBe(true);
  fireEvent.click(screen.getByText("模拟保存完成"));
  const after = new Event("beforeunload", { cancelable: true });window.dispatchEvent(after);expect(after.defaultPrevented).toBe(false);
  await act(async () => { await router.navigate("/nodes"); });
  expect(await screen.findByText("节点列表")).toBeVisible();
});
it("does not allow discarding a request whose save result is still pending", async () => {
  const router = mount(true);
  await act(async () => { await router.navigate("/nodes"); });
  await waitFor(() => expect(screen.getByText("正在保存配置")).toBeVisible());
  expect(screen.getByRole("button", { name: "放弃修改并离开" })).toBeDisabled();
  fireEvent.click(screen.getByText("模拟保存完成"));
  await waitFor(() => expect(screen.getByText("配置已保存")).toBeVisible());
  fireEvent.click(screen.getByRole("button", { name: "离开页面" }));
  expect(await screen.findByText("节点列表")).toBeVisible();
});
