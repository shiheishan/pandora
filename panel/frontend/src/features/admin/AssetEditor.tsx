import { useState } from "react";
import { App, Button } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog, type FormField } from "../../core/dialogs";
import { failure } from "../../core/api";
import { enc, record, rows, text, type Row } from "../../core/data";

export const serverFields: FormField[] = [
  { name: "name", label: "服务器名称", required: true, placeholder: "例如：台湾 bage · 主线路" },
  { name: "notes", label: "备注", type: "textarea", help: "记录用途、服务商、到期时间或维护说明。" },
  { name: "region", label: "地区（可选覆盖）", placeholder: "留空，探针接入后自动识别", help: "根据探针上报的公网 IP 查询本站 GeoIP 库；手填地区优先保留。" },
  { name: "capacity_nodes", label: "计划承载节点数", type: "number", min: 1, max: 10000, help: "容量规划参考，不是连接数或流量限制。" },
];
const nodeFields: FormField[] = [
  { name: "name", label: "内部名称", required: true },
  { name: "display_name", label: "显示名称" },
  { name: "server_host", label: "连接地址", required: true },
  { name: "server_port", label: "端口", type: "number", min: 1, max: 65535, required: true },
  { name: "traffic_rate", label: "流量倍率", type: "number", min: 0, max: 9999, required: true },
];

// Read the exact record when opening: a list row may be old or omit editable fields.
export function AssetEditButton({ kind, id, label = "编辑" }: { kind: "servers" | "nodes"; id: string; label?: string }) {
  const { api, can } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [loading, setLoading] = useState(false);
  if (!can("node.write") || !id) return null;
  const edit = async () => {
    setLoading(true);
    try {
      const path = `v1/${kind}/${enc(id)}`;
      const response = await api.get(path);
      const current = record(response[kind === "servers" ? "server" : "node"] ?? response);
      if (!current.id || !Number.isSafeInteger(current.row_version) || Number(current.row_version) < 1) throw new Error("服务器未返回可编辑的完整记录，请刷新后重试");
      const fields = kind === "servers" ? serverFields : [...nodeFields];
      if (kind === "nodes") {
        const pools = rows(await api.get("v1/node-pools"), "pools");
        const options = pools.map(pool => ({ value: text(pool.id), label: text(pool.name) }));
        if (current.pool_id && !options.some(option => option.value === current.pool_id)) options.push({ value: text(current.pool_id), label: "当前分组（未在列表中）" });
        fields.push({ name: "pool_id", label: "权限组", type: "select", options: [{ value: "", label: "未分组" }, ...options], help: "分组决定套餐可以使用的线路；调整后会更新节点配置。" });
      }
      await open({
        title: kind === "servers" ? "编辑服务器" : "编辑节点",
        protectDraft: true,
        description: kind === "servers" ? "修改名称和备注等资料。探针在线状态由心跳决定，维护与退役通过服务器状态操作管理。" : "修改线路信息，保留现有协议参数与密钥。",
        fields,
        initial: Object.fromEntries(fields.map(field => [field.name, current[field.name] ?? ""])),
        submitLabel: "更新",
        onSubmit: async values => {
          const payload: Row = Object.fromEntries(fields
            .filter(field => values[field.name] !== (current[field.name] ?? ""))
            .map(field => [field.name, values[field.name]]));
          if (!Object.keys(payload).length) return;
          await api.write(path, { ...payload, row_version: current.row_version }, { method: "PATCH" });
          message.success(kind === "servers" ? "服务器资料已更新" : "节点资料已更新");
        },
      });
    } catch (error) {
      message.error(failure(error).message);
    } finally {
      setLoading(false);
    }
  };
  return <Button loading={loading} onClick={() => void edit()}>{label}</Button>;
}
