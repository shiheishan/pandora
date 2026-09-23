/**
 * [INPUT]: 依赖 core/auth、core/runtime 的 hasContract、core/dialogs、core/operations 的 Operation、core/data、core/numbers、components/common、zod
 * [OUTPUT]: 对外提供 CouponsPage 组件与 couponPayload / couponInitial
 * [POS]: features/admin 的优惠券页：创建、批量生成、启停、使用明细走现行后端；编辑按钮受待接契约 coupon-edit-v1 门控
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import { Button, Space, Typography } from "antd";
import { z } from "zod";
import { useAuth } from "../../core/auth";
import { hasContract } from "../../core/runtime";
import { useDialog, type FormField } from "../../core/dialogs";
import { Operation } from "../../core/operations";
import { dateText, enc, idOf, principalSubject, rows, text, type Row } from "../../core/data";
import { asBigInt, legacyInteger, money, parseMinor } from "../../core/numbers";
import { DataTable, QueryPanel, ResourcePage, Status, useData } from "../../components/common";

export function couponPayload(values: Row, batch: boolean): Row {
 const percent=values.discount_type === "percent";
 const amount=legacyInteger(parseMinor(String(values.discount_input ?? ""),{zero:false}));
 if(percent && amount>10000) throw new Error("减免百分比不能超过100%");
 const optionalAmount=(v:unknown)=>v==null||v===""?null:legacyInteger(parseMinor(String(v)));
 const date=(v:unknown)=>v?new Date(String(v)).toISOString():"";
 const valid_from=date(values.valid_from),valid_until=date(values.valid_until);
 if(valid_from && valid_until && valid_until<=valid_from) throw new Error("结束时间必须晚于开始时间");
 return {name:values.name,discount_type:values.discount_type,discount_value:amount,currency:values.currency || "CNY",
 min_order_amount:optionalAmount(values.min_order_amount) ?? 0,max_discount:optionalAmount(values.max_discount),
 max_redemptions:values.max_redemptions || null,max_redemptions_per_user:values.max_redemptions_per_user || 1,
 applicable_plan_ids:values.applicable_plan_ids || [],valid_from,valid_until,
 ...(batch?{count:values.count,prefix:values.prefix || ""}:{code:values.code})};
}
export function couponInitial(row:Row):Row {
 const decimal=(value:unknown)=>{const n=asBigInt(value);return n===null?"":`${n/100n}.${(n%100n).toString().padStart(2,"0")}`;};
 const local=(value:unknown)=>{if(!value)return "";const date=new Date(String(value));if(!Number.isFinite(date.getTime()))throw new Error("优惠券时间格式不正确，请刷新后重试");return new Date(date.getTime()-date.getTimezoneOffset()*60000).toISOString().slice(0,-1);};
 return {...row,discount_input:decimal(row.discount_value),min_order_amount:decimal(row.min_order_amount),max_discount:decimal(row.max_discount),valid_from:local(row.valid_from),valid_until:local(row.valid_until)};
}
function Redemptions({id}:{id:string}) {
 const query=useData("coupon-redemptions",`v1/coupons/${enc(id)}/redemptions`);
 return <QueryPanel query={query}><DataTable data={rows(query.data,"redemptions")} columns={[
 {title:"用户",dataIndex:"email"},{title:"订单",dataIndex:"order_no"},
 {title:"优惠金额",render:(_,r)=>money(r.discount,r.currency)},{title:"使用时间",dataIndex:"at",render:dateText},
 {title:"已回退",render:(_,r)=>r.reverted?"是":"否"},]}/><p className="secondary">显示最近200条使用记录。</p></QueryPanel>;
}
export function CouponsPage() {
 const {api,can,principal}=useAuth();const dialog=useDialog();
 const plans=useData("plans","v1/plans",can("catalog.read"));
 const create=(batch:boolean,existing?:Row)=>{
  const op=new Operation(existing?"coupon-update":batch?"coupon-batch":"coupon-create",principalSubject(principal));
  const fields:FormField[]=[
   {name:"name",label:"活动名称",required:true,placeholder:"例如：秋季新用户优惠"},
   ...(batch?[{name:"prefix",label:"券码前缀",placeholder:"可选，仅使用英文字母和数字"},{name:"count",label:"生成数量",type:"number" as const,min:1,max:1000,required:true}]:[{name:"code",label:"优惠码",required:true,disabled:!!existing,placeholder:"例如：WELCOME"}]),
   {name:"discount_type",label:"优惠方式",type:"select",required:true,options:[{value:"fixed",label:"固定金额立减"},{value:"percent",label:"按百分比减免"}]},
   {name:"discount_input",label:"立减金额 / 减免百分比",required:true,help:"固定金额填元；百分比填减免比例，如10表示减免10%（九折），不是打一折。最多两位小数。"},
   {name:"currency",label:"币种",type:"select",options:[{value:"CNY",label:"人民币 CNY"},{value:"USD",label:"美元 USD"}]},
   {name:"min_order_amount",label:"最低订单金额（元）",help:"填0表示无最低消费。"},
   {name:"max_discount",label:"最高优惠金额（元）",help:"百分比优惠可设置封顶金额；留空不封顶。"},
   {name:"max_redemptions",label:"每个优惠码总使用次数",type:"number",min:1,help:"留空不限次数；批量生成时建议每码限用1次。"},
   {name:"max_redemptions_per_user",label:"每位用户最多使用次数",type:"number",min:1,required:true},
   {name:"applicable_plan_ids",label:"适用套餐",type:"select",multiple:true,options:rows(plans.data,"plans").map(r=>({value:idOf(r),label:text(r.name)})),help:"不选择表示适用全部套餐。"},
   {name:"valid_from",label:"开始时间",type:"datetime",help:"留空立即生效；按当前浏览器时区填写。"},
   {name:"valid_until",label:"结束时间",type:"datetime",help:"留空长期有效。"},
  ];
  return dialog({title:existing?"编辑优惠券":batch?"批量生成优惠券":"创建优惠券",width:680,fields,description:existing?"修改仅用于之后的新订单；已有订单优惠不变。已发放的优惠码保留原值。":undefined,
   initial:{discount_type:"fixed",currency:"CNY",min_order_amount:"0",max_redemptions_per_user:1,...(batch?{count:10,max_redemptions:1}:{}),...(existing?couponInitial(existing):{})},
   submitLabel:existing?"保存修改":batch?"生成优惠码":"创建优惠券",
   onSubmit:async values=>{
    const result=await op.send(api,existing?`v1/coupons/${enc(idOf(existing))}`:batch?"v1/coupons/batch":"v1/coupons",{...couponPayload(values,batch),...(existing?{code:existing.code,expected_updated_at:existing.updated_at}:{})},{},batch?z.object({codes:z.array(z.string()),count:z.number()}):z.object({id:z.string().min(1)}));
    if(batch && Array.isArray(result.codes)) void dialog({title:"优惠码已生成",description:<><p>共 {result.codes.length} 个，可复制保存；也可在列表中搜索活动名称。</p><Typography.Paragraph copyable style={{maxHeight:300,overflow:"auto",whiteSpace:"pre-wrap"}}>{result.codes.join("\n")}</Typography.Paragraph></>,submitLabel:"完成"});
   }});
 };
 const setStatus=(row:Row)=>{
  const op=new Operation("coupon-status",principalSubject(principal));
  return dialog({title:row.status==="active"?"停用优惠券":"启用优惠券",description:`优惠码：${text(row.code)}。停用不会撤销已经完成的优惠。`,onSubmit:async()=>{await op.send(api,`v1/coupons/${enc(idOf(row))}/status`,{status:row.status==="active"?"paused":"active"});}});
 };
 return <ResourcePage title="优惠券" description="管理优惠码、使用限制和有效期，金额默认使用人民币。" resource="coupons" path="v1/coupons" listKey="coupons" serverPagination searchable statuses={[{value:"active",label:"启用"},{value:"paused",label:"停用"}]} extra={can("marketing.coupon.write")&&<Space wrap><Button onClick={()=>void create(true)}>批量生成</Button><Button type="primary" onClick={()=>void create(false)}>创建优惠券</Button></Space>} columns={[
 {title:"优惠码 / 活动",render:(_,r)=><><Typography.Text copyable>{text(r.code)}</Typography.Text><div className="secondary small">{text(r.name)}</div></>},
 {title:"优惠",render:(_,r)=>r.discount_type==="percent"?`减免 ${Number(r.discount_value)/100}%`:money(r.discount_value,r.currency)},
 {title:"门槛 / 封顶",render:(_,r)=><>{money(r.min_order_amount,r.currency)} 起<div className="secondary small">{r.max_discount==null?"不封顶":`最高 ${money(r.max_discount,r.currency)}`}</div></>},
 {title:"使用情况",render:(_,r)=><>{text(r.redeemed_count)} / {r.max_redemptions==null?"不限":text(r.max_redemptions)}<div className="secondary small">待支付占用 {text(r.reserved_count)} · 已结算优惠 {money(r.discounted_total,r.currency)}</div></>},
 {title:"结束时间",render:(_,r)=>r.valid_until?dateText(r.valid_until):"长期有效"},
 {title:"状态",dataIndex:"status",render:v=><Status value={v}/>},
 {title:"操作",render:(_,r)=><Space>{can("marketing.coupon.write")&&hasContract("coupon-edit-v1")&&<Button type="link" onClick={()=>void create(false,r)}>编辑</Button>}{can("billing.order.read")&&<Button type="link" onClick={()=>void dialog({title:`使用明细 · ${text(r.code)}`,width:900,description:<Redemptions id={idOf(r)}/>,submitLabel:"关闭"})}>使用明细</Button>}{can("marketing.coupon.write")&&<Button type="link" onClick={()=>void setStatus(r)}>{r.status==="active"?"停用":"启用"}</Button>}</Space>},
 ]}/>;
}
