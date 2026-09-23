import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Form,
  Input,
  Modal,
  Select,
  Space,
  Typography,
} from "antd";
import { Link } from "react-router-dom";
import { z } from "zod";
import { useAuth } from "../../core/auth";
import { Operation } from "../../core/operations";
import { failure } from "../../core/api";
import {
  enc,
  principalSubject,
  record,
  rows,
  text,
  type Row,
} from "../../core/data";
import { money } from "../../core/numbers";
import { useData } from "../../components/common";
const resultSchema = z
  .object({
    order_id: z.uuid(),
    order_no: z.string().min(1),
    status: z.literal("fulfilled"),
    total_amount: z.literal(0),
    payable_amount: z.literal(0),
  })
  .passthrough();
function period(price: Row) {
  const names: Record<string, string> = {
    day: "天",
    week: "周",
    month: "个月",
    year: "年",
    one_time: "一次性",
  };
  return price.billing_interval === "one_time"
    ? "一次性"
    : `${text(price.interval_count, "1")} ${names[text(price.billing_interval)] || text(price.billing_interval)}`;
}
function Editor({ close }: { close: () => void }) {
  const { api, principal } = useAuth();
  const { message } = App.useApp();
  const [form] = Form.useForm();
  const [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [error, setError] = useState(""),
    [pending, setPending] = useState(false),
    [uncertain, setUncertain] = useState(false);
  const locked = useRef(false);
  const [operation] = useState(
    () => new Operation("manual-order", principalSubject(principal), true),
  );
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 300);
    return () => clearTimeout(timer);
  }, [search]);
  const users = useData(
    "manual-order-users",
    `v1/users?status=active&limit=20&q=${enc(query)}`,
  );
  const plans = useData("manual-order-plans", "v1/plans");
  const planID = Form.useWatch("plan_id", form),
    priceID = Form.useWatch("price_id", form);
  const available = rows(plans.data, "plans").filter(
    (p) => p.status === "active" && p.current_version_id,
  );
  const plan = available.find((p) => p.id === planID);
  const prices = (Array.isArray(plan?.prices) ? plan.prices : [])
    .map(record)
    .filter((p) => p.status === "active");
  const price = prices.find((p) => p.id === priceID);
  const submit = async () => {
    if (locked.current) return;
    locked.current = true;
    setPending(true);
    setError("");
    try {
      const values = await form.validateFields();
      if (
        operation.state === "editing" &&
        Operation.restore(
          principalSubject(principal),
          "manual-order",
          true,
        ).some((op) => op.id !== operation.id)
      )
        throw new Error("已有赠送订单待核实，请关闭本窗口核对原订单");
      const result = await operation.send(
        api,
        "v1/orders/manual",
        {
          user_id: values.user_id,
          plan_id: values.plan_id,
          price_id: values.price_id,
          reason: String(values.reason).trim(),
        },
        {},
        resultSchema,
      );
      message.success(`订单 ${text(result.order_no)} 已开通，实收为 0`);
      close();
    } catch (e) {
      setUncertain(operation.state === "unknown");
      if (!(e && typeof e === "object" && "errorFields" in e))
        setError(failure(e).message);
    } finally {
      locked.current = false;
      setPending(false);
    }
  };
  return (
    <Modal
      open
      title="人工开单 · 赠送订阅"
      width={640}
      style={{ top: 24 }}
      styles={{
        body: { maxHeight: "calc(100dvh - 180px)", overflowY: "auto" },
      }}
      onCancel={() => {
        if (!locked.current) close();
      }}
      mask={{ closable: !pending }}
      footer={
        <Space>
          <Button disabled={pending} onClick={close}>
            {uncertain ? "稍后核实" : "取消"}
          </Button>
          <Button
            type="primary"
            loading={pending}
            disabled={Boolean(users.error || plans.error)}
            onClick={() => void submit()}
          >
            {uncertain ? "重试原订单" : "赠送并立即开通"}
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        title="立即赠送完整订阅，实收为 0"
        description="按所选价格的周期开通，原价全额减免，不计入收入。线下已收到款项请在对应待支付订单中处理。隐藏或停止新购的有效套餐也可以赠送。"
        className="mb"
      />
      {error && <Alert type="error" showIcon title={error} className="mb" />}
      {(users.error || plans.error) && (
        <Alert
          type="error"
          title="无法读取用户或套餐"
          description={failure(users.error || plans.error).message}
          action={
            <Button
              onClick={() => {
                void users.refetch();
                void plans.refetch();
              }}
            >
              重新加载
            </Button>
          }
        />
      )}
      <Form form={form} layout="vertical" disabled={pending || uncertain}>
        <Form.Item
          name="user_id"
          label="接收用户"
          rules={[{ required: true, message: "请选择已注册的有效用户" }]}
          extra="输入邮箱或名称查找，最多显示20条匹配结果。"
        >
          <Select
            showSearch={{ filterOption: false, onSearch: setSearch }}
            loading={users.isLoading}
            placeholder="搜索邮箱或名称"
            options={rows(users.data, "users")
              .filter((u) => u.status === "active")
              .map((u) => ({
                value: u.id,
                label: `${text(u.email)}${u.display_name ? ` · ${text(u.display_name)}` : ""}`,
              }))}
          />
        </Form.Item>
        <Form.Item
          name="plan_id"
          label="赠送套餐"
          rules={[{ required: true, message: "请选择已发布的套餐" }]}
        >
          <Select
            showSearch={{ optionFilterProp: "label" }}
            loading={plans.isLoading}
            placeholder="选择套餐"
            onChange={() => form.setFieldValue("price_id", undefined)}
            options={available.map((p) => ({
              value: p.id,
              label: `${text(p.name)}${p.visibility === "hidden" ? " · 隐藏" : ""}`,
            }))}
          />
        </Form.Item>
        <Form.Item
          name="price_id"
          label="订阅周期与原价"
          rules={[{ required: true, message: "请选择订阅周期" }]}
        >
          <Select
            disabled={!planID || pending || uncertain}
            placeholder="先选套餐，再选择周期"
            options={prices.map((p) => ({
              value: p.id,
              label: `${period(p)} · ${money(p.unit_amount, p.currency)}`,
            }))}
          />
        </Form.Item>
        {planID && prices.length === 0 && (
          <Alert
            type="warning"
            title="该套餐没有有效价格，请先在套餐管理中配置。"
            className="mb"
          />
        )}
        {price && (
          <p className="secondary">
            本次赠送：{text(plan?.name)} · {period(price)}；减免{" "}
            {money(price.unit_amount, price.currency)}，实收{" "}
            {money(0, price.currency)}。
          </p>
        )}
        <Form.Item
          name="reason"
          label="赠送原因"
          rules={[
            {
              validator: (_, v) => {
                const n = Array.from(String(v || "").trim()).length;
                return n >= 5 && n <= 500
                  ? Promise.resolve()
                  : Promise.reject(new Error("请写清赠送原因，5 到 500 个字"));
              },
            },
          ]}
          extra="记录到订单审计中，例如补偿工单编号或活动依据。"
        >
          <Input.TextArea rows={3} maxLength={500} showCount />
        </Form.Item>
      </Form>
      {!plans.isLoading && !plans.error && available.length === 0 && (
        <Alert
          type="warning"
          title="暂无可赠送的已发布套餐"
          description={
            <Link to="/plans">前往套餐管理，发布版本并配置有效价格。</Link>
          }
        />
      )}
    </Modal>
  );
}
export function ManualOrderAction() {
  const { api, can, principal } = useAuth();
  const { message } = App.useApp();
  const subject = principalSubject(principal);
  const [opened, setOpened] = useState(false),
    [recoveries, setRecoveries] = useState<Operation[]>([]),
    [recoveryError, setRecoveryError] = useState(""),
    [busy, setBusy] = useState(""),
    [error, setError] = useState("");
  const lock = useRef(false);
  const refreshRecoveries = useCallback(() => {
    try {
      const found = Operation.restore(subject, "manual-order", true);
      setRecoveries(found);
      setRecoveryError("");
      return found.length === 0;
    } catch (e) {
      setRecoveries([]);
      setRecoveryError(failure(e).message);
      return false;
    }
  }, [subject]);
  useEffect(() => {
    refreshRecoveries();
    setOpened(false);
    setError("");
  }, [subject, refreshRecoveries]);
  const close = () => {
    setOpened(false);
    refreshRecoveries();
  };
  const writable = can("billing.order.write"),
    readable = can("iam.user.read") && can("catalog.read");
  const retry = async (op: Operation) => {
    if (lock.current) return;
    lock.current = true;
    setBusy(op.id);
    setError("");
    try {
      const result = await op.replay(api, resultSchema);
      message.success(`订单 ${text(result.order_no)} 已核实开通`);
      refreshRecoveries();
    } catch (e) {
      setError(failure(e).message);
      refreshRecoveries();
    } finally {
      lock.current = false;
      setBusy("");
    }
  };
  if (!writable) return null;
  return (
    <>
      <Button
        type="primary"
        disabled={!readable || recoveries.length > 0 || Boolean(recoveryError)}
        onClick={() => {
          if (refreshRecoveries()) setOpened(true);
        }}
      >
        人工开单
      </Button>
      {!readable && (
        <Typography.Text type="secondary">
          需要用户和套餐读取权限
        </Typography.Text>
      )}
      {recoveries.map((op) => {
        const payload = op.recoveryPayload() || {};
        return (
          <Alert
            key={op.id}
            type="warning"
            showIcon
            title="有一笔赠送订单结果待核实"
            description={
              <>
                <p>
                  {text(payload.reason)} · 操作号 {op.id}
                </p>
                <Space wrap>
                  <Link to={`/users/${enc(payload.user_id)}`}>查看用户</Link>
                  <Link to={`/plans/${enc(payload.plan_id)}`}>查看套餐</Link>
                  <Button
                    loading={busy === op.id}
                    disabled={Boolean(busy) && busy !== op.id}
                    onClick={() => void retry(op)}
                  >
                    核实并重试原订单
                  </Button>
                </Space>
              </>
            }
          />
        );
      })}
      {recoveryError && <Alert type="error" title={recoveryError} />}
      {error && <Alert type="error" title={error} />}{" "}
      {opened && <Editor key={subject} close={close} />}
    </>
  );
}
