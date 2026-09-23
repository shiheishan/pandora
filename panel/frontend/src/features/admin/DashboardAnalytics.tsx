import { useState } from "react";
import { Alert, Card, Empty, Select, Space } from "antd";
import { Link } from "react-router-dom";
import { QueryPanel, useData } from "../../components/common";
import { rows, record, text, dateText, type Row } from "../../core/data";
import { asBigInt, money, bytes } from "../../core/numbers";

export function RevenueChart({ points, currency, field }: { points: Row[]; currency: string; field: string }) {
  const [selected, setSelected] = useState(0);
  if (!points.length) return <Empty description="所选时间范围暂无收入数据" />;
  const values = points.map(point => asBigInt(point[field]));
  if (values.some(value => value === null)) return <Alert type="warning" title="部分金额无法准确解析，请查看收入明细" />;
  const amounts = values as bigint[];
  const min = amounts.reduce((a, b) => a < b ? a : b, 0n);
  const max = amounts.reduce((a, b) => a > b ? a : b, 0n);
  const span = max - min || 1n;
  const y = (value: bigint) => 230 - Number((value - min) * 19000n / span) / 100;
  const x = (index: number) => 95 + index * 865 / Math.max(1, points.length - 1);
  const index = Math.min(selected, points.length - 1);
  const point = points[index]!;
  const total = amounts.reduce((a, b) => a + b, 0n);
  return <>
    <div className="analytics-summary"><div><span>所选周期合计</span><strong>{money(total, currency)}</strong></div>
      <div><span>{text(points[0]?.date)} 至 {text(points.at(-1)?.date)}</span><p>按站点时区统计 · 金额按币种独立计算</p></div></div>
    <div className="revenue-chart"><svg viewBox="0 0 1000 275" role="img" aria-label="每日收入折线图">
      {[0, 1, 2, 3, 4].map(i => { const v = min + span * BigInt(i) / 4n; return <g key={i}><line x1="95" x2="960" y1={y(v)} y2={y(v)} stroke="#e8edf4" strokeDasharray="4 5" /><text x="86" y={y(v) + 4} textAnchor="end" fontSize="11" fill="#768398">{money(v, currency)}</text></g>; })}
      <path d={`M ${x(0)} ${y(0n)} L ${amounts.map((v, i) => `${x(i)} ${y(v)}`).join(" L ")} L ${x(points.length - 1)} ${y(0n)} Z`} fill="#4263df0d" />
      <polyline points={amounts.map((v, i) => `${x(i)},${y(v)}`).join(" ")} fill="none" stroke="#4263df" strokeWidth="2.5" />
      {amounts.map((v, i) => <g key={i}>
        <circle cx={x(i)} cy={y(v)} r={i === index ? 5 : 3} fill="#4263df" />
        <rect x={x(i) - 865 / Math.max(1, amounts.length - 1) / 2} y="20" width={865 / Math.max(1, amounts.length - 1)} height="220" fill="transparent" onMouseEnter={() => setSelected(i)} onClick={() => setSelected(i)} />
        {(i === 0 || i === amounts.length - 1 || i % Math.ceil(amounts.length / 7) === 0) && <text x={x(i)} y="258" fontSize="11" textAnchor="middle" fill="#768398">{text(points[i]?.date).slice(5)}</text>}
      </g>)}
    </svg></div>
    <div className="analytics-inspector"><Select aria-label="查看某日收入" value={index} onChange={setSelected} options={points.map((p, i) => ({ value: i, label: text(p.date) }))} />
      <span>入账 {money(point.actual_credit, currency)}</span><span>冲减 {money(point.actual_debit, currency)}</span><span>报表调整 {money(point.adjustment, currency)}</span><strong>展示净额 {money(point.displayed_net, currency)}</strong></div>
  </>;
}
export function RevenueAnalytics() {
  const [days, setDays] = useState(30); const [currency, setCurrency] = useState("CNY"); const [field, setField] = useState("displayed_net");
  const query = useData("revenue-trend", `v1/revenue/timeseries?days=${days}&currency=${currency}`);
  return <Card className="mb analytics-card" title="收入趋势" extra={<Space wrap>
    <Select aria-label="收入币种" value={currency} onChange={setCurrency} options={[{ value: "CNY", label: "人民币 CNY" }, { value: "USD", label: "美元 USD" }]} />
    <Select aria-label="收入统计周期" value={days} onChange={setDays} options={[7, 30, 90].map(value => ({ value, label: `最近${value}天` }))} />
    <Select aria-label="收入口径" value={field} onChange={setField} options={[{ value: "displayed_net", label: "展示净额" }, { value: "actual_credit", label: "实际入账" }, { value: "actual_debit", label: "实际冲减" }]} />
  </Space>}><p className="metric-note">展示净额 = 实际入账 − 实际冲减 + 报表调整。调整不代表新增收款。</p>
    <QueryPanel query={query}><RevenueChart key={`${days}:${currency}:${field}`} points={rows(query.data, "points")} currency={currency} field={field} /></QueryPanel></Card>;
}
export function TrafficRanking({ kind }: { kind: "nodes" | "users" }) {
  const [range, setRange] = useState("24h");
  const query = useData(`traffic-ranking-${kind}`, `v1/dashboard/traffic/${kind}?range=${range}&limit=5`);
  const items = rows(query.data, "items");
  const totals = record(query.data?.totals); const quality = record(query.data?.quality);
  const max = items.reduce((a, item) => { const n = asBigInt(item.total_bytes) || 0n; return n > a ? n : a; }, 0n);
  return <Card className="analytics-card" title={kind === "nodes" ? "节点流量排行" : "用户流量排行"} extra={<Select aria-label={`${kind}流量周期`} value={range} onChange={setRange} options={[{ value: "24h", label: "最近24小时" }, { value: "7d", label: "最近7天" }, { value: "30d", label: "最近30天" }]} />}>
    <QueryPanel query={query}>
      <p className="metric-note">上报流量，不等同于已结算用量 · 前 5 名</p>
      {!items.length ? <Empty description="所选时间范围暂无流量上报" /> : items.map((item, i) => {
        const amount = asBigInt(item.total_bytes); const ratio = amount !== null && amount >= 0n && max > 0n ? Number(amount * 10000n / max) / 100 : 0;
        return <div className="traffic-rank" key={text(item.node_id || item.user_id)}>
          <div className="traffic-rank-title"><Link to={`/${kind}/${encodeURIComponent(text(item.node_id || item.user_id))}`}>{i + 1}. {text(kind === "nodes" ? item.display_name || item.name : item.email_masked)}</Link><strong>{bytes(item.total_bytes)}</strong></div>
          <div className="traffic-rank-track"><span style={{ width: `${ratio}%` }} /></div>
          <div className="metric-note">上传 {bytes(item.upload_bytes)} · 下载 {bytes(item.download_bytes)}</div>
        </div>;
      })}
      <p className="metric-note">已归属 {bytes(totals.attributed_bytes)} · 未归属 {bytes(totals.unattributed_bytes)}<br />统计更新：{dateText(query.data?.snapshot_at)}</p>
      {Object.values(quality).some(v => (asBigInt(v) || 0n) > 0n) && <Alert type="warning" title="上报存在重复或无效记录，排行不能代替计费核对" />}
    </QueryPanel>
  </Card>;
}
