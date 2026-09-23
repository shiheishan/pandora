/**
 * [INPUT]: 依赖 core/auth、core/runtime 的 hasContract、core/dialogs、core/api 的 failure、core/operations 的 Operation、core/data、components/common、zod 与 antd 的 Modal/Form
 * [OUTPUT]: 对外提供 MailTemplates 组件
 * [POS]: features/admin 的通知模板页：列表、编辑保存、恢复默认走现行后端；草稿预览按钮受待接契约 mail-template-preview-v1 门控
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
import {useEffect,useRef,useState} from "react";
import {Alert,Button,Form,Input,Modal,Space,Tag,Typography} from "antd";
import {z} from "zod";
import {useAuth} from "../../core/auth";
import {hasContract} from "../../core/runtime";
import {useDialog} from "../../core/dialogs";
import {failure} from "../../core/api";
import {Operation} from "../../core/operations";
import {principalSubject,text,type Row} from "../../core/data";
import {ResourcePage,Status} from "../../components/common";
const previewSchema=z.object({preview_subject:z.string(),preview_body:z.string()});
function TemplateEditor({row,close}:{row:Row;close:()=>void}){
 const {api,can,principal}=useAuth();const dialog=useDialog();const [form]=Form.useForm();
 const writable=can("platform.settings.write")&&row.locale==="zh-CN";
 const [pending,setPending]=useState(false),[previewing,setPreviewing]=useState(false),[error,setError]=useState("");
 const [preview,setPreview]=useState<{preview_subject:string;preview_body:string}|null>(()=>typeof row.preview_subject==="string"&&typeof row.preview_body==="string"?{preview_subject:row.preview_subject,preview_body:row.preview_body}:null);
 const [saveOp]=useState(()=>new Operation("mail-template-save",principalSubject(principal)));
 const [resetOp]=useState(()=>new Operation("mail-template-reset",principalSubject(principal)));
 const locked=useRef(false),sequence=useRef(0),flight=useRef<AbortController|null>(null);
 useEffect(()=>()=>{sequence.current++;flight.current?.abort();},[]);
 const payload=()=>({code:row.code,channel:row.channel,expected_version:row.version,...form.getFieldsValue()});
 const save=async()=>{if(locked.current||!writable)return;locked.current=true;setPending(true);setError("");
  try{await form.validateFields();await saveOp.send(api,"v1/mail/templates",payload(),{},z.object({template:z.object({code:z.string(),version:z.number().int()})}));close();}
  catch(e){if(!(e&&typeof e==="object"&&"errorFields" in e))setError(failure(e).message);}finally{locked.current=false;setPending(false);}
 };
 const showPreview=async()=>{if(row.locale!=="zh-CN")return;const seq=++sequence.current;flight.current?.abort();const controller=new AbortController();flight.current=controller;setPreviewing(true);setError("");
  try{await form.validateFields();const result=await api.request("v1/mail/templates/preview",previewSchema,{method:"POST",body:payload(),signal:controller.signal});if(sequence.current===seq)setPreview(result);}
  catch(e){if(sequence.current===seq&&!(e&&typeof e==="object"&&"errorFields" in e))setError(failure(e).message);}finally{if(sequence.current===seq)setPreviewing(false);}
 };
 const reset=()=>dialog({title:"恢复默认模板",description:"将覆盖当前保存内容和此窗口中的草稿，恢复系统内置主题与正文。",danger:true,submitLabel:"恢复默认",onSubmit:async()=>{
  await resetOp.send(api,"v1/mail/templates/reset",{code:row.code,channel:row.channel,expected_version:row.version},{},z.object({template:z.object({code:z.string(),version:z.number().int()})}));close();
 }});
 const changed=()=>{sequence.current++;flight.current?.abort();setPreviewing(false);setPreview(null);};
 const variables=Array.isArray(row.allowed_variables)?row.allowed_variables.map(String):[];
 return <Modal open width={820} style={{top:24}} styles={{body:{maxHeight:"calc(100dvh - 190px)",overflowY:"auto",paddingRight:4}}} title={`通知模板 · ${text(row.code)}`} onCancel={()=>{if(!locked.current)close();}} mask={{closable:!pending}} footer={<Space wrap>
  {writable&&<Button disabled={pending} onClick={()=>void reset()}>恢复默认</Button>}
  {hasContract("mail-template-preview-v1")&&<Button disabled={pending||row.locale!=="zh-CN"} loading={previewing} onClick={()=>void showPreview()}>预览草稿</Button>}<Button disabled={pending} onClick={close}>关闭</Button>
  {writable&&<Button type="primary" loading={pending} onClick={()=>void save()}>保存模板</Button>}
 </Space>}>
  <p>{text(row.description,"此模板用于系统通知。")}</p><p className="secondary">{row.channel==="email"?"邮件":"站内通知"} · {text(row.locale)} · 版本 {text(row.version)} · {row.is_default?"系统默认内容":"自定义内容"}</p>
  {error&&<Alert type="error" showIcon title={error} className="mb"/>}
  {!writable&&<Alert type="info" title="当前为只读预览" className="mb"/>}
  <Form form={form} layout="vertical" initialValues={{subject:row.subject,body:row.body}} onValuesChange={changed}>
   <Form.Item name="subject" label="通知主题" rules={[{required:true,message:"请输入主题"},{max:200,message:"主题最多200字"}]}><Input maxLength={200} disabled={!writable||pending}/></Form.Item>
   <Form.Item name="body" label="通知正文" rules={[{required:true,message:"请输入正文"},{max:20000,message:"正文最多20000字"}]}><Input.TextArea rows={9} maxLength={20000} showCount disabled={!writable||pending}/></Form.Item>
  </Form>
  <p className="secondary">可用变量，点击添加到正文末尾：</p><Space wrap>{variables.map(name=><Button size="small" key={name} disabled={!writable||pending} onClick={()=>{form.setFieldValue("body",String(form.getFieldValue("body")||"")+`{{${name}}}`);changed();}}>{`{{${name}}}`}</Button>)}</Space>
  {preview&&<div className="mt"><h3>示例预览</h3><Typography.Text strong>{preview.preview_subject}</Typography.Text><pre style={{whiteSpace:"pre-wrap",overflowWrap:"anywhere",fontFamily:"inherit"}}>{preview.preview_body}</pre><p className="secondary">仅使用示例数据，没有保存草稿或发送通知。</p></div>}
 </Modal>;
}
export function MailTemplates(){const [editing,setEditing]=useState<Row|null>(null);return <>
 <ResourcePage title="通知模板" description="管理邮件和站内通知的主题、正文与变量。模板代码对应既有业务触发条件。" resource="mail-templates" path="v1/mail/templates" listKey="templates" columns={[
  {title:"模板",render:(_,row)=><><strong>{text(row.code)}</strong><div className="secondary">{text(row.description,"")}</div></>},
  {title:"渠道",render:(_,row)=><Tag>{row.channel==="email"?"邮件":"站内通知"}</Tag>},{title:"主题",dataIndex:"subject"},{title:"状态",dataIndex:"status",render:value=><Status value={value}/>},{title:"版本",dataIndex:"version"},{title:"操作",render:(_,row)=><Button type="link" onClick={()=>setEditing(row)}>查看 / 编辑</Button>}
 ]}/>{editing&&<TemplateEditor row={editing} close={()=>setEditing(null)}/>}</>;
}
