import {render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {expect,it,vi} from "vitest";
import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {createMemoryRouter,RouterProvider} from "react-router-dom";
import {DialogHost} from "../src/core/dialogs";
import {AuthProvider} from "../src/core/auth";
import {CouponsPage} from "../src/features/admin/Coupons";

// 编辑入口属于待接后端契约，测试显式打开。
document.head.insertAdjacentHTML("beforeend",'<meta name="pandora-contracts" content="coupon-edit-v1">');

// Multi-step business flow: allow cold module loading and real UI events; assertions remain unchanged.
it("keeps an uncertain coupon edit open and retries the same body and idempotency key",async()=>{
 localStorage.setItem("aegis_admin_token","fixture-token");
 const coupon={id:"coupon-a",code:"WELCOME",name:"原活动",discount_type:"fixed",discount_value:999,currency:"CNY",min_order_amount:0,max_redemptions:5,max_redemptions_per_user:1,redeemed_count:1,reserved_count:2,discounted_total:999,status:"active",updated_at:"2026-09-07T08:00:00.123456Z",applicable_plan_ids:[]};
 const attempts:RequestInit[]=[];
 vi.stubGlobal("fetch",vi.fn(async(input:string,options?:RequestInit)=>{
  const path=new URL(input).pathname;
  if(path.endsWith("/coupons/coupon-a")&&options?.method==="POST"){
   attempts.push(options);if(attempts.length===1)throw new TypeError("fixture lost response");
   return new Response(JSON.stringify({id:coupon.id,code:coupon.code}),{status:200});
  }
  const data=path.endsWith("/v1/me")?{user_id:"admin",permissions:["marketing.coupon.write","catalog.read"]}:path.endsWith("/plans")?{plans:[]}:{coupons:[coupon],total:1};
  return new Response(JSON.stringify(data),{status:200});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const router=createMemoryRouter([{path:"/coupons",element:<CouponsPage/>}],{initialEntries:["/coupons"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 await userEvent.click(await screen.findByRole("button",{name:"编辑"}));
 expect(await screen.findByLabelText("优惠码")).toBeDisabled();
 const name=screen.getByLabelText("活动名称");await userEvent.clear(name);await userEvent.type(name,"新活动");
 await userEvent.click(screen.getByRole("button",{name:"保存修改"}));
 await waitFor(()=>expect(attempts).toHaveLength(1));
 await waitFor(()=>expect(screen.getByRole("button",{name:"保存修改"})).toBeEnabled());
 expect(screen.getByLabelText("活动名称")).toHaveValue("新活动");
 await userEvent.click(screen.getByRole("button",{name:"保存修改"}));
 await waitFor(()=>expect(attempts).toHaveLength(2));
 const [first,second]=attempts;if(!first||!second)throw new Error("missing retry attempts");
 expect(second.body).toBe(first.body);
 expect((second.headers as Record<string,string>)["Idempotency-Key"]).toBe((first.headers as Record<string,string>)["Idempotency-Key"]);
 expect(JSON.parse(String(second.body))).toMatchObject({name:"新活动",code:"WELCOME",discount_value:999,max_redemptions:5,expected_updated_at:coupon.updated_at});
 await waitFor(()=>expect(screen.queryByRole("button",{name:"保存修改"})).not.toBeInTheDocument());
});
