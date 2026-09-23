import { useRef, useState } from "react";
import { Alert, Button, Form, Input, Modal, Select, Space, Tag } from "antd";
import { Link } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "../../core/auth";
import { failure } from "../../core/api";
import { useDialog } from "../../core/dialogs";
import type { Row } from "../../core/data";
import { ResourcePage } from "../../components/common";
import { UnsavedChangesGuard } from "../../components/UnsavedChangesGuard";

const actions: Record<string, string> = { block: "禁止访问", direct: "直连", proxy: "转发", dns: "指定 DNS 服务器进行解析" };
export function RouteGroupsPage() {
  const { api, can } = useAuth(); const dialog = useDialog(); const cache = useQueryClient();
  const [editing, setEditing] = useState<Row>();
  const refresh = () => cache.invalidateQueries();
  return <>
    <ResourcePage title="路由管理" description="管理所有路由组，包括添加、删除、编辑等操作。" resource="route-groups" path="v1/route-groups" listKey="route_groups" searchable clientSearchFields={["remarks", "group_no", "action_value"]}
      extra={<Space><Link to="/routing?advanced=1">节点独立路由</Link>{can("node.config.publish") && <Button type="primary" onClick={() => setEditing({})}>添加路由</Button>}</Space>}
      columns={[
        { title: "组 ID", dataIndex: "group_no", width: 90, sorter: (a,b) => Number(a.group_no)-Number(b.group_no) },
        { title: "备注", dataIndex: "remarks" },
        { title: "动作值", render: (_,row) => <>{String(row.action_value || actions[String(row.action)] || "—")} <span className="muted">匹配 {Array.isArray(row.match) ? row.match.length : 0} 条规则</span></> },
        { title: "动作", render: (_,row) => <Tag>{actions[String(row.action)] || String(row.action)}</Tag> },
        { title: "关联节点", dataIndex: "node_count", width: 100 },
        { title: "操作", width: 160, render: (_,row) => can("node.config.publish") && <Space size={0}>
          <Button type="link" onClick={() => setEditing(row)}>编辑</Button>
          <Button type="link" danger onClick={() => void dialog({ title: "删除路由组", danger: true, description: `删除“${row.remarks}”后，将从 ${row.node_count || 0} 个关联节点移除这些规则。`, submitLabel: "删除", onSubmit: async () => { await api.write(`v1/route-groups/${encodeURIComponent(String(row.id))}`, { row_version: row.row_version }, { method: "DELETE" }); await refresh(); } })}>删除</Button>
        </Space> },
      ]} />
    {editing && <RouteGroupEditor key={String(editing.id || "new")} initial={editing} close={() => setEditing(undefined)} saved={refresh} />}
  </>;
}

function RouteGroupEditor({ initial, close, saved }: { initial: Row; close: () => void; saved: () => Promise<unknown> }) {
  const { api } = useAuth(); const dialog = useDialog(); const [form] = Form.useForm();
  const [dirty,setDirty] = useState(false); const [saving,setSaving] = useState(false); const [error,setError] = useState("");
  const action = Form.useWatch("action",form) || initial.action || "block";
  const uncertain = useRef<{ key: string; payload: Row } | undefined>(undefined);
  const [locked,setLocked] = useState(false);
  const cancel = () => { if (saving) return; if (dirty || locked) { void dialog({ title: "关闭编辑", description: locked ? "上次提交的结果尚未确认，建议先重试确认结果。关闭后请在列表核对，避免重复创建。" : "关闭将丢弃当前未保存的修改。", onSubmit: async () => close() }); } else close(); };
  const submit = async () => {
    if (saving) return;
    let pending = uncertain.current;
    if (!pending) {
      let values: Row;
      try { values = await form.validateFields(); } catch { return; }
      const match = String(values.match || "").split(/\r?\n/).map(s => s.trim()).filter(Boolean);
      if (!match.length) { form.setFields([{ name:"match", errors:["请至少输入一条匹配规则"] }]); return; }
      pending = { key: crypto.randomUUID(), payload: { remarks: String(values.remarks).trim(), match, action: values.action, action_value: values.action === "proxy" || values.action === "dns" ? String(values.action_value || "").trim() : "", ...(initial.id ? { row_version: initial.row_version } : {}) } };
      uncertain.current = pending;
    }
    setSaving(true); setError("");
    try {
      await api.write(initial.id ? `v1/route-groups/${encodeURIComponent(String(initial.id))}` : "v1/route-groups", pending.payload, { method: initial.id ? "PUT" : "POST", idempotencyKey: pending.key });
      uncertain.current = undefined; setDirty(false); setLocked(false);
      await saved(); close();
    } catch (e) {
      const f = failure(e); setError(f.message);
      const unknown = f.uncertain;
      setLocked(unknown); if (!unknown) uncertain.current = undefined;
    } finally { setSaving(false); }
  };
  return <><UnsavedChangesGuard dirty={dirty || locked} saving={saving} /><Modal open title={initial.id ? "编辑路由" : "添加路由"} width={600} onCancel={cancel} maskClosable={false}
    footer={<Space><Button disabled={saving} onClick={cancel}>取消</Button><Button type="primary" loading={saving} onClick={() => void submit()}>{locked ? "重试确认结果" : "确认"}</Button></Space>}>
    {error && <Alert className="mb" type="error" showIcon title={error} />}
    <Form form={form} layout="vertical" disabled={saving || locked} initialValues={{ remarks:initial.remarks || "", match:Array.isArray(initial.match) ? initial.match.join("\n") : "", action:initial.action || "block", action_value:initial.action_value || "" }} onValuesChange={() => setDirty(true)}>
      <Form.Item label="备注" name="remarks" rules={[{ required:true, whitespace:true, message:"请输入备注" },{ max:200, message:"备注最多200字" }]}><Input maxLength={200} /></Form.Item>
      <Form.Item label="匹配规则" name="match" rules={[{ required:true, whitespace:true, message:"请输入匹配规则" }]} extra="每行一个域名、域名后缀或 IP/CIDR。任一规则匹配即执行动作。"><Input.TextArea autoSize={{ minRows:6, maxRows:12 }} /></Form.Item>
      <Form.Item label="动作" name="action" rules={[{ required:true }]}><Select options={[{ label:"禁止访问",value:"block" },{ label:"指定 DNS 服务器进行解析",value:"dns" },{ label:"直连",value:"direct" },{ label:"转发",value:"proxy" }]} /></Form.Item>
      {action === "dns" && <Form.Item label="DNS 服务器" name="action_value" rules={[{ required:true, whitespace:true, message:"请输入 DNS 服务器" }]} extra="填写 IP 地址或 IP:端口，例如 1.1.1.1。匹配的域名使用该服务器解析后直连。"><Input placeholder="1.1.1.1" /></Form.Item>}
      {action === "proxy" && <Form.Item label="转发标签（OUTBOUND TAG）" name="action_value" rules={[{ required:true, whitespace:true, message:"请输入转发标签" }]} extra="关联节点必须存在相同标识的转发出口。"><Input maxLength={64} /></Form.Item>}
    </Form>
  </Modal></>;
}
