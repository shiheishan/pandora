import { useState } from "react";
import { Alert, Button, Card, Space, Table, Tag, Tabs } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { principalSubject, record, rows, type Row } from "../../core/data";
import { PageHeader, QueryPanel, useData } from "../../components/common";

import { ContentSlots } from "./ContentSlots";
export const themeFields = [["brand", "主色"], ["brand-2", "辅色"], ["brand-soft", "主色浅底"], ["brand-on-soft", "浅底文字颜色"], ["r-lg", "卡片圆角"]];
export function themePayload(original: Row, values: Row): Row {
  const tokens = { ...record(original.tokens) }, branding = { ...record(original.branding) };
  for (const [key] of themeFields) {
    const value = String(values[key!] || "").trim();
    if (value) tokens[key!] = value; else delete tokens[key!];
  }
  branding.site_name = String(values.site_name || "").trim();
  branding.tagline = String(values.tagline || "").trim();
  const code = String(values.code || "").trim().toLowerCase(), name = String(values.name || "").trim();
  if (!/^[a-z][a-z0-9_-]{0,63}$/.test(code)) throw new Error("主题标识须以小写字母开头，仅含字母、数字、横线或下划线，最多 64 位");
  if (!name) throw new Error("请填写主题名称");
  return { code, name, tokens, branding, custom_css: String(values.custom_css || "") };
}
export function AppearancePage() {
  const [tab, setTab] = useState("themes");
  const query = useData("themes", "v1/themes");
  const { api, can, principal } = useAuth();
  const dialog = useDialog();
  const [notice, setNotice] = useState<string>();
  const edit = (theme: Row = {}, copy = false) => {
    const clone = copy || theme.is_builtin === true;
    const attempt = new Operation("appearance-theme-save", principalSubject(principal));
    void dialog({ title: clone ? "复制主题" : theme.code ? "编辑主题" : "新建主题", width: 640,
      description: clone ? "内置主题保留原样。修改保存为新主题后，可单独启用。" : "保存站点品牌与用户端外观，未启用的主题不会影响当前用户。",
      initial: { ...record(theme.tokens), ...record(theme.branding), code: clone || !theme.code ? `theme-${crypto.randomUUID()}` : theme.code, name: clone ? `${theme.name} 副本` : theme.name || "", custom_css: theme.custom_css || "" },
      fields: [
        { name: "name", label: "主题名称", required: true },
        { name: "code", label: "主题标识", required: true, disabled: !!theme.code && !clone, help: "系统已为新主题生成唯一标识，创建后不可修改。" },
        { name: "site_name", label: "站点名称", help: "显示于新版用户端标题、登录页和侧栏；留空使用 Pandora。" },
        { name: "tagline", label: "站点标语", help: "显示在用户登录页。" },
        ...themeFields.map(([name, label]) => ({ name: name!, label: label!, placeholder: name === "r-lg" ? "例如 14px" : "例如 #4263df", help: "留空沿用默认样式。" })),
        { name: "custom_css", label: "自定义 CSS", type: "textarea", help: "保留原有 CSS。服务器会移除不允许的内容，并在保存后报告。旧版页面的专用选择器可能需适配新组件。" },
      ], onSubmit: async values => {
        const payload = themePayload(theme, values);
        if ((clone || !theme.code) && rows(query.data, "themes").some(t => t.code === payload.code)) throw new Error("这个主题标识已存在，请使用新的标识");
        const result = await attempt.send(api, "v1/themes", payload);
        const dropped = Array.isArray(result.dropped) ? result.dropped.map(String) : [];
        setNotice(dropped.length ? `主题已保存，服务器已过滤：${dropped.join("；")}` : "主题已保存");
        await query.refetch();
      },
    });
  };
  return <>
    <PageHeader title="主题配置" description="管理站点品牌、用户端配色和自定义样式。内置主题可以复制后编辑。" extra={tab === "themes" && can("platform.appearance.write") && <Button type="primary" onClick={() => edit()}>新建主题</Button>} />
    <Tabs activeKey={tab} onChange={setTab} items={[{ key: "themes", label: "主题与品牌" }, { key: "slots", label: "自定义内容" }]} />
    {tab === "slots" ? <ContentSlots /> : <>
    {notice && <Alert className="mb" title={notice} showIcon type="info" closable onClose={() => setNotice(undefined)} />}
    <QueryPanel query={query}><Card><Table<Row> rowKey="code" size="small" scroll={{ x: 720 }} dataSource={rows(query.data, "themes")} columns={[
      { title: "主题", render: (_, t) => <><strong>{String(t.name)}</strong><div className="secondary small">{String(t.code)}</div></> },
      { title: "站点名称", render: (_, t) => String(record(t.branding).site_name || "Pandora") },
      { title: "状态", render: (_, t) => <Space>{t.is_active ? <Tag color="green">使用中</Tag> : <Tag>未启用</Tag>}{t.is_builtin ? <Tag>内置</Tag> : null}</Space> },
      { title: "操作", width: 290, render: (_, t) => can("platform.appearance.write") && <Space size={0}>
        <Button type="link" onClick={() => edit(t)}>{t.is_builtin ? "复制编辑" : "编辑"}</Button>
        {!t.is_builtin && <Button type="link" onClick={() => edit(t, true)}>复制</Button>}
        <Button type="link" disabled={!!t.is_active} onClick={() => void dialog({ title: `启用「${t.name}」`, description: "用户端将在重新读取外观配置后应用此主题。", submitLabel: "启用", onSubmit: async () => { await api.write(`v1/themes/${encodeURIComponent(String(t.code))}/activate`, {}); await query.refetch(); setNotice("主题已启用，请在用户端核对显示效果"); } })}>启用</Button>
        <Button type="link" danger disabled={!!t.is_builtin || !!t.is_active} onClick={() => void dialog({ title: `删除「${t.name}」`, description: "只能删除未启用的自定义主题。", danger: true, submitLabel: "删除", onSubmit: async () => { await api.write(`v1/themes/${encodeURIComponent(String(t.code))}`, {}, { method: "DELETE" }); await query.refetch(); } })}>删除</Button>
      </Space> },
    ]} /></Card></QueryPanel>
    </>}
  </>;
}
