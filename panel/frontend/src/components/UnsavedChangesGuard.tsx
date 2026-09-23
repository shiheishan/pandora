import { useCallback } from "react";
import { Modal } from "antd";
import { useBeforeUnload, useBlocker } from "react-router-dom";

export function UnsavedChangesGuard({ dirty, saving = false }: { dirty: boolean; saving?: boolean }) {
  const blocker = useBlocker(({ currentLocation, nextLocation }) =>
    (dirty || saving) && (currentLocation.pathname !== nextLocation.pathname || currentLocation.search !== nextLocation.search));
  useBeforeUnload(useCallback((event: BeforeUnloadEvent) => {
    if (dirty || saving) { event.preventDefault(); event.returnValue = ""; }
  }, [dirty, saving]));
  return <Modal open={blocker.state === "blocked"} title={saving ? "正在保存配置" : dirty ? "有尚未保存的修改" : "配置已保存"}
    okText={dirty || saving ? "放弃修改并离开" : "离开页面"} cancelText="继续编辑" okButtonProps={{ danger: dirty, disabled: saving }}
    onOk={() => { if (!saving && blocker.state === "blocked") blocker.proceed(); }}
    onCancel={() => { if (blocker.state === "blocked") blocker.reset(); }}>
    {saving ? "请等待保存结果，再离开页面。" : dirty ? "离开将丢弃当前修改。选择继续编辑可保留草稿，保存后再离开。" : "修改已保存，可以离开页面。"}
  </Modal>;
}
