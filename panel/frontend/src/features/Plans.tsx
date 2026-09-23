import { principalSubject } from "../core/data";
import { createdOrderSchema, createdResourceSchema } from "../core/data";
import { useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Col,
  Form,
  Input,
  InputNumber,
  Radio,
  Row as GridRow,
  Select,
  Space,
  Tabs,
  Typography,
} from "antd";
import { Link, useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../core/auth";
import { Operation } from "../core/operations";
import { useDialog } from "../core/dialogs";
import { useObjectDraft } from "../core/formDraft";

import {
  dateText,
  enc,
  idOf,
  integer,
  record,
  rows,
  text,
  type Row,
} from "../core/data";
import {
  asBigInt,
  bytes,
  legacyInteger,
  money,
  parseMinor,
} from "../core/numbers";
import { failure } from "../core/api";
import {
  DataTable,
  Details,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../components/common";

const period: Record<string, string> = {
  day: "天",
  week: "周",
  month: "月",
  year: "年",
  lifetime: "永久",
};
export const priceLabel = (price: Row) =>
  `${money(price.unit_amount, price.currency)} / ${integer(price.interval_count, 1)}${period[text(price.billing_interval)] || text(price.billing_interval)}`;
export function PlansPage() {
  const { can } = useAuth();
  return (
    <ResourcePage
      title="套餐"
      description="从版本、价格和线路三个方面核对可售状态。"
      resource="plans"
      path="v1/plans"
      listKey="plans"
      extra={
        can("catalog.write") && (
          <Link to="/plans/new">
            <Button type="primary">新建套餐</Button>
          </Link>
        )
      }
      columns={[
        {
          title: "套餐",
          render: (_, row) => (
            <div>
              <EntityLink row={row} path="/plans" />
              <div className="secondary small">{text(row.code)}</div>
            </div>
          ),
        },
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        { title: "可见范围", dataIndex: "visibility" },
        {
          title: "新购",
          dataIndex: "allow_new_purchase",
          render: (value) => (value ? "允许" : "关闭"),
        },
        {
          title: "续费",
          dataIndex: "allow_renewal",
          render: (value) => (value ? "允许" : "关闭"),
        },
        { title: "排序", dataIndex: "sort_order" },
      ]}
    />
  );
}
export function PlanCreate() {
  const { api, principal, can } = useAuth();
  const pools = useData("pools", "v1/node-pools", can("node.read"));
  const navigate = useNavigate();
  const [form] = Form.useForm<Row>();
  const draft = useObjectDraft(principalSubject(principal), "plan:create");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(
    new Operation("plan-create", principalSubject(principal)),
  );
  const submit = async (values: Row) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      const prices = rows(values, "prices").map((price) => ({
        billing_interval: price.billing_interval,
        interval_count: integer(price.interval_count, 1),
        currency: "CNY",
        unit_amount: legacyInteger(
          parseMinor(text(price.amount, ""), { zero: false }),
        ),
        trial_days: 0,
      }));
      const publish = Boolean(values.publish);
      const poolIDs = Array.isArray(values.pool_ids) ? values.pool_ids : [];
      if (publish && !poolIDs.length)
        throw new Error("发布前至少选择一个节点分组");
      if (publish && !prices.length) throw new Error("发布前至少填写一档价格");
      const result = await attempt.current.send(
        api,
        "v1/plans/complete",
        {
          code: values.code,
          name: values.name,
          description: text(values.description, ""),
          visibility: "public",
          sort_order: integer(values.sort_order),
          allow_new_purchase: true,
          allow_renewal: true,
          allow_upgrade: true,
          visible_group_ids: [],
          traffic_gb: values.traffic_gb ?? null,
          max_devices: values.max_devices ?? null,
          quota_reset_strategy: "billing_cycle",
          pool_ids: poolIDs,
          prices,
          publish,
        },
        {},
        createdResourceSchema("plan"),
      );
      draft.clear();
      navigate(`/plans/${enc(text(record(result.plan).id))}`);
    } catch (value) {
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader
        title="创建一个可用套餐"
        description="填写基本资料、额度、售价和线路，再决定是否发布。"
        extra={<Link to="/plans">返回套餐</Link>}
      />
      {error && <Alert className="mb" type="error" title={error} showIcon />}
      <Form
        form={form}
        layout="vertical"
        onFinish={(values) => void submit(values)}
        initialValues={{
          sort_order: 0,
          prices: [{ billing_interval: "month", interval_count: 1 }],
          publish: false,
          ...draft.value,
        }}
        onValuesChange={(_, values) => draft.setValue(values)}
        disabled={busy}
      >
        <div className="split-detail">
          <div>
            <Card title="基本资料" className="mb">
              <GridRow gutter={16}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    name="name"
                    label="套餐名称"
                    rules={[{ required: true }]}
                  >
                    <Input placeholder="标准套餐" />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    name="code"
                    label="套餐代码"
                    rules={[
                      { required: true },
                      {
                        pattern: /^[a-z0-9][a-z0-9_-]{1,63}$/,
                        message: "2–64位小写字母、数字、下划线或短横线",
                      },
                    ]}
                  >
                    <Input placeholder="standard_100g" />
                  </Form.Item>
                </Col>
              </GridRow>
              <Form.Item name="description" label="套餐说明">
                <Input.TextArea rows={3} />
              </Form.Item>
              <Form.Item name="sort_order" label="排序">
                <InputNumber min={0} precision={0} />
              </Form.Item>
            </Card>
            <Card title="用量与设备" className="mb">
              <GridRow gutter={16}>
                <Col xs={24} sm={12}>
                  <Form.Item
                    name="traffic_gb"
                    label="每周期流量（GB）"
                    extra="留空为不限量；按账单周期重置。"
                  >
                    <InputNumber min={1} max={8388607} precision={0} />
                  </Form.Item>
                </Col>
                <Col xs={24} sm={12}>
                  <Form.Item
                    name="max_devices"
                    label="同时在线设备上限"
                    extra="留空为不限制"
                  >
                    <InputNumber min={1} max={100000} precision={0} />
                  </Form.Item>
                </Col>
              </GridRow>
            </Card>
            <Card title="销售价格" className="mb">
              <Form.List name="prices">
                {(fields, { add, remove }) => (
                  <>
                    {fields.map((field) => (
                      <div className="price-editor-row" key={field.key}>
                        <Form.Item
                          name={[field.name, "amount"]}
                          label="价格（元）"
                          rules={[{ required: true }]}
                        >
                          <Input placeholder="19.90" />
                        </Form.Item>
                        <Form.Item
                          name={[field.name, "interval_count"]}
                          label="周期数"
                          rules={[{ required: true }]}
                        >
                          <InputNumber min={1} max={100} precision={0} />
                        </Form.Item>
                        <Form.Item
                          name={[field.name, "billing_interval"]}
                          label="周期"
                          rules={[{ required: true }]}
                        >
                          <Select
                            options={["month", "year", "day", "week"].map(
                              (value) => ({ value, label: period[value] }),
                            )}
                          />
                        </Form.Item>
                        <Button danger onClick={() => remove(field.name)}>
                          移除
                        </Button>
                      </div>
                    ))}
                    <Button
                      onClick={() =>
                        add({ billing_interval: "month", interval_count: 1 })
                      }
                    >
                      增加价格档
                    </Button>
                  </>
                )}
              </Form.List>
            </Card>
          </div>
          <div>
            <Card title="可用线路" className="mb">
              {can("node.read") ? (
                <QueryPanel query={pools}>
                  <Form.Item
                    name="pool_ids"
                    extra="发布时至少绑定一个可用节点分组。"
                  >
                    <Checkbox.Group
                      className="vertical-options"
                      options={rows(pools.data, "pools").map((row) => ({
                        value: idOf(row),
                        label: `${text(row.name)} · ${integer(row.active_nodes)} 个在役节点`,
                      }))}
                    />
                  </Form.Item>
                </QueryPanel>
              ) : (
                <Alert
                  type="info"
                  title="没有节点读取权限，请保存草稿后由有权限的管理员配置线路。"
                />
              )}
            </Card>
            <Card title="发布">
              <Form.Item name="publish" valuePropName="checked">
                <Checkbox disabled={!can("catalog.publish")}>
                  创建后直接发布
                </Checkbox>
              </Form.Item>
              <Typography.Paragraph type="secondary">
                保存草稿可以继续完善。发布要求完整价格和线路，服务端会再次验证。
              </Typography.Paragraph>
              <Button type="primary" block htmlType="submit" loading={busy}>
                确认创建
              </Button>
            </Card>
          </div>
        </div>
      </Form>
    </>
  );
}
export function PlanDetail() {
  const { id = "" } = useParams();
  const query = useData("plans", `v1/plans/${enc(id)}`);
  const plan = record(query.data?.plan);
  const { api, principal, can } = useAuth();
  const dialog = useDialog();
  const { message } = App.useApp();
  const editEntitlements = async () => {
    try {
      const result = await api.get("v1/node-pools");
      const version =
        rows(plan, "versions").find(
          (row) => row.id === plan.current_version_id,
        ) ||
        rows(plan, "versions")[0] ||
        {};
      const quota = rows(version, "quotas").find(
        (row) => row.metric === "traffic.bytes" && row.period === "cycle",
      );
      const limit = asBigInt(quota?.limit);
      const attempt = new Operation(
        "plan-entitlements",
        principalSubject(principal),
      );
      await dialog({
        title: "修改套餐额度与线路",
        description:
          "额度与线路变更会生成新版本；已购用户保留原版本，后续购买或续费按服务端规则使用新版本。留空额度表示此次不修改，原有精确字节额度及其他周期限制会保留。",
        initial: {
          traffic_gb:
            limit != null && limit % 1073741824n === 0n
              ? Number(limit / 1073741824n)
              : undefined,
          max_devices: version.max_devices,
          pool_ids: version.pool_ids || [],
        },
        fields: [
          {
            name: "traffic_gb",
            label: "每周期流量（GB）",
            type: "number",
            min: 1,
            max: 8388607,
          },
          {
            name: "max_devices",
            label: "设备上限",
            type: "number",
            min: 1,
            max: 100000,
          },
          {
            name: "pool_ids",
            label: "可用节点分组",
            type: "select",
            multiple: true,
            options: rows(result, "pools").map((row) => ({
              value: idOf(row),
              label: text(row.name),
            })),
          },
        ],
        onSubmit: async (values) => {
          const payload: Row = {
            expected_row_version: plan.row_version,
            ...values,
          };
          for (const key of [
            "code",
            "name",
            "description",
            "visibility",
            "sort_order",
            "allow_new_purchase",
            "allow_renewal",
            "allow_upgrade",
            "visible_group_ids",
            "purchase_limit_per_user",
            "stock_total",
          ])
            payload[key] = plan[key];
          payload.pool_ids = values.pool_ids || [];
          const saved = await attempt.send(
            api,
            `v1/plans/${enc(id)}/complete`,
            payload,
            { method: "PUT" },
          );
          await query.refetch();
          const changed = Array.isArray(saved.changed)
            ? saved.changed.filter(
                (item): item is string => typeof item === "string",
              )
            : [];
          void dialog({
            title: "套餐变更已确认",
            description: (
              <div>
                {changed.map((item) => (
                  <p key={item}>{item}</p>
                ))}
              </div>
            ),
            submitLabel: "完成",
          });
        },
      });
    } catch (value) {
      message.error(failure(value).message);
    }
  };
  const command = (title: string, path: string, payload: Row) => {
    const attempt = new Operation("plan-command", principalSubject(principal));
    return dialog({
      title,
      description: "会更新套餐销售状态。现有订阅权益以服务器规则为准。",
      onSubmit: async () => {
        await attempt.send(api, path, payload);
        await query.refetch();
        message.success("更新已确认");
      },
    });
  };
  const edit = () =>
    dialog({
      title: "编辑套餐资料",
      initial: plan,
      fields: [
        { name: "name", label: "名称", required: true },
        { name: "description", label: "说明", type: "textarea" },
        { name: "allow_new_purchase", label: "允许新购", type: "switch" },
        { name: "allow_renewal", label: "允许续费", type: "switch" },
        { name: "allow_upgrade", label: "允许升级", type: "switch" },
      ],
      onSubmit: async (values) => {
        const payload: Row = {};
        for (const key of [
          "code",
          "visibility",
          "visible_group_ids",
          "visible_from",
          "visible_until",
          "purchase_limit_per_user",
          "stock_total",
          "sort_order",
        ])
          payload[key] = plan[key];
        await api.write(
          `v1/plans/${enc(id)}`,
          { ...payload, ...values, expected_row_version: plan.row_version },
          { method: "PUT" },
        );
        await query.refetch();
      },
    });
  const addPrice = () => {
    const attempt = new Operation("plan-price", principalSubject(principal));
    return dialog({
      title: "新增价格档",
      initial: { billing_interval: "month", interval_count: 1 },
      fields: [
        { name: "amount", label: "价格（元）", required: true },
        {
          name: "billing_interval",
          label: "周期",
          type: "select",
          required: true,
          options: Object.entries(period)
            .filter(([value]) => value !== "lifetime")
            .map(([value, label]) => ({ value, label })),
        },
        {
          name: "interval_count",
          label: "周期数",
          type: "number",
          min: 1,
          max: 100,
          required: true,
        },
      ],
      onSubmit: async (values) => {
        await attempt.send(api, `v1/plans/${enc(id)}/prices`, {
          currency: "CNY",
          unit_amount: legacyInteger(
            parseMinor(text(values.amount, ""), { zero: false }),
          ),
          billing_interval: values.billing_interval,
          interval_count: values.interval_count,
          trial_days: 0,
        });
        await query.refetch();
      },
    });
  };
  return (
    <>
      <PageHeader
        title={text(plan.name, "套餐详情")}
        description={text(plan.code, "")}
        extra={
          <Space>
            <Link to="/plans">返回套餐</Link>
            {can("catalog.write") && (
              <Button onClick={() => void edit()}>编辑资料</Button>
            )}
            {can("catalog.write") && can("node.read") && (
              <Button onClick={() => void editEntitlements()}>
                额度与线路
              </Button>
            )}
            {can("catalog.publish") && (
              <Button
                danger
                onClick={() =>
                  void command("归档套餐", `v1/plans/${enc(id)}/archive`, {
                    expected_row_version: plan.row_version,
                  })
                }
              >
                归档
              </Button>
            )}
          </Space>
        }
      />
      <QueryPanel query={query}>
        <Card className="mb">
          <Status value={plan.status} />
          <Typography.Paragraph>
            {text(plan.description, "")}
          </Typography.Paragraph>
          <Details
            data={plan}
            fields={[
              ["visibility", "可见范围"],
              [
                "allow_new_purchase",
                "新购",
                (value) => (value ? "允许" : "关闭"),
              ],
              ["allow_renewal", "续费", (value) => (value ? "允许" : "关闭")],
              ["created_at", "创建时间", dateText],
            ]}
          />
        </Card>
        <Card>
          <Tabs
            items={[
              {
                key: "prices",
                label: "价格",
                children: (
                  <>
                    <div className="table-toolbar">
                      <span>价格归档不会改写历史订单</span>
                      {can("catalog.publish") && (
                        <Button onClick={() => void addPrice()}>
                          新增价格
                        </Button>
                      )}
                    </div>
                    <DataTable
                      data={rows(plan, "prices")}
                      columns={[
                        {
                          title: "周期与价格",
                          render: (_, row) => priceLabel(row),
                        },
                        {
                          title: "状态",
                          dataIndex: "status",
                          render: (value) => <Status value={value} />,
                        },
                        {
                          title: "操作",
                          render: (_, row) =>
                            can("catalog.publish") &&
                            row.status !== "archived" && (
                              <Button
                                type="link"
                                danger
                                onClick={() =>
                                  void command(
                                    "归档价格",
                                    `v1/plans/${enc(id)}/prices/${enc(idOf(row))}/archive`,
                                    { expected_row_version: row.row_version },
                                  )
                                }
                              >
                                归档
                              </Button>
                            ),
                        },
                      ]}
                    />
                  </>
                ),
              },
              {
                key: "versions",
                label: "权益版本",
                children: (
                  <>
                    {rows(plan, "versions").map((version) => (
                      <Card
                        size="small"
                        className="mb"
                        key={idOf(version)}
                        title={
                          <Space>
                            版本 {text(version.version)}
                            <Status value={version.status} />
                          </Space>
                        }
                        extra={
                          can("catalog.publish") &&
                          version.status === "draft" && (
                            <Button
                              onClick={() =>
                                void command(
                                  "发布权益版本",
                                  `v1/plans/${enc(id)}/versions/${enc(idOf(version))}/publish`,
                                  {
                                    expected_plan_row_version: plan.row_version,
                                    expected_version_row_version:
                                      version.row_version,
                                  },
                                )
                              }
                            >
                              发布版本
                            </Button>
                          )
                        }
                      >
                        <Details
                          data={version}
                          fields={[
                            ["max_devices", "设备上限"],
                            ["quota_reset_strategy", "额度重置方式"],
                            ["grace_period_hours", "宽限小时数"],
                          ]}
                        />
                        <DataTable
                          data={rows(version, "quotas")}
                          columns={[
                            { title: "额度", dataIndex: "metric" },
                            {
                              title: "限制",
                              render: (_, row) =>
                                row.limit == null
                                  ? "不限"
                                  : row.metric === "traffic.bytes"
                                    ? bytes(row.limit)
                                    : text(row.limit),
                            },
                            { title: "周期", dataIndex: "period" },
                          ]}
                        />
                      </Card>
                    ))}
                  </>
                ),
              },
            ]}
          />
        </Card>
      </QueryPanel>
    </>
  );
}
export function StorePlans() {
  const query = useData("plans", "v1/plans");
  return (
    <>
      <PageHeader
        title="选择适合你的套餐"
        description="查看包含的流量、设备数量与购买周期。"
      />
      <QueryPanel query={query} empty={!rows(query.data, "plans").length}>
        <div className="plan-grid">
          {rows(query.data, "plans").map((plan) => (
            <Card key={idOf(plan)} className="plan-card">
              <span className="eyebrow">PANDORA CONNECT</span>
              <Typography.Title level={3}>{text(plan.name)}</Typography.Title>
              <Typography.Paragraph type="secondary">
                {text(plan.description, "稳定连接，灵活选择。")}
              </Typography.Paragraph>
              <div className="plan-price">
                {rows(plan, "prices")[0]
                  ? priceLabel(rows(plan, "prices")[0]!)
                  : "当前暂无可购价格"}
              </div>
              <div className="plan-benefits">
                {rows(plan, "quotas").map((quota) => (
                  <div key={text(quota.metric)}>
                    {quota.metric === "traffic.bytes"
                      ? "流量"
                      : text(quota.metric)}{" "}
                    <b>
                      {quota.limit == null
                        ? "不限"
                        : quota.metric === "traffic.bytes"
                          ? bytes(quota.limit)
                          : text(quota.limit)}
                    </b>
                  </div>
                ))}
                {plan.max_devices != null && (
                  <div>
                    在线设备 <b>{text(plan.max_devices)} 台</b>
                  </div>
                )}
              </div>
              <Link to={`/plans/${enc(idOf(plan))}/checkout`}>
                <Button
                  type="primary"
                  block
                  disabled={!rows(plan, "prices").length}
                >
                  选择套餐
                </Button>
              </Link>
            </Card>
          ))}
        </div>
      </QueryPanel>
    </>
  );
}
export function Checkout() {
  const { id = "" } = useParams();
  const query = useData("plans", "v1/plans");
  const wallet = useData("balance", "v1/me/balance");
  const plan = rows(query.data, "plans").find((row) => idOf(row) === id) || {};
  const { api, principal } = useAuth();
  const navigate = useNavigate();
  const [priceID, setPriceID] = useState("");
  const [coupon, setCoupon] = useState("");
  const [balance, setBalance] = useState(false);
  const [preview, setPreview] = useState<Row | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(
    new Operation("purchase", principalSubject(principal)),
  );
  const prices = rows(plan, "prices");
  const selected = prices.find((row) => idOf(row) === priceID) || prices[0];
  const available = asBigInt(wallet.data?.balance);
  const due = asBigInt(preview?.payable_amount ?? selected?.unit_amount);
  const balanceUsable =
    wallet.isSuccess &&
    available !== null &&
    due !== null &&
    available > 0n &&
    selected?.currency === wallet.data.currency;
  const applied =
    balance && balanceUsable ? (available! < due! ? available! : due!) : 0n;
  const submit = async () => {
    if (!selected || busy) return;
    setBusy(true);
    setError("");
    try {
      const result = await attempt.current.send(
        api,
        "v1/orders",
        {
          plan_id: id,
          price_id: idOf(selected),
          coupon_code: coupon.trim(),
          use_balance: legacyInteger(applied.toString()),
        },
        {},
        createdOrderSchema,
      );
      navigate(`/orders/${enc(text(result.order_id))}`);
    } catch (value) {
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  const checkCoupon = async () => {
    if (!selected) return;
    setError("");
    const captured = {
      plan_id: id,
      price_id: idOf(selected),
      coupon_code: coupon.trim(),
    };
    setBusy(true);
    try {
      const result = await api.write("v1/coupons/preview", captured);
      setPreview(result);
    } catch (value) {
      setPreview(null);
      setError(failure(value).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader
        title="确认购买"
        description={text(plan.name, "")}
        extra={<Link to="/plans">返回套餐</Link>}
      />
      <QueryPanel query={query}>
        <div className="split-detail">
          <Card title="选择周期">
            <Radio.Group
              value={selected ? idOf(selected) : undefined}
              onChange={(event) => {
                setPriceID(event.target.value as string);
                setPreview(null);
              }}
              className="vertical-options"
              disabled={busy}
            >
              {prices.map((price) => (
                <Radio value={idOf(price)} key={idOf(price)}>
                  {priceLabel(price)}
                </Radio>
              ))}
            </Radio.Group>
            <Typography.Paragraph className="mt">
              {text(plan.description, "")}
            </Typography.Paragraph>
            <Form layout="vertical">
              <Form.Item label="优惠码">
                <Space.Compact block>
                  <Input
                    value={coupon}
                    disabled={busy}
                    onChange={(event) => {
                      setCoupon(event.target.value);
                      setPreview(null);
                    }}
                  />
                  <Button
                    disabled={!coupon.trim() || busy}
                    onClick={() => void checkCoupon()}
                  >
                    验证
                  </Button>
                </Space.Compact>
              </Form.Item>
              <Checkbox
                checked={balance}
                disabled={!balanceUsable || busy}
                onChange={(event) => setBalance(event.target.checked)}
              >
                使用账户余额{" "}
                {money(wallet.data?.balance, wallet.data?.currency)}
              </Checkbox>
              {wallet.isError && (
                <Alert
                  className="mt"
                  type="warning"
                  title="余额暂未加载，仍可使用外部支付"
                />
              )}
            </Form>
          </Card>
          <Card title="订单预览">
            {error && (
              <Alert className="mb" type="error" title={error} showIcon />
            )}
            <div className="checkout-amount">
              {selected
                ? money(
                    due === null ? undefined : (due - applied).toString(),
                    selected.currency,
                  )
                : "暂无可购价格"}
            </div>
            {applied > 0n && (
              <Typography.Paragraph>
                使用余额 {money(applied, selected?.currency)}
              </Typography.Paragraph>
            )}
            {preview && (
              <Typography.Paragraph>
                优惠 {money(preview.discount_amount, selected?.currency)}
              </Typography.Paragraph>
            )}
            <Typography.Paragraph type="secondary">
              价格以服务器创建的订单为准。确认后可在订单详情中选择支付方式并追踪开通结果。
            </Typography.Paragraph>
            <Button
              type="primary"
              size="large"
              block
              loading={busy}
              disabled={!selected}
              onClick={() => void submit()}
            >
              确认创建订单
            </Button>
          </Card>
        </div>
      </QueryPanel>
    </>
  );
}
