import { principalSubject } from "../../core/data";
import { createdOrderSchema } from "../../core/data";
import { useState } from "react";
import { Alert, App, Button, Card, Progress, Space, Typography } from "antd";
import { Link, useNavigate } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { dateText, enc, idOf, rows, text, type Row } from "../../core/data";
import { asBigInt, bytes } from "../../core/numbers";
import {
  DataTable,
  PageHeader,
  QueryPanel,
  Status,
  useData,
} from "../../components/common";
import { priceLabel } from "../Plans";
import { SubscriptionImport } from "./SubscriptionImport";
import { failure } from "../../core/api";

export function Subscriptions() {
  const query = useData("subscriptions", "v1/me/subscriptions");
  const links = useData("subscription-links", "v1/me/subscription-links");
  return (
    <>
      <PageHeader
        title="我的订阅"
        description="查看用量、取得订阅链接和管理续费。"
        extra={
          <Link to="/plans">
            <Button type="primary">选购套餐</Button>
          </Link>
        }
      />
      <QueryPanel
        query={query}
        empty={!rows(query.data, "subscriptions").length}
      >
        {rows(query.data, "subscriptions").map((sub) => (
          <Subscription
            key={idOf(sub)}
            sub={sub}
            link={rows(links.data, "links").find(
              (row) => row.subscription_id === sub.id,
            )}
            refresh={() => {
              void query.refetch();
              void links.refetch();
            }}
          />
        ))}
      </QueryPanel>
      {links.isError && (
        <Alert
          type="error"
          title="订阅链接加载失败"
          description={failure(links.error).message}
          action={
            <Button onClick={() => void links.refetch()}>重试链接</Button>
          }
        />
      )}
    </>
  );
}
function Subscription({
  sub,
  link,
  refresh,
}: {
  sub: Row;
  link?: Row;
  refresh: () => void;
}) {
  const { api, principal } = useAuth();
  const open = useDialog();
  const navigate = useNavigate();
  const { message } = App.useApp();
  const [expanded, setExpanded] = useState(false);
  const nodes = useData(
    "subscription-nodes",
    `v1/me/subscriptions/${enc(idOf(sub))}/nodes`,
    expanded,
  );
  const rotate = () => {
    const attempt = new Operation(
      "subscription-rotate",
      principalSubject(principal),
    );
    return open({
      title: "重置订阅链接",
      danger: true,
      description: "旧链接将立即失效。你需要将新链接重新导入每个客户端。",
      submitLabel: "确认重置",
      onSubmit: async () => {
        await attempt.send(
          api,
          `v1/me/subscriptions/${enc(idOf(sub))}/rotate`,
          {},
        );
        refresh();
        message.success("链接重置已确认，请重新导入");
      },
    });
  };
  const renew = async () => {
    try {
      const plans = await api.get("v1/plans");
      const plan = rows(plans, "plans").find((row) => row.id === sub.plan_id);
      const prices = rows(plan, "prices");
      const attempt = new Operation(
        "subscription-renew",
        principalSubject(principal),
      );
      await open({
        title: "续费当前订阅",
        description: (
          <>
            <p>
              {text(sub.plan_name)} · 当前到期{" "}
              {dateText(sub.current_period_end)}
            </p>
            <p>
              从当前订阅续费，避免另外购买一条订阅。到期与额度以服务器应用的权益规则为准。
            </p>
          </>
        ),
        initial: {
          price_id: prices.some((row) => row.id === sub.price_id)
            ? sub.price_id
            : prices[0]?.id,
        },
        fields: [
          ...(prices.length
            ? [
                {
                  name: "price_id",
                  label: "续费周期",
                  type: "select" as const,
                  required: true,
                  options: prices.map((row) => ({
                    label: priceLabel(row),
                    value: idOf(row),
                  })),
                },
              ]
            : []),
          { name: "coupon_code", label: "优惠码（可选）" },
        ],
        submitLabel: "创建续费订单",
        onSubmit: async (values) => {
          const result = await attempt.send(
            api,
            `v1/me/subscriptions/${enc(idOf(sub))}/renew`,
            {
              ...(values.price_id ? { price_id: values.price_id } : {}),
              use_balance: 0,
              coupon_code: text(values.coupon_code, ""),
            },
            {},
            createdOrderSchema,
          );
          navigate(`/orders/${enc(text(result.order_id))}`);
        },
      });
    } catch (value) {
      message.error(failure(value).message);
    }
  };
  return (
    <Card className="subscription-card mb">
      <div className="detail-heading">
        <div>
          <Typography.Title level={3}>{text(sub.plan_name)}</Typography.Title>
          <Space>
            <Status value={sub.status} />
            <Typography.Text type="secondary">
              到期 {dateText(sub.current_period_end)}
            </Typography.Text>
          </Space>
        </div>
        <Button onClick={() => void renew()}>续费当前订阅</Button>
      </div>
      {rows(sub, "quotas").map((quota) => {
        const consumed = asBigInt(quota.consumed);
        const limit = quota.limit == null ? null : asBigInt(quota.limit);
        const percent =
          consumed !== null && limit !== null && limit > 0n
            ? Number((consumed * 100n) / limit)
            : null;
        return (
          <div className="quota-row" key={text(quota.metric)}>
            <div>
              <b>
                {quota.metric === "traffic.bytes"
                  ? "流量使用"
                  : text(quota.metric)}
              </b>
              <span>
                {quota.metric === "traffic.bytes"
                  ? bytes(quota.consumed)
                  : text(quota.consumed)}{" "}
                /{" "}
                {quota.limit == null
                  ? "不限"
                  : limit === null
                    ? "额度格式异常"
                    : quota.metric === "traffic.bytes"
                      ? bytes(quota.limit)
                      : text(quota.limit)}
              </span>
            </div>
            {percent !== null && (
              <Progress
                percent={Math.min(100, percent)}
                showInfo={false}
                status={percent >= 100 ? "exception" : "normal"}
              />
            )}
          </div>
        );
      })}
      {link && (
        <div className="subscription-link">
          <Typography.Text type="secondary">
            订阅链接含访问凭据，请勿公开分享。
          </Typography.Text>
          <Typography.Paragraph copyable className="command-text">
            {text(link.url)}
          </Typography.Paragraph>
          <Space wrap>
            <Button
              onClick={() =>
                void open({
                  title: "导入客户端",
                  width: 620,
                  description: <SubscriptionImport url={String(link.url || "")} />,
                  submitLabel: "知道了",
                })
              }
            >
              一键导入
            </Button>
            <Button danger type="text" onClick={() => void rotate()}>
              重置链接
            </Button>
            <Button onClick={() => setExpanded((value) => !value)}>
              {expanded ? "收起节点" : "查看可用节点"}
            </Button>
          </Space>
        </div>
      )}
      {expanded && (
        <div className="mt">
          <QueryPanel query={nodes}>
            <DataTable
              data={rows(nodes.data, "nodes")}
              columns={[
                { title: "节点", dataIndex: "name" },
                { title: "协议", dataIndex: "protocol" },
                {
                  title: "流量倍率",
                  dataIndex: "traffic_rate",
                  render: (value) => `${text(value)}×`,
                },
              ]}
            />
          </QueryPanel>
        </div>
      )}
    </Card>
  );
}
