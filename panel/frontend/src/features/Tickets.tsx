import { principalSubject, createdIdSchema } from "../core/data";
import { BackLink } from "../components/common";
import { useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Input,
  Space,
  Tag,
  Typography,
} from "antd";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../core/auth";
import { runtime } from "../core/runtime";
import { useDraft } from "../core/drafts";
import { useDialog } from "../core/dialogs";
import { Operation } from "../core/operations";
import { enc, rows, text, dateText } from "../core/data";
import { failure } from "../core/api";
import {
  Details,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../components/common";

const admin = runtime.domain === "admin";
const endpoint = admin ? "v1/tickets" : "v1/support/tickets";
const route = admin ? "/tickets" : "/support";
export function TicketsPage() {
  const { api, principal } = useAuth();
  const dialog = useDialog();
  const navigate = useNavigate();
  const draft = useDraft(principalSubject(principal), "ticket:create");
  const create = () => {
    const operation = new Operation(
      "ticket-create",
      principalSubject(principal),
    );
    return dialog({
      title: "提交工单",
      description: "描述问题、发生时间及已尝试的步骤。请勿填写密码或订阅密钥。",
      fields: [
        { name: "body", label: "问题说明", type: "textarea", required: true },
      ],
      initial: { body: draft.value },
      onValuesChange: (values) => draft.setValue(text(values.body, "")),
      onSubmit: async (values) => {
        const body = text(values.body, "").trim();
        if (body.length < 10) throw new Error("问题说明至少需要10个字符");
        const result = await operation.send(
          api,
          endpoint,
          { body },
          {},
          createdIdSchema,
        );
        draft.clear();
        navigate(`${route}/${enc(text(result.id))}`);
      },
    });
  };
  return (
    <ResourcePage
      title={admin ? "工单工作台" : "帮助与支持"}
      description={
        admin
          ? "查看客户上下文，连续回复并跟进处理状态。"
          : "提交问题后，可以在这里继续补充和查看回复。"
      }
      resource="tickets"
      path={endpoint}
      listKey="tickets"
      serverPagination={admin}
      searchable={admin}
      extra={
        !admin && (
          <Button type="primary" onClick={() => void create()}>
            提交工单
          </Button>
        )
      }
      columns={[
        {
          title: "工单",
          render: (_, row) => (
            <div>
              <EntityLink row={row} path={route} label="subject" />
              <div className="secondary small">{text(row.ticket_no)}</div>
            </div>
          ),
        },
        ...(admin
          ? [
              { title: "用户", dataIndex: "user_email" },
              {
                title: "负责人",
                dataIndex: "assignee_email",
                render: (value: unknown) => text(value, "未分配"),
              },
            ]
          : []),
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        { title: "回复数", dataIndex: "message_count" },
        { title: "最后更新", dataIndex: "last_reply_at", render: dateText },
      ]}
    />
  );
}
export function TicketDetail() {
  const { id = "" } = useParams();
  const { api, principal, can } = useAuth();
  const dialog = useDialog();
  const { message } = App.useApp();
  const query = useData("tickets", `${endpoint}/${enc(id)}`);
  const ticket = query.data || {};
  const draft = useDraft(principalSubject(principal), `ticket:${id}:reply`);
  const [internal, setInternal] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(
    new Operation("ticket-reply", principalSubject(principal)),
  );
  const writable = !admin || can("ops.ticket.write");
  const reply = async () => {
    if (busy) return;
    const body = draft.value.trim();
    if (!body) return;
    setBusy(true);
    setError("");
    try {
      await attempt.current.send(
        api,
        `${endpoint}/${enc(id)}/reply`,
        admin ? { body, internal_note: internal } : { body },
      );
      attempt.current = new Operation(
        "ticket-reply",
        principalSubject(principal),
      );
      draft.clear();
      message.success(internal ? "内部备注已保存" : "回复已发送");
      await query.refetch();
    } catch (value) {
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  const changeStatus = (status: string) => {
    const operation = new Operation(
      "ticket-status",
      principalSubject(principal),
    );
    return dialog({
      title: status === "closed" ? "关闭工单" : "标记为已解决",
      description: "会记录本次处理。已输入的回复草稿会保留。",
      fields: admin
        ? [{ name: "reason", label: "处理说明", type: "textarea" }]
        : [],
      onSubmit: async (values) => {
        await operation.send(
          api,
          admin
            ? `${endpoint}/${enc(id)}/status`
            : `${endpoint}/${enc(id)}/close`,
          admin ? { status, reason: text(values.reason, "") } : {},
        );
        await query.refetch();
      },
    });
  };
  const assign = async () => {
    try {
      const result = await api.get("v1/tickets/assignees");
      const operation = new Operation(
        "ticket-assign",
        principalSubject(principal),
      );
      await dialog({
        title: "指派负责人",
        fields: [
          {
            name: "assigned_to",
            label: "负责人",
            type: "select",
            options: [
              { value: "", label: "未分配" },
              ...rows(result, "assignees").map((row) => ({
                value: text(row.id),
                label: text(row.email),
              })),
            ],
          },
        ],
        initial: { assigned_to: ticket.assigned_to || "" },
        onSubmit: async (values) => {
          await operation.send(api, `${endpoint}/${enc(id)}/assign`, {
            assigned_to: values.assigned_to || "",
          });
          await query.refetch();
        },
      });
    } catch (value) {
      message.error(failure(value).message);
    }
  };
  return (
    <>
      <PageHeader
        title={text(ticket.subject, "工单详情")}
        description={text(ticket.ticket_no, "")}
        extra={
          <Space>
            <BackLink to={route}>返回工单列表</BackLink>
            {admin && writable && (
              <Button onClick={() => void assign()}>指派</Button>
            )}
            {writable && ticket.status !== "closed" && (
              <Button onClick={() => void changeStatus("closed")}>
                关闭工单
              </Button>
            )}
            {admin &&
              writable &&
              ticket.status !== "resolved" &&
              ticket.status !== "closed" && (
                <Button onClick={() => void changeStatus("resolved")}>
                  标记解决
                </Button>
              )}
          </Space>
        }
      />
      <QueryPanel query={query}>
        <div className="split-detail">
          <Card>
            <div className="thread" aria-live="polite">
              {rows(ticket, "messages")
                .filter((row) => admin || !row.internal_note)
                .map((row, index) => (
                  <article
                    className={`message ${row.author_kind === "agent" ? "agent" : ""} ${row.internal_note ? "note" : ""}`}
                    key={text(row.id, String(index))}
                  >
                    <div className="message-meta">
                      <b>
                        {row.internal_note
                          ? "内部备注 · 用户不可见"
                          : row.author_kind === "agent"
                            ? "客服"
                            : row.author_kind === "system"
                              ? "系统"
                              : "用户"}
                      </b>
                      <span>{dateText(row.created_at)}</span>
                    </div>
                    <div className="message-body">{text(row.body, "")}</div>
                  </article>
                ))}
            </div>
            {writable && ticket.status !== "closed" && (
              <div className="reply-composer">
                <Typography.Title level={5}>继续回复</Typography.Title>
                {error && (
                  <Alert type="error" title={error} showIcon className="mb" />
                )}
                <Input.TextArea
                  aria-label="回复内容"
                  rows={5}
                  value={draft.value}
                  onChange={(event) => draft.setValue(event.target.value)}
                  disabled={busy}
                  placeholder="输入回复内容…"
                />
                <div className="composer-footer">
                  <Space>
                    {admin && (
                      <Checkbox
                        checked={internal}
                        disabled={busy}
                        onChange={(event) => setInternal(event.target.checked)}
                      >
                        内部备注
                      </Checkbox>
                    )}
                    <Typography.Text type="secondary">
                      {draft.persisted
                        ? "草稿保存在此标签页"
                        : "草稿存储不可用，请勿关闭"}
                    </Typography.Text>
                  </Space>
                  <Button
                    type="primary"
                    loading={busy}
                    disabled={!draft.value.trim()}
                    onClick={() => void reply()}
                  >
                    发送{internal ? "备注" : "回复"}
                  </Button>
                </div>
              </div>
            )}
          </Card>
          <Card title="工单信息">
            <Status value={ticket.status} />
            <Details
              data={ticket}
              fields={[
                ["user_email", "用户"],
                ["category", "分类"],
                ["assignee_email", "负责人"],
                ["created_at", "创建时间", dateText],
                ["sla_first_response_due", "首次响应期限", dateText],
              ]}
            />
            {ticket.sla_breached === true && <Tag color="error">已超时</Tag>}
            {admin && ticket.user_id != null && (
              <Link to={`/users/${enc(text(ticket.user_id))}`}>
                查看用户资料及订单
              </Link>
            )}
          </Card>
        </div>
      </QueryPanel>
    </>
  );
}
