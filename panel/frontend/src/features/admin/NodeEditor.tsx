/**
 * [INPUT]: 依赖 core/auth、core/data、core/api 的 failure、core/protocol 与 core/protocolInputs 的协议表单、core/operations 的 Operation、core/runtime 的 hasContract、components/common 的 useData、components/UnsavedChangesGuard；依赖 admin/ProtocolEditor 与 admin/NodeActions
 * [OUTPUT]: 对外提供 NodeEditDialog、NodeEditButton 组件与 editorProtocolValues / editorProtocolPatch
 * [POS]: features/admin 的节点创建与编辑对话框：按后端稳定 Schema 组装 protocol_patch，管理父节点与服务器归属；路由组多选只在待接契约 route-groups-v1 打开时渲染与提交
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { useEffect, useRef, useState } from "react";
import {
  Alert,
  App,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Tag,
} from "antd";
import { useAuth } from "../../core/auth";
import { enc, record, rows, text, type Row } from "../../core/data";
import { failure } from "../../core/api";
import { protocolField } from "../../core/protocol";
import { protocolChoices, protocolChanges } from "./ProtocolEditor";
import { protocolLabels, rangeKeys } from "../../core/protocolInputs";
import { UnsavedChangesGuard } from "../../components/UnsavedChangesGuard";
import { NodeActions } from "./NodeActions";
import { Operation } from "../../core/operations";
import { createdIdSchema, createdResourceSchema, principalSubject } from "../../core/data";
import { z } from "zod";
import { useData } from "../../components/common";
import { hasContract } from "../../core/runtime";

function rateScheduleValues(value: unknown): Row {
 const v=record(value);
 return {enabled:v.enabled===true,ranges:Array.isArray(v.ranges)?v.ranges:[]};
}
const names: Record<string, string> = {
  anytls: "AnyTLS",
  shadowsocks: "Shadowsocks",
  vmess: "VMess",
  trojan: "Trojan",
  hysteria2: "Hysteria",
  vless: "VLess",
  tuic: "TUIC",
  socks: "SOCKS",
  naive: "Naive",
  http: "HTTP",
  mieru: "Mieru",
  juicity: "Juicity",
  shadowtls: "ShadowTLS",
};
const colors: Record<string, string> = {
  anytls: "#8056c5",
  shadowsocks: "#409954",
  vmess: "#c42f82",
  trojan: "#edb541",
  hysteria2: "#528bea",
  vless: "#202020",
  tuic: "#10bd65",
  socks: "#269fee",
  naive: "#a028b5",
  http: "#fc6037",
  mieru: "#48aa55",
};
const labels: Record<string, string> = {
  ...protocolLabels,
  network: "传输协议",
  tls: "TLS 模式",
  utls: "TLS 指纹",
  cert_path: "证书路径",
  key_path: "证书私钥路径",
  padding_scheme: "填充方案",
  cipher: "加密方式",
  security: "安全模式",
  password: "密码",
  zero_rtt: "零往返握手",
  heartbeat: "心跳间隔",
  auth_timeout: "认证超时",
  "network_settings.path": "传输路径",
  "network_settings.headers.Host": "Host 请求头",
  "network_settings.serviceName": "gRPC 服务名",
  "reality_settings.dest": "REALITY 目标",
  "reality_settings.server_name": "REALITY SNI",
  "reality_settings.private_key": "REALITY 私钥",
  "reality_settings.public_key": "REALITY 公钥",
  "reality_settings.short_id": "REALITY Short ID",
  "obfs.type": "混淆方式",
  "obfs.password": "混淆密码",
  "bandwidth.up": "上行带宽（Mbps）",
  "bandwidth.down": "下行带宽（Mbps）",
};
const structured = (schema: Row, key: string) =>
  ["object", "array"].includes(protocolField(schema, key).kind) ||
  rangeKeys.includes(key) ||
  key === "headers";
function valueAt(config: Row, key: string): unknown {
  return key in config
    ? config[key]
    : key.split(".").reduce<unknown>((v, k) => record(v)[k], config);
}
export function editorProtocolValues(schema: Row, config: Row): Row {
  return Object.fromEntries(
    ((schema.allowed_properties as string[]) || []).map((key) => {
      const value = valueAt(config, key);
      return [
        key,
        protocolField(schema, key).sensitive
          ? ""
          : key === "padding_scheme"
            ? Array.isArray(value)
              ? value.join("\n")
              : text(value, "")
            : structured(schema, key) && value != null
              ? JSON.stringify(value, null, 2)
              : (value ??
                (protocolField(schema, key).kind === "boolean" ? false : "")),
      ];
    }),
  );
}
export function editorProtocolPatch(
  schema: Row,
  initial: Row,
  values: Row,
  config: Row,
): Row {
  const before = { ...initial },
    after = { ...values };
  if ("padding_scheme" in after) {
    const toJSON = (v: unknown) =>
      String(v ?? "").trim()
        ? JSON.stringify(
            String(v)
              .split(/\r?\n/)
              .map((s) => s.trim())
              .filter(Boolean),
          )
        : "";
    before.padding_scheme = toJSON(before.padding_scheme);
    after.padding_scheme = toJSON(after.padding_scheme);
  }
  return protocolChanges(schema, before, after, config);
}
export function NodeEditButton({
  id,
  label = "编辑",
}: {
  id: string;
  label?: string;
}) {
  const { api, can } = useAuth();
  const { message } = App.useApp();
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState<{
    node: Row;
    schemas: Row[];
    pools: Row[];
  } | null>(null);
  if (!can("node.write") || !id) return null;
  const open = async () => {
    setBusy(true);
    try {
      const [nodeResult, schemaResult, poolResult, serverResult] = await Promise.all([
        api.get(`v1/nodes/${enc(id)}`),
        api.get("v1/node-protocol-schemas"),
        api.get("v1/node-pools"),
        api.get("v1/servers"),
      ]);
      const node = record(nodeResult.node ?? nodeResult);
      const server = rows(serverResult, "servers").find(server => server.id === node.server_id);
      if (server) node.server_name = server.name;
      if (
        node.id !== id ||
        node.runtime_role === "probe" ||
        !Number.isSafeInteger(node.row_version) ||
        Number(node.row_version) < 1
      )
        throw new Error("节点记录不可编辑，请刷新后重试");
      const schemas = rows(schemaResult, "schemas").filter(
        (s) => s.status === "stable",
      );
      if (!schemas.some((s) => s.node_type === node.node_type))
        schemas.unshift({
          node_type: node.node_type,
          status: "legacy-read-compatible",
          allowed_properties: [],
        });
      setLoaded({ node, schemas, pools: rows(poolResult, "pools") });
    } catch (e) {
      message.error(failure(e).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <Button aria-label={label} aria-busy={busy} disabled={busy} loading={busy} onClick={() => void open()}>
        {label}
      </Button>
      {loaded && (
        <NodeEditDialog
          {...loaded}
          routeProtection
          close={() => setLoaded(null)}
        />
      )}
    </>
  );
}
export function NodeEditDialog({
  node,
  schemas,
  pools,
  close,
  routeProtection = false,
  create = false,
  servers = [],
  onCreated,
}: {
  node: Row;
  schemas: Row[];
  pools: Row[];
  close: () => void;
  routeProtection?: boolean;
  create?: boolean;
  servers?: Row[];
  onCreated?: (node: Row) => void;
}) {
  const { api, principal, can } = useAuth();
  const parentNodes = useData("node-parent-options", "v1/nodes?runtime_role=business", true, "nodes");
  const routeGroups = useData("route-groups", "v1/route-groups", hasContract("route-groups-v1"), "route_groups");
  const routeWritable = typeof can === "function" && can("node.config.publish");
  const { message } = App.useApp();
  const [form] = Form.useForm();
 const dynamicRate=Form.useWatch(["rate_schedule","enabled"],form);
 const parentID=Form.useWatch("parent_node_id",form);
  const [kind, setKind] = useState(text(node.node_type));
  const [advanced, setAdvanced] = useState(false),
    [dirty, setDirty] = useState(false),
    [discard, setDiscard] = useState(false),
    [pending, setPending] = useState(false),
    [error, setError] = useState("");
  const lock = useRef(false);
  const attempt = useRef(new Operation("node-create", principalSubject(principal)));
  const [created, setCreated] = useState<Row>();
  const [closing, setClosing] = useState(false);
  useEffect(() => {
    if (created) onCreated?.(created);
    else if (closing) close();
  }, [created, closing, onCreated, close]);
  const finishClose = () => { setDirty(false); setClosing(true); };
  const protocolDrafts = useRef<Record<string, Row>>({});
  const schema = schemas.find((s) => s.node_type === kind) || { allowed_properties: [] };
  const original = record(node.protocol_config);
  const firstProtocol = useRef(editorProtocolValues(schema, original));
  const closeDraft = () => {
    if (pending) return;
    if (dirty) setDiscard(true);
    else finishClose();
  };
  useEffect(() => {
    const guard = (e: BeforeUnloadEvent) => {
      if (dirty) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  }, [dirty]);
  const switchProtocol = (next: string) => {
    protocolDrafts.current[kind] = form.getFieldValue("protocol") || {};
    const nextSchema = schemas.find((s) => s.node_type === next)!;
    form.setFieldValue(
      "protocol",
      protocolDrafts.current[next] ||
        editorProtocolValues(
          nextSchema,
          next === node.node_type ? original : {},
        ),
    );
    form.setFieldValue("parent_node_id",null);
    setKind(next);
    setDirty(true);
    setError("");
  };
  const keys = (schema.allowed_properties as string[]) || [];
  const advancedKey = (key: string) =>
    ["cert_path", "key_path"].includes(key) ||
    key.startsWith("reality_settings.") ||
    /^(sc_|session_|seq_|uplink_|server_max_|mtu$|tti$|downlink_capacity$|congestion$|read_buffer_size$|write_buffer_size$|mask)/.test(key);
  const renderProtocol = (key: string) => {
    const info = protocolField(schema, key),
      options = protocolChoices(schema, key);
    const valueType = info.sensitive
      ? "secret"
      : key === "padding_scheme" || structured(schema, key)
        ? "textarea"
        : options.length
          ? "select"
          : info.kind;
    return (
      <Form.Item
        key={key}
        name={["protocol", key]}
        label={labels[key] || key}
        valuePropName={valueType === "boolean" ? "checked" : "value"}
        rules={
          info.required && (create || kind !== node.node_type)
            ? [{ required: true, message: `请填写${labels[key] || key}` }]
            : []
        }
        extra={
          info.sensitive
            ? !create && kind === node.node_type
              ? "留空保留已配置的密钥。"
              : "请填写新协议所需的密钥。"
            : key === "padding_scheme"
              ? "每行一条填充规则。"
              : structured(schema, key)
                ? "结构化参数，使用 JSON 配置。"
                : undefined
        }
      >
        {valueType === "secret" ? (
          <Input.Password autoComplete="new-password" />
        ) : valueType === "textarea" ? (
          <Input.TextArea autoSize={{ minRows: 4, maxRows: 8 }} />
        ) : valueType === "select" ? (
          <Select options={options} allowClear placeholder="使用默认配置" />
        ) : valueType === "boolean" ? (
          <Switch />
        ) : valueType === "number" ? (
          <InputNumber style={{ width: "100%" }} />
        ) : (
          <Input />
        )}
      </Form.Item>
    );
  };
  const submit = async () => {
    if (lock.current) return;
    lock.current = true;
    try {
      const v = await form.validateFields();
      if (create) {
        if (schema.status !== "stable") throw new Error("请选择节点协议");
        const config = editorProtocolPatch(schema, Object.fromEntries(keys.map(key => [key, protocolField(schema, key).kind === "boolean" ? undefined : ""])), record(v.protocol), {});
        setPending(true);
        setError("");
        const result = await attempt.current.send(api, "v1/nodes", {
          parent_node_id:v.parent_node_id || null,
          name: v.name, transfer_limit_bytes:Math.round(Number(v.transfer_limit_gb || 0)*1073741824), custom_code:v.custom_code?.trim() || null, tags:v.tags || [], route_group_ids:routeWritable ? v.route_group_ids || [] : undefined, display_name: v.display_name || "", server_host: v.server_host,
          server_port: v.server_port, client_port: v.client_port ?? null, traffic_rate: v.traffic_rate, pool_id: v.pool_id || "",
          server_id: v.server_id, sort_order: v.sort_order || 0, rate_schedule:rateScheduleValues(v.rate_schedule),
          node_type: kind, kernel: "pandora-native", protocol_config: config,
        }, {}, z.union([createdIdSchema, createdResourceSchema("node")]));
        setDirty(false);
        setCreated(record(result.node ?? result));
        message.success("节点已创建");
        return;
      }
      const payload: Row = {};
      for (const key of [
        "name",
        "display_name",
        "server_host",
        "server_port",
        "traffic_rate",
        "pool_id",
      ])
        if ((v[key] ?? "") !== (node[key] ?? "")) payload[key] = v[key] ?? "";
      if((v.parent_node_id || null)!==(node.parent_node_id || null)) payload.parent_node_id=v.parent_node_id || null;
      if(Math.round(Number(v.transfer_limit_gb || 0)*1073741824)!==Number(node.transfer_limit_bytes || 0)) payload.transfer_limit_bytes=Math.round(Number(v.transfer_limit_gb || 0)*1073741824);
      if((v.custom_code?.trim() || null)!==(node.custom_code || null)) payload.custom_code=v.custom_code?.trim() || null;
      if(v.parent_node_id) delete payload.traffic_rate;
      if(JSON.stringify(v.tags || [])!==JSON.stringify(node.tags || [])) payload.tags=v.tags || [];
      if(routeWritable && JSON.stringify(v.route_group_ids || [])!==JSON.stringify(node.route_group_ids || [])) payload.route_group_ids=v.route_group_ids || [];
      const values = record(v.protocol);
      const schedule=rateScheduleValues(v.rate_schedule);
      if(!v.parent_node_id && JSON.stringify(schedule)!==JSON.stringify(rateScheduleValues(node.rate_schedule))) payload.rate_schedule=schedule;
      if ((v.client_port ?? null) !== (node.client_port ?? null)) payload.client_port = v.client_port ?? null;
      if (kind === node.node_type) {
        const delta = editorProtocolPatch(
          schema,
          firstProtocol.current,
          values,
          original,
        );
        if (Object.keys(delta).length) payload.protocol_patch = delta;
      } else {
        payload.node_type = kind;
        payload.protocol_config = editorProtocolPatch(
          schema,
          Object.fromEntries(
            keys.map((k) => [
              k,
              protocolField(schema, k).kind === "boolean" ? undefined : "",
            ]),
          ),
          values,
          {},
        );
      }
      if (!Object.keys(payload).length) {
        close();
        return;
      }
      setPending(true);
      setError("");
      await api.write(
        `v1/nodes/${enc(text(node.id))}`,
        { ...payload, row_version: node.row_version },
        { method: "PATCH" },
      );
      message.success("节点配置已保存");
      close();
    } catch (e) {
      if ((e as { errorFields?: unknown }).errorFields) {
        setAdvanced(true);
        return;
      }
      const problem = failure(e);
      setError(problem.message);
      form.setFields(
        Object.entries(problem.fields).map(([name, errors]) => ({
          name: name.startsWith("protocol_config.")
            ? ["protocol", name.slice(16)]
            : [name],
          errors,
        })),
      );
      setAdvanced(true);
    } finally {
      setPending(false);
      lock.current = false;
    }
  };
  const currentPool = text(node.pool_id, "");
  const poolOptions = pools.map((p) => ({
    value: text(p.id),
    label: text(p.name),
  }));
  if (currentPool && !poolOptions.some((p) => p.value === currentPool))
    poolOptions.push({ value: currentPool, label: "当前分组" });
  return (
    <>
      {routeProtection && (
        <UnsavedChangesGuard dirty={dirty && !closing && !created} saving={pending} />
      )}
      <Modal
        open
        className="xboard-node-editor"
        width={840}
        style={{ top: 24 }}
        destroyOnHidden
        mask={{ closable: false }}
        closable={!pending}
        onCancel={closeDraft}
        title={
          <div className="xnode-heading">
            <div>
              <div className="xnode-title">
                {create ? "新建节点" : "编辑节点"}{" "}
                <Tag color={colors[kind] || "#8056c5"}>
                  {names[kind] || kind}
                </Tag>
              </div>
              <p>管理所有节点，包括添加、删除、编辑等操作。</p>
            </div>
            <Select
              aria-label="节点协议"
              value={kind || undefined}
              placeholder="选择协议类型"
              onChange={switchProtocol}
              disabled={pending}
              options={schemas.map((s) => ({
                value: text(s.node_type),
                label: (
                  <span className="xnode-protocol">
                    <i
                      style={{
                        background: colors[text(s.node_type)] || "#8056c5",
                      }}
                    />
                    {names[text(s.node_type)] || text(s.node_type)}
                  </span>
                ),
              }))}
            />
          </div>
        }
        footer={
          <div className="xnode-footer">
            <Button
              onClick={() => setAdvanced(!advanced)}
              aria-expanded={advanced}
            >
              高级设置 · {names[kind] || kind}
            </Button>
            <Button onClick={closeDraft} disabled={pending}>
              取消
            </Button>
            <Button
              type="primary"
              loading={pending}
              onClick={() => void submit()}
            >
              提交
            </Button>
          </div>
        }
      >
        {error && <Alert type="error" showIcon title={error} />}
        {!create && schema.status !== "stable" && (
          <Alert
            type="info"
            title="旧协议配置将原样保留，可修改节点名称、地址、端口和倍率。"
          />
        )}
        {discard && (
          <Alert
            type="warning"
            title="有尚未保存的修改"
            action={
              <Space>
                <Button onClick={() => setDiscard(false)}>继续编辑</Button>
                <Button danger onClick={finishClose}>
                  放弃修改并关闭
                </Button>
              </Space>
            }
          />
        )}
        {!create && kind !== node.node_type && (
          <Alert
            type="warning"
            showIcon
            title="提交后会切换协议并替换该协议配置，请填写新协议必需参数。原节点信息保留。"
          />
        )}
        <Form
          form={form}
          layout="vertical"
          disabled={pending}
          initialValues={{
            parent_node_id:node.parent_node_id ?? null,
            name: node.name,
            display_name: node.display_name ?? "",
            traffic_rate: node.parent_traffic_rate ?? node.traffic_rate,
            server_host: node.server_host,
            server_port: node.server_port,
            client_port: node.client_port ?? null,
            rate_schedule:rateScheduleValues(node.rate_schedule),
            tags:node.tags || [],
            custom_code:node.custom_code || "",
            transfer_limit_gb:Number(node.transfer_limit_bytes || 0)/1073741824,
            route_group_ids:node.route_group_ids || [],
            pool_id: node.pool_id ?? "",
            server_id: node.server_id,
            sort_order: node.sort_order || 0,
            protocol: firstProtocol.current,
          }}
          onValuesChange={() => {
            setDirty(true);
            setDiscard(false);
          }}
        >
          <section className="xnode-config-section" aria-label="基础配置">
          <h3>基础配置</h3>
          <div className="xnode-primary-grid">
            <Form.Item
              name="name"
              label="节点名称"
              rules={[{ required: true, message: "请输入节点名称" }]}
            >
              <Input />
            </Form.Item>
            <Form.Item
              name="traffic_rate"
              label="基础倍率"
              extra={parentID ? `子节点倍率继承自父节点，当前 ${text(rows(parentNodes.data,"nodes").find(n=>n.id===parentID)?.current_traffic_rate ?? node.current_traffic_rate)}×` : undefined}
              rules={[{ required: true, message: "请输入基础倍率" }]}
            >
              <InputNumber
                disabled={!!parentID}
                min={0}
                max={9999}
                suffix="x"
                style={{ width: "100%" }}
              />
            </Form.Item>
          </div>
          <div className="xnode-rate-toggle">
            <div><strong>启用动态倍率</strong><p className="secondary small">按{node.rate_timezone ? text(node.rate_timezone) : "站点时区"}设置时段倍率，未匹配时使用基础倍率。</p></div>
            {parentID ? <Switch aria-label="启用动态倍率" disabled checked={!!((rows(parentNodes.data,"nodes").find(n=>n.id===parentID)?.rate_schedule ?? node.parent_rate_schedule) as {enabled?:boolean} | undefined)?.enabled} /> : <Form.Item name={["rate_schedule","enabled"]} valuePropName="checked" noStyle><Switch aria-label="启用动态倍率" /></Form.Item>}
          </div>
          {!parentID && <div hidden={!dynamicRate} className="xnode-rate-ranges">
            <p className="secondary small">按列表顺序匹配，结束时间包含该分钟。跨午夜请拆成两段，0 表示免费流量。</p>
            <Form.List name={["rate_schedule","ranges"]}>
              {(fields,{add,remove})=><>
                {fields.map(({key,name,...rest})=><div key={key} className="xnode-rate-range">
                  <Form.Item {...rest} name={[name,"start"]} label={`开始时间 ${name+1}`} rules={[{required:true,message:"请选择开始时间"}]}><Input type="time" /></Form.Item>
                  <Form.Item {...rest} name={[name,"end"]} label={`结束时间 ${name+1}`} rules={[{required:true,message:"请选择结束时间"}]}><Input type="time" /></Form.Item>
                  <Form.Item {...rest} name={[name,"rate"]} label={`时段倍率 ${name+1}`} rules={[{required:true,message:"请输入倍率"}]}><InputNumber min={0} max={9999} precision={4} suffix="×" /></Form.Item>
                  <Button aria-label={`删除时间段 ${name+1}`} onClick={()=>{remove(name);setDirty(true)}}>删除</Button>
                </div>)}
                <Button disabled={fields.length>=48} onClick={()=>{add({start:"00:00",end:"23:59",rate:1});setDirty(true)}}>添加时间段</Button>
              </>}
            </Form.List>
          </div>}
          <Form.Item name="transfer_limit_gb" label="流量限制（GB）" extra="0 表示不限。达到上限后从订阅中隐藏，已建立的连接不强制中断。"><InputNumber min={0} max={1000000} step={1} /></Form.Item>
          <Form.Item name="custom_code" label="自定义节点 ID（选填）" extra="用于兼容节点端的 node_id；留空使用原节点 UUID。修改后使用旧别名的节点端需同步更新。" rules={[{pattern:/^[A-Za-z0-9_]{1,64}$/,message:"请输入 1–64 位字母、数字或下划线"}]}><Input maxLength={64} placeholder="选填" /></Form.Item>
          <Form.Item name="tags" label="节点标签" extra="输入后回车添加标签，最多 32 个，每个不超过 64 个字符。" rules={[{validator:(_,value: string[] | undefined)=> !value || (value.length<=32 && value.every(tag=>tag.trim().length>0 && Array.from(tag.trim()).length<=64)) ? Promise.resolve() : Promise.reject(new Error("最多 32 个标签，每个须为 1–64 个字符"))}]}><Select mode="tags" placeholder="输入后回车添加标签" /></Form.Item>
          <Form.Item name="display_name" label="订阅显示名称" extra="留空时使用节点名称。">
            <Input />
          </Form.Item>
          <Form.Item
            name="pool_id"
            label="权限组"
            extra="套餐通过权限组选择可用线路。"
          >
            <Select
              options={[{ value: "", label: "未分组" }, ...poolOptions]}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
          </section>
          <section className="xnode-config-section" aria-label="连接配置">
          <h3>连接配置</h3>
          <Form.Item
            name="server_host"
            label="节点地址"
            rules={[{ required: true, message: "请输入节点地址" }]}
          >
            <Input />
          </Form.Item>
          <div className="xnode-primary-grid">
          <Form.Item name="client_port" label="连接端口" extra="客户端连接的端口，留空时沿用服务端口。">
            <InputNumber min={1} max={65535} precision={0} placeholder="同服务端口" style={{width:"100%"}} />
          </Form.Item>
          <Form.Item
            name="server_port"
            label="服务端口"
            extra="节点内核实际监听的端口。"
            rules={[{ required: true, message: "请输入端口" }]}
          >
            <InputNumber
              min={1}
              max={65535}
              precision={0}
              style={{ width: "100%" }}
            />
          </Form.Item>
          </div>
          </section>
          <section className="xnode-config-section" aria-label="协议配置">
          <h3>协议配置 · {names[kind] || kind}</h3>
          {keys.filter((k) => !advancedKey(k)).map(renderProtocol)}
          {schema.status !== "stable" && <p className="secondary">此旧协议的参数保持不变。</p>}
          </section>
          <section className="xnode-config-section" aria-label="关联配置">
            <h3>关联配置</h3>
          <Form.Item name="parent_node_id" label="父级节点" extra={parentNodes.error ? "父节点列表读取失败，已有绑定保留。" : "选择同协议的父节点，继承倍率及动态倍率规则。"}>
            <Select allowClear showSearch optionFilterProp="label" placeholder="无" loading={parentNodes.isFetching} disabled={!!parentNodes.error} onChange={id=>{const parent=rows(parentNodes.data,"nodes").find(n=>n.id===id);form.setFieldValue("traffic_rate",parent ? parent.traffic_rate : node.traffic_rate);}} options={[
              ...rows(parentNodes.data,"nodes").filter(n=>n.id!==node.id && !n.parent_node_id && n.node_type===kind).map(n=>({value:String(n.id),label:String(n.name)})),
              ...(node.parent_node_id && !rows(parentNodes.data,"nodes").some(n=>n.id===node.parent_node_id) ? [{value:String(node.parent_node_id),label:"已关联父节点"}] : []),
            ]} />
          </Form.Item>
          {hasContract("route-groups-v1") && <Form.Item name="route_group_ids" label="路由组" extra={routeGroups.error ? "路由组读取失败，请重试；已有选择会保留。" : "按选择顺序匹配，先应用共享路由组，再应用节点独立路由。"}>
            <Select mode="multiple" allowClear showSearch optionFilterProp="label" placeholder="选择路由组" loading={routeGroups.isFetching} disabled={!routeWritable || !!routeGroups.error} options={[
              ...rows(routeGroups.data,"route_groups").map(g=>({value:String(g.id),label:String(g.remarks)})),
              ...(Array.isArray(node.route_group_ids) ? node.route_group_ids : []).filter(id=>!rows(routeGroups.data,"route_groups").some(g=>g.id===id)).map(id=>({value:String(id),label:"已关联路由组"})),
            ]} />
          </Form.Item>}

            {create ? <Form.Item name="server_id" label="绑定服务器" rules={[{required:true,message:"请选择绑定服务器"}]} extra="创建后可在此服务器管理节点，配置校验通过后启用下发。"><Select showSearch optionFilterProp="label" placeholder="选择服务器" options={servers.filter(server => !["retired", "quarantined"].includes(text(server.status))).map(server => ({value:text(server.id),label:text(server.name)}))}/></Form.Item> : <>
            <div className="xnode-association-row"><div><strong>绑定服务器</strong><p>{node.server_id ? text(node.server_name, text(node.server_id)) : "未绑定服务器"}</p></div>
            {Boolean(node.server_id) && <a href={`#/servers/${enc(text(node.server_id))}`}>查看服务器</a>}</div>
            {routeProtection && <NodeActions id={text(node.id)} onlyAction="move" disabled={dirty || pending} onCompleted={close} />}
            {dirty && <p className="secondary small">更换服务器前，请先保存或取消当前修改。</p>}
            <div className="xnode-association-row"><div><strong>路由配置</strong><p className="secondary small">管理此节点的匹配规则与自定义出站。</p></div><a href={`#/routing?node=${enc(text(node.id))}`}>配置路由</a></div>
            </>}
          </section>
          <div className="xnode-advanced" hidden={!advanced}>
            <h3>高级设置</h3>
            {create && <Form.Item name="sort_order" label="排序"><InputNumber precision={0}/></Form.Item>}
            {keys.filter(advancedKey).map(renderProtocol)}
          </div>
        </Form>
      </Modal>
    </>
  );
}
