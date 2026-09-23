/**
 * [INPUT]: 依赖 core/auth、core/runtime 的 hasContract、core/api 的 failure、core/dialogs、core/data、components/common、components/UnsavedChangesGuard；依赖 admin/RouteGroups 的 RouteGroupsPage
 * [OUTPUT]: 对外提供 RoutingPage 组件与 matcherFields / mergeMatcher / routingPayload 纯函数
 * [POS]: features/admin 的路由管理页：默认列出业务节点并按节点编辑 nodes/{id}/routing；共享路由组页只在待接契约 route-groups-v1 打开时成为默认页
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useState } from "react";
import { Alert, Button, Card, Space, Table, Tag } from "antd";
import { Link, useSearchParams } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { hasContract } from "../../core/runtime";
import { failure } from "../../core/api";
import { useDialog } from "../../core/dialogs";
import { rows, type Row } from "../../core/data";
import { PageHeader, QueryPanel, ResourcePage, useData } from "../../components/common";
import { UnsavedChangesGuard } from "../../components/UnsavedChangesGuard";
import { RouteGroupsPage } from "./RouteGroups";

const matches = [["domain", "完整域名"], ["domain_suffix", "域名后缀"], ["ip_cidr", "目标 IP / CIDR"], ["port", "目标端口"], ["network", "传输协议"], ["source_ip_cidr", "来源 IP / CIDR"], ["source_port", "来源端口"]];
const aliases: Record<string, string[]> = { domain: ["domain", "domains"], domain_suffix: ["domain_suffix", "domain_suffixes"], ip_cidr: ["ip", "ip_cidr", "ip_cidrs"], port: ["port", "ports"], network: ["network", "networks"], source_ip_cidr: ["source", "source_ip_cidr", "source_cidrs"], source_port: ["source_port", "source_ports"] };
const lines = (value: unknown) => value == null ? "" : Array.isArray(value) ? value.join("\n") : String(value);
export function matcherFields(matcher: Row): Row {
  return Object.fromEntries(matches.map(([key]) => [key!, lines(matcher[(aliases[key!] || []).find(alias => alias in matcher) || key!])]));
}
export function mergeMatcher(original: Row, values: Row): Row {
  const result = structuredClone(original);
  const before = matcherFields(original);
  for (const [key] of matches) {
    if (String(values[key!] ?? "") === before[key!]) continue;
    for (const alias of aliases[key!] || []) delete result[alias];
    const entries = String(values[key!] || "").split(/\r?\n/).map(s => s.trim()).filter(Boolean);
    if (entries.length) result[key!] = entries;
  }
  return result;
}
function objectJSON(value: unknown): Row {
  const result = JSON.parse(String(value));
  if (!result || typeof result !== "object" || Array.isArray(result)) throw new Error("请输入 JSON 对象");
  return result;
}
export function routingPayload(draft: Row): Row {
  if (!Number.isSafeInteger(draft.row_version) || Number(draft.row_version) < 1) throw new Error("节点版本无效，请重新加载");
  const outbounds = rows(draft, "outbounds").map(outbound => ({ ...outbound, tag: String(outbound.tag || "").trim(), type: String(outbound.type || "").trim().toLowerCase() }));
  const routes: Row[] = rows(draft, "routes").map((route, index) => ({ ...route, priority: (index + 1) * 10 }));
  const tags = new Map([["direct", "direct"], ["block", "block"]]);
  for (const outbound of outbounds) {
    const tag = String(outbound.tag || "").trim().toLowerCase();
    if (!tag || !String(outbound.type || "").trim() || tags.has(tag)) throw new Error("出站标识不能为空、重复或使用 direct / block 保留名称");
    tags.set(tag, String(outbound.tag));
  }
  const enabled = routes.filter(route => route.enabled);
  routes.forEach(route => {
    if (!tags.has(String(route.outbound_tag || "").trim().toLowerCase())) throw new Error("规则引用的出站不存在");
    route.outbound_tag = tags.get(String(route.outbound_tag || "").trim().toLowerCase());
  });
  enabled.slice(0, -1).forEach(route => {
    if (!Object.values(route.matcher as Row).some(v => Array.isArray(v) ? v.length > 0 : String(v).length > 0)) throw new Error("匹配全部流量的规则必须放在最后");
  });
  return { row_version: draft.row_version, outbounds, routes };
}

export function RoutingPage() {
  const [params] = useSearchParams();
  const node = params.get("node") || "";
  if (!node && !params.has("advanced") && hasContract("route-groups-v1")) return <RouteGroupsPage />;
  if (!node) return <ResourcePage title="路由管理" description="选择业务节点，管理它的匹配规则与自定义出站。" resource="routing-nodes" path="v1/nodes?runtime_role=business" listKey="nodes" columns={[
    { title: "节点名称", dataIndex: "name" }, { title: "显示名称", dataIndex: "display_name" },
    { title: "协议", dataIndex: "node_type" },
    { title: "操作", render: (_, row) => <Link to={`/routing?node=${encodeURIComponent(String(row.id))}`}>配置路由</Link> },
  ]} />;
  return <>
    <PageHeader title="路由管理" description="按节点管理匹配规则和自定义出站。规则从上到下匹配，修改后统一保存。" extra={<Link to="/nodes">选择节点</Link>} />
    <RoutingEditor key={node} node={node} />
  </>;
}
function RoutingEditor({ node }: { node: string }) {
  const query = useData("node-routing", `v1/nodes/${encodeURIComponent(node)}/routing`);
  const { api, can } = useAuth();
  const dialog = useDialog();
  const [draft, setDraft] = useState<Row>();
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<{ error: boolean; text: string }>();
  useEffect(() => { if (query.data && !dirty) setDraft(structuredClone(query.data)); }, [query.data, dirty]);
  const writable = can("node.config.publish") && !saving;
  const change = (key: string, value: Row[]) => { setDraft(current => ({ ...current, [key]: value })); setDirty(true); setStatus(undefined); };
  const rules = rows(draft, "routes");
  const outbounds = rows(draft, "outbounds");
  const editRule = (index?: number, advanced = false) => {
    const rule = index === undefined ? undefined : rules[index];
    void dialog({ title: rule ? "编辑路由规则" : "添加路由规则", width: 600,
      initial: { note: rule?.note || "", enabled: rule ? rule.enabled : true, outbound_tag: rule?.outbound_tag || "block", matcher: JSON.stringify(rule?.matcher || {}, null, 2), ...matcherFields((rule?.matcher || {}) as Row) },
      fields: [
        { name: "note", label: "备注", placeholder: "例如：屏蔽广告域名" },
        { name: "outbound_tag", label: "执行动作", required: true, type: "select", options: [{ label: "直接连接", value: "direct" }, { label: "阻断连接", value: "block" }, ...outbounds.map(o => ({ label: `转发至 ${o.tag}`, value: String(o.tag) }))] },
        ...(advanced ? [{ name: "matcher", label: "匹配条件（JSON）", type: "textarea" as const, required: true, help: "用于编辑原有复杂条件，未修改的参数保持原样。" }] : matches.map(([name, label]) => ({ name: name!, label: label!, type: "textarea" as const, help: name === "network" ? "每行 tcp 或 udp。全部条件留空表示匹配所有流量，须放在最后。" : "每行一项，留空不限制。" }))),
        { name: "enabled", label: "启用规则", type: "switch" },
      ], onSubmit: async values => {
        const matcher = advanced ? objectJSON(values.matcher) : mergeMatcher((rule?.matcher || {}) as Row, values);
        const next = { ...rule, note: String(values.note || ""), outbound_tag: values.outbound_tag, enabled: values.enabled === true, matcher };
        change("routes", index === undefined ? [...rules, next] : rules.map((r, i) => i === index ? next : r));
      }, submitLabel: "应用到草稿",
    });
  };
  const editOutbound = (index?: number) => {
    const outbound = index === undefined ? undefined : outbounds[index];
    void dialog({ title: outbound ? "编辑自定义出站" : "添加自定义出站", width: 600,
      initial: { tag: outbound?.tag || "", type: outbound?.type || "socks", settings: JSON.stringify(outbound?.settings || {}, null, 2) },
      fields: [ { name: "tag", label: "出站标识", required: true, disabled: !!outbound, help: "创建后用于路由规则引用。" }, { name: "type", label: "出站协议", required: true, help: "使用当前节点内核支持的协议名称。" }, { name: "settings", label: "协议参数（JSON）", required: true, type: "textarea", help: "保留我们的自定义内核参数；不会按 Xboard 字段裁剪。" } ],
      onSubmit: async values => {
        const next = { ...outbound, tag: String(values.tag).trim(), type: String(values.type).trim(), settings: objectJSON(values.settings) };
        const nextOutbounds = index === undefined ? [...outbounds, next] : outbounds.map((o, i) => i === index ? next : o);
        routingPayload({ ...draft, outbounds: nextOutbounds });
        change("outbounds", nextOutbounds);
      }, submitLabel: "应用到草稿",
    });
  };
  const save = async () => {
    try {
      const payload = routingPayload(draft || {});
      setSaving(true); setStatus(undefined);
      const saved = await api.write(`v1/nodes/${encodeURIComponent(node)}/routing`, payload, { method: "PUT" });
      // A successful response establishes the next CAS version even if readback fails.
      setDraft(current => ({ ...current, row_version: saved.row_version }));
      const result = await query.refetch();
      if (result.error) throw new Error("路由已提交，但刷新失败。草稿已保留，请重新加载核对。");
      if (result.data?.row_version !== saved.row_version) throw new Error("路由已被再次修改，请重新加载核对。当前草稿已保留。");
      setDraft(structuredClone(result.data)); setDirty(false); setStatus({ error: false, text: "路由配置已保存；实际节点生效状态请在节点详情核对。" });
    } catch (error) { setStatus({ error: true, text: failure(error).message }); }
    finally { setSaving(false); }
  };
  return <><UnsavedChangesGuard dirty={dirty} saving={saving} /><QueryPanel query={query}>
    <Space className="mb" wrap><Link to={`/nodes/${encodeURIComponent(node)}`}>返回节点详情</Link><Tag>配置版本 {String(draft?.row_version || "—")}</Tag>{dirty && <Tag color="orange">未保存</Tag>}
      {can("node.config.publish") && <Button type="primary" disabled={!dirty} loading={saving} onClick={() => void save()}>保存路由配置</Button>}
      <Button disabled={saving} onClick={() => void dialog({ title: "重新加载路由", description: dirty ? "重新加载会丢弃当前未保存的修改。" : "读取服务器当前配置。", onSubmit: async () => { const result = await query.refetch(); if (result.error) throw result.error; setDraft(structuredClone(result.data)); setDirty(false); setStatus(undefined); } })}>重新加载</Button>
    </Space>
    {status && <Alert className="mb" showIcon type={status.error ? "error" : "success"} title={status.text} />}
    <Card className="mb" title="匹配规则" extra={writable && <Button onClick={() => editRule()}>添加规则</Button>}>
      <Table<Row> rowKey={(_, i) => String(i)} size="small" pagination={false} scroll={{ x: 700 }} dataSource={rules} columns={[
        { title: "顺序", width: 60, render: (_, __, i) => i + 1 }, { title: "备注", dataIndex: "note" },
        { title: "匹配条件", render: (_, r) => <span style={{ overflowWrap: "anywhere" }}>{JSON.stringify(r.matcher)}</span> },
        { title: "动作", dataIndex: "outbound_tag" }, { title: "状态", render: (_, r) => r.enabled ? "启用" : "停用" },
        { title: "操作", width: 220, render: (_, r, i) => writable && <Space size={0}><Button type="link" onClick={() => editRule(i)}>编辑</Button><Button type="link" onClick={() => editRule(i, true)}>高级编辑</Button><Button type="link" disabled={i === 0} onClick={() => { const next = [...rules]; [next[i - 1], next[i]] = [next[i]!, next[i - 1]!]; change("routes", next); }}>上移</Button><Button type="link" disabled={i === rules.length - 1} onClick={() => { const next = [...rules]; [next[i], next[i + 1]] = [next[i + 1]!, next[i]!]; change("routes", next); }}>下移</Button><Button type="link" danger onClick={() => change("routes", rules.filter(x => x !== r))}>移除</Button></Space> },
      ]} />
    </Card>
    <Card title="自定义出站" extra={writable && <Button onClick={() => editOutbound()}>添加出站</Button>}>
      <Table<Row> rowKey="tag" size="small" pagination={false} scroll={{ x: 450 }} dataSource={outbounds} columns={[{ title: "标识", dataIndex: "tag" }, { title: "协议", dataIndex: "type" }, { title: "操作", render: (_, o, i) => writable && <Space><Button type="link" onClick={() => editOutbound(i)}>编辑</Button><Button type="link" danger disabled={rules.some(r => String(r.outbound_tag).trim().toLowerCase() === String(o.tag).trim().toLowerCase())} onClick={() => change("outbounds", outbounds.filter(x => x !== o))}>移除</Button></Space> }]} />
    </Card>
  </QueryPanel></>;
}
