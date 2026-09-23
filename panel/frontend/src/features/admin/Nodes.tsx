import { ServerActions } from "./ServerActions";
import { NodeEditButton, NodeEditDialog } from "./NodeEditor";
import { NodeActions, AssociateNodeButton, NodeBatchActions } from "./NodeActions";
import { AssetEditButton, serverFields } from "./AssetEditor";
import { ServerMetrics } from "./ServerMetrics";
import { principalSubject } from "../../core/data";
import { createdIdSchema, createdResourceSchema } from "../../core/data";
import { z } from "zod";
import { useState } from "react";
import {
  Alert,
  Button,
  Card,
  Modal,
  Progress,
  Select,
  Space,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  Link,
  useNavigate,
  useParams,
  useSearchParams,
} from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { runtime } from "../../core/runtime";
import {
  dateText,
  enc,
  idOf,
  integer,
  record,
  rows,
  text,
  type Row,
} from "../../core/data";
import {
  DataTable,
  Details,
  EntityLink,
  PageHeader,
  QueryPanel,
  ResourcePage,
  Status,
  useData,
} from "../../components/common";

function ServerLoad({ server }: { server: Row }) {
  if (!server.metrics_at) return <span className="secondary">尚无负载上报</span>;
  const metrics = [
    { label: "CPU", used: server.cpu_bp, total: 10000, suffix: "%" },
    { label: "内存", used: server.mem_used_mb, total: server.mem_total_mb, suffix: "MB" },
    { label: "磁盘", used: server.disk_used_gb, total: server.disk_total_gb, suffix: "GB" },
  ];
  return <div style={{ minWidth: 180, maxWidth: 420 }}>
    {metrics.map(metric => {
      const used = Number(metric.used), total = Number(metric.total);
      const valid = metric.used != null && metric.total != null && Number.isFinite(used) && Number.isFinite(total) && used >= 0 && total > 0;
      const percent = valid ? used / total * 100 : 0;
      return <div key={metric.label}><div style={{ display: "flex", justifyContent: "space-between", fontSize: 12 }}><span>{metric.label}</span><span>{!valid ? "未上报" : metric.suffix === "%" ? `${percent.toFixed(1)}%` : `${used} / ${total} ${metric.suffix}`}</span></div>
      {valid && <Progress percent={Math.min(100, percent)} showInfo={false} size="small" strokeColor={percent >= 90 ? "#e5484d" : "#4263df"} />}</div>;
    })}
    <span className="secondary small">采样：{dateText(server.metrics_at)}</span>
  </div>;
}
export function ServersPage() {
  const [detailId, setDetailId] = useState("");
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const navigate = useNavigate();
  const create = () => {
    const operation = new Operation(
      "server-create",
      principalSubject(principal),
    );
    return open({
      title: "新增服务器",
      description: "先创建资产记录，再在这台机器上执行一次性接入命令。",
      fields: serverFields,
      initial: { capacity_nodes: 32 },
      submitLabel: "创建并继续接入",
      onSubmit: async (values) => {
        const result = await operation.send(
          api,
          "v1/servers",
          values,
          {},
          z.union([createdIdSchema, createdResourceSchema("server")]),
        );
        navigate(`/servers/${enc(idOf(record(result.server ?? result)))}`);
      },
    });
  };
  return (
    <ResourcePage
      title="服务器管理"
      description="查看服务器接入状态、CPU / 内存 / 磁盘负载及关联节点。创建记录后需执行接入命令，才会开始上报。"
      resource="servers"
      path="v1/servers"
      listKey="servers"
      searchable
      clientFilters={[
        { key: "probe_status", label: "探针状态", field: "heartbeat_online", value: row => !row.last_heartbeat_at ? "pending" : row.heartbeat_online ? "online" : "offline", options: [{ value: "online", label: "在线" }, { value: "offline", label: "离线" }, { value: "pending", label: "未接入" }] },
        { key: "region", label: "地区", field: "region" },
        { key: "has_nodes", label: "关联节点", field: "node_count", value: row => Number(row.node_count) > 0 ? "yes" : "no", options: [{ value: "yes", label: "已关联节点" }, { value: "no", label: "尚无节点" }] },
      ]}
      footer={<Modal className="server-detail-modal" open={Boolean(detailId)} title="服务器详情" footer={null} width={1100} style={{ top: 24 }} styles={{ body: { maxHeight: "calc(100dvh - 140px)", overflowY: "auto" } }} destroyOnHidden onCancel={() => setDetailId("")}>{detailId && <ServerDetail serverId={detailId} onDeleted={() => setDetailId("")} />}</Modal>}
      extra={
        can("node.write") && (
          <Button type="primary" onClick={() => void create()}>
            新增服务器
          </Button>
        )
      }
      columns={[
        {
          title: "服务器",
          sorter: (a, b) => text(a.name).localeCompare(text(b.name), "zh-CN"),
          render: (_, row) => (
            <div>
              <EntityLink row={row} path="/servers" />
              <div className="secondary small">
                {text(row.region, "地区待识别")} · {text(row.public_ipv4, text(row.hostname))}
                {row.notes ? <div>{text(row.notes)}</div> : null}
              </div>
            </div>
          ),
        },
        {
          title: "生命周期",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        {
          title: "探针",
          render: (_, row) => (
            <Status value={!row.last_heartbeat_at ? "未接入" : row.heartbeat_online ? "online" : "offline"} />
          ),
        },
        {
          title: "实时负载",
          render: (_, row) => <ServerLoad server={row} />,
        },
        {
          title: "节点 / 容量",
          render: (_, row) =>
            `${integer(row.node_count)} / ${integer(row.capacity_nodes)}`,
        },
        {
          title: "Agent版本",
          dataIndex: "agent_version",
          render: (value) => text(value, "未接入"),
        },
        { title: "最近心跳", dataIndex: "last_heartbeat_at", render: dateText },
        { title: "操作", key: "actions", fixed: "right", width: 300, render: (_, row) => <Space><Button onClick={() => setDetailId(idOf(row))}>详情</Button><AssetEditButton kind="servers" id={idOf(row)} /><ServerActions id={idOf(row)} /></Space> },
      ]}
    />
  );
}
export function ServerDetail({ serverId, onDeleted }: { serverId?: string; onDeleted?: () => void } = {}) {
  const navigate = useNavigate();
  const params = useParams();
  const id = serverId || params.id || "";
  const query = useData("servers", `v1/servers/${enc(id)}`);
  const server = record(query.data?.server ?? query.data);
  const nodes = useData("nodes", `v1/servers/${enc(id)}/nodes`);
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const bootstrap = () => {
    const operation = new Operation(
      "server-enroll",
      principalSubject(principal),
    );
    return open({
      title: "签发一次性接入命令",
      description:
        "令牌绑定此服务器，20分钟内有效且仅能使用一次。请在目标服务器执行。命令含凭据，仅本次展示。",
      submitLabel: "签发命令",
      onSubmit: async () => {
        const result = await operation.send(
          api,
          `v1/servers/${enc(id)}/bootstrap-token`,
          { ttl_minutes: 20 },
        );
        if (!result.install_command || !result.token)
          throw new Error("服务未返回安装命令，请核对接入配置");
        void open({
          title: "服务器接入命令",
          description: (
            <>
              <Alert
                type="warning"
                title="一次性凭据，不要发送到公开渠道。关闭后不会保存到浏览器存储。"
                className="mb"
              />
              <p>1. 复制命令到目标服务器执行。</p>
              <Typography.Paragraph className="command-text" copyable>
                {text(result.install_command)}
              </Typography.Paragraph>
              <p>
                2. 终端出现 Bootstrap token 提示时，粘贴下面的一次性令牌（不是管理员密码）。
              </p>
              <Typography.Paragraph className="command-text" copyable>{text(result.token)}</Typography.Paragraph>
              <p>有效期至 {dateText(result.expires_at)}。安装后本页自动刷新心跳。使用默认接入命令安装后，在此服务器下创建并启用业务节点，代理会自动接入；端口和配置仍需校验。</p>
            </>
          ),
          submitLabel: "已保存并关闭",
        });
      },
    });
  };
  return (
    <section className="server-detail-view">
      <PageHeader
        title={text(server.name, "服务器详情")}
        description={text(server.region, "地区待识别") + (server.notes ? " · " + text(server.notes) : "")}
        extra={
          <Space wrap>
            {!serverId && <Link to="/servers">返回列表</Link>}
            {can("node.write") && (
              <AssetEditButton kind="servers" id={idOf(server)} />
            )}
            <ServerActions id={idOf(server)} onDeleted={onDeleted || (() => navigate("/servers"))} />
            {can("node.provision") && (
              <Button type="primary" onClick={() => void bootstrap()}>
                取得接入命令
              </Button>
            )}
          </Space>
        }
      />
      <QueryPanel query={query} empty={!server.id}>
        <Card className="mb server-summary">
          <Space className="mb">
            <Status value={server.status} />
            <Status value={!server.last_heartbeat_at ? "未接入" : server.heartbeat_online ? "online" : "offline"} />
          </Space>
          <Details
            data={server}
            fields={[
              ["node_count", "关联节点数"],
              ["capacity_nodes", "计划承载节点数"],
              ["last_heartbeat_at", "最近心跳", dateText],
            ]}
          />
        </Card>
        {!server.last_heartbeat_at && <Alert className="mb" showIcon type="info" title="尚未接入探针" description="此记录尚未收到心跳。请点击取得接入命令，在目标机器完成安装。创建记录不代表内核已启动。" />}
        <div className="split-detail server-telemetry"><ServerMetrics nodeId={text(server.control_node_id, "")} /><Card className="mb" title="实时负载"><ServerLoad server={server} /></Card></div>
        <Card
          className="mb asset-related-card"
          title="关联节点"
          extra={
            can("node.provision") && (
              <Space wrap><AssociateNodeButton serverId={id} /><Link to={`/nodes/new?server=${enc(id)}`}>
                <Button>新增节点到此服务器</Button>
              </Link></Space>
            )
          }
        >
          <p className="server-section-hint">创建节点后，校验协议配置并启用下发，即可自动接入。</p>
          <QueryPanel query={nodes}>
            <DataTable
              data={rows(nodes.data, "nodes")}
              scrollX={940}
              columns={[
                { title: "节点 / 入口", width: 290, render: (_, row) => <div className="server-node-name"><EntityLink row={row} path="/nodes" /><div className="secondary small">{text(row.node_type).toUpperCase()} · {text(row.server_host)}:{text(row.client_port ?? row.server_port)}</div></div> },
                { title: "运行状态", width: 150, render: (_, row) => <div className="server-node-state"><Space size={4}><Status value={row.status} /><span>{row.serving_status === "active" ? "已启用" : row.serving_status === "disabled" ? "已停用" : text(row.serving_status)}</span></Space><span className="secondary small">{row.identity_active ? "已接入" : "等待接入"}</span></div> },
                { title: "配置同步", width: 125, render: (_, row) => !row.config_validated_at ? "待校验" : !row.last_issued_generation ? "等待下发" : row.last_issued_generation === row.last_applied_generation ? `已确认 · #${text(row.last_applied_generation)}` : `待确认 · #${text(row.last_issued_generation)}` },
                { title: "最近心跳", width: 155, dataIndex: "last_heartbeat_at", render: dateText },
                { title: "操作", width: 220, render: (_, row) => <Space size={6}><NodeEditButton id={idOf(row)} /><NodeActions id={idOf(row)} /></Space> },
              ]}
            />
          </QueryPanel>
        </Card>

        <Card className="mb server-machine-card" title="机器信息"><Details data={server} fields={[
              ["id", "服务器 ID"],
              ["notes", "备注"],
              ["hostname", "主机名"],
              ["public_ipv4", "公网IPv4"],
              ["public_ipv6", "公网IPv6"],
              ["architecture", "架构"],
              ["os_name", "系统"],
              ["agent_version", "Agent版本"],
              ["control_node_id", "控制节点ID"],
        ]} /></Card>
      </QueryPanel>
    </section>
  );
}
export function NodesPage() {
  const [detailId, setDetailId] = useState("");
  const [params, setParams] = useSearchParams();
  const { can } = useAuth();
 const [role,setRole]=useState("business");
  return (
    <ResourcePage
      title="节点管理"
      description="每条业务线路独立管理协议、归属和服务状态。"
      resource="nodes"
      selection={can("node.lifecycle") || can("node.write") ? { enabled: row => row.runtime_role !== "probe" && row.serving_status !== "retired", actions: (selected, clear) => <NodeBatchActions selected={selected} clear={clear} /> } : undefined}
      path={`v1/nodes${role?`?runtime_role=${role}`:""}`}
      listKey="nodes"
      serverPagination
      searchable
      clientSearchFields={["name", "display_name", "id", "server_host", "server_name", "pool_name", "tags"]}
      clientFilters={[
        { key: "protocol", label: "协议", field: "node_type" },
        { key: "serving", label: "下发状态", field: "serving_status", options: [{ value: "active", label: "已启用" }, { value: "disabled", label: "已停用" }, { value: "draft", label: "草稿" }] },
        { key: "server", label: "归属服务器", field: "server_name" },
        { key: "pool", label: "权限组", field: "pool_name" },
      ]}
      footer={<Modal open={Boolean(detailId)} title="节点详情" footer={null} width={1100} style={{ top: 24 }} styles={{ body: { maxHeight: "calc(100dvh - 140px)", overflowY: "auto" } }} destroyOnHidden onCancel={() => setDetailId("")}>{detailId && <NodeDetail nodeId={detailId} />}</Modal>}
      extra={
        <Space wrap>
          <Select aria-label="节点角色" value={role} onChange={value => { setRole(value); const next = new URLSearchParams(params); next.set("page", "1"); setParams(next); }} options={[{value:"business",label:"业务节点"},{value:"probe",label:"服务器探针"},{value:"",label:"全部记录"}]} style={{width:140}}/>
          <Link to="/node-pools">
            <Button>权限组</Button>
          </Link>
          {can("node.provision") && (
            <Link to="/nodes/new">
              <Button type="primary">新增节点</Button>
            </Link>
          )}
        </Space>
      }
      columns={[
        {title:"节点 ID",dataIndex:"node_no",width:90,render:(value)=>value==null?"—":String(value)},
        {title:"角色",responsive:["md"],render:(_,row)=>row.runtime_role==="probe"?"服务器探针":"业务节点"},
        { title: "排序", responsive: ["md"], dataIndex: "sort_order", sorter: (a, b) => Number(a.sort_order || 0) - Number(b.sort_order || 0), defaultSortOrder: "ascend" },
        {
          title: "节点",
          sorter: (a, b) => text(a.name).localeCompare(text(b.name), "zh-CN"),
          render: (_, row) => (
            <div>
              {row.runtime_role==="probe" && row.server_id ? <Link to={`/servers/${enc(text(row.server_id))}`}>{text(row.name)}</Link> : <EntityLink row={row} path="/nodes" />}
              <div className="secondary small">
                {text(row.display_name, "")}
                {Array.isArray(row.tags) && row.tags.map((tag: unknown)=><Tag key={String(tag)}>{String(tag)}</Tag>)}
              </div>
            </div>
          ),
        },
        {
          title: "服务状态",
          dataIndex: "serving_status",
          render: (value,row) => row.runtime_role==="probe" ? "—" : <Status value={value} />,
        },
        {
          title: "生命周期",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        { title: "协议", dataIndex: "node_type" },
        {
          title: "入口",
          render: (_, row) =>
            row.runtime_role==="probe" ? "—" : `${text(row.server_host)}:${text(row.client_port ?? row.server_port)}`,
        },
        {
          title: "倍率",
          dataIndex: "traffic_rate",
          sorter: (a, b) => Number(a.current_traffic_rate ?? a.traffic_rate ?? 0) - Number(b.current_traffic_rate ?? b.traffic_rate ?? 0),
          render: (value,row) => row.runtime_role==="probe" ? "—" : `${text(row.current_traffic_rate ?? value)}×${record(row.parent_node_id ? row.parent_rate_schedule : row.rate_schedule).enabled ? " · 动态" : ""}${row.parent_node_id ? " · 继承" : ""}`,
        },
        {
          title: "配置校验",
          dataIndex: "config_validated_at",
          render: dateText,
        },
        { title: "操作", key: "actions", fixed: "right", width: 270, render: (_, row) => row.runtime_role === "probe" ? <Link to={`/servers/${enc(text(row.server_id))}`}>服务器详情</Link> : <Space><Button onClick={() => setDetailId(idOf(row))}>详情</Button><NodeEditButton id={idOf(row)} /><NodeActions id={idOf(row)} /></Space> },
      ]}
    />
  );
}
export function NodeCreate() {
  const schemas = useData("schemas", "v1/node-protocol-schemas");
  const servers = useData("servers", "v1/servers");
  const pools = useData("pools", "v1/node-pools");
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const { can } = useAuth();
  const ready = schemas.isSuccess && servers.isSuccess && pools.isSuccess;
  return <>
    <NodesPage />
    {!ready && <QueryPanel query={schemas}><QueryPanel query={servers}><QueryPanel query={pools}><span>正在加载节点配置…</span></QueryPanel></QueryPanel></QueryPanel>}
    {ready && can("node.provision") && <NodeEditDialog
      create routeProtection
      node={{name:"",display_name:"",server_id:params.get("server") || undefined,server_port:443,traffic_rate:1,sort_order:0}}
      schemas={rows(schemas.data,"schemas").filter(schema=>schema.status==="stable")}
      pools={rows(pools.data,"pools")} servers={rows(servers.data,"servers")}
      close={()=>navigate("/nodes")}
      onCreated={node=>navigate(`/nodes/${enc(idOf(node))}`)}
    />}
  </>;
}
export function NodeDetail({ nodeId }: { nodeId?: string } = {}) {
  const params = useParams();
  const id = nodeId || params.id || "";
  const query = useData("nodes", `v1/nodes/${enc(id)}`,Boolean(id));
  const node = record(query.data);
 const isProbe=node.runtime_role==="probe";
  const delivery = useData("nodes", `v1/servers/${enc(text(node.server_id, ""))}/nodes`, Boolean(node.server_id) && !isProbe);
  const delivered = rows(delivery.data, "nodes").find(row => row.id === id);
  const metrics = useData("metrics", `v1/nodes/${enc(id)}/metrics?minutes=60`);
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const status = () => {
    const attempt = new Operation("node-status", principalSubject(principal));
    return open({
      title:
        node.serving_status === "active"
          ? "停止向用户下发此线路"
          : "启用此线路",
      description:
        "修改配置中的服务显隐。是否能够实际连接，仍需确认身份、应用状态和数据面。",
      fields: [{ name: "reason", label: "原因", required: true }],
      onSubmit: async (values) => {
        await attempt.send(api, "v1/nodes/status:batch", {
          items: [{ id, row_version: node.row_version }],
          serving_status:
            node.serving_status === "active" ? "disabled" : "active",
          reason: values.reason,
        });
        await query.refetch();
      },
    });
  };
  return (
    <>
      <PageHeader
        title={text(node.display_name, text(node.name, "节点详情"))}
        description={text(node.name, "")}
        extra={
          <Space>
            {!nodeId && <Link to="/nodes">返回节点</Link>}
            {Boolean(node.id) && !isProbe && <Link to={`/routing?node=${enc(id)}`}>路由配置</Link>}
            {can("node.write") && Boolean(node.id) && !isProbe && (
              <Space wrap><NodeEditButton id={id} /><NodeActions id={id} /></Space>
            )}
            {can("node.lifecycle") && Boolean(node.id) && !isProbe && node.serving_status !== "retired" && (
              <Button onClick={() => void status()}>
                {node.serving_status === "active" ? "停止下发" : "启用下发"}
              </Button>
            )}
          </Space>
        }
      />
      <QueryPanel query={query} empty={!node.id}>
        <Card className="mb">
          <Space className="mb">
            <Status value={node.status} />
            <Status value={node.serving_status} />
          </Space>
          <Details
            data={node}
            fields={[
              ["id", isProbe?"探针NodeID":"业务NodeID"],
              ["node_type", "协议"],
              ["server_host", "入口地址"],
              ["client_port", "连接端口", value => text(value ?? node.server_port)],
              ["server_port", "服务端口"],
              ["kernel", "内核"],
              ["traffic_rate", "基础倍率", value => text(node.parent_traffic_rate ?? value)],
              ["custom_code", "自定义节点 ID", value => text(value,"未设置")],
              ["transfer_limit_bytes", "节点流量上限", value => Number(value)>0 ? `${(Number(value)/1073741824).toLocaleString()} GB` : "不限"],
              ["transfer_used_bytes", "节点已用流量", value => `${(Number(value || 0)/1073741824).toLocaleString(undefined,{maximumFractionDigits:3})} GB`],
              ["transfer_reset_at", "最近重置", dateText],
              ["parent_node_id", "父级节点", value => value ? <a href={`#/nodes/${enc(text(value))}`}>查看父节点</a> : "无"],
              ["current_traffic_rate", "当前倍率", value => text(value ?? node.traffic_rate)],
              ["rate_timezone", "倍率时区"],
              ["protocol_schema_version", "协议Schema版本"],
              ["config_validated_at", "配置验证时间", dateText],
              [
                "server_id",
                "归属服务器",
                (value) =>
                  value ? (
                    <Link to={`/servers/${enc(text(value))}`}>查看服务器</Link>
                  ) : (
                    "未绑定"
                  ),
              ],
            ]}
          />
        </Card>
        <Card>
          <Tabs
            items={[
              {
                key: "metrics",
                label: "探针指标",
                children: (
                  <QueryPanel query={metrics}>
                    <Details
                      data={record(metrics.data?.latest)}
                      fields={[
                        ["cpu_percent", "CPU（%）"],
                        ["mem_used_mb", "已用内存MB"],
                        ["mem_total_mb", "总内存MB"],
                        ["disk_used_gb", "磁盘GB"],
                      ]}
                    />
                    <DataTable
                      data={rows(metrics.data, "points")}
                      columns={[
                        { title: "时间", dataIndex: "at", render: dateText },
                        { title: "CPU（%）", dataIndex: "cpu_percent" },
                        { title: "内存（%）", dataIndex: "mem_percent" },
                      ]}
                    />
                  </QueryPanel>
                ),
              },
              {
                key: "delivery",
                label: "配置应用状态",
                children: (
                  !node.server_id || isProbe ? <Alert type="info" title={isProbe ? "服务器探针不承载业务线路配置" : "绑定服务器后可查看节点配置应用状态"} /> :
                  <QueryPanel query={delivery} empty={!delivered}>
                    <Details data={delivered || {}} fields={[
                      ["identity_active", "接入身份", value => value === true ? "有效" : "尚无有效接入身份"],
                      ["config_validated_at", "配置校验时间", dateText],
                      ["last_issued_generation", "期望配置代次", value => value == null ? "尚未下发" : `#${text(value)}`],
                      ["last_applied_generation", "已应用配置代次", value => value == null ? "尚未确认" : `#${text(value)}`],
                      ["last_heartbeat_at", "最近心跳", dateText],
                    ]} />
                    <Alert className="mt" type={delivered?.last_issued_generation != null && delivered.last_issued_generation === delivered.last_applied_generation ? "success" : "info"}
                      title={delivered?.last_issued_generation == null ? "等待配置下发" : delivered.last_issued_generation === delivered.last_applied_generation ? "节点已确认当前配置" : "等待节点确认新配置"}
                      description="配置确认来自节点上报；真实代理连接和流量计费需要分别验证。" />
                  </QueryPanel>
                ),
              },
            ]}
          />
        </Card>
      </QueryPanel>
    </>
  );
}
export function PoolsPage() {
  const { api, can, principal } = useAuth();
  const open = useDialog();
  const client = useQueryClient();
  const edit = (row?: Row) => {
    const attempt = new Operation("node-pool", principalSubject(principal));
    return open({
      title: row ? "编辑权限组" : "新增权限组",
      protectDraft: true,
      initial: row || { status: "active" },
      fields: [
        { name: "name", label: "名称", required: true },
        {
          name: "code",
          label: "分组代码",
          disabled: Boolean(row),
          help: "留空时自动生成；创建后保持不变，供接口和脚本引用。",
        },
        { name: "region", label: "地区（可选）", placeholder: "例如：香港、日本", help: "用于分类和筛选权限组；服务器地区由各服务器的探针单独识别。" },
        {
          name: "status",
          label: "状态",
          type: "select",
          options: [
            { value: "active", label: "启用" },
            { value: "disabled", label: "停用" },
          ],
        },
      ],
      onSubmit: async (values) => {
        await attempt.send(
          api,
          `v1/node-pools${row ? "/" + enc(idOf(row)) : ""}`,
          values,
        );
        await client.invalidateQueries({ queryKey: [runtime.domain] });
      },
    });
  };
  return (
    <ResourcePage
      title="权限组"
      description="把线路分组，再绑定到套餐权益。"
      resource="pools"
      path="v1/node-pools"
      listKey="pools"
      searchable
      clientSearchFields={["name", "code", "region"]}
      clientFilters={[
        { key: "status", label: "状态", field: "status", options: [{ value: "active", label: "启用" }, { value: "disabled", label: "停用" }] },
        { key: "region", label: "地区", field: "region" },
      ]}
      extra={
        <Space>
          <Link to="/nodes">返回节点</Link>
          {can("node.write") && (
            <Button type="primary" onClick={() => void edit()}>
              新增权限组
            </Button>
          )}
        </Space>
      }
      columns={[
        { title: "分组", dataIndex: "name" },
        { title: "代码", dataIndex: "code" },
        { title: "地区", dataIndex: "region" },
        {
          title: "状态",
          dataIndex: "status",
          render: (value) => <Status value={value} />,
        },
        { title: "在役节点", dataIndex: "active_nodes" },
        { title: "绑定套餐", dataIndex: "plans" },
        {
          title: "操作",
          render: (_, row) =>
            can("node.write") && (
              <Space size={0}><Button type="link" onClick={() => void edit(row)}>
                编辑
              </Button><Button type="link" danger
                disabled={integer(row.nodes) > 0 || integer(row.plans) > 0}
                title={integer(row.nodes) > 0 || integer(row.plans) > 0 ? "请先移走关联节点并解除套餐绑定" : "删除权限组"}
                onClick={() => {
                  const attempt = new Operation("node-pool-delete", principalSubject(principal));
                  void open({ title: "删除权限组", danger: true, submitLabel: "删除",
                    description: `确定删除“${text(row.name)}”？有关联节点、套餐、模板或配置的权限组不能删除。`,
                    onSubmit: async () => {
                      await attempt.send(api, `v1/node-pools/${enc(idOf(row))}`, {}, { method: "DELETE" });
                      await client.invalidateQueries({ queryKey: [runtime.domain] });
                    },
                  });
                }}>删除</Button></Space>
            ),
        },
      ]}
    />
  );
}
