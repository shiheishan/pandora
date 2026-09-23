import { Alert, Button, Card, Space, Typography } from "antd";
import { Link } from "react-router-dom";
import { runtime } from "../core/runtime";
import { useAuth } from "../core/auth";
import { dateText, idOf, integer, record, rows, text } from "../core/data";
import { RevenueAnalytics, TrafficRanking } from "./admin/DashboardAnalytics";
import { money } from "../core/numbers";
import {
  DataTable,
  EntityLink,
  PageHeader,
  QueryPanel,
  Status,
  useData,
} from "../components/common";

export function Overview() {
  return runtime.domain === "admin" ? <AdminOverview /> : <PortalOverview />;
}
function AdminOverview() {
  const query = useData("overview", "v1/overview");
  const data = query.data || {};
  const { can } = useAuth();
  const metrics = [
    {
      title: "注册用户",
      note: "站点累计注册账户",
      value: record(data.users).total,
      path: "/users",
      permission: "iam.user.read",
    },
    {
      title: "活跃订阅",
      note: "当前有效的订阅数量，不等于在线人数",
      value: record(data.subscriptions).active,
      path: "/users?status=active",
      permission: "iam.user.read",
    },
    {
      title: "待支付订单",
      note: "尚未完成支付的订单",
      value: record(data.orders).pending,
      path: "/orders?status=pending_payment",
      permission: "billing.order.read",
    },
    {
      title: "七天内到期",
      note: "未来7天到期的订阅",
      value: record(data.subscriptions).expiring_7_days,
      path: "/users",
      permission: "iam.user.read",
    },
  ];
  return (
    <div className="admin-dashboard">
      <PageHeader
        title="仪表盘"
        description={`运营数据概览 · 最近更新：${query.dataUpdatedAt ? dateText(new Date(query.dataUpdatedAt).toISOString()) : "正在读取"}`}
        extra={
          <Button
            loading={query.isFetching}
            onClick={() => void query.refetch()}
          >
            刷新概况
          </Button>
        }
      />
      <QueryPanel query={query}>
        <div className="dashboard-stats">
          {metrics.map((metric) => (
            <Card key={metric.title}>
              <div className="stat-label">{metric.title}</div>
              <div className="stat-number">{text(metric.value)}</div>
              <p className="metric-note">{metric.note}</p>
              {can(metric.permission) && (
                <Link to={metric.path}>查看详情 →</Link>
              )}
            </Card>
          ))}
        </div>
        {integer(data.ledger_drift_accounts) > 0 && (
          <Alert
            className="mb"
            type="error"
            showIcon
            title={`${integer(data.ledger_drift_accounts)} 个账户存在账本漂移，需要核对`}
          />
        )}
        <div className="dashboard-stats revenue-metrics">
          {["CNY"].flatMap(currency => {
            const revenue = rows(data, "revenue").find(row => row.currency === currency);
            return [{ title: "今日实收", key: "actual_today", note: "按站点当天口径统计" }, { title: "近30天实收", key: "actual_30_days", note: "滚动30天，非自然月收入" }].map(metric => (
              <Card key={`${currency}:${metric.key}`}><div className="stat-label">{metric.title} · {currency}</div>
                <div className="stat-number">{money(revenue?.[metric.key], currency)}</div><p className="metric-note">{metric.note}</p></Card>
            ));
          })}
        </div>
        {can("billing.ledger.read") && <RevenueAnalytics />}
        <div className="dashboard-rankings">
          {can("metering.read") && can("node.read") && <TrafficRanking kind="nodes" />}
          {can("metering.read") && can("iam.user.read") && <TrafficRanking kind="users" />}
        </div>
        <Card title="收入明细 · 各币种独立核对" className="mb">
          <DataTable data={rows(data, "revenue")} columns={[
            { title: "币种", dataIndex: "currency" },
            { title: "今日实收", render: (_, row) => money(row.actual_today, row.currency) },
            { title: "今日报表调整", render: (_, row) => money(row.adjustment_today, row.currency) },
            { title: "近30天实收", render: (_, row) => money(row.actual_30_days, row.currency) },
            { title: "累计实收", render: (_, row) => money(row.actual_total, row.currency) },
          ]} />
        </Card>
        <Card title="常用工作">
          <Space wrap>
            {[
              ["/users", "处理用户", "iam.user.read"],
              ["/tickets", "回复工单", "ops.ticket.read"],
              ["/servers", "接入服务器", "node.read"],
              ["/plans", "管理套餐", "catalog.read"],
            ]
              .filter((item) => can(item[2]))
              .map((item) => (
                <Link key={item[0]} to={item[0]!}>
                  <Button>{item[1]}</Button>
                </Link>
              ))}
          </Space>
        </Card>
      </QueryPanel>
    </div>
  );
}
function PortalOverview() {
  const { principal } = useAuth();
  const subscriptions = useData("subscriptions", "v1/me/subscriptions");
  const balance = useData("balance", "v1/me/balance");
  const orders = useData("orders", "v1/orders");
  const announcements = useData("announcements", "v1/me/announcements");
  return (
    <>
      <section className="welcome-card">
        <div>
          <span className="eyebrow">YOUR CONNECTION, SIMPLIFIED</span>
          <Typography.Title level={2}>欢迎回来</Typography.Title>
          <Typography.Paragraph>{principal?.email}</Typography.Paragraph>
          <Space>
            <Link to="/subscriptions">
              <Button type="primary" size="large">
                查看我的订阅
              </Button>
            </Link>
            <Link to="/plans">
              <Button size="large">选购套餐</Button>
            </Link>
          </Space>
        </div>
        <div className="welcome-symbol" aria-hidden>
          P
        </div>
      </section>
      <div className="dashboard-stats portal-stats">
        <Card title="我的订阅">
          <QueryPanel query={subscriptions}>
            <div className="stat-number">
              {
                rows(subscriptions.data, "subscriptions").filter((row) =>
                  ["active", "trialing"].includes(text(row.status)),
                ).length
              }
            </div>
            <Link to="/subscriptions">查看连接与用量</Link>
          </QueryPanel>
        </Card>
        <Card title="账户余额">
          <QueryPanel query={balance}>
            <div className="stat-number">
              {money(balance.data?.balance, balance.data?.currency)}
            </div>
            <Link to="/account">查看账户</Link>
          </QueryPanel>
        </Card>
      </div>
      <div className="split-detail">
        <Card title="最近订单" extra={<Link to="/orders">全部订单</Link>}>
          <QueryPanel query={orders}>
            <DataTable
              data={rows(orders.data, "orders").slice(0, 5)}
              columns={[
                {
                  title: "订单",
                  render: (_, row) => (
                    <EntityLink row={row} path="/orders" label="order_no" />
                  ),
                },
                {
                  title: "状态",
                  dataIndex: "status",
                  render: (value) => <Status value={value} />,
                },
                {
                  title: "金额",
                  render: (_, row) => money(row.total_amount, row.currency),
                },
              ]}
            />
          </QueryPanel>
        </Card>
        <Card title="最新公告">
          <QueryPanel query={announcements}>
            {rows(announcements.data, "announcements")
              .slice(0, 5)
              .map((row) => (
                <article key={idOf(row)} className="announcement">
                  <Typography.Title level={5}>
                    {text(row.title)}
                  </Typography.Title>
                  <Typography.Paragraph>
                    {text(row.body, "")}
                  </Typography.Paragraph>
                  <Typography.Text type="secondary">
                    {dateText(row.publish_at ?? row.created_at)}
                  </Typography.Text>
                </article>
              ))}
          </QueryPanel>
        </Card>
      </div>
    </>
  );
}
