import {fireEvent,render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {App as AntApp,ConfigProvider} from "antd";
import {expect,it,vi} from "vitest";
import {QueryClient,QueryClientProvider} from "@tanstack/react-query";
import {createMemoryRouter,RouterProvider} from "react-router-dom";
import {DialogHost} from "../src/core/dialogs";
import {AuthProvider} from "../src/core/auth";
import {GiftCardsPage} from "../src/features/admin/GiftCards";

it("exports the selected gift status and batch through the actual dialog",async()=>{
 localStorage.setItem("aegis_admin_token","fixture-token");let exported="";let downloaded:Blob|undefined;
 vi.stubGlobal("fetch",vi.fn(async(input:string)=>{
  const url=new URL(input);if(url.pathname.endsWith("/codes/export")){exported=url.toString();return new Response("卡密,状态\r\nABC,unused\r\n",{status:200});}
  return new Response(JSON.stringify(url.pathname.endsWith("/v1/me")?{user_id:"admin",permissions:["marketing.giftcard.read"]}:{templates:[],codes:[],total:0}),{status:200});
 }));
 Object.defineProperty(URL,"createObjectURL",{configurable:true,value:vi.fn((blob:Blob)=>{downloaded=blob;return "blob:fixture";})});
 Object.defineProperty(URL,"revokeObjectURL",{configurable:true,value:vi.fn()});
 const click=vi.spyOn(HTMLAnchorElement.prototype,"click").mockImplementation(()=>{});
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const router=createMemoryRouter([{path:"/gift-cards",element:<GiftCardsPage/>}],{initialEntries:["/gift-cards?status=unused"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 fireEvent.click(await screen.findByRole("tab",{name:"卡密管理"}));
 fireEvent.click(await screen.findByRole("button",{name:"导出 CSV"}));
 await userEvent.type(await screen.findByLabelText("生成批次 ID（可选）"),"12345678-1234-4234-8234-123456789012");
 fireEvent.click(screen.getByRole("button",{name:"下载 CSV"}));
 await waitFor(()=>expect(click).toHaveBeenCalledTimes(1));
 expect(new URL(exported).searchParams.get("status")).toBe("unused");
 expect(new URL(exported).searchParams.get("batch_id")).toBe("12345678-1234-4234-8234-123456789012");
 expect(downloaded?.type).toBe("text/csv;charset=utf-8");
 const bytes=await new Promise<ArrayBuffer>((resolve,reject)=>{const reader=new FileReader();reader.onload=()=>resolve(reader.result as ArrayBuffer);reader.onerror=reject;reader.readAsArrayBuffer(downloaded!);});
 expect([...new Uint8Array(bytes).slice(0,3)]).toEqual([239,187,191]);
});
