import {fireEvent,render,screen,waitFor} from "@testing-library/react";
import {App as AntApp,ConfigProvider} from "antd";
import {expect,it,vi} from "vitest";
import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {createMemoryRouter,RouterProvider} from "react-router-dom";
import {DialogHost} from "../src/core/dialogs";
import {AuthProvider} from "../src/core/auth";
import {MailTemplates} from "../src/features/admin/MailTemplates";

// 预览入口属于待接后端契约，测试显式打开。
document.head.insertAdjacentHTML("beforeend",'<meta name="pandora-contracts" content="mail-template-preview-v1">');

it.each(["save","reset"])("mail template %s preserves version and operation after a lost response",async(mode)=>{
 localStorage.setItem("aegis_admin_token","fixture-token");const writes:RequestInit[]=[];
 const row={code:"quota.warning",channel:"email",locale:"zh-CN",version:7,subject:"Hello {{site}}",body:"For {{plan}}",allowed_variables:["site","plan"],status:"active",description:"流量预警",is_default:false};
 vi.stubGlobal("fetch",vi.fn(async(input:string,options?:RequestInit)=>{
  const path=new URL(input).pathname;
  if(path.endsWith("/preview"))return new Response(JSON.stringify({preview_subject:"Hello 潘多拉面板",preview_body:"For 旗舰套餐"}));
  if(options?.method==="POST"){
   writes.push(options);if(writes.length===1)throw new TypeError("lost template response");
   return new Response(JSON.stringify({template:{...row,version:8},preview_subject:"Saved",preview_body:"Saved"}));
  }
  return new Response(JSON.stringify(path.endsWith("/v1/me")?{user_id:"admin",permissions:["ops.notification.read","platform.settings.write"]}:{templates:[row]}));
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});const router=createMemoryRouter([{path:"/mail",element:<MailTemplates/>}],{initialEntries:["/mail"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 fireEvent.click(await screen.findByRole("button",{name:"查看 / 编辑"}));
 expect(await screen.findByLabelText("通知主题")).toHaveValue("Hello {{site}}");
 fireEvent.click(screen.getByRole("button",{name:"预览草稿"}));await screen.findByText("Hello 潘多拉面板");expect(writes).toHaveLength(0);
 if(mode==="save"){
  fireEvent.click(screen.getByRole("button",{name:"保存模板"}));await waitFor(()=>expect(writes).toHaveLength(1));await waitFor(()=>expect(screen.getByRole("button",{name:"保存模板"})).toBeEnabled());
  expect(screen.getByLabelText("通知正文")).toHaveValue("For {{plan}}");fireEvent.click(screen.getByRole("button",{name:"保存模板"}));
 }else{
  fireEvent.click(screen.getByRole("button",{name:"恢复默认"}));
  const confirm=()=>screen.getAllByRole("button",{name:"恢复默认"}).at(-1)!;
  fireEvent.click(await waitFor(()=>{expect(screen.getAllByRole("button",{name:"恢复默认"})).toHaveLength(2);return confirm();}));
  await waitFor(()=>expect(writes).toHaveLength(1));await waitFor(()=>expect(confirm()).toBeEnabled());fireEvent.click(confirm());
 }
 await waitFor(()=>expect(writes).toHaveLength(2));const [a,b]=writes;if(!a||!b)throw new Error("missing attempts");
 expect(a.body).toBe(b.body);expect((a.headers as Record<string,string>)["Idempotency-Key"]).toBe((b.headers as Record<string,string>)["Idempotency-Key"]);
 expect(JSON.parse(String(a.body))).toMatchObject({code:"quota.warning",channel:"email",expected_version:7});
 await waitFor(()=>expect(screen.queryByLabelText("通知正文")).not.toBeInTheDocument());
});
