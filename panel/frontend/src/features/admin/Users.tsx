/**
 * [INPUT]: 依赖 core/auth、core/dialogs、core/operations 的 Operation、core/runtime 的 runtime 与 hasContract、core/data、core/numbers、core/api 的 failure、components/common、@tanstack/react-query 的 useQueryClient
 * [OUTPUT]: 对外提供 UsersPage、UserDetail 组件
 * [POS]: features/admin 的用户列表与详情页：状态、分组、改密、余额调账、流量重置、订阅链接重置；调账回查按钮受待接契约 balance-operation-v1 门控，默认走整数 amount 的现行接口
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { principalSubject, generatedUsersSchema } from "../../core/data";
import { useState } from "react";
import { Alert, App, Button, Card, Space, Tabs, Typography } from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { Link, useParams } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { hasContract, runtime } from "../../core/runtime";
import { enc, idOf, rows, text, dateText, type Row } from "../../core/data";
import { bytes, legacyInteger, money, parseMinor } from "../../core/numbers";
import {
  BackLink,
  DataTable,
  Details,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../../components/common";
import { failure } from "../../core/api";

export function UsersPage() {
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const client = useQueryClient();
  const { message } = App.useApp();
  const create = async () => {
    const operation = new Operation("user-create", principalSubject(principal));
    await open({
      title: "新建用户",
      description: "创建一个账户，口令只展示一次，请妥善交付。",
      fields: [
        { name: "email_prefix", label: "邮箱前缀", required: true },
        {
          name: "email_domain",
          label: "邮箱域名",
          required: true,
          placeholder: "example.com",
        },
      ],
      onSubmit: async (values) => {
        const result = await operation.send(
          api,
          "v1/users/bulk/generate",
          {
            count: 1,
            email_prefix: text(values.email_prefix, ""),
            email_domain: text(values.email_domain, ""),
            group_id: "",
            reason: "管理员手动创建",
          },
          {},
          generatedUsersSchema,
        );
        await client.invalidateQueries({ queryKey: [runtime.domain] });
        void open({
          title: "用户已生成",
          description: (
            <div>
              {rows(result, "users").map((row) => (
                <Card key={text(row.email)}>
                  <Typography.Paragraph copyable>
                    {text(row.email)}
                  </Typography.Paragraph>
                  <Typography.Paragraph copyable>
                    {text(row.password)}
                  </Typography.Paragraph>
                </Card>
              ))}
            </div>
          ),
          submitLabel: "已安全保存",
        });
        message.success("用户已创建");
      },
    });
  };
  return (
    <ResourcePage
      title="用户"
      description="把订阅、订单与支持记录放在同一个上下文中处理。"
      resource="users"
      path="v1/users"
      listKey="users"
      serverPagination
      searchable
      statuses={[
        { value: "active", label: "正常" },
        { value: "suspended", label: "停用" },
        { value: "banned", label: "封禁" },
      ]}
      extra={
        can("iam.user.write") && (
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => void create()}
          >
            新建用户
          </Button>
        )
      }
      columns={[
        {
          title: "用户",
          key: "email",
          render: (_, row) => (
            <div>
              <EntityLink row={row} path="/users" label="email" />
              <div className="secondary small">
                {text(row.group_name, "未分组")}
              </div>
            </div>
          ),
        },
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        {
          title: "当前套餐",
          dataIndex: "active_plan",
          render: (value) => text(value, "暂无订阅"),
        },
        {
          title: "余额",
          key: "balance",
          render: (_, row) => money(row.balance, row.currency),
        },
        { title: "注册时间", dataIndex: "created_at", render: dateText },
        { title: "最近登录", dataIndex: "last_login_at", render: dateText },
      ]}
    />
  );
}

export function UserDetail() {
  const { id = "" } = useParams();
  const query = useData("users", `v1/users/${enc(id)}`);
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [profileEnabled, setProfileEnabled] = useState(false);
  const profile = useData(
    "profiles",
    `v1/users/${enc(id)}/profile`,
    profileEnabled && can("security.audit.read"),
  );
  const user = query.data || {};
  const subject = principalSubject(principal);
  const [pending, setPending] = useState(() => Operation.restore(subject));
  const refresh = () => void query.refetch();
  const rotateSubscription = (subscription: Row) =>
    open({
      title: "重置用户订阅链接",
      description: `${text(user.email)} 的 ${text(subscription.plan_name)} 旧链接将立即失效，需要通知用户重新导入。`,
      danger: true,
      submitLabel: "确认重置链接",
      onSubmit: async () => {
        await api.write(
          `v1/subscriptions/${enc(idOf(subscription))}/rotate`,
          {},
        );
        message.success("链接已重置，请提醒用户在用户中心复制新链接");
        refresh();
      },
    });
  const balance = async () => {
    const operation = new Operation("balance-adjustment", subject, true);
    const modern = hasContract("balance-operation-v1");
    await open({
      title: "调整账户余额",
      description: (
        <>
          <p>
            {text(user.email)} · {text(user.currency, "CNY")}
          </p>
          <Alert
            type="warning"
            showIcon
            title="增加使用正数，扣减使用负数。网络失败后请保留原金额与理由重试。"
          />
          {!modern && (
            <p className="secondary">
              当前使用兼容接口；同标签保存操作身份，独立结果查询尚未启用。
            </p>
          )}
        </>
      ),
      fields: [
        {
          name: "amount",
          label: "调整金额（元）",
          required: true,
          placeholder: "例如 10.00 或 -10.00",
        },
        { name: "reason", label: "调整理由", type: "textarea", required: true },
      ],
      onSubmit: async (values) => {
        const minor = parseMinor(text(values.amount, ""), {
          signed: true,
          zero: false,
        });
        const reason = text(values.reason, "").trim();
        if (reason.length < 5) throw new Error("请填写至少5个字符的调整理由");
        const payload: Row = modern
          ? {
              operation_id: operation.id,
              amount_minor: minor,
              currency: text(user.currency, "CNY"),
              reason,
            }
          : {
              amount: legacyInteger(minor),
              currency: text(user.currency, "CNY"),
              reason,
            };
        try {
          await operation.send(api, `v1/users/${enc(id)}/balance`, payload);
          message.success("余额调整已确认");
          refresh();
        } finally {
          setPending(Operation.restore(subject));
        }
      },
    });
  };
  const status = () =>
    open({
      title: "修改账户状态",
      description: "停用或封禁将撤销该用户会话。",
      initial: { status: user.status },
      fields: [
        {
          name: "status",
          label: "目标状态",
          type: "select",
          required: true,
          options: [
            { label: "正常", value: "active" },
            { label: "停用", value: "suspended" },
            { label: "封禁", value: "banned" },
          ],
        },
        { name: "reason", label: "处理原因", type: "textarea", required: true },
      ],
      onSubmit: async (values) => {
        await api.write(`v1/users/${enc(id)}/status`, values);
        message.success("状态已更新");
        refresh();
      },
    });
  const assignGroup = async () => {
    try {
      const result = await api.get("v1/user-groups");
      await open({
        title: "设置用户分组",
        initial: { group_id: user.group_id || "" },
        fields: [
          {
            name: "group_id",
            label: "用户分组",
            type: "select",
            options: [
              { label: "未分组", value: "" },
              ...rows(result, "groups").map((row) => ({
                label: text(row.name),
                value: idOf(row),
              })),
            ],
          },
        ],
        onSubmit: async (values) => {
          await api.write(`v1/users/${enc(id)}/group`, {
            group_id: values.group_id || "",
          });
          refresh();
        },
      });
    } catch (error) {
      message.error(failure(error).message);
    }
  };
  const resetPassword = () =>
    open({
      title: "重置用户密码",
      description: "重置后请通过安全渠道将新密码交给用户。",
      fields: [
        {
          name: "new_password",
          label: "新密码",
          type: "password",
          required: true,
        },
        { name: "reason", label: "原因", required: true },
      ],
      onSubmit: async (values) => {
        await api.write(`v1/users/${enc(id)}/reset-password`, values);
        message.success("密码已重置");
      },
    });
  const resetTraffic = () => {
    const operation = new Operation("traffic-reset", subject);
    return open({
      title: "重置用户流量",
      description:
        "将对该用户当前符合条件的订阅重置已用额度，写入人工重置记录。",
      danger: true,
      fields: [
        { name: "note", label: "重置原因", required: true, type: "textarea" },
      ],
      onSubmit: async (values) => {
        const result = await operation.send(
          api,
          `v1/users/${enc(id)}/traffic-reset`,
          values,
        );
        message.success(`已确认释放流量 ${bytes(result.freed_bytes)}`);
        refresh();
      },
    });
  };
  return (
    <>
      <PageHeader
        title={text(user.email, "用户详情")}
        description="用户资料与业务记录"
        extra={<BackLink to="/users" />}
      />
      <QueryPanel query={query}>
        <Card className="mb">
          <div className="detail-heading">
            <Status value={user.status} />
            <Space wrap>
              {can("metering.reset.write") && (
                <Button onClick={() => void resetTraffic()}>重置流量</Button>
              )}
              {can("billing.provider.write") && (
                <Button
                  type="primary"
                  disabled={pending.length > 0}
                  onClick={() => void balance()}
                >
                  调整余额
                </Button>
              )}
              {can("iam.user.write") && (
                <>
                  <Button onClick={() => void assignGroup()}>设置分组</Button>
                  <Button onClick={() => void resetPassword()}>重置密码</Button>
                  <Button onClick={() => void status()}>账户状态</Button>
                </>
              )}
            </Space>
          </div>
          <Details
            data={user}
            fields={[
              ["id", "用户ID"],
              ["email", "邮箱"],
              ["balance", "账户余额", (value) => money(value, user.currency)],
              ["group_name", "用户分组"],
              ["created_at", "注册时间", dateText],
              ["last_login_at", "最后登录", dateText],
            ]}
          />
        </Card>
        {pending.length > 0 && (
          <Alert
            className="mb"
            type="warning"
            showIcon
            title="此管理员有待核实操作"
            description={
              <Space direction="vertical">
                {pending.map((operation) => (
                  <div key={operation.id}>
                    <Typography.Text code>{operation.id}</Typography.Text>
                    {hasContract("balance-operation-v1") &&
                      can("billing.ledger.read") && (
                        <Button
                          size="small"
                          onClick={async () => {
                            try {
                              const result = await operation.verify(api);
                              setPending(Operation.restore(subject));
                              refresh();
                              message.success(
                                `原操作已确认，历史余额 ${money(result.balance_after_minor, result.currency)}`,
                              );
                            } catch (value) {
                              const error = failure(value);
                              message.error(
                                error.status === 404
                                  ? "尚未查到提交结果，仍需核实；只能重试原操作，不能创建新意图"
                                  : error.message,
                              );
                            }
                          }}
                        >
                          查询结果
                        </Button>
                      )}
                    <Button
                      size="small"
                      onClick={async () => {
                        try {
                          await operation.replay(api);
                          setPending(Operation.restore(subject));
                          refresh();
                          message.success("原操作已确认");
                        } catch (error) {
                          message.error(failure(error).message);
                        }
                      }}
                    >
                      重试原操作
                    </Button>
                  </div>
                ))}
                <span>只重放已保存的相同业务参数，不会创建新的调账意图。</span>
              </Space>
            }
          />
        )}
        <Card>
          <Tabs
            items={[
              {
                key: "subscriptions",
                label: "订阅",
                children: (
                  <DataTable
                    data={rows(user, "subscriptions")}
                    columns={[
                      { title: "套餐", dataIndex: "plan_name" },
                      {
                        title: "状态",
                        dataIndex: "status",
                        render: (value) => <Status value={value} />,
                      },
                      {
                        title: "到期时间",
                        dataIndex: "current_period_end",
                        render: dateText,
                      },
                      {
                        title: "金额",
                        render: (_, row) => money(row.amount, row.currency),
                      },
                      ...(can("iam.user.write")
                        ? [
                            {
                              title: "操作",
                              render: (_: unknown, row: Row) => (
                                <Button
                                  type="link"
                                  onClick={() => void rotateSubscription(row)}
                                >
                                  重置链接
                                </Button>
                              ),
                            },
                          ]
                        : []),
                    ]}
                  />
                ),
              },
              {
                key: "orders",
                label: "最近订单",
                children: (
                  <DataTable
                    data={rows(user, "recent_orders")}
                    columns={[
                      {
                        title: "订单号",
                        render: (_, row) => (
                          <EntityLink
                            row={row}
                            path="/orders"
                            label="order_no"
                          />
                        ),
                      },
                      {
                        title: "状态",
                        dataIndex: "status",
                        render: (value) => <Status value={value} />,
                      },
                      {
                        title: "金额",
                        render: (_, row) =>
                          money(row.total_amount, row.currency),
                      },
                      {
                        title: "时间",
                        dataIndex: "created_at",
                        render: dateText,
                      },
                    ]}
                  />
                ),
              },
              ...(can("security.audit.read")
                ? [
                    {
                      key: "profile",
                      label: "访问画像",
                      children: profileEnabled ? (
                        <QueryPanel query={profile}>
                          <DataTable
                            data={rows(profile.data, "fetches")}
                            columns={[
                              { title: "来源IP", dataIndex: "ip" },
                              { title: "客户端", dataIndex: "family" },
                              { title: "结果", dataIndex: "result" },
                              {
                                title: "时间",
                                dataIndex: "at",
                                render: dateText,
                              },
                            ]}
                          />
                        </QueryPanel>
                      ) : (
                        <Button onClick={() => setProfileEnabled(true)}>
                          加载有权限的访问记录
                        </Button>
                      ),
                    },
                  ]
                : []),
              {
                key: "support",
                label: "支持",
                children: (
                  <Link
                    to={`/tickets?q=${encodeURIComponent(text(user.email, ""))}`}
                  >
                    在工单队列中查找该用户
                  </Link>
                ),
              },
            ]}
          />
        </Card>
      </QueryPanel>
    </>
  );
}
