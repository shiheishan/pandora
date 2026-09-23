import { useState } from "react";
import { App, Button, Dropdown } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { enc, record, text } from "../../core/data";
import { failure } from "../../core/api";

const transitions: Record<string, string[]> = {
  draft: ["ready", "maintenance", "retired"], ready: ["draining", "unhealthy", "quarantined"],
  draining: ["ready", "maintenance", "retired"], maintenance: ["ready", "retired"],
  unhealthy: ["draining", "maintenance", "quarantined", "retired"], quarantined: ["draining", "maintenance", "retired"],
};
const labels: Record<string, string> = { ready: "就绪 · 允许启用线路", maintenance: "维护中", retired: "已退役", draining: "排空中", unhealthy: "异常", quarantined: "隔离中" };

export function ServerActions({ id, onDeleted }: { id: string; onDeleted?: () => void }) {
  const { api, can } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  if (!can("node.lifecycle") || !id) return null;
  const act = async (action: string) => {
    if (busy) return;
    setBusy(true);
    try {
      const result = await api.get(`v1/servers/${enc(id)}`);
      const server = record(result.server ?? result);
      if (server.id !== id || !Number.isSafeInteger(server.row_version) || Number(server.row_version) < 1) throw new Error("未取得有效服务器资料，请刷新重试");
      if (action === "delete") {
        if (!["draft", "retired"].includes(text(server.status))) throw new Error("服务器仍在服务，请先通过变更状态排空并退役，再删除");
        await open({
          title: "删除服务器", danger: true,
          description: `删除「${text(server.name)}」会停用其关联节点和控制探针、吊销身份并终止未完成任务。历史记录保留，机器上的程序不会自动卸载。`,
          fields: [{ name: "confirm_name", label: "输入服务器名称确认", required: true }], submitLabel: "确认删除",
          onSubmit: async values => {
            if (values.confirm_name !== server.name) throw new Error("服务器名称不匹配，请核对后再提交");
            await api.write(`v1/servers/${enc(id)}`, { row_version: server.row_version }, { method: "DELETE" });
            message.success("服务器已删除，关联身份已吊销"); onDeleted?.();
          },
        });
      } else {
        const next = transitions[text(server.status)] || [];
        if (!next.length) throw new Error("该服务器已退役，不能再变更服务状态");
        await open({
          title: "变更服务器状态",
          description: "就绪状态允许启用线路；维护、排空和隔离分别保留现有生命周期规则。退役前请检查关联节点，后端会校验状态转换条件。",
          fields: [{ name: "status", label: "目标状态", type: "select", required: true, options: next.map(value => ({ value, label: labels[value] || value })) }, { name: "reason", label: "原因", required: true }],
          onSubmit: async values => {
            await api.write(`v1/servers/${enc(id)}/status`, { status: values.status, reason: values.reason, row_version: server.row_version });
            message.success("服务器状态已更新");
          },
        });
      }
    } catch (error) { message.error(failure(error).message); }
    finally { setBusy(false); }
  };
  return <Dropdown trigger={["click"]} menu={{ items: [{ key: "status", label: "变更状态" }, { key: "delete", label: "删除服务器", danger: true }], onClick: ({ key }) => void act(key) }}><Button loading={busy}>更多操作</Button></Dropdown>;
}
