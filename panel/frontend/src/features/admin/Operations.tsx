import { MailTemplates } from "./MailTemplates";
import { principalSubject, generatedUsersSchema } from "../../core/data";
import { App, Button, Card, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useParams } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import {
  dateText,
  enc,
  idOf,
  integer,
  record,
  text,
  type Row,
} from "../../core/data";
import { bytes, legacyInteger, money, parseMinor } from "../../core/numbers";
import {
  DataTable,
  Details,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  Unavailable,
  useData,
} from "../../components/common";
import { useQueryClient } from "@tanstack/react-query";
import { runtime } from "../../core/runtime";

type Screen = {
  title: string;
  path: string;
  key: string;
  fields: [
    string,
    string,
    ("date" | "status" | "money" | "bytes" | "boolean")?,
  ][];
  paginated?: boolean;
  search?: boolean;
};
const screens: Record<string, Screen> = {
  "device-limits": {
    title: "设备限制",
    path: "v1/devices",
    key: "devices",
    fields: [
      ["email", "用户"],
      ["plan", "套餐"],
      ["online", "在线设备"],
      ["limit", "设备上限"],
      ["exceeded", "超限", "boolean"],
      ["last_seen_at", "最后在线", "date"],
    ],
  },
  "traffic-resets": {
    title: "流量重置记录",
    path: "v1/traffic-resets",
    key: "logs",
    paginated: true,
    fields: [
      ["user_email", "用户"],
      ["reason", "触发方式"],
      ["consumed_before", "重置前用量", "bytes"],
      ["note", "说明"],
      ["created_at", "执行时间", "date"],
    ],
  },
  "late-payments": {
    title: "待处理款项",
    path: "v1/late-payments",
    key: "cases",
    paginated: true,
    fields: [
      ["order_no", "订单号"],
      ["user_email", "用户"],
      ["amount", "金额", "money"],
      ["status", "状态", "status"],
      ["received_at", "收款时间", "date"],
    ],
  },
  coupons: {
    title: "优惠券",
    path: "v1/coupons",
    key: "coupons",
    paginated: true,
    search: true,
    fields: [
      ["code", "优惠码"],
      ["name", "名称"],
      ["discount_type", "优惠方式"],
      ["discount_value", "折扣值"],
      ["redeemed_count", "已使用"],
      ["status", "状态", "status"],
      ["valid_until", "有效期", "date"],
    ],
  },
  "gift-cards": {
    title: "礼品卡模板",
    path: "v1/gift-cards",
    key: "templates",
    fields: [
      ["name", "名称"],
      ["type", "类型"],
      ["status", "状态", "status"],
      ["created_at", "创建时间", "date"],
    ],
  },
  commissions: {
    title: "佣金提现",
    path: "v1/withdrawals",
    key: "withdrawals",
    fields: [
      ["email", "用户"],
      ["amount", "提现金额", "money"],
      ["status", "状态", "status"],
      ["payout_detail", "收款信息"],
      ["reject_reason", "驳回原因"],
      ["requested_at", "申请时间", "date"],
    ],
  },
  "payment-providers": {
    title: "支付渠道",
    path: "v1/payment-providers",
    key: "providers",
    fields: [
      ["display_name", "渠道名称"],
      ["code", "代码"],
      ["enabled", "启用", "boolean"],
      ["accepting_new", "接收新支付", "boolean"],
      ["has_credentials", "已配置凭据", "boolean"],
    ],
  },
  announcements: {
    title: "公告",
    path: "v1/announcements",
    key: "announcements",
    fields: [
      ["title", "标题"],
      ["severity", "级别"],
      ["status", "状态", "status"],
      ["pinned", "置顶", "boolean"],
      ["version", "版本"],
      ["publish_at", "发布时间", "date"],
    ],
  },
  content: {
    title: "知识库",
    path: "v1/content-pages?limit=200",
    key: "pages",
    fields: [
      ["title", "标题"],
      ["slug", "地址别名"],
      ["category", "分类"],
      ["status", "状态", "status"],
      ["version", "版本"],
      ["updated_at", "更新", "date"],
    ],
  },
  appearance: {
    title: "外观主题",
    path: "v1/themes",
    key: "themes",
    fields: [
      ["name", "主题名称"],
      ["code", "代码"],
      ["is_active", "当前使用", "boolean"],
      ["is_builtin", "内置主题", "boolean"],
    ],
  },
  plugins: {
    title: "Webhook插件",
    path: "v1/plugin-hooks",
    key: "hooks",
    fields: [
      ["name", "名称"],
      ["code", "代码"],
      ["endpoint_url", "回调地址"],
      ["enabled", "启用", "boolean"],
      ["max_attempts", "最多尝试"],
      ["timeout_ms", "超时ms"],
    ],
  },
  risk: {
    title: "风控访问记录",
    path: "v1/access-log",
    key: "items",
    fields: [
      ["user_email", "账户"],
      ["ip", "来源IP"],
      ["action", "行为"],
      ["outcome", "结果"],
      ["occurred_at", "时间", "date"],
    ],
  },
  switches: {
    title: "服务降级开关",
    path: "v1/switches",
    key: "switches",
    fields: [
      ["code", "功能"],
      ["enabled", "启用", "boolean"],
      ["essential", "核心功能", "boolean"],
      ["reason", "原因"],
    ],
  },
  audit: {
    title: "审计日志",
    path: "v1/audit",
    key: "events",
    paginated: true,
    fields: [
      ["occurred_at", "时间", "date"],
      ["actor_email", "操作者"],
      ["action", "操作"],
      ["resource_type", "资源类型"],
      ["resource_id", "资源ID"],
      ["outcome", "结果"],
      ["reason", "原因"],
    ],
  },
};
const unfinishedActions: Record<string, string> = {
  coupons: "优惠券创建、批量生成和使用明细尚未迁移。",
  "gift-cards": "模板奖励编辑、发码、停码及使用明细尚未迁移。",

  plugins: "Webhook配置、投递记录和失败重试尚未迁移。",
  appearance:
    "当前可切换服务端主题；新界面暂使用固定设计，主题令牌和插槽编辑尚未迁移。",
  "payment-providers": "当前可管理渠道启停；支付凭据编辑与渠道测试尚未迁移。",
  risk: "当前展示访问日志；安全策略与管理员会话处置尚未迁移。",
};
export function OperationsPage({ screen }: { screen: string }) {
  const config = screens[screen];
  const { api, can, principal } = useAuth();
  const dialog = useDialog();
  const client = useQueryClient();
  const { message } = App.useApp();
  if (!config)
    return (
      <Unavailable
        title="此页面尚未迁移"
        description="可以从账户菜单返回原版，继续处理已有业务。"
      />
    );
  const refresh = () =>
    client.invalidateQueries({ queryKey: [runtime.domain] });
  const action = (
    title: string,
    path: string,
    payload: Row,
    reason = false,
  ) => {
    const operation = new Operation(screen, principalSubject(principal));
    return dialog({
      title,
      description:
        "提交后会记录真实操作结果。网络结果不明时请保持相同参数重试。",
      fields: reason
        ? [
            {
              name: "reason",
              label: "处理原因",
              required: true,
              type: "textarea",
            },
          ]
        : [],
      onSubmit: async (values) => {
        await operation.send(api, path, { ...payload, ...values });
        await refresh();
        message.success("操作已确认");
      },
    });
  };
  const rowActions = (row: Row) => {
    if(screen === "commissions" && can("billing.provider.write")) return <Space>
      {["requested","reviewing"].includes(text(row.status)) && <><Button type="link" onClick={()=>void action("批准提现申请", `v1/withdrawals/${enc(idOf(row))}/review`, {action:"approve"})}>批准</Button><Button type="link" danger onClick={()=>void action("拒绝提现申请", `v1/withdrawals/${enc(idOf(row))}/review`, {action:"reject"}, true)}>拒绝</Button></>}
      {row.status === "approved" && <Button type="link" onClick={()=>{
        const op = new Operation("withdrawal-paid",principalSubject(principal));
        void dialog({title:"登记实际打款",description:`用户：${text(row.email)}，金额：${money(row.amount,row.currency)}。请在实际转账完成后登记，系统会记录账本支出。`,fields:[{name:"payout_reference",label:"实际转账流水号",required:true}],submitLabel:"确认已打款",onSubmit:async v=>{await op.send(api,`v1/withdrawals/${enc(idOf(row))}/paid`,{payout_reference:text(v.payout_reference).trim()});}});
      }}>登记已打款</Button>}
    </Space>;

    if (screen === "payment-providers" && can("billing.provider.write"))
      return (
        <Button
          type="link"
          onClick={() =>
            void action(
              row.enabled ? "停用渠道" : "启用渠道",
              `v1/payment-providers/${enc(row.code)}/toggle`,
              { enabled: !row.enabled, accepting_new: !row.enabled },
            )
          }
        >
          {row.enabled ? "停用" : "启用"}
        </Button>
      );
    if (
      screen === "late-payments" &&
      can("billing.adjustment.write") &&
      row.status === "pending"
    )
      return (
        <Button
          type="link"
          onClick={() =>
            void action(
              "将待处理款项入用户余额",
              `v1/late-payments/${enc(idOf(row))}/apply-to-balance`,
              {},
              true,
            )
          }
        >
          处理入账
        </Button>
      );
    if (screen === "switches" && can("platform.settings.write"))
      return (
        <Button
          type="link"
          onClick={() =>
            void action(
              row.enabled ? "停用功能" : "启用功能",
              `v1/switches/${enc(row.code)}`,
              { enabled: !row.enabled },
              true,
            )
          }
        >
          {row.enabled ? "停用" : "启用"}
        </Button>
      );
    if (screen === "device-limits" && can("iam.user.write"))
      return (
        <Button
          type="link"
          onClick={() =>
            void dialog({
              title: "设置订阅设备上限",
              initial: { limit: row.limit },
              fields: [
                {
                  name: "limit",
                  label: "设备上限",
                  type: "number",
                  min: 0,
                  max: 100000,
                  help: "留空取消单独覆盖，恢复套餐默认。",
                },
              ],
              onSubmit: async (values) => {
                await api.write(
                  `v1/subscriptions/${enc(row.subscription_id)}/device-limit`,
                  { limit: values.limit ?? null },
                );
                await refresh();
              },
            })
          }
        >
          调整限制
        </Button>
      );
    if (screen === "announcements" && can("ops.announcement.write"))
      return (
        <Space>
          <Button type="link" onClick={() => void editAnnouncement(row)}>
            编辑
          </Button>
          {row.status === "published" && (
            <Button
              type="link"
              danger
              onClick={() =>
                void action(
                  "撤回公告",
                  `v1/announcements/${enc(idOf(row))}/withdraw`,
                  { expected_version: row.version },
                )
              }
            >
              撤回
            </Button>
          )}
        </Space>
      );
    if (screen === "appearance" && can("platform.appearance.write"))
      return (
        <Button
          type="link"
          disabled={Boolean(row.is_active)}
          onClick={() =>
            void action(
              "切换当前主题",
              `v1/themes/${enc(row.code)}/activate`,
              {},
            )
          }
        >
          应用主题
        </Button>
      );
    if (screen === "coupons" && can("marketing.coupon.write"))
      return (
        <Button
          type="link"
          onClick={() =>
            void action(
              "更改优惠券状态",
              `v1/coupons/${enc(idOf(row))}/status`,
              { status: row.status === "active" ? "paused" : "active" },
            )
          }
        >
          {row.status === "active" ? "停用" : "启用"}
        </Button>
      );
    return null;
  };
  const editAnnouncement = (row?: Row) => {
    const operation = new Operation(
      "announcement-save",
      principalSubject(principal),
    );
    return dialog({
      title: row ? "编辑公告" : "新建公告",
      initial: row
        ? { ...row, publish: row.status === "published" }
        : { severity: "info", publish: false },
      fields: [
        { name: "title", label: "标题", required: true },
        { name: "body", label: "正文", type: "textarea", required: true },
        {
          name: "severity",
          label: "级别",
          type: "select",
          options: ["info", "warning", "critical"].map((value) => ({
            value,
            label: value,
          })),
        },
        { name: "pinned", label: "置顶", type: "switch" },
        { name: "publish", label: "立即发布", type: "switch" },
      ],
      onSubmit: async (values) => {
        await operation.send(
          api,
          `v1/announcements${row ? "/" + enc(idOf(row)) : ""}`,
          {
            ...values,
            target_plan_ids: row?.target_plan_ids || [],
            publish_at: row?.publish_at || "",
            expires_at: row?.expires_at || "",
            expected_version: row?.version || 0,
          },
        );
        await refresh();
      },
    });
  };
  const columns: ColumnsType<Row> = config.fields.map(([key, label, kind]) => ({
    title: label,
    dataIndex: key,
    render: (value, row) =>
      kind === "date" ? (
        dateText(value)
      ) : kind === "status" ? (
        <Status value={value} />
      ) : kind === "money" ? (
        money(value, row.currency)
      ) : kind === "bytes" ? (
        bytes(value)
      ) : kind === "boolean" ? (
        value === true ? (
          "是"
        ) : value === false ? (
          "否"
        ) : (
          "—"
        )
      ) : (
        text(value)
      ),
  }));
  columns.push({
    title: "操作",
    key: "actions",
    render: (_, row) => rowActions(row),
  });
  return (
    <ResourcePage
      title={config.title}
      resource={screen}
      path={config.path}
      listKey={config.key}
      columns={columns}
      serverPagination={config.paginated}
      searchable={config.search}
      extra={
        screen === "announcements" &&
        can("ops.announcement.write") && (
          <Button type="primary" onClick={() => void editAnnouncement()}>
            新建公告
          </Button>
        )
      }
      footer={
        unfinishedActions[screen] && (
          <Unavailable
            title="此页还有操作等待迁移"
            description={
              unfinishedActions[screen]! + " 可从账户菜单返回原版继续处理。"
            }
          />
        )
      }
    />
  );
}
export function NotificationSettings() {
  const query = useData("mail-settings", "v1/settings/mail");
  const { api, can } = useAuth();
  const dialog = useDialog();
  return (
    <>
      <PageHeader
        title="通知设置"
        description="邮件配置与实际发送能力分别核对。"
      />
      <QueryPanel query={query}>
        <Card title="邮件服务" className="mb">
          <Details
            data={query.data || {}}
            fields={[
              ["encryption", "加密模式"],
              ["smtp_host", "SMTP主机"],
              ["smtp_port", "端口"],
              ["smtp_username", "用户名"],
              ["from_address", "发信邮箱"],
              ["from_name", "发信名称"],
            ]}
          />
          {can("platform.settings.write") && (
            <Button
              onClick={() =>
                void dialog({
                  title: "修改邮件设置",
                  initial: query.data,
                  fields: [
                    {
                      name: "encryption",
                      label: "加密模式",
                      type: "select",
                      required: true,
                      options: [
                        { value: "ssl", label: "SSL" },
                        { value: "tls", label: "STARTTLS" },
                        { value: "none", label: "无加密" },
                      ],
                    },
                    { name: "smtp_host", label: "SMTP主机" },
                    {
                      name: "smtp_port",
                      label: "端口",
                      type: "number",
                      min: 1,
                      max: 65535,
                    },
                    { name: "smtp_username", label: "用户名" },
                    {
                      name: "smtp_password",
                      label: "密码（留空保留）",
                      type: "password",
                    },
                    { name: "from_address", label: "发件邮箱" },
                    { name: "from_name", label: "发件名称" },
                  ],
                  onSubmit: async (values) => {
                    await api.write("v1/settings/mail", values);
                    await query.refetch();
                  },
                })
              }
            >
              编辑SMTP设置
            </Button>
          )}
        </Card>
      </QueryPanel>
      {can("ops.notification.read") ? (
        <MailTemplates/>
      ) : (
        <Unavailable
          title="邮件模板需要读取权限"
          description="此账户没有 ops.notification.read 权限。SMTP设置使用独立权限。"
        />
      )}
    </>
  );
}
export function BulkPage() {
  const { api, principal, can } = useAuth();
  const dialog = useDialog();
  const { message } = App.useApp();
  const generate = () => {
    const attempt = new Operation("bulk-generate", principalSubject(principal));
    return dialog({
      title: "批量生成账户",
      description: "创建后口令仅展示一次，请立即安全保存。",
      initial: { count: 1 },
      fields: [
        {
          name: "count",
          label: "数量",
          type: "number",
          required: true,
          min: 1,
          max: 500,
        },
        { name: "email_prefix", label: "邮箱前缀", required: true },
        { name: "email_domain", label: "邮箱域名", required: true },
        { name: "reason", label: "创建理由", required: true },
      ],
      onSubmit: async (values) => {
        const result = await attempt.send(
          api,
          "v1/users/bulk/generate",
          {
            ...values,
            group_id: "",
          },
          {},
          generatedUsersSchema,
        );
        void dialog({
          title: "已创建的账户 · 请保存口令",
          width: 760,
          description: (
            <DataTable
              data={Array.isArray(result.users) ? (result.users as Row[]) : []}
              columns={[
                { title: "邮箱", dataIndex: "email" },
                { title: "口令（仅此次）", dataIndex: "password" },
              ]}
            />
          ),
          submitLabel: "已保存",
        });
        message.success("批量创建已确认");
      },
    });
  };
  return (
    <>
      <PageHeader
        title="批量运营"
        description="先确认目标范围，再执行批量操作。"
      />
      <Card>
        <Space wrap>
          {can("iam.user.write") && (
            <Button type="primary" onClick={() => void generate()}>
              批量生成账户
            </Button>
          )}
          <Button disabled>批量邮件：目标预览与发送确认仍在迁移</Button>
          <Button disabled>导出：敏感字段选择仍在迁移</Button>
        </Space>
      </Card>
    </>
  );
}
export function ReadRecord() {
  const { id = "" } = useParams();
  return (
    <Unavailable
      title="记录详情暂未迁移"
      description={`记录 ${id} 请从原版专用页面查看。`}
    />
  );
}
export const amountToNumber = (value: unknown) =>
  legacyInteger(parseMinor(text(value, ""), { zero: false }));
export const countValue = integer;
export const objectValue = record;
