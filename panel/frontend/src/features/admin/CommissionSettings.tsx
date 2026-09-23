import { useEffect, useRef, useState } from "react";
import { Alert, Button, Card, Form, Input, InputNumber, Space } from "antd";
import { Link } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { failure } from "../../core/api";
import { asBigInt, legacyInteger, parseMinor } from "../../core/numbers";
import { PageHeader, QueryPanel, useData } from "../../components/common";

export function commissionPayload(values: { rate_percent: number; freeze_days: number; min_withdraw: string }) {
  if (!Number.isInteger(values.rate_percent) || values.rate_percent < 0 || values.rate_percent > 50) throw new Error("佣金比例须为 0–50 的整数");
  if (!Number.isInteger(values.freeze_days) || values.freeze_days < 0 || values.freeze_days > 90) throw new Error("冻结天数须为 0–90 的整数");
  return { ...values, min_withdraw: legacyInteger(parseMinor(values.min_withdraw)) };
}
export function CommissionSettings() {
  const query = useData("commission-settings", "v1/commission/overview");
  const { api, can } = useAuth();
  const [form] = Form.useForm();
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const locked = useRef(false);
  const [editRevision, setEditRevision] = useState(0);
  const [status, setStatus] = useState<{ error: boolean; text: string }>();
  useEffect(() => {
    if (!query.data || dirty) return;
    const minor = asBigInt(query.data.min_withdraw);
    if (minor === null) return;
    form.setFieldsValue({ rate_percent: query.data.rate_percent, freeze_days: query.data.freeze_days, min_withdraw: `${minor / 100n}.${(minor % 100n).toString().padStart(2, "0")}` });
  }, [query.data, dirty, form]);
  const save = async () => {
    if (locked.current) return;
    locked.current = true;
    try {
      const payload = commissionPayload(await form.validateFields());
      setSaving(true); setStatus(undefined);
      await api.write("v1/commission/config", payload);
      const result = await query.refetch();
      if (result.error) throw new Error("设置已提交，但刷新失败。请刷新核对，当前输入已保留。");
      if (!result.data || Object.entries(payload).some(([key, value]) => String(result.data?.[key]) !== String(value))) throw new Error("服务器当前设置与本次提交不一致，输入已保留，请核对后再保存。");
      setDirty(false); setStatus({ error: false, text: "佣金设置已保存" });
    } catch (error) {
      if (!(error && typeof error === "object" && "errorFields" in error)) setStatus({ error: true, text: failure(error).message });
    } finally { locked.current = false; setSaving(false); }
  };
  useEffect(() => {
    if (!dirty) return;
    const timer = window.setTimeout(() => void save(), 1000);
    return () => window.clearTimeout(timer);
    // Changes schedule one validated write. A failure requires another edit or explicit retry.
  }, [editRevision, dirty]);
  return <>
    <PageHeader title="邀请与佣金设置" description="设置邀请返佣比例、冻结期限和提现门槛。" extra={<Link to="/commissions">查看佣金与提现</Link>} />
    <QueryPanel query={query}><Card>
      <Form form={form} layout="vertical" style={{ maxWidth: 600 }} disabled={!can("billing.provider.write") || saving} onValuesChange={() => { setDirty(true); setEditRevision(value => value + 1); setStatus(undefined); }} onFinish={() => void save()}>
        <Form.Item name="rate_percent" label="邀请佣金比例" extra="按订单佣金基数计算，0 表示不产生返佣。" rules={[{ required: true, message: "请填写佣金比例" }]}><InputNumber min={0} max={50} precision={0} addonAfter="%" style={{ width: "100%" }} /></Form.Item>
        <Form.Item name="freeze_days" label="佣金冻结期限" extra="佣金入账后，经过冻结期才能转为可提现金额。" rules={[{ required: true, message: "请填写冻结天数" }]}><InputNumber min={0} max={90} precision={0} addonAfter="天" style={{ width: "100%" }} /></Form.Item>
        <Form.Item name="min_withdraw" label="最低提现金额" extra="默认计费币种 CNY，金额最多保留两位小数。" rules={[{ required: true, message: "请填写最低提现金额" }]}><Input addonBefore="CNY" inputMode="decimal" /></Form.Item>
        {status && <Alert className="mb" showIcon type={status.error ? "error" : "success"} title={status.text} />}
        {can("billing.provider.write") && <Space><Button type="primary" htmlType="submit" loading={saving} disabled={!dirty}>保存设置</Button><span>{saving ? "正在保存…" : dirty ? "修改后自动保存，失败时可点击重试" : "修改后自动保存"}</span></Space>}
      </Form>
    </Card></QueryPanel>
  </>;
}
