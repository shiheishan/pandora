import { useState } from "react";
import { App, Button, Dropdown } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog, type FormField } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { enc, principalSubject, rows, text, type Row } from "../../core/data";
import { failure } from "../../core/api";

export function NodeBatchActions({ selected, clear }: { selected: Row[]; clear: () => void }) {
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  const act = async (action: "active" | "disabled" | "order") => {
    if (busy || !selected.length) return;
    const limit = action === "order" ? 200 : 100;
    if (selected.length > limit) { message.error(`每次最多操作 ${limit} 个节点，请减少选择`); return; }
    setBusy(true);
    try {
      const current: Row[] = [];
      for (let offset = 0; offset < selected.length; offset += 5) {
        current.push(...await Promise.all(selected.slice(offset, offset + 5).map(node => api.get(`v1/nodes/${enc(text(node.id))}`))));
      }
      if (current.some((node, index) => node.id !== selected[index]?.id || node.runtime_role === "probe" || node.serving_status === "retired" || !Number.isSafeInteger(node.row_version) || Number(node.row_version) < 1)) throw new Error("所选节点已发生变化或不可操作，请刷新后重新选择");
      const targets = action === "order" ? current : current.filter(node => node.serving_status !== action);
      if (!targets.length) { message.info("所选节点已处于目标状态，无需重复操作"); return; }
      const attempt = new Operation(`node-batch-${action}`, principalSubject(principal));
      await open({
        title: action === "order" ? "保存节点排序" : action === "active" ? "批量启用节点" : "批量停用节点",
        description: action === "order" ? "数字越小越靠前。只更新以下选中节点的排序；未选中的节点保持原值。" : `将${action === "active" ? "启用" : "停用"} ${targets.length} 个节点：${targets.map(node => text(node.name)).join("、")}。任一节点条件不符或版本冲突时整批不执行。`,
        fields: action === "order" ? targets.map((node, index) => ({ name: `sort_${index}`, label: text(node.name), type: "number" as const, required: true, min: -2147483648, max: 2147483647 })) : [{ name: "reason", label: "操作原因", required: true }],
        initial: action === "order" ? Object.fromEntries(targets.map((node, index) => [`sort_${index}`, Number(node.sort_order || 0)])) : {},
        submitLabel: action === "order" ? "保存排序" : "确认执行",
        onSubmit: async values => {
          const items = targets.map((node, index) => ({ id: node.id, row_version: node.row_version, ...(action === "order" ? { sort_order: Number(values[`sort_${index}`]) } : {}) }));
          if (action === "order") {
            if (items.some(item => !Number.isInteger(item.sort_order))) throw new Error("排序必须为整数");
            await api.write("v1/nodes/order", { items }, { method: "PUT" });
          } else await attempt.send(api, "v1/nodes/status:batch", { items, serving_status: action, reason: values.reason });
          clear(); message.success(action === "order" ? "节点排序已保存" : "批量操作已完成");
        },
      });
    } catch (error) { message.error(failure(error).message); }
    finally { setBusy(false); }
  };
  return <>
    {can("node.lifecycle") && <><Button disabled={!selected.length || busy} onClick={() => void act("active")}>批量启用</Button><Button disabled={!selected.length || busy} onClick={() => void act("disabled")}>批量停用</Button></>}
    {can("node.write") && <Button disabled={!selected.length || busy} onClick={() => void act("order")}>保存排序</Button>}
    {busy && <span role="status">正在处理所选节点…</span>}
  </>;
}

export function NodeActions({ id, onlyAction, disabled = false, onCompleted }: { id: string; onlyAction?: "move"; disabled?: boolean; onCompleted?: () => void }) {
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  const items = [
    ...(can("node.write") ? [{ key: "reset_transfer", label: "重置节点流量" }] : []),
    ...(can("node.provision") ? [{ key: "copy", label: "复制节点" }, { key: "move", label: "迁移到服务器" }] : []),
    ...(can("node.lifecycle") ? [{ key: "status", label: "启用 / 停用" }] : []),
  ];
  if (!items.length || !id || (onlyAction && !items.some(item => item.key === onlyAction))) return null;
  const act = async (action: string) => {
    setBusy(true);
    try {
      const node = await api.get(`v1/nodes/${enc(id)}`);
      if (!node.id || node.runtime_role === "probe" || !Number.isSafeInteger(node.row_version) || Number(node.row_version) < 1) throw new Error("此记录不可执行节点操作，请刷新列表");
      if(action==="reset_transfer") {
        await open({title:"重置节点流量",description:"将此节点的流量上限统计归零，用户已用流量、账单和历史流水保持不变。",fields:[],submitLabel:"确认重置",onSubmit:async()=>{
          await api.write(`v1/nodes/${enc(id)}`,{row_version:node.row_version,reset_transfer:true},{method:"PATCH"});
          message.success("节点流量已重置");onCompleted?.();
        }});return;
      }
      const attempt = new Operation(`node-${action}`, principalSubject(principal));
      const status = node.serving_status === "active" ? "disabled" : "active";
      if (action === "status" && node.serving_status === "retired") throw new Error("已退役的节点不能重新启用");
      let fields: FormField[] = [{ name: "reason", label: "操作原因", required: true }];
      if (action !== "status") {
        const result = await api.get("v1/servers");
        const options = rows(result, "servers").filter(server => !["retired", "quarantined"].includes(text(server.status)) && (action !== "move" || server.id !== node.server_id)).map(server => ({ value: text(server.id), label: text(server.name) }));
        fields = [{ name: "target", label: "目标服务器", type: "select", required: true, options }];
        if (action === "copy") fields.push({ name: "name", label: "新节点名称", required: true }, { name: "copy_routing", label: "同时复制路由规则", type: "switch" });
        else fields.push({ name: "reason", label: "迁移原因", required: true });
      }
      await open({
        title: action === "copy" ? "复制节点" : action === "move" ? "迁移到服务器" : status === "active" ? "启用节点" : "停用节点",
        description: action === "copy" ? (node.protocol_schema_version === 0 ? "源节点使用旧版 v0 配置。后端不会复制这份旧协议；副本需重新选择稳定协议并填写参数。原节点保持不变。" : "保留原节点的协议和分组，新节点以草稿状态创建。启用前请检查地址、端口与配置。") : action === "move" ? "仅尚未接入、没有身份和流量等历史记录的停用或草稿节点可迁移。已运行过的线路请先复制到目标服务器，再单独验证新线路；后端会保留历史归属。" : "调整订阅与配置下发状态。实际连接状态仍需确认内核应用结果和节点心跳。",
        fields,
        initial: { name: `${text(node.name)}-copy`, target: action === "copy" ? node.server_id : undefined, copy_routing: false },
        submitLabel: action === "copy" ? "创建副本" : action === "move" ? "确认迁移" : "确认",
        onSubmit: async values => {
          if (action === "status") await attempt.send(api, "v1/nodes/status:batch", { items: [{ id, row_version: node.row_version }], serving_status: status, reason: values.reason });
          else await attempt.send(api, `v1/nodes/${enc(id)}/${action}`, action === "copy" ? { row_version: node.row_version, name: values.name, target_server_id: values.target, copy_routing: Boolean(values.copy_routing) } : { row_version: node.row_version, server_id: values.target, reason: values.reason });
          message.success(action === "copy" ? "节点副本已创建，请检查后启用" : "节点操作已完成");
          onCompleted?.();
        },
      });
    } catch (error) { message.error(failure(error).message); }
    finally { setBusy(false); }
  };
  if (onlyAction) return <Button disabled={disabled || busy} loading={busy} onClick={() => void act(onlyAction)}>更换绑定服务器</Button>;
  return <Dropdown menu={{ items, onClick: ({ key }) => void act(key) }} trigger={["click"]}><Button loading={busy}>更多操作</Button></Dropdown>;
}

export function AssociateNodeButton({ serverId }: { serverId: string }) {
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  if (!can("node.provision") || !serverId) return null;
  const associate = async () => {
    setBusy(true);
    try {
      const candidates = rows(await api.get("v1/nodes?runtime_role=business"), "nodes").filter(node => node.runtime_role !== "probe" && node.server_id !== serverId && node.serving_status !== "retired");
      if (!candidates.length) { message.info("没有可关联的业务节点；当前服务器下的节点已在列表中"); return; }
      const attempt = new Operation("node-server-associate", principalSubject(principal));
      await open({
        title: "关联已有节点",
        description: "把尚未接入且没有身份、配置应用和流量历史的业务节点迁移到当前服务器。已运行过的节点可能被后端拒绝，请使用复制方式建立新线路后验证。",
        fields: [
          { name: "node_id", label: "选择节点", type: "select", required: true, options: candidates.map(node => ({ value: text(node.id), label: `${text(node.name)} · ${text(node.server_name, "其他服务器")}` })) },
          { name: "reason", label: "关联原因", required: true },
        ],
        submitLabel: "确认关联",
        onSubmit: async values => {
          const selected = candidates.find(node => node.id === values.node_id);
          if (!selected) throw new Error("请选择列表中的业务节点");
          const current = await api.get(`v1/nodes/${enc(text(selected.id))}`);
          if (current.runtime_role === "probe" || current.server_id !== selected.server_id) throw new Error("节点归属已发生变化，请重新打开窗口核对后再关联");
          if (!Number.isSafeInteger(current.row_version) || Number(current.row_version) < 1) throw new Error("未取得有效节点版本，请刷新重试");
          await attempt.send(api, `v1/nodes/${enc(text(selected.id))}/move`, { row_version: current.row_version, server_id: serverId, reason: values.reason });
          message.success("节点已关联到当前服务器");
        },
      });
    } catch (error) { message.error(failure(error).message); }
    finally { setBusy(false); }
  };
  return <Button loading={busy} onClick={() => void associate()}>关联已有节点</Button>;
}
