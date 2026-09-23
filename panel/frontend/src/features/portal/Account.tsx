import { principalSubject } from "../../core/data";
import { useRef, useState } from "react";
import { Alert, App, Button, Card, Input, Space, Typography } from "antd";
import { Link, useParams } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import {
  dateText,
  enc,
  idOf,
  record,
  rows,
  text,
  type Row,
} from "../../core/data";
import { legacyInteger, money, parseMinor } from "../../core/numbers";
import { failure } from "../../core/api";
import {
  DataTable,
  Details,
  PageHeader,
  QueryPanel,
  Status,
  useData,
} from "../../components/common";

export function Account() {
  const { api, principal, logout } = useAuth();
  const sessions = useData("sessions", "v1/me/sessions");
  const balance = useData("balance", "v1/me/balance");
  const open = useDialog();
  const password = () =>
    open({
      title: "修改密码",
      description: "修改后所有会话会失效，请使用新密码登录。",
      fields: [
        {
          name: "old_password",
          label: "当前密码",
          type: "password",
          required: true,
        },
        {
          name: "new_password",
          label: "新密码",
          type: "password",
          required: true,
        },
      ],
      onSubmit: async (values) => {
        if (text(values.new_password, "").length < 8)
          throw new Error("新密码至少8位");
        await api.write("v1/me/password", values);
        await logout();
      },
    });
  const revoke = (id: string) =>
    open({
      title: "撤销此登录会话",
      description: "该设备需要重新登录才能继续访问账户。",
      danger: true,
      onSubmit: async () => {
        await api.write(`v1/me/sessions/${enc(id)}`, {}, { method: "DELETE" });
        await sessions.refetch();
      },
    });
  return (
    <>
      <PageHeader
        title="账号安全"
        description="查看登录设备，管理账户安全。"
        extra={<Button onClick={() => void password()}>修改密码</Button>}
      />
      <Card title="账户资料" className="mb">
        <Details
          data={principal || {}}
          fields={[
            ["email", "登录邮箱"],
            ["status", "账户状态", (value) => <Status value={value} />],
            ["created_at", "注册时间", dateText],
          ]}
        />
      </Card>
      <Card title="当前登录会话" className="mb">
        <QueryPanel query={sessions}>
          <DataTable
            data={rows(sessions.data, "sessions")}
            columns={[
              { title: "设备 / 浏览器", dataIndex: "user_agent" },
              { title: "地区", dataIndex: "country" },
              {
                title: "最近访问",
                dataIndex: "last_seen_at",
                render: dateText,
              },
              {
                title: "操作",
                render: (_, row) => (
                  <Button
                    type="link"
                    danger
                    onClick={() => void revoke(idOf(row))}
                  >
                    撤销会话
                  </Button>
                ),
              },
            ]}
          />
        </QueryPanel>
      </Card>
      <Card title="余额明细">
        <QueryPanel query={balance}>
          <Typography.Title level={3}>
            {money(balance.data?.balance, balance.data?.currency)}
          </Typography.Title>
          <DataTable
            data={rows(balance.data, "history")}
            columns={[
              { title: "类型", dataIndex: "kind" },
              {
                title: "金额",
                render: (_, row) => money(row.amount, row.currency),
              },
              { title: "说明", dataIndex: "memo" },
              { title: "时间", dataIndex: "created_at", render: dateText },
            ]}
          />
        </QueryPanel>
      </Card>
    </>
  );
}
export function Gifts() {
  const { api, principal } = useAuth();
  const [code, setCode] = useState("");
  const [card, setCard] = useState<Row | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const open = useDialog();
  const { message } = App.useApp();
  const history = useData("gift-cards", "v1/me/gift-cards");
  const attempt = useRef(
    new Operation("gift-redeem", principalSubject(principal)),
  );
  const preview = async () => {
    setBusy(true);
    setError("");
    try {
      const result = await api.write("v1/gift-cards/preview", {
        code: code.trim(),
      });
      setCard(record(result.card));
    } catch (value) {
      setCard(null);
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  const redeem = () => {
    const captured = code.trim();
    return open({
      title: "确认兑换礼品卡",
      description: (
        <Details
          data={card || {}}
          fields={[
            ["name", "礼品卡"],
            ["type", "权益类型"],
            ["description", "说明"],
          ]}
        />
      ),
      submitLabel: "兑换",
      onSubmit: async () => {
        await attempt.current.send(api, "v1/gift-cards/redeem", {
          code: captured,
        });
        attempt.current = new Operation(
          "gift-redeem",
          principalSubject(principal),
        );
        setCode("");
        setCard(null);
        await history.refetch();
        message.success("礼品卡兑换已确认");
      },
    });
  };
  return (
    <>
      <PageHeader title="兑换礼品卡" description="先核对权益，再确认兑换。" />
      <Card className="mb">
        <Space.Compact block>
          <Input
            aria-label="礼品卡兑换码"
            size="large"
            value={code}
            disabled={busy}
            onChange={(event) => {
              setCode(event.target.value);
              setCard(null);
            }}
            placeholder="输入兑换码"
          />
          <Button
            size="large"
            type="primary"
            loading={busy}
            disabled={!code.trim()}
            onClick={() => void preview()}
          >
            查看权益
          </Button>
        </Space.Compact>
        {error && <Alert className="mt" type="error" title={error} showIcon />}
        {card && (
          <div className="mt">
            <Details
              data={card}
              fields={[
                ["name", "礼品卡"],
                ["type", "类型"],
                ["description", "说明"],
              ]}
            />
            <Button type="primary" onClick={() => void redeem()}>
              确认兑换
            </Button>
          </div>
        )}
      </Card>
      <Card title="我的兑换记录">
        <QueryPanel query={history}>
          <DataTable
            data={rows(history.data, "redemptions")}
            columns={[
              { title: "礼品卡", dataIndex: "template_name" },
              { title: "权益", dataIndex: "type" },
              { title: "兑换时间", dataIndex: "redeemed_at", render: dateText },
            ]}
          />
        </QueryPanel>
      </Card>
    </>
  );
}
export function Help() {
  const query = useData("content", "v1/content/pages");
  return (
    <>
      <PageHeader
        title="帮助中心"
        description="使用教程、常见问题与服务说明。"
      />
      <QueryPanel query={query} empty={!rows(query.data, "pages").length}>
        <div className="plan-grid">
          {rows(query.data, "pages").map((row) => (
            <Card key={text(row.slug)}>
              <Typography.Text type="secondary">
                {text(row.category, "帮助文档")}
              </Typography.Text>
              <Typography.Title level={4}>
                <Link to={`/help/${enc(row.slug)}`}>{text(row.title)}</Link>
              </Typography.Title>
              <Typography.Paragraph>
                {text(row.summary, "")}
              </Typography.Paragraph>
            </Card>
          ))}
        </div>
      </QueryPanel>
    </>
  );
}
export function HelpArticle() {
  const { slug = "" } = useParams();
  const query = useData("content", `v1/content/pages/${enc(slug)}`);
  const page = record(query.data?.page);
  return (
    <>
      <PageHeader
        title={text(page.title, "帮助文档")}
        extra={<Link to="/help">返回帮助中心</Link>}
      />
      <QueryPanel query={query}>
        <Card>
          <Typography.Paragraph className="article-body">
            {text(page.body, "")}
          </Typography.Paragraph>
        </Card>
      </QueryPanel>
    </>
  );
}
export function Referrals() {
  const invite = useData("invite", "v1/me/invite");
  const commission = useData("commission", "v1/me/commission");
  const { api, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const summary = record(commission.data?.summary);
  const transfer = () => {
    const attempt = new Operation(
      "commission-transfer",
      principalSubject(principal),
    );
    return open({
      title: "佣金转入账户余额",
      description: "转入后可用于站内购买和续费，不能再申请佣金提现。",
      fields: [{ name: "amount", label: "转入金额（元）", required: true }],
      onSubmit: async (values) => {
        await attempt.send(api, "v1/me/commission/transfer", {
          amount: legacyInteger(
            parseMinor(text(values.amount, ""), { zero: false }),
          ),
        });
        await commission.refetch();
        message.success("转入余额已确认");
      },
    });
  };
  const withdraw = () => {
    const attempt = new Operation("withdraw", principalSubject(principal));
    return open({
      title: "申请佣金提现",
      description: "可提现额度和最低提现金额以服务器审核为准。",
      fields: [
        { name: "amount", label: "提现金额（元）", required: true },
        {
          name: "payout_detail",
          label: "收款方式与账号",
          required: true,
          type: "textarea",
        },
      ],
      onSubmit: async (values) => {
        await attempt.send(api, "v1/me/withdrawals", {
          amount: legacyInteger(
            parseMinor(text(values.amount, ""), { zero: false }),
          ),
          payout_detail: values.payout_detail,
        });
        await commission.refetch();
        message.success("提现申请已提交");
      },
    });
  };
  return (
    <>
      <PageHeader
        title="邀请返利"
        description="分享邀请码，查看返利和提现进度。"
        extra={
          <Space>
            <Button onClick={() => void transfer()}>转入余额</Button>
            <Button onClick={() => void withdraw()}>申请提现</Button>
          </Space>
        }
      />
      <Card title="你的邀请信息" className="mb">
        <QueryPanel query={invite}>
          <Typography.Paragraph copyable>
            {text(record(invite.data?.invite).code)}
          </Typography.Paragraph>
          <Typography.Text type="secondary">
            已邀请 {text(record(invite.data?.invite).invited)} 人
          </Typography.Text>
        </QueryPanel>
      </Card>
      <Card title="佣金概况" className="mb">
        <QueryPanel query={commission}>
          <Details
            data={summary}
            fields={[
              ["available", "可提现", (value) => money(value, "CNY")],
              ["pending", "冻结中", (value) => money(value, "CNY")],
              ["settled", "已提现", (value) => money(value, "CNY")],
            ]}
          />
        </QueryPanel>
      </Card>
      <Card title="提现记录">
        <QueryPanel query={commission}>
          <DataTable
            data={rows(commission.data, "withdrawals")}
            columns={[
              {
                title: "金额",
                render: (_, row) => money(row.amount, row.currency),
              },
              {
                title: "状态",
                dataIndex: "status",
                render: (value) => <Status value={value} />,
              },
              {
                title: "申请时间",
                dataIndex: "requested_at",
                render: dateText,
              },
              { title: "原因", dataIndex: "reject_reason" },
            ]}
          />
        </QueryPanel>
      </Card>
    </>
  );
}
