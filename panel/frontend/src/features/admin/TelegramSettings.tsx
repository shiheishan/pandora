import { Button, Card, Tag } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Details, PageHeader, QueryPanel, useData } from "../../components/common";

export function TelegramSettings() {
  const query = useData("telegram-settings", "v1/settings/telegram");
  const { api, can } = useAuth();
  const dialog = useDialog();
  return <>
    <PageHeader title="Telegram 设置" description="配置用于通知的机器人。启用前，请填写机器人用户名和 Bot Token。" />
    <QueryPanel query={query}>
      <Card title="机器人配置">
        <Details data={query.data || {}} fields={[["bot_username", "机器人用户名"]]} />
        <p><Tag color={query.data?.enabled ? "green" : "default"}>{query.data?.enabled ? "已启用" : "未启用"}</Tag>
          <Tag>{query.data?.has_token ? "Token 已配置" : "Token 未配置"}</Tag></p>
        <p>Bot Token 不会回显。修改时留空保留原值；填写新值会替换现有 Token。</p>
        {can("platform.settings.write") && <Button type="primary" onClick={() => void dialog({
          title: "编辑 Telegram 设置",
          initial: { enabled: query.data?.enabled === true, bot_username: query.data?.bot_username || "", bot_token: "" },
          fields: [
            { name: "enabled", label: "启用 Telegram 通知", type: "switch" },
            { name: "bot_username", label: "机器人用户名（可省略 @）" },
            { name: "bot_token", label: "Bot Token（留空保留）", type: "password" },
          ],
          onSubmit: async values => {
            const username = String(values.bot_username || "").trim().replace(/^@/, "");
            const token = String(values.bot_token || "").trim();
            if (values.enabled && !username) throw new Error("启用前请填写机器人用户名");
            if (values.enabled && !token && !query.data?.has_token) throw new Error("首次启用前请填写 Bot Token");
            await api.write("v1/settings/telegram", { enabled: values.enabled === true, bot_username: username, bot_token: token });
            await query.refetch();
          },
        })}>编辑机器人配置</Button>}
      </Card>
    </QueryPanel>
  </>;
}
