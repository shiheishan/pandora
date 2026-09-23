import { useState } from "react";
import { App, Button } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog, type FormField } from "../../core/dialogs";
import { failure } from "../../core/api";
import { enc, record, rows, text, type Row } from "../../core/data";
import { protocolField } from "../../core/protocol";
import { protocolLabels, rangeKeys } from "../../core/protocolInputs";

const labels: Record<string, string> = { ...protocolLabels, network: "传输协议", tls: "TLS 模式", cipher: "加密方式", cert_path: "证书路径", key_path: "私钥路径", "network_settings.path": "传输路径", "network_settings.headers.Host": "Host 请求头", "network_settings.serviceName": "gRPC 服务名", "reality_settings.dest": "REALITY 目标", "reality_settings.server_name": "REALITY SNI", "reality_settings.private_key": "REALITY 私钥", "reality_settings.public_key": "REALITY 公钥", "reality_settings.short_id": "REALITY Short ID", "bandwidth.up": "上行带宽（Mbps）", "bandwidth.down": "下行带宽（Mbps）" };
const readField = (config: Row, key: string): unknown => key in config ? config[key] : key.split(".").reduce<unknown>((value, part) => record(value)[part], config);
const jsonField = (schema: Row, key: string) => ["object", "array"].includes(protocolField(schema, key).kind) || rangeKeys.includes(key) || ["headers", "padding_scheme"].includes(key);
export function protocolChanges(schema: Row, initial: Row, values: Row, original: Row): Row {
  const patch: Row = {};
  for (const key of Object.keys(initial)) {
    if (values[key] === initial[key]) continue;
    const value = values[key];
    // Empty secret fields mean preserve; secrets are intentionally absent in GET.
    if (protocolField(schema, key).sensitive && (value == null || value === "")) continue;
    let parsed: unknown = value == null || value === "" ? null : value;
    if (parsed !== null && jsonField(schema, key)) {
      parsed = JSON.parse(String(value));
      if (!parsed || typeof parsed !== "object") throw new Error(`${labels[key] || key} 必须填写 JSON 对象或数组`);
    }
    const parts = key in original ? [key] : key.split(".");
    let target = patch;
    for (const part of parts.slice(0, -1)) { target[part] ??= {}; target = target[part] as Row; }
    target[parts.at(-1)!] = parsed;
  }
  return patch;
}

export function protocolChoices(schema: Row, key: string) {
  const { kind, enums } = protocolField(schema, key);
  return enums.map(value => ({ value: kind === "number" ? Number(value) : value,
    label: key === "tls" && kind === "number" ? ({ 0: "关闭 TLS", 1: "普通 TLS", 2: "REALITY" } as Record<string, string>)[value] || value : value }));
}

export function ProtocolEditButton({ id }: { id: string }) {
  const { api, can } = useAuth();
  const open = useDialog();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  if (!can("node.write") || !id) return null;
  const edit = async () => {
    setBusy(true);
    try {
      const node = await api.get(`v1/nodes/${enc(id)}`);
      if (!node.id || node.runtime_role === "probe" || !Number.isSafeInteger(node.row_version) || Number(node.row_version) < 1) throw new Error("节点记录不可编辑，请刷新后重试");
      const result = await api.get("v1/node-protocol-schemas");
      const schema = rows(result, "schemas").find(row => row.node_type === node.node_type && row.status === "stable");
      if (!schema) throw new Error("当前协议没有可编辑的稳定字段定义，原配置未修改");
      const keys = Array.isArray(schema.allowed_properties) ? schema.allowed_properties.filter((key): key is string => typeof key === "string") : [];
      const config = record(node.protocol_config);
      const initial: Row = {};
      const fields: FormField[] = keys.map(key => {
        const { kind, sensitive } = protocolField(schema, key);
        const value = readField(config, key);
        initial[key] = sensitive ? "" : value == null ? kind === "boolean" ? false : "" : jsonField(schema, key) ? JSON.stringify(value, null, 2) : value;
        const options = protocolChoices(schema, key);
        return { name: key, group: protocolGroup(key), label: labels[key] || key, type: sensitive ? "password" : jsonField(schema, key) ? "textarea" : options.length ? "select" : kind === "boolean" ? "switch" : kind === "number" ? "number" : "text", options, help: sensitive ? "已配置的密钥不回显；留空保留原值，填写则替换。" : jsonField(schema, key) ? "使用 JSON 编辑结构化参数；留空移除此字段。" : undefined };
      });
      await open({ title: `编辑协议配置 · ${text(node.node_type)}`, width: 720, protectDraft: true, description: "只更新修改过的参数，保留其他旧配置与未回显的密钥。保存时由原后端校验，下发后仍需确认内核应用状态。", fields, initial, submitLabel: "校验并保存", onSubmit: async values => {
        const patch = protocolChanges(schema, initial, values, config);
        if (!Object.keys(patch).length) return;
        await api.write(`v1/nodes/${enc(id)}`, { row_version: node.row_version, protocol_patch: patch }, { method: "PATCH" });
        message.success("协议配置已通过校验并保存");
      } });
    } catch (error) { message.error(failure(error).message); }
    finally { setBusy(false); }
  };
  return <Button loading={busy} onClick={() => void edit()}>协议配置</Button>;
}

export function protocolGroup(key: string): string {
  if (/^(tls$|security$|utls$|flow$|cert_path$|key_path$|certificate$|private_key$|sni$|alpn$|reality_settings\.)/.test(key)) return "安全与证书";
  if (/^(mtu$|tti$|uplink_capacity$|downlink_capacity$|congestion$|read_buffer_size$|write_buffer_size$|mask)/.test(key)) return "mKCP 与掩码";
  if (/^(network_settings\.|ws_path$|grpc_path$|headers$|sc_|session_|seq_|uplink_http|uplink_data|uplink_chunk|server_max_header)/.test(key)) return "传输参数";
  return "基础参数";
}