import { useState } from "react";
import { Alert, Button, Card, Table, Tag } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { principalSubject, rows, type Row } from "../../core/data";
import { QueryPanel, useData } from "../../components/common";

export function ContentSlots() {
  const query = useData("slots", "v1/slots");
  const { api, can, principal } = useAuth();
  const dialog = useDialog();
  const [notice, setNotice] = useState<string>();
  const edit = (slot: Row) => {
    const operation = new Operation("appearance-slot-save", principalSubject(principal));
    void dialog({ title: `编辑${slot.label}`, width: 720,
      description: String(slot.where || ""),
      initial: { content: String(slot.content || ""), enabled: slot.enabled === true },
      fields: [
        { name: "enabled", label: "在用户端显示", type: "switch", help: "关闭后保留内容，用户端不再显示。" },
        { name: "content", label: "HTML 内容", type: "textarea", help: "支持段落、链接、图片和表格。服务器会过滤脚本及危险属性，过滤后最多 32KB；留空不占页面空间。" },
      ],
      onSubmit: async values => {
        const result = await operation.send(api, `v1/slots/${encodeURIComponent(String(slot.key))}`, { content: String(values.content || ""), enabled: values.enabled === true });
        const dropped = Array.isArray(result.dropped) ? result.dropped.map(String) : [];
        setNotice(dropped.length ? `内容已保存，服务器已过滤：${dropped.join("；")}` : "内容已保存，用户端将在下一次读取外观配置时更新（通常一分钟内）");
        await query.refetch();
      },
    });
  };
  return <>
    <Alert className="mb" type="info" showIcon title="用户端自定义内容" description="沿用原版的七个位置。登录提示对访客可见；其他位置随对应页面显示。重新编辑时读取服务器实际保存的内容。" />
    {notice && <Alert className="mb" type="info" showIcon title={notice} closable onClose={() => setNotice(undefined)} />}
    <QueryPanel query={query}><Card><Table<Row> rowKey="key" size="small" pagination={false} scroll={{ x: 640 }} dataSource={rows(query.data, "slots")} columns={[
      { title: "位置", render: (_, slot) => <><strong>{String(slot.label)}</strong><div className="secondary small">{String(slot.where)}</div></> },
      { title: "状态", width: 120, render: (_, slot) => slot.enabled && String(slot.content || "").trim() ? <Tag color="green">已显示</Tag> : <Tag>{slot.enabled ? "内容为空" : "未显示"}</Tag> },
      { title: "最近更新", dataIndex: "updated_at", width: 160, render: value => value || "尚未设置" },
      { title: "操作", width: 90, render: (_, slot) => can("platform.appearance.write") && <Button type="link" onClick={() => edit(slot)}>编辑</Button> },
    ]} /></Card></QueryPanel>
  </>;
}
