import { useState, useRef, useEffect } from "react";
import {
  Alert,
  Button,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Tabs,
} from "antd";
import { failure } from "./api";
import type { Row } from "./data";
import type { Entry } from "./dialogs";
export default function FormDialog({
  entry,
  active,
  settle,
}: {
  entry: Entry;
  active: boolean;
  settle: (id: number, value: boolean) => void;
}) {
  const [form] = Form.useForm<Row>();
  const [pending, setPending] = useState(false);
  const locked = useRef(false);
  const [error, setError] = useState("");
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [activeGroup, setActiveGroup] = useState<string>();
  const revealField = (name: string) => {
    const field = entry.fields?.find(field => field.name === name);
    if (field && entry.fields?.some(item => item.group)) setActiveGroup(field.group || "其他参数");
  };
  const cancel = () => {
    if (entry.protectDraft && dirty && !pending) setConfirmDiscard(true);
    else settle(entry.id, false);
  };
  useEffect(() => {
    if (!entry.protectDraft || !dirty || pending) return;
    const guard = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [entry.protectDraft, dirty, pending]);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const submit = async () => {
    if (locked.current) return;
    locked.current = true;
    let values: Row;
    try {
      values = await form.validateFields();
    } catch (validation) {
      const fields = (validation as { errorFields?: { name: (string | number)[] }[] }).errorFields;
      if (fields?.[0]) revealField(fields[0].name.join("."));
      locked.current = false;
      return;
    }
    if (!mounted.current) {
      locked.current = false;
      return;
    }
    setPending(true);
    setError("");
    try {
      await entry.onSubmit?.(values);
      settle(entry.id, true);
    } catch (value) {
      if (!mounted.current) return;
      const error = failure(value);
      setError(error.message);
      const firstField = Object.keys(error.fields)[0];
      if (firstField) revealField(firstField);
      form.setFields(
        Object.entries(error.fields).map(([name, errors]) => ({
          name: entry.fields?.some(field => field.name === name) ? [name] : name.split("."),
          errors,
        })),
      );
    } finally {
      locked.current = false;
      if (mounted.current) setPending(false);
    }
  };
  return (
    <Modal
      className="compact-form-modal"
      style={{top:24}}
      styles={{body:{maxHeight:"calc(100dvh - 190px)",overflowY:"auto",paddingRight:4}}}
      open
      title={entry.title}
      width={entry.width || 540}
      destroyOnHidden
      keyboard={active}
      mask={{ closable: active && !pending }}
      closable
      onCancel={cancel}
      footer={
        <Space>
          <Button onClick={cancel}>
            {pending ? "关闭等待" : "取消"}
          </Button>
          <Button
            type="primary"
            danger={entry.danger}
            loading={pending}
            onClick={() => void submit()}
          >
            {entry.submitLabel || "保存"}
          </Button>
        </Space>
      }
    >
      {pending && (
        <Alert
          className="mb"
          type="info"
          title="操作已提交。关闭窗口只结束等待，不能撤销服务器操作。"
        />
      )}
      {entry.description && (
        <div className="dialog-description">{entry.description}</div>
      )}
      {error && <Alert type="error" showIcon title={error} className="mb" />}
      {confirmDiscard && <Alert type="warning" className="mb" title="有尚未保存的修改" description="关闭会丢弃本次输入，服务器上的配置不会改变。" action={<Space wrap><Button onClick={() => setConfirmDiscard(false)}>继续编辑</Button><Button danger onClick={() => settle(entry.id, false)}>放弃修改并关闭</Button></Space>} />}
      <Form
        form={form}
        layout="vertical"
        initialValues={entry.initial}
        onValuesChange={(_, values) => {
          setDirty(Boolean(entry.fields?.some(field => JSON.stringify(values[field.name]) !== JSON.stringify(entry.initial?.[field.name]))));
          setConfirmDiscard(false);
          entry.onValuesChange?.(values);
        }}
        onSubmitCapture={(event) => {
          event.preventDefault();
          event.stopPropagation();
          void submit();
        }}
        disabled={pending}
        preserve={false}
      >
        {(() => {
          const renderField = (field: NonNullable<Entry["fields"]>[number]) => (
          <Form.Item
            key={field.name}
            name={field.name}
            label={field.label}
            extra={field.help}
            valuePropName={field.type === "switch" ? "checked" : "value"}
            rules={
              field.required
                ? [{ required: true, message: `请填写${field.label}` }]
                : []
            }
          >
            {field.type === "password" ? (
              <Input.Password
                autoComplete="current-password"
                disabled={field.disabled}
              />
            ) : field.type === "textarea" ? (
              <Input.TextArea
                rows={4}
                placeholder={field.placeholder}
                disabled={field.disabled}
              />
            ) : field.type === "number" ? (
              <InputNumber
                min={field.min}
                max={field.max}
                style={{ width: "100%" }}
                disabled={field.disabled}
              />
            ) : field.type === "select" ? (
              <Select
                options={field.options}
                mode={field.multiple ? "multiple" : undefined}
                showSearch
                optionFilterProp="label"
                allowClear={!field.required}
                disabled={field.disabled}
              />
            ) : field.type === "switch" ? (
              <Switch disabled={field.disabled} />
            ) : (field.type === "date" || field.type === "datetime") ? (
              <DatePicker showTime={field.type === "datetime"} style={{ width: "100%" }} disabled={field.disabled} />
            ) : (
              <Input
                placeholder={field.placeholder}
                disabled={field.disabled}
              />
            )}
          </Form.Item>
          );
          if (!entry.fields?.some(field => field.group)) return entry.fields?.map(renderField);
          const groups = [...new Set(entry.fields.map(field => field.group || "其他参数"))];
          return <Tabs activeKey={activeGroup || groups[0]} onChange={setActiveGroup} items={groups.map(group => ({ key: group, label: group, forceRender: true, children: entry.fields?.filter(field => (field.group || "其他参数") === group).map(renderField) }))} />;
        })()}
      </Form>
    </Modal>
  );
}
