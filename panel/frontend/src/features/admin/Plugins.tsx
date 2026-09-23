import { Button, Space, Typography } from "antd";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { dateText, enc, principalSubject, rows, text, type Row } from "../../core/data";
import { DataTable, QueryPanel, ResourcePage, Status, useData } from "../../components/common";
function Deliveries({code}:{code:string}) {const q=useData("hook-deliveries",`v1/plugin-hooks/${enc(code)}/deliveries`);return <QueryPanel query={q}><DataTable data={rows(q.data,"deliveries")} columns={[{title:"事件",dataIndex:"event"},{title:"状态",dataIndex:"status",render:v=><Status value={v}/>},{title:"尝试次数",dataIndex:"attempts"},{title:"响应码",dataIndex:"response_code"},{title:"最近错误",dataIndex:"error_message"},{title:"创建时间",dataIndex:"created_at",render:dateText}]}/><p className="secondary">最近 50 次投递。自动重试按配置的次数和退避策略执行。</p></QueryPanel>;}
export function PluginsPage(){
 const {api,can,principal}=useAuth();const dialog=useDialog();const catalog=useData("hook-catalog","v1/plugin-hooks");
 const edit=(row?:Row)=>{const op=new Operation("plugin-hook-save",principalSubject(principal));return dialog({title:row?"编辑事件插件":"添加事件插件",width:680,initial:row?{...row,secret:""}:{enabled:false,timeout_ms:5000,max_attempts:5,events:[]},fields:[
  {name:"code",label:"插件标识",required:true,disabled:!!row,help:"使用简短英文标识，创建后保持不变。"},{name:"name",label:"显示名称",required:true},{name:"description",label:"用途说明",type:"textarea"},
  {name:"endpoint_url",label:"接收地址",required:true,placeholder:"https://example.com/webhook",help:"填写插件服务的 HTTPS 地址。生产环境拒绝内网和云元数据地址。"},
  {name:"events",label:"订阅事件",type:"select",multiple:true,required:true,options:rows(catalog.data,"events").map(e=>({value:text(e.Name||e.name),label:text(e.Desc||e.desc)}))},
  {name:"secret",label:"签名密钥",type:"password",help:row?"留空保留现有密钥；填写新值将替换。":"留空自动生成，保存成功后会显示供复制。"},
  {name:"timeout_ms",label:"请求超时（毫秒）",type:"number",min:100,max:30000,required:true},{name:"max_attempts",label:"最多投递次数",type:"number",min:1,max:20,required:true},{name:"enabled",label:"启用事件投递",type:"switch"}
 ],onSubmit:async v=>{const result=await op.send(api,"v1/plugin-hooks",v);if(result.secret)void dialog({title:"保存签名密钥",description:<><p>请将密钥填写到接收服务，关闭后不会继续显示。</p><Typography.Paragraph copyable>{text(result.secret)}</Typography.Paragraph></>,submitLabel:"已保存"});}});};
 return <ResourcePage title="插件管理" description="通过签名事件通知连接外部服务，查看投递状态和错误。" resource="plugin-hooks" path="v1/plugin-hooks" listKey="hooks" extra={can("platform.plugin.write")&&<Button type="primary" onClick={()=>void edit()}>添加插件</Button>} columns={[
 {title:"名称",dataIndex:"name"},{title:"接收地址",dataIndex:"endpoint_url",ellipsis:true},{title:"事件数",render:(_,r)=>Array.isArray(r.events)?r.events.length:0},{title:"启用",render:(_,r)=>r.enabled?"是":"否"},{title:"队列 / 近7日失败",render:(_,r)=>`${text(r.queued_count)} / ${text(r.failed_count)}`},{title:"最近成功",dataIndex:"last_sent_at"},
 {title:"操作",render:(_,r)=><Space><Button type="link" onClick={()=>void dialog({title:`投递记录 · ${text(r.name)}`,description:<Deliveries code={text(r.code)}/>,width:1000,submitLabel:"关闭"})}>投递记录</Button>{can("platform.plugin.write")&&<><Button type="link" onClick={()=>void edit(r)}>编辑</Button><Button type="link" onClick={()=>void dialog({title:"发送测试事件",description:`将向 ${text(r.endpoint_url)} 发送一次测试请求。`,submitLabel:"发送测试",onSubmit:async()=>{await api.write(`v1/plugin-hooks/${enc(r.code)}/test`,{});}})}>测试</Button></>}</Space>}
 ]}/>;
}
