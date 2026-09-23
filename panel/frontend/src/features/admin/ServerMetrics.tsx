import { useState } from "react";
import { Card, Empty, Grid, Select, Space } from "antd";
import { QueryPanel, useData } from "../../components/common";
import { dateText, enc, rows, record } from "../../core/data";
import { bytes } from "../../core/numbers";
export function ServerMetrics({nodeId}:{nodeId:string}) {
 const screens=Grid.useBreakpoint(); const chartWidth=screens.md ? 600 : 360;
 const [minutes,setMinutes]=useState(360);
 const query=useData("metrics",`v1/nodes/${enc(nodeId)}/metrics?minutes=${minutes}`,Boolean(nodeId));
 const points=rows(query.data,"points");const last=record(points.at(-1));
 const [selection,setSelection]=useState<{nodeId:string;at:string}|null>(null);
 const found=selection?.nodeId===nodeId ? points.findIndex(point=>point.at===selection.at) : -1;
 const index=found>=0 ? found : points.length-1;
 const selected=record(points[index]);
 const percentage=(value:unknown)=>typeof value==="number"&&Number.isFinite(value)?`${value.toFixed(1)}%`:"—";
 const series=[{key:"cpu_percent",label:"CPU",color:"#4263df"},{key:"mem_percent",label:"内存",color:"#d99013"}];
 return <Card className="mb analytics-card server-history-card" title="负载趋势" extra={<Select aria-label="负载时间范围" value={minutes} onChange={value=>{setMinutes(value);setSelection(null);}} options={[60,360,720,1440].map(value=>({value,label:`${value/60} 小时`}))}/>}>
 {!nodeId ? <Empty description="接入探针后显示负载趋势"/> : <QueryPanel query={query}>
 {!points.length ? <Empty description="所选时段暂无负载采样"/> : <>
 <Space className="server-chart-legend" wrap>{series.map(s=><span key={s.key} style={{color:s.color}}>● {s.label}</span>)}<span className="secondary">百分比 · 最近采样 {dateText(last.at)}</span></Space>
 <svg viewBox={`0 0 ${chartWidth} 235`} preserveAspectRatio="none" style={{width:"100%",height:180,display:"block"}} role="img" aria-label="CPU和内存历史负载">
 {[0,25,50,75,100].map(v=><g key={v}><line x1="48" x2={chartWidth-12} y1={205-v*1.8} y2={205-v*1.8} stroke="#edf0f5"/><text x="2" y={209-v*1.8} fontSize="14" fill="#7c8799">{v}%</text></g>)}
 {series.map(s=><polyline key={s.key} fill="none" stroke={s.color} strokeWidth="2" points={points.map((p,i)=>`${48+i*(chartWidth-60)/Math.max(1,points.length-1)},${205-Math.max(0,Math.min(100,Number(p[s.key])||0))*1.8}`).join(" ")}/>)}
 </svg>
 <Space className="server-chart-sample" wrap><Select aria-label="负载采样时间" value={String(index)} onChange={v=>setSelection({nodeId,at:String(points[Number(v)]?.at || "")})} options={points.map((p,i)=>({value:String(i),label:dateText(p.at)}))} style={{width:205}}/>
 <span>CPU {percentage(selected.cpu_percent)}</span><span>内存 {percentage(selected.mem_percent)}</span><span>↓ {bytes(selected.rx_speed)}/s</span><span>↑ {bytes(selected.tx_speed)}/s</span></Space>
 </>}
 </QueryPanel>}
 </Card>;
}
