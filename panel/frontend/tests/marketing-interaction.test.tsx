import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App as AntApp, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { DialogHost } from "../src/core/dialogs";
import { AuthProvider } from "../src/core/auth";
import { GiftCardsPage } from "../src/features/admin/GiftCards";
import { CouponsPage } from "../src/features/admin/Coupons";
import { PluginsPage } from "../src/features/admin/Plugins";
import type { ReactNode } from "react";
function setup(page:ReactNode,permissions:string[],extra:(path:string)=>object=()=>({})){
 localStorage.setItem("aegis_admin_token","fixture-token");const writes:{path:string;body:Record<string,unknown>;key:unknown}[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:string,init?:RequestInit)=>{
  const path=new URL(input).pathname;let data:object;
  if(init?.method==="POST"){writes.push({path,body:JSON.parse(String(init.body)),key:(init.headers as Record<string,string>)["Idempotency-Key"]});data={id:"created",template:{id:"created"},saved:true};}
  else data=path.endsWith("/v1/me")?{user_id:"admin",permissions}:path.endsWith("/plans")?{plans:[]}:path.endsWith("/gift-cards")?{templates:[]}:path.endsWith("/plugin-hooks")?{hooks:[],events:[{Name:"order.paid",Desc:"订单支付成功"}]}:path.endsWith("/coupons")?{coupons:[{id:"coupon",code:"QA",name:"Fixture",status:"active",discount_value:100,discount_type:"fixed",currency:"CNY"}],total:1}:extra(path);
  return new Response(JSON.stringify(data),{status:200});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});const router=createMemoryRouter([{path:"/",element:page}]);
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);return writes;
}
it("creates a gift template with exact yuan conversion and a usable form",async()=>{
 const writes=setup(<GiftCardsPage/>,["marketing.giftcard.read","marketing.giftcard.write","catalog.read"]);
 await userEvent.click(await screen.findByRole("button",{name:"新建模板"}));
 await userEvent.type(await screen.findByLabelText("模板名称"),"新用户奖励");
 const balance=screen.getByLabelText("余额（人民币元）");await userEvent.clear(balance);await userEvent.type(balance,"9.99");
 await userEvent.click(screen.getByRole("button",{name:"保存模板"}));
 await waitFor(()=>expect(writes).toHaveLength(1));expect(writes[0]?.body).toMatchObject({type:"general",status:"active",rewards:{balance:999,traffic_bytes:0,expire_days:0}});
});
it("uses paused for coupon disabling and hides private redemption details from coupon-only admins",async()=>{
 const writes=setup(<CouponsPage/>,["marketing.coupon.write"]);
 const stop = await screen.findByRole("button",{name:"停用"});
 await act(async () => { await userEvent.click(stop); await import("../src/core/FormDialog"); });
 expect(screen.queryByRole("button",{name:"使用明细"})).not.toBeInTheDocument();
 await userEvent.click(await screen.findByRole("button",{name:/保\s*存/}));
 await waitFor(()=>expect(writes).toHaveLength(1));expect(writes[0]).toMatchObject({path:"/v1/coupons/coupon/status",body:{status:"paused"}});expect(writes[0]?.key).toBeTruthy();
});
it("opens plugin configuration with the server-provided event catalogue",async()=>{
 setup(<PluginsPage/>,["platform.plugin.read","platform.plugin.write"]);
 await userEvent.click(await screen.findByRole("button",{name:"添加插件"}));
 await waitFor(()=>expect(screen.getByLabelText("接收地址")).toBeVisible());
 await userEvent.click(screen.getByLabelText("订阅事件"));
 await waitFor(()=>expect(screen.getByText("订单支付成功")).toBeVisible());
});
