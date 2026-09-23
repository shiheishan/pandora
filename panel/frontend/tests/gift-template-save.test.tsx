import {fireEvent,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {expect,it,vi} from "vitest";
import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {createMemoryRouter,RouterProvider} from "react-router-dom";
import {DialogHost} from "../src/core/dialogs";
import {AuthProvider} from "../src/core/auth";
import {GiftCardsPage} from "../src/features/admin/GiftCards";

it("retries an uncertain gift template save with the same operation and exact rewards",async()=>{
 localStorage.setItem("aegis_admin_token","fixture-token");
 const attempts:RequestInit[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:string,options?:RequestInit)=>{
  const path=new URL(input).pathname;
  if(path.endsWith("/gift-cards")&&options?.method==="POST"){
   attempts.push(options);if(attempts.length===1)throw new TypeError("fixture lost response");
   return new Response(JSON.stringify({template:{id:"gift-a"}}),{status:200});
  }
  const data=path.endsWith("/v1/me")?{user_id:"admin",permissions:["marketing.giftcard.read","marketing.giftcard.write","catalog.read"]}:path.endsWith("/plans")?{plans:[]}:{templates:[],codes_total:0,codes_used:0,codes_unused:0,balance_out:0,traffic_out:0};
  return new Response(JSON.stringify(data),{status:200});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const router=createMemoryRouter([{path:"/gift-cards",element:<GiftCardsPage/>}],{initialEntries:["/gift-cards"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 await userEvent.click(await screen.findByRole("button",{name:"新建模板"}));
 await userEvent.type(await screen.findByLabelText("模板名称"),"测试权益模板");
 const balance=screen.getByLabelText("余额（人民币元）");await userEvent.clear(balance);await userEvent.type(balance,"0.29");
 await userEvent.click(screen.getByRole("button",{name:"保存模板"}));
 await waitFor(()=>expect(attempts).toHaveLength(1));
 await waitFor(()=>expect(screen.getByRole("button",{name:"保存模板"})).toBeEnabled());
 expect(screen.getByLabelText("模板名称")).toHaveValue("测试权益模板");
 await userEvent.click(screen.getByRole("button",{name:"保存模板"}));
 await waitFor(()=>expect(attempts).toHaveLength(2));
 const [first,second]=attempts;if(!first||!second)throw new Error("missing retry attempts");
 expect(second.body).toBe(first.body);
 expect((second.headers as Record<string,string>)["Idempotency-Key"]).toBe((first.headers as Record<string,string>)["Idempotency-Key"]);
 expect(JSON.parse(String(second.body))).toMatchObject({name:"测试权益模板",type:"general",rewards:{balance:29,traffic_bytes:0,expire_days:0}});
 await waitFor(()=>expect(screen.queryByRole("button",{name:"保存模板"})).not.toBeInTheDocument());
});

it("preserves existing sub-MiB traffic and large money through the actual edit form",async()=>{
 localStorage.setItem("aegis_admin_token","fixture-token");
 const attempts:RequestInit[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:string,options?:RequestInit)=>{
  const path=new URL(input).pathname;
  if(path.endsWith("/gift-cards")&&options?.method==="POST"){
   attempts.push(options);if(attempts.length===1)throw new TypeError("fixture lost response");
   return new Response(JSON.stringify({template:{id:"gift-a"}}),{status:200});
  }
  const data=path.endsWith("/v1/me")?{user_id:"admin",permissions:["marketing.giftcard.read","marketing.giftcard.write","catalog.read"]}:path.endsWith("/plans")?{plans:[]}:{templates:[{id:"gift-a",name:"Existing gift",type:"general",status:"active",rewards:{balance:9007199254740991,traffic_bytes:1}}],codes_total:0,codes_used:0,codes_unused:0,balance_out:0,traffic_out:0};
  return new Response(JSON.stringify(data),{status:200});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const router=createMemoryRouter([{path:"/gift-cards",element:<GiftCardsPage/>}],{initialEntries:["/gift-cards"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 await screen.findByText("Existing gift");fireEvent.click(screen.getByText("编辑").closest("button")!);
 expect(await screen.findByLabelText("模板名称")).toHaveValue("Existing gift");
 expect(screen.getByLabelText("余额（人民币元）")).toHaveValue("90071992547409.91");
 fireEvent.click(screen.getByText("保存模板").closest("button")!);
 await waitFor(()=>expect(attempts).toHaveLength(1));
 await waitFor(()=>expect(screen.getByRole("button",{name:"保存模板"})).toBeEnabled());
 expect(screen.getByLabelText("模板名称")).toHaveValue("Existing gift");
 fireEvent.click(screen.getByText("保存模板").closest("button")!);
 await waitFor(()=>expect(attempts).toHaveLength(2));
 const [first,second]=attempts;if(!first||!second)throw new Error("missing retry attempts");
 expect(second.body).toBe(first.body);
 expect((second.headers as Record<string,string>)["Idempotency-Key"]).toBe((first.headers as Record<string,string>)["Idempotency-Key"]);
 expect(JSON.parse(String(second.body))).toMatchObject({name:"Existing gift",type:"general",rewards:{balance:9007199254740991,traffic_bytes:1,expire_days:0}});
 await waitFor(()=>expect(screen.queryByRole("button",{name:"保存模板"})).not.toBeInTheDocument());
});
