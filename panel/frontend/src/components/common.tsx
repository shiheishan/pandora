import {
  Alert,
  Button,
  Card,
  Descriptions,
  Empty,
  Grid,
  Input,
  Select,
  Skeleton,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import { ReloadOutlined, SearchOutlined } from "@ant-design/icons";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { useAuth } from "../core/auth";
import { failure } from "../core/api";
import { idOf, integer, record, rows, text, type Row } from "../core/data";
import { runtime } from "../core/runtime";
import { z } from "zod";
import { cachePolicy } from "../core/cachePolicy";
import { recordSchema } from "../core/data";

export function useData(
  resource: string,
  path: string,
  enabled = true,
  listKey?: string,
) {
  const { api, scope, principal } = useAuth();
  return useQuery({
    ...cachePolicy(path),
    queryKey: [runtime.domain, scope, resource, path],
    queryFn: ({ signal }) =>
      listKey
        ? api.request(
            path,
            z
              .object({
                [listKey]: z
                  .array(recordSchema)
                  .nullable()
                  .transform((value) => value || []),
              })
              .passthrough(),
            { signal },
          )
        : api.get(path, signal),
    enabled: Boolean(principal) && enabled,
  });
}
export function PageHeader({
  title,
  description,
  extra,
}: {
  title: string;
  description?: string;
  extra?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        <Typography.Title level={2}>{title}</Typography.Title>
        {description && (
          <Typography.Paragraph type="secondary">
            {description}
          </Typography.Paragraph>
        )}
      </div>
      <div className="page-actions">{extra}</div>
    </div>
  );
}
export function QueryPanel({
  query,
  children,
  empty = false,
}: {
  query: UseQueryResult<Row, Error>;
  children: ReactNode;
  empty?: boolean;
}) {
  if (query.isPending)
    return (
      <Card>
        <Skeleton active paragraph={{ rows: 5 }} />
      </Card>
    );
  if (query.isError && !query.data)
    return (
      <Alert
        type="error"
        showIcon
        title="暂时无法加载"
        description={failure(query.error).message}
        action={<Button onClick={() => void query.refetch()}>重试</Button>}
      />
    );
  return (
    <>
      {query.isError && (
        <Alert
          className="mb"
          type="warning"
          showIcon
          title="刷新失败，正在显示上次结果"
          description={failure(query.error).message}
          action={<Button onClick={() => void query.refetch()}>重试</Button>}
        />
      )}
      {empty ? (
        <Card>
          <Empty description="暂无数据" />
        </Card>
      ) : (
        children
      )}
    </>
  );
}
const statusLabels: Record<string, [string, string]> = {
  active: ["正常", "success"],
  suspended: ["停用", "warning"],
  banned: ["封禁", "error"],
  draft: ["草稿", "default"],
  online: ["在线", "success"],
  offline: ["离线", "default"],
  retired: ["已退役", "default"],
  enabled: ["启用", "success"],
  disabled: ["关闭", "default"],
  pending: ["待处理", "processing"],
  pending_review: ["待审批", "processing"],
  approved: ["已批准，待执行", "processing"],
  rejected: ["已拒绝或撤回", "default"],
  manual_required: ["需人工核对", "warning"],
  pending_payment: ["待支付", "warning"],
  processing: ["处理中", "processing"],
  paid: ["已支付", "success"],
  fulfilled: ["已开通", "success"],
  succeeded: ["成功", "success"],
  failed: ["失败", "error"],
  expired: ["已到期", "default"],
  cancelled: ["已取消", "default"],
  refunded: ["已退款", "default"],
  partially_refunded: ["部分退款", "warning"],
  open: ["待处理", "processing"],
  waiting: ["等待回复", "warning"],
  closed: ["已关闭", "default"],
  resolved: ["已解决", "success"],
  published: ["已发布", "success"],
  archived: ["已归档", "default"],
  queued: ["待执行", "processing"],
  checking: ["正在向渠道查单", "processing"],
  synced: ["支付事实已同步", "success"],
  needs_review: ["需要人工核对", "warning"],
  sent: ["已发送", "success"],
  trialing: ["试用中", "processing"],
  unknown: ["待核实", "warning"],
  ready: ["已就绪", "success"],
  bootstrapping: ["接入中", "processing"],
  attesting: ["待验证", "processing"],
  provisioning: ["等待接入", "processing"],
  bootstrap_failed: ["接入失败", "error"],
};
export function Status({ value }: { value: unknown }) {
  const key = text(value, "unknown");
  const status = statusLabels[key];
  return <Tag color={status?.[1]}>{status?.[0] || key}</Tag>;
}
export function EntityLink({
  row,
  path,
  label = "name",
}: {
  row: Row;
  path: string;
  label?: string;
}) {
  const location = useLocation();
  return (
    <Link
      className="entity-link"
      to={`${path}/${encodeURIComponent(idOf(row))}`}
      state={{ returnTo: location.pathname + location.search }}
    >
      {text(row[label], idOf(row))}
    </Link>
  );
}
export function BackLink({
  to,
  children = "返回列表",
}: {
  to: string;
  children?: ReactNode;
}) {
  const location = useLocation();
  const from: unknown = location.state?.returnTo;
  const safe =
    typeof from === "string" && (from === to || from.startsWith(to + "?"))
      ? from
      : to;
  return <Link to={safe}>{children}</Link>;
}
export function Details({
  data,
  fields,
}: {
  data: Row;
  fields: [string, string, ((value: unknown) => ReactNode)?][];
}) {
  return (
    <Descriptions
      column={{ xs: 1, sm: 2, lg: 3 }}
      items={fields.map(([key, label, render]) => ({
        key,
        label,
        children: render ? render(data[key]) : text(data[key]),
      }))}
    />
  );
}
export function DataTable({
  data,
  columns,
  loading = false,
  scrollX = 760,
}: {
  data: Row[];
  columns: ColumnsType<Row>;
  loading?: boolean;
  scrollX?: number;
}) {
  return (
    <Table<Row>
      rowKey={(row) =>
        idOf(row) ||
        text(row.email ?? row.created_at ?? row.at ?? row.period_start)
      }
      columns={columns}
      dataSource={data}
      loading={loading}
      size="middle"
      scroll={{ x: scrollX }}
      pagination={
        data.length > 15
          ? { defaultPageSize: 15, showSizeChanger: true }
          : false
      }
    />
  );
}
export function ResourcePage({
  title,
  description,
  resource,
  path,
  listKey,
  columns,
  extra,
  serverPagination = false,
  searchable = false,
  footer,
  statuses,
  clientSearchFields,
  clientFilters = [],
  selection,
}: {
  title: string;
  description?: string;
  resource: string;
  path: string;
  listKey: string;
  columns: ColumnsType<Row>;
  extra?: ReactNode;
  serverPagination?: boolean;
  searchable?: boolean;
  footer?: ReactNode;
  statuses?: { label: string; value: string }[];
  clientSearchFields?: string[];
  clientFilters?: { key: string; label: string; field: string; options?: { label: string; value: string }[]; value?: (row: Row) => string }[];
  selection?: { enabled: (row: Row) => boolean; actions: (selected: Row[], clear: () => void) => ReactNode };
}) {
  const [params, setParams] = useSearchParams();
  const screens = Grid.useBreakpoint();
  const [selectedKeys, setSelectedKeys] = useState<React.Key[]>([]);
  const selectionScope = path + "?" + params.toString();
  useEffect(() => setSelectedKeys([]), [selectionScope]);
  const current = Math.max(1, integer(params.get("page"), 1));
  const pageSize = [20, 50, 100].includes(integer(params.get("pageSize")))
    ? integer(params.get("pageSize"))
    : 20;
  const q = params.get("q") || "";
  const [draft, setDraft] = useState(q);
  useEffect(() => setDraft(q), [q]);
  const filters = new URLSearchParams();
  if (serverPagination) {
    filters.set("offset", String((current - 1) * pageSize));
    filters.set("limit", String(pageSize));
    for (const filter of clientFilters) {
      if (params.get(filter.key)) filters.set(filter.key, params.get(filter.key)!);
    }
  }
  if (searchable && q && (!clientSearchFields || serverPagination)) filters.set("q", q);
  if (params.get("status")) filters.set("status", params.get("status")!);
  const url =
    path + (filters.size ? (path.includes("?") ? "&" : "?") + filters : "");
  const query = useData(resource, url, true, listKey);
  const source = rows(query.data, listKey);
  const localFiltering = !serverPagination;
  const data = source.filter(row => {
    if (!localFiltering) return true;
    if (clientSearchFields && q && !clientSearchFields.some(field => (Array.isArray(row[field]) ? (row[field] as unknown[]).map(value => text(value, "")).join(" ") : text(row[field], "")).toLocaleLowerCase().includes(q.toLocaleLowerCase()))) return false;
    return clientFilters.every(filter => !params.get(filter.key) || (filter.value ? filter.value(row) : text(row[filter.field], "")) === params.get(filter.key));
  });
  const visiblePage = serverPagination ? current : Math.min(current, Math.max(1, Math.ceil(data.length / pageSize)));
  const selectedRows = data.filter(row => selectedKeys.includes(idOf(row)) && selection?.enabled(row));
  const update = (values: Record<string, string>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(values))
      value ? next.set(key, value) : next.delete(key);
    setParams(next);
  };
  return (
    <>
      <PageHeader title={title} description={description} extra={extra} />
      <Card className="data-card">
        <div className="table-toolbar">
          <Space wrap>
            {searchable && (
              <Input.Search
                prefix={<SearchOutlined />}
                placeholder="搜索"
                value={draft}
                onChange={(event) => setDraft(event.target.value)}
                onSearch={() => update({ q: draft.trim(), page: "1" })}
                allowClear
                style={{ width: 280 }}
              />
            )}
            {statuses && (
              <Select
                aria-label="筛选状态"
                placeholder="全部状态"
                allowClear
                value={params.get("status") || undefined}
                options={statuses}
                style={{ width: 145 }}
                onChange={(value) => update({ status: value || "", page: "1" })}
              />
            )}
            {clientFilters.map(filter => <Select key={filter.key}
              aria-label={filter.label} placeholder={filter.label} allowClear showSearch optionFilterProp="label"
              value={params.get(filter.key) || undefined}
              options={filter.options || (serverPagination
                ? (Array.isArray(record(query.data?.facets)[filter.key]) ? (record(query.data?.facets)[filter.key] as unknown[]).map(value => text(value, "")).filter(Boolean) : [])
                : [...new Set(source.map(row => text(row[filter.field], "")).filter(Boolean))].sort()).map(value => ({ value, label: value }))}
              style={{ minWidth: 140, maxWidth: 240 }}
              onChange={value => update({ [filter.key]: value || "", page: "1" })} />)}
            {params.get("status") && !statuses && (
              <Tag closable onClose={() => update({ status: "", page: "1" })}>
                状态：{params.get("status")}
              </Tag>
            )}
          </Space>
          <Button
            icon={<ReloadOutlined />}
            loading={query.isFetching}
            onClick={() => void query.refetch()}
          >
            刷新
          </Button>
        </div>
        <QueryPanel query={query}>
          {selection && <Space wrap className="mb">
            <span>已选择 {selectedRows.length} 项</span>
            {selection.actions(selectedRows, () => setSelectedKeys([]))}
            {selectedRows.length > 0 && <Button onClick={() => setSelectedKeys([])}>取消选择</Button>}
          </Space>}
          <Table<Row>
            rowKey={(row) =>
              idOf(row) || text(row.created_at ?? row.at ?? row.period_start)
            }
            columns={screens.md ? columns : columns.map(column => ({ ...column, fixed: undefined, width: column.width || 140 }))}
            rowSelection={selection ? {
              selectedRowKeys: selectedRows.map(idOf),
              onChange: keys => setSelectedKeys(keys),
              getCheckboxProps: row => ({ disabled: !selection.enabled(row) }),
            } : undefined}
            dataSource={data}
            loading={query.isFetching}
            size="middle"
            scroll={{ x: screens.md ? 800 : "max-content" }}
            locale={{ emptyText: <Empty description="暂无符合条件的数据" /> }}
            pagination={
              serverPagination
                ? {
                    current: visiblePage,
                    pageSize,
                    total: integer(query.data?.total, data.length),
                    showSizeChanger: true,
                    pageSizeOptions: [20, 50, 100],
                    showTotal: (total) => `共 ${total} 项`,
                    onChange: (page, size) =>
                      update({
                        page: String(size === pageSize ? page : 1),
                        pageSize: String(size),
                      }),
                  }
                : {
                    current: visiblePage,
                    pageSize,
                    total: data.length,
                    showSizeChanger: true,
                    pageSizeOptions: [20, 50, 100],
                    onChange: (page, size) =>
                      update({
                        page: String(size === pageSize ? page : 1),
                        pageSize: String(size),
                      }),
                  }
            }
          />
        </QueryPanel>
      </Card>
      {footer && <div className="mt">{footer}</div>}
    </>
  );
}
export function Unavailable({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return <Alert type="info" showIcon title={title} description={description} />;
}
