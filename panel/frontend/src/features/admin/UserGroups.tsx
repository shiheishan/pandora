import { useState } from "react";
import { Button, Input, Space, Table, Tooltip } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { rows, type Row } from "../../core/data";
import { PageHeader, QueryPanel, useData } from "../../components/common";

export function UserGroupsPage() {
  const query = useData("user-groups", "v1/user-groups");
  const { api, can } = useAuth();
  const dialog = useDialog();
  const [search, setSearch] = useState("");
  const edit = (group?: Row) => {
    // A stable explicit code also prevents duplicate creation after a lost response.
    const code = group?.code || `group-${crypto.randomUUID()}`;
    void dialog({ title: group ? "编辑用户组" : "添加用户组", width: 540,
      initial: { code, name: group?.name || "", description: group?.description || "" },
      fields: [
        { name: "name", label: "用户组名称", required: true, placeholder: "例如：老用户、代理用户" },
        { name: "code", label: "分组标识", required: true, disabled: !!group, help: "唯一标识，创建后不可修改。" },
        { name: "description", label: "备注", type: "textarea", placeholder: "说明这个分组的用途（选填）" },
      ],
      onSubmit: async values => {
        const name = String(values.name || "").trim();
        const stableCode = String(values.code || "").trim();
        if (!name || !stableCode) throw new Error("请填写名称和分组标识");
        await api.write(group ? `v1/user-groups/${group.id}` : "v1/user-groups", { ...values, name, code: stableCode });
        await query.refetch();
      },
    });
  };
  return <>
    <PageHeader title="用户组管理" description="管理用户分组、套餐可见范围、专属价格和优惠券适用范围。" extra={can("iam.user.write") && <Button type="primary" onClick={() => edit()}>添加用户组</Button>} />
    <Space className="mb"><Input.Search aria-label="搜索用户组" placeholder="搜索名称、标识或备注" allowClear value={search} onChange={e => setSearch(e.target.value)} /><Button onClick={() => void query.refetch()}>刷新</Button></Space>
    <QueryPanel query={query}><Table<Row> rowKey="id" size="small" scroll={{ x: 820 }} dataSource={rows(query.data, "groups").filter(g => [g.name, g.code, g.description].join(" ").toLowerCase().includes(search.toLowerCase()))} columns={[
      { title: "名称", dataIndex: "name" }, { title: "标识", dataIndex: "code", ellipsis: true },
      { title: "备注", dataIndex: "description", ellipsis: true },
      ...[["users", "用户"], ["plans", "套餐"], ["prices", "专属价格"], ["coupons", "优惠券"]].map(([key, title]) => ({ title, dataIndex: key, width: 85 })),
      { title: "操作", width: 140, render: (_, group) => can("iam.user.write") && <Space><Button type="link" onClick={() => edit(group)}>编辑</Button><Tooltip title={["users", "plans", "prices", "coupons"].some(k => Number(group[k]) > 0) ? "请先解除用户、套餐、价格或优惠券的引用" : "删除空分组"}><Button type="link" danger disabled={["users", "plans", "prices", "coupons"].some(k => Number(group[k]) > 0)} onClick={() => void dialog({ title: `删除用户组「${group.name}」`, description: "只有没有关联数据的分组才能删除。", danger: true, submitLabel: "删除", onSubmit: async () => { await api.write(`v1/user-groups/${group.id}`, {}, { method: "DELETE" }); await query.refetch(); } })}>删除</Button></Tooltip></Space> },
    ]} /></QueryPanel>
  </>;
}
