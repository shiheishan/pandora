/**
 * [INPUT]: 依赖 core/auth 的 useAuth、core/runtime 的 runtime 与 hasContract、core/operations 的 Operation、core/dialogs、core/data、core/numbers；依赖 admin/Refunds 的 RefundWorkspace、admin/ManualOrder 的 ManualOrderAction、components/common 的列表与详情组件
 * [OUTPUT]: 对外提供 OrdersPage、OrderDetail、RefundReviewQueue 组件与 reconciliationExplanation
 * [POS]: features 的订单共用页，admin 与 portal 同一份源码按 runtime.domain 分支；退款与主动查单入口受待接契约 refunds-be4 / reconcile-be4 门控，默认不渲染
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { principalSubject } from "../core/data";
import { useState } from "react";
import { Alert, App, Button, Card, Space, Tabs, Typography } from "antd";
import { Link, useParams } from "react-router-dom";
import { useAuth } from "../core/auth";
import { hasContract, runtime } from "../core/runtime";
import { Operation } from "../core/operations";
import { useDialog } from "../core/dialogs";
import { enc, record, rows, text, dateText, type Row } from "../core/data";
import { money } from "../core/numbers";
import { RefundWorkspace } from "./admin/Refunds";
import {
  DataTable,
  BackLink,
  Details,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../components/common";

import { ManualOrderAction } from "./admin/ManualOrder";

const admin = runtime.domain === "admin";
export function reconciliationExplanation(value: unknown): string {
  const code = text(value, "");
  const descriptions: Record<string, string> = {
    provider_query_failed: "渠道暂不可查询，将按计划重试",
    payment_reconciliation_failed: "渠道结果尚未完成入账核对",
    provider_reports_refund: "渠道报告退款，请人工核对",
    provider_reports_failed: "渠道报告支付失败，请人工核对",
    payment_not_confirmed: "渠道尚未确认付款",
    query_window_exhausted: "自动查询期限已结束，请人工核对",
    payment_requires_review: "款项未应用于订单，请核对待处理款项",
  };
  return descriptions[code] || (code ? `待核对（${code}）` : "—");
}
export function OrdersPage() {
  return (
    <ResourcePage
      title="订单"
      description="每笔购买、支付与开通的状态都可以在详情中核对。"
      resource="orders"
      extra={admin ? <ManualOrderAction /> : undefined}
      path="v1/orders"
      listKey="orders"
      serverPagination={admin}
      searchable={admin}
      statuses={[
        { value: "pending_payment", label: "待支付" },
        { value: "paid", label: "已支付" },
        { value: "fulfilled", label: "已开通" },
        { value: "cancelled", label: "已取消" },
        { value: "expired", label: "已过期" },
        { value: "refunded", label: "已退款" },
      ]}
      columns={[
        {
          title: "订单",
          render: (_, row) => (
            <div>
              <EntityLink row={row} path="/orders" label="order_no" />
              <div className="secondary small">
                {text(row.plan_name, text(row.kind))}
              </div>
            </div>
          ),
        },
        ...(admin ? [{ title: "用户", dataIndex: "user_email" }] : []),
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        {
          title: "订单金额",
          render: (_, row) => money(row.total_amount, row.currency),
        },
        {
          title: "已支付",
          render: (_, row) => money(row.paid_amount, row.currency),
        },
        { title: "创建时间", dataIndex: "created_at", render: dateText },
      ]}
    />
  );
}
export function OrderDetail() {
  const { id = "" } = useParams();
  const query = useData("orders", `v1/orders/${enc(id)}`);
  const order = record(query.data?.order);
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const payments = useData(
    "payments",
    `v1/orders/${enc(id)}/payments`,
    admin && can("billing.payment.read"),
  );
  const [paymentUrl, setPaymentUrl] = useState("");
  const reconciliation = useData(
    "reconciliation",
    `v1/orders/${enc(id)}/reconciliation-jobs`,
    admin && can("billing.payment.read") && hasContract("reconcile-be4"),
    "jobs",
  );
  const reconcile = () =>
    open({
      title: "向支付渠道查询此订单",
      description:
        "只提交查单任务，不会手工标记为已付款。需要等待渠道结果与订单状态更新。",
      submitLabel: "提交查单",
      onSubmit: async () => {
        await api.write(`v1/orders/${enc(id)}/reconcile`, {});
        await reconciliation.refetch();
        message.info("查单任务已提交，尚未确认到账");
      },
    });
  const cancel = () => {
    const operation = new Operation(
      "order-cancel",
      principalSubject(principal),
    );
    return open({
      title: "取消订单",
      description: `将取消订单 ${text(order.order_no)}。支付中的订单请先核对支付结果。`,
      danger: true,
      fields: admin
        ? [{ name: "reason", label: "取消原因", required: true }]
        : [],
      onSubmit: async (values) => {
        await operation.send(
          api,
          `v1/orders/${enc(id)}/cancel`,
          admin ? { reason: text(values.reason, "") } : {},
        );
        await query.refetch();
        message.success("订单取消已确认");
      },
    });
  };
  const pay = () => {
    const operation = new Operation("order-pay", principalSubject(principal));
    return open({
      title: "继续支付",
      description: `本订单外部应付 ${money(order.payable_amount, order.currency)}。支付完成后以订单开通状态为准。`,
      initial: { method: "alipay" },
      fields: [
        {
          name: "method",
          label: "支付方式",
          type: "select",
          required: true,
          options: [
            { value: "alipay", label: "支付宝" },
            { value: "wxpay", label: "微信支付" },
          ],
        },
      ],
      submitLabel: "取得支付链接",
      onSubmit: async (values) => {
        const result = await operation.send(api, `v1/orders/${enc(id)}/pay`, {
          provider: "epay",
          method: values.method,
        });
        const url = new URL(text(result.redirect_url, ""));
        if (!["https:", "http:"].includes(url.protocol))
          throw new Error("支付链接格式异常，请联系管理员核实");
        setPaymentUrl(url.href);
      },
    });
  };
  return (
    <>
      <PageHeader
        title={text(order.order_no, "订单详情")}
        description="订单总额、实收和权益开通分别核对"
        extra={
          <Space>
            <BackLink to="/orders">返回订单列表</BackLink>
            {admin && can("billing.payment.read") && hasContract("reconcile-be4") && (
              <Button onClick={() => void reconcile()}>主动查单</Button>
            )}
            {order.status === "pending_payment" && (
              <>
                {!admin && (
                  <Button type="primary" onClick={() => void pay()}>
                    继续支付
                  </Button>
                )}
                {(!admin || can("billing.order.write")) && (
                  <Button danger onClick={() => void cancel()}>
                    取消订单
                  </Button>
                )}
              </>
            )}
            <Button
              loading={query.isFetching}
              onClick={() => void query.refetch()}
            >
              核对最新状态
            </Button>
          </Space>
        }
      />
      {paymentUrl && (
        <Alert
          className="mb"
          type="info"
          showIcon
          title="支付链接已生成"
          description={
            <a href={paymentUrl} target="_blank" rel="noopener noreferrer">
              打开支付页面
            </a>
          }
        />
      )}
      <QueryPanel query={query}>
        <Card className="mb">
          <div className="detail-heading">
            <Status value={order.status} />
            {order.user_id != null && admin && (
              <Link to={`/users/${enc(text(order.user_id))}`}>
                {text(order.user_email, "查看用户")}
              </Link>
            )}
          </div>
          <div className="metric-grid">
            <div>
              <small>订单总额</small>
              <strong>{money(order.total_amount, order.currency)}</strong>
            </div>
            <div>
              <small>外部应付</small>
              <strong>{money(order.payable_amount, order.currency)}</strong>
            </div>
            <div>
              <small>已支付</small>
              <strong>{money(order.paid_amount, order.currency)}</strong>
            </div>
            <div>
              <small>已退款</small>
              <strong>{money(order.refunded_amount, order.currency)}</strong>
            </div>
          </div>
          <Details
            data={order}
            fields={[
              ["created_at", "创建时间", dateText],
              ["paid_at", "支付时间", dateText],
              ["fulfilled_at", "开通时间", dateText],
              ["expires_at", "支付截止", dateText],
              [
                "balance_applied",
                "余额抵扣",
                (value) => money(value, order.currency),
              ],
              [
                "discount_amount",
                "优惠",
                (value) => money(value, order.currency),
              ],
            ]}
          />
        </Card>
        <Card>
          <Tabs
            items={[
              {
                key: "items",
                label: "商品快照",
                children: (
                  <DataTable
                    data={rows(order, "items")}
                    columns={[
                      { title: "商品", dataIndex: "product_name" },
                      { title: "套餐", dataIndex: "plan_name" },
                      { title: "数量", dataIndex: "quantity" },
                      {
                        title: "小计",
                        render: (_, row) =>
                          money(row.line_amount, row.currency),
                      },
                    ]}
                  />
                ),
              },
              ...(admin && can("billing.ledger.read") && hasContract("refunds-be4")
                ? [
                    {
                      key: "refund-review",
                      label: "退款待核对",
                      children: <RefundReviewQueue />,
                    },
                  ]
                : []),
              ...(admin &&
              hasContract("refunds-be4") &&
              (can("billing.ledger.read") ||
                can("billing.refund.request") ||
                can("billing.refund.approve"))
                ? [
                    {
                      key: "refunds",
                      label: "退款申请与审批",
                      children: <RefundWorkspace orderId={id} />,
                    },
                  ]
                : []),
              ...(admin && can("billing.payment.read") && hasContract("reconcile-be4")
                ? [
                    {
                      key: "reconciliation",
                      label: "主动查单",
                      children: (
                        <QueryPanel query={reconciliation}>
                          <Alert
                            className="mb"
                            type="info"
                            title="任务已排队或查询完成，不等同于订单已开通"
                            description="请同时核对订单支付状态、履约时间及需要人工处理的错误。"
                          />
                          <DataTable
                            data={rows(reconciliation.data, "jobs")}
                            columns={[
                              { title: "渠道", dataIndex: "provider_code" },
                              {
                                title: "状态",
                                dataIndex: "status",
                                render: (value: unknown) => (
                                  <Status value={value} />
                                ),
                              },
                              { title: "尝试次数", dataIndex: "attempts" },
                              {
                                title: "下次查询",
                                dataIndex: "next_check_at",
                                render: dateText,
                              },
                              {
                                title: "最后查询",
                                dataIndex: "last_checked_at",
                                render: dateText,
                              },
                              {
                                title: "处理说明",
                                dataIndex: "last_error_code",
                                render: reconciliationExplanation,
                              },
                            ]}
                          />
                        </QueryPanel>
                      ),
                    },
                    {
                      key: "payments",
                      label: "支付与退款",
                      children: (
                        <QueryPanel query={payments}>
                          <Typography.Title level={5}>实收</Typography.Title>
                          <DataTable
                            data={rows(payments.data, "payments")}
                            columns={[
                              { title: "渠道", dataIndex: "provider_name" },
                              {
                                title: "状态",
                                dataIndex: "status",
                                render: (value: unknown) => (
                                  <Status value={value} />
                                ),
                              },
                              {
                                title: "金额",
                                render: (_: unknown, row: Row) =>
                                  money(row.amount, row.currency),
                              },
                              {
                                title: "时间",
                                dataIndex: "paid_at",
                                render: dateText,
                              },
                            ]}
                          />
                          <Typography.Title level={5}>退款</Typography.Title>
                          <DataTable
                            data={rows(payments.data, "refunds")}
                            columns={[
                              {
                                title: "状态",
                                dataIndex: "status",
                                render: (value: unknown) => (
                                  <Status value={value} />
                                ),
                              },
                              { title: "原因", dataIndex: "reason" },
                              {
                                title: "金额",
                                render: (_: unknown, row: Row) =>
                                  money(row.amount, row.currency),
                              },
                              {
                                title: "失败说明",
                                dataIndex: "failure_message",
                              },
                            ]}
                          />
                        </QueryPanel>
                      ),
                    },
                  ]
                : []),
            ]}
          />
        </Card>
      </QueryPanel>
    </>
  );
}
export function RefundReviewQueue() {
  const query = useData(
    "refund-review",
    "v1/refund-review-cases",
    true,
    "items",
  );
  const reasons: Record<string, string> = {
    unproven_refund_semantics: "渠道退款金额或事实尚未得到证实",
    unmatched_refund: "退款事实已接收，尚未完成关联与冲账",
    refund_fact_conflict: "相同渠道退款标识出现不同事实，需要人工核对",
  };
  return (
    <QueryPanel query={query}>
      <Alert
        className="mb"
        showIcon
        type="warning"
        title="全租户待核对队列（最多200条），当前接口未提供订单关联"
        description="以下记录仅证明已收到待核对退款通知，不代表此订单已退款、账务已冲正或订阅已撤销。空列表不能证明此订单没有外部退款。"
      />
      <DataTable
        data={rows(query.data, "items")}
        columns={[
          { title: "渠道", dataIndex: "provider_code" },
          {
            title: "渠道退款号",
            dataIndex: "provider_refund_id",
            render: (value) => text(value),
          },
          {
            title: "已证实退款金额",
            render: (_, row) =>
              row.amount_minor == null
                ? "金额尚未证实"
                : money(row.amount_minor, row.currency),
          },
          {
            title: "状态",
            dataIndex: "status",
            render: (value) => <Status value={value} />,
          },
          {
            title: "核对原因",
            dataIndex: "reason",
            render: (value) =>
              reasons[text(value)] || `待核对（${text(value)}）`,
          },
          { title: "通知收据", dataIndex: "receipt_id" },
          { title: "接收时间", dataIndex: "created_at", render: dateText },
        ]}
      />
    </QueryPanel>
  );
}
