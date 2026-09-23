import { useRef, useState } from "react";
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Switch, Tabs, Typography } from "antd";
import { z } from "zod";
import { useSearchParams } from "react-router-dom";
import { useAuth } from "../../core/auth";
import { useDialog } from "../../core/dialogs";
import { failure } from "../../core/api";
import { Operation } from "../../core/operations";
import { dateText, enc, idOf, principalSubject, record, rows, text, type Row } from "../../core/data";
import { asBigInt, bytes, legacyInteger, money, parseMinor } from "../../core/numbers";
import { DataTable, QueryPanel, ResourcePage, Status, useData } from "../../components/common";

const types=[{value:"general",label:"通用权益卡"},{value:"plan",label:"套餐兑换卡"},{value:"mystery",label:"随机奖励卡"}];
export function giftRewardFields(value:Row):Row {
 const balance=asBigInt(value.balance??0),traffic=asBigInt(value.traffic_bytes??0);
 if(balance===null||traffic===null||balance<0n||traffic<0n)throw new Error("历史权益数值超出精确读取范围，请先核对原始数据");
 const fraction=((traffic%1048576n)*95367431640625n).toString().padStart(20,"0").replace(/0+$/,"");
 return {...value,balance:`${balance/100n}.${(balance%100n).toString().padStart(2,"0")}`,traffic_mib:`${traffic/1048576n}${fraction?"."+fraction:""}`};
}
export function giftTrafficBytes(value:unknown):number {
 const source=String(value??"0").trim();
 if(!/^\d+(?:\.\d{1,20})?$/.test(source))throw new Error("流量请输入非负数，最多20位小数");
 const [whole="0",fraction=""]=source.split(".");const scale=10n**BigInt(fraction.length);
 const bytes=(BigInt(whole)*scale+BigInt(fraction||"0"))*1048576n;
 if(bytes%scale!==0n)throw new Error("流量必须能精确换算为整数 Byte，请使用 MiB 整数或精确值");
 return legacyInteger((bytes/scale).toString());
}
export function giftRewards(values:Row):Row {
 const reward=(v:Row):Row=>({balance:legacyInteger(parseMinor(String(v.balance||"0"))),traffic_bytes:giftTrafficBytes(v.traffic_mib||0),expire_days:v.expire_days||0});
 if(values.type==="plan")return {plan_id:values.plan_id,price_id:values.price_id||""};
 if(values.type==="mystery")return {pool:rows(values,"pool").map(p=>({...reward(p),label:p.label,weight:p.weight}))};
 return {...reward(values),reset_quota:Boolean(values.reset_quota)};
}
function rewardSummary(value:unknown):string {
 const r=record(value);return [Number(r.balance)>0?money(r.balance):"",Number(r.traffic_bytes)>0?bytes(r.traffic_bytes):"",Number(r.expire_days)>0?`延长 ${r.expire_days} 天`:"",r.reset_quota?"重置流量":"",r.plan_id?"开通指定套餐":"",Array.isArray(r.pool)?`${r.pool.length} 项随机奖励`:""].filter(Boolean).join(" · ")||"无奖励";
}
function TemplateEditor({row,close}:{row:Row;close:()=>void}) {
 const {api,can,principal}=useAuth(); const [form]=Form.useForm(); const type=Form.useWatch("type",form)||row.type||"general";
 const planId=Form.useWatch("plan_id",form);const plans=useData("gift-plans","v1/plans",can("catalog.read"));
 const plan=useData("gift-plan",`v1/plans/${enc(planId)}`,Boolean(planId)&&can("catalog.read"));
 const [pending,setPending]=useState(false),[error,setError]=useState("");
 const r=record(row.rewards),c=record(row.conditions),l=record(row.limits);
 const editReward=giftRewardFields;
 let initial:Row={},initialError="";
 try{initial={...row,type:row.type||"general",status:row.status||"active",...editReward(r),...c,...l,pool:rows(r,"pool").map(editReward)};}catch(e){initialError=failure(e).message;}
 const [saveOperation]=useState(()=>new Operation("gift-template-save",principalSubject(principal)));
 const locked=useRef(false);
 const save=async()=>{
  if(locked.current||initialError)return;locked.current=true;
  try{const v=await form.validateFields();setPending(true);setError("");
   if(v.new_user_only&&v.paid_user_only)throw new Error("仅新用户与仅付费用户不能同时开启");
   await saveOperation.send(api,"v1/gift-cards",{id:row.id||"",name:v.name,description:v.description||"",type:v.type,status:v.status,rewards:giftRewards(v),
    conditions:{new_user_only:!!v.new_user_only,paid_user_only:!!v.paid_user_only,require_invite:!!v.require_invite,allowed_plan_ids:v.allowed_plan_ids||[]},
    limits:{max_use_per_user:v.max_use_per_user||0,cooldown_hours:v.cooldown_hours||0},theme_color:v.theme_color||""},{},z.object({template:z.object({id:z.string().min(1)})}));close();
  }catch(e){if(!(e&&typeof e==="object"&&"errorFields" in e))setError(failure(e).message);}finally{locked.current=false;setPending(false);}
 };
 const rewardInputs=(prefix:(string|number)[]=[])=> <div className="gift-reward-grid">
  <Form.Item name={[...prefix,"balance"]} label="余额（人民币元）"><Input placeholder="0.00"/></Form.Item>
  <Form.Item name={[...prefix,"traffic_mib"]} label="追加流量（MiB）" tooltip="1024 MiB = 1 GiB"><Input placeholder="0" inputMode="decimal"/></Form.Item>
  <Form.Item name={[...prefix,"expire_days"]} label="延长天数"><InputNumber min={0} precision={0} style={{width:"100%"}}/></Form.Item>
 </div>;
 return <Modal open className="compact-form-modal" style={{top:24}} styles={{body:{maxHeight:"calc(100dvh - 190px)",overflowY:"auto",paddingRight:4}}} title={row.id?"编辑礼品卡模板":"新建礼品卡模板"} width={780} onCancel={()=>{if(!locked.current)close();}} mask={{closable:!pending}} footer={<Space><Button disabled={pending} onClick={()=>{if(!locked.current)close();}}>取消</Button><Button type="primary" loading={pending} disabled={Boolean(initialError)} onClick={()=>void save()}>保存模板</Button></Space>}>
  {(error||initialError)&&<Alert type="error" showIcon title={error||initialError} className="mb"/>}
  <Form form={form} layout="vertical" initialValues={initial}>
   <Form.Item name="name" label="模板名称" rules={[{required:true,message:"请输入模板名称"}]}><Input maxLength={120}/></Form.Item>
   <div className="gift-reward-grid"><Form.Item name="type" label="卡片类型"><Select options={types} disabled={Boolean(row.id)}/></Form.Item><Form.Item name="status" label="模板状态"><Select options={[{value:"active",label:"启用"},{value:"paused",label:"暂停兑换"},{value:"archived",label:"归档"}]}/></Form.Item><Form.Item name="theme_color" label="主题颜色"><Input placeholder="#4466ee"/></Form.Item></div>
   <Form.Item name="description" label="给用户的说明"><Input.TextArea rows={2}/></Form.Item>
   {type==="general"&&<>{rewardInputs()}<Form.Item name="reset_quota" valuePropName="checked" label="重置当前周期已用流量"><Switch/></Form.Item></>}
   {type==="plan"&&<><Form.Item name="plan_id" label="赠送套餐" rules={[{required:true,message:"请选择套餐"}]}><Select onChange={()=>form.setFieldValue("price_id",undefined)} options={rows(plans.data,"plans").map(p=>({value:idOf(p),label:text(p.name)}))}/></Form.Item><Form.Item name="price_id" label="套餐价格 / 周期" extra="留空由后端选择该套餐的默认价格。"><Select allowClear options={rows(record(plan.data?.plan),"prices").map(p=>({value:idOf(p),label:`${money(p.unit_amount,p.currency)} / ${text(p.interval_count,"1")}${({day:"天",week:"周",month:"月",year:"年",one_time:"次"} as Record<string,string>)[text(p.billing_interval)]||text(p.billing_interval)}`}))}/></Form.Item></>}
   {type==="mystery"&&<><p className="secondary">每次兑换随机发放一项。权重是相对比例，例如 7 和 3 分别约为 70% 和 30%。</p><Form.List name="pool" rules={[{validator:async(_,v)=>{if(!v||v.length<2)throw new Error("至少设置两个奖品");}}]}>{(fields,{add,remove},{errors})=><>{fields.map(f=><div className="gift-prize" key={f.key}><div className="gift-reward-grid"><Form.Item name={[f.name,"label"]} label="奖品名称" rules={[{required:true}]}><Input/></Form.Item><Form.Item name={[f.name,"weight"]} label="权重" rules={[{required:true}]}><InputNumber min={1} precision={0}/></Form.Item><Button onClick={()=>remove(f.name)}>移除奖品</Button></div>{rewardInputs([f.name])}</div>)}<Form.ErrorList errors={errors}/><Button disabled={fields.length>=50} onClick={()=>add({weight:1,balance:"0",traffic_mib:0,expire_days:0})}>添加奖品</Button></>}</Form.List></>}
   <div className="gift-reward-grid"><Form.Item name="new_user_only" label="仅未付费新用户" valuePropName="checked"><Switch/></Form.Item><Form.Item name="paid_user_only" label="仅付费用户" valuePropName="checked"><Switch/></Form.Item><Form.Item name="require_invite" label="必须由邀请注册" valuePropName="checked"><Switch/></Form.Item></div>
   <Form.Item name="allowed_plan_ids" label="允许兑换的用户套餐" extra="留空不限制用户当前套餐。"><Select mode="multiple" options={rows(plans.data,"plans").map(p=>({value:idOf(p),label:text(p.name)}))}/></Form.Item>
   <div className="gift-reward-grid"><Form.Item name="max_use_per_user" label="每人最多兑换次数" extra="0 表示不限。"><InputNumber min={0} precision={0}/></Form.Item><Form.Item name="cooldown_hours" label="兑换间隔（小时）" extra="0 表示无冷却时间。"><InputNumber min={0} precision={0}/></Form.Item></div>
  </Form>
 </Modal>;
}
function GiftCodes({template}:{template?:string}) {
 const {api,can}=useAuth();const dialog=useDialog();const [params]=useSearchParams();
 const exportCodes=()=>dialog({title:"导出卡密",description:"按当前模板和状态导出，最多5000条。更多卡密请按生成批次分别导出，超限不会生成残缺文件。",fields:[{name:"batch_id",label:"生成批次 ID（可选）",help:"生成卡密后的结果中可复制批次 ID。"}],submitLabel:"下载 CSV",onSubmit:async values=>{
  const filters=new URLSearchParams();if(template)filters.set("template_id",template);if(params.get("status"))filters.set("status",params.get("status")!);
  if(values.batch_id)filters.set("batch_id",String(values.batch_id).trim());
  const csv=await api.request(`v1/gift-cards/codes/export?${filters}`,z.string(),{responseType:"text"});
  const url=URL.createObjectURL(new Blob(["\uFEFF"+csv.replace(/^\uFEFF/,"")],{type:"text/csv;charset=utf-8"}));const link=document.createElement("a");link.href=url;link.download="gift-codes.csv";document.body.appendChild(link);link.click();link.remove();setTimeout(()=>URL.revokeObjectURL(url),1000);
 }});
 return <ResourcePage title="卡密管理" extra={can("marketing.giftcard.read")&&<Button onClick={()=>void exportCodes()}>导出 CSV</Button>} resource="gift-codes" path={`v1/gift-cards/codes${template?`?template_id=${enc(template)}`:""}`} listKey="codes" serverPagination statuses={[{value:"unused",label:"未兑换"},{value:"used",label:"已兑换"},{value:"disabled",label:"停用"},{value:"expired",label:"过期"}]} columns={[
  {title:"卡密",render:(_,r)=><Typography.Text copyable>{text(r.code)}</Typography.Text>},{title:"状态",dataIndex:"status",render:v=><Status value={v}/>},{title:"有效期",render:(_,r)=>r.expires_at?dateText(r.expires_at):"长期有效"},{title:"兑换用户",dataIndex:"used_email"},{title:"兑换时间",dataIndex:"used_at",render:dateText},
  {title:"操作",render:(_,r)=>can("marketing.giftcard.write")&&["unused","disabled"].includes(text(r.status))&&<Button type="link" onClick={()=>void dialog({title:r.status==="unused"?"停用卡密":"恢复卡密",description:"仅影响该卡密，已经兑换的权益不会变动。",onSubmit:async()=>{await api.write(`v1/gift-cards/codes/${enc(idOf(r))}/toggle`,{disabled:r.status==="unused"});}})}>{r.status==="unused"?"停用":"恢复"}</Button>},
 ]}/>;
}
function GiftUsages(){const query=useData("gift-usages","v1/gift-cards/usages");return <QueryPanel query={query}><DataTable data={rows(query.data,"usages")} columns={[{title:"模板",dataIndex:"template_name"},{title:"用户",dataIndex:"user_email"},{title:"实际奖励",render:(_,r)=>rewardSummary(r.granted)},{title:"兑换时间",dataIndex:"redeemed_at",render:dateText}]}/><p className="secondary">最近 200 条兑换记录。</p></QueryPanel>;}
export function GiftCardsPage(){
 const {api,can,principal}=useAuth();const dialog=useDialog();const [editing,setEditing]=useState<Row|null>(null);const stats=useData("gift-stats","v1/gift-cards/stats");
 const generate=(row:Row)=>{const op=new Operation("gift-code-generate",principalSubject(principal));return dialog({title:`生成卡密 · ${text(row.name)}`,fields:[{name:"count",label:"数量",type:"number",min:1,max:5000,required:true},{name:"prefix",label:"前缀",help:"最多8位英文字母或数字，可留空。"},{name:"expires_at",label:"兑换截止时间",type:"datetime",help:"留空长期有效。"}],initial:{count:10},submitLabel:"生成卡密",onSubmit:async v=>{
  const result=await op.send(api,`v1/gift-cards/${enc(idOf(row))}/codes`,{count:v.count,prefix:v.prefix||"",expires_at:v.expires_at?new Date(String(v.expires_at)).toISOString():""},{},z.object({batch_id:z.string(),codes:z.array(z.string())}));
  const codes=Array.isArray(result.codes)?result.codes.map(String):[];
  void dialog({title:"卡密已生成",description:<><p>共 {codes.length} 个。复制后妥善交付，可在卡密管理查看兑换状态。</p><p>生成批次：<Typography.Text copyable>{text(result.batch_id)}</Typography.Text></p><Typography.Paragraph copyable style={{maxHeight:300,overflow:"auto",whiteSpace:"pre-wrap"}}>{codes.join("\n")}</Typography.Paragraph></>,submitLabel:"完成"});
 }});};
 return <><h1>礼品卡</h1><QueryPanel query={stats}><p className="secondary">卡密 {text(stats.data?.codes_total)} · 已兑换 {text(stats.data?.codes_used)} · 待兑换 {text(stats.data?.codes_unused)} · 已过期 {text(stats.data?.codes_expired,"0")} · 累计余额奖励 {money(stats.data?.balance_out)} · 流量奖励 {bytes(stats.data?.traffic_out)}</p></QueryPanel><Tabs destroyOnHidden items={[
 {key:"templates",label:"卡片模板",children:<ResourcePage title="卡片模板" description="配置权益和兑换条件，再生成独立卡密。" resource="gift-templates" path="v1/gift-cards" listKey="templates" extra={can("marketing.giftcard.write")&&<Button type="primary" onClick={()=>setEditing({})}>新建模板</Button>} columns={[{title:"名称",dataIndex:"name"},{title:"卡型",render:(_,r)=>types.find(t=>t.value===r.type)?.label||text(r.type)},{title:"奖励",render:(_,r)=>rewardSummary(r.rewards)},{title:"状态",dataIndex:"status",render:v=><Status value={v}/>},{title:"操作",render:(_,r)=><Space><Button type="link" onClick={()=>void dialog({title:`卡密 · ${text(r.name)}`,width:1000,description:<GiftCodes template={idOf(r)}/>,submitLabel:"关闭"})}>卡密</Button>{can("marketing.giftcard.write")&&<><Button type="link" onClick={()=>setEditing(r)}>编辑</Button>{r.status!=="archived"&&<Button type="link" onClick={()=>void generate(r)}>生成卡密</Button>}</>}</Space>}]}/>},
 {key:"codes",label:"卡密管理",children:<GiftCodes/>},{key:"usages",label:"兑换记录",children:<GiftUsages/>}]}/>{editing&&<TemplateEditor row={editing} close={()=>setEditing(null)}/>}</>;
}
