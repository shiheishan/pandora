import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect,it,vi } from "vitest";
import { ApiFailure } from "../src/core/api";
import { DialogHost } from "../src/core/dialogs";
import { RouteGroupsPage } from "../src/features/admin/RouteGroups";

const api=vi.hoisted(()=>({request:vi.fn(),write:vi.fn()}));
vi.mock("../src/core/auth",()=>({useAuth:()=>({api,scope:"fixture",principal:{id:"fixture"},can:()=>true})}));
function mount(edit=false){
 api.request.mockReset().mockResolvedValue({route_groups:edit?[{id:"group",group_no:1,remarks:"原规则",match:[".example.com"],action:"block",action_value:"",row_version:3,node_count:2}]:[]});
 api.write.mockReset().mockResolvedValue({id:"group",row_version:4});
 const router=createMemoryRouter([{path:"/routing",element:<DialogHost><RouteGroupsPage/></DialogHost>}],{initialEntries:["/routing"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><RouterProvider router={router}/></QueryClientProvider></ConfigProvider>);
}
it("edits shared group rules and only exposes the forwarding tag for forwarding",async()=>{
 mount(true);await screen.findByText("原规则");await userEvent.click(screen.getByRole("button",{name:"编辑"}));
 expect(screen.queryByLabelText("转发标签（OUTBOUND TAG）")).not.toBeInTheDocument();
 await userEvent.click(screen.getByLabelText("动作"));await userEvent.click(await screen.findByText("转发"));
 await userEvent.type(screen.getByLabelText("转发标签（OUTBOUND TAG）"),"exit-a");
 await userEvent.click(screen.getByRole("button",{name:/确\s*认/}));
 await waitFor(()=>expect(api.write).toHaveBeenCalled());
 expect(api.write.mock.calls[0]?.[0]).toBe("v1/route-groups/group");
 expect(api.write.mock.calls[0]?.[1]).toEqual({remarks:"原规则",match:[".example.com"],action:"proxy",action_value:"exit-a",row_version:3});
});
it("freezes uncertain creation and retries the same payload and idempotency key",async()=>{
 mount();api.write.mockRejectedValueOnce(new ApiFailure("请求超时，结果未确认","timeout"));
 await userEvent.click(screen.getByRole("button",{name:"添加路由"}));
 await userEvent.type(screen.getByLabelText("备注"),"新规则");await userEvent.type(screen.getByLabelText("匹配规则"),".example.com");
 await userEvent.click(screen.getByRole("button",{name:/确\s*认/}));
 await screen.findByText("请求超时，结果未确认");expect(screen.getByLabelText("备注")).toBeDisabled();
 await userEvent.click(screen.getByRole("button",{name:"重试确认结果"}));
 await waitFor(()=>expect(api.write).toHaveBeenCalledTimes(2));
 expect(api.write.mock.calls[1]).toEqual(api.write.mock.calls[0]);
 expect(api.write.mock.calls[0]?.[2].idempotencyKey).toMatch(/^[a-f0-9-]{36}$/);
});
it("saves a dedicated DNS server without turning it into a forwarding tag",async()=>{
 mount();await userEvent.click(screen.getByRole("button",{name:"添加路由"}));
 await userEvent.type(screen.getByLabelText("备注"),"DNS规则");await userEvent.type(screen.getByLabelText("匹配规则"),".example.net");
 await userEvent.click(screen.getByLabelText("动作"));await userEvent.click(await screen.findByText("指定 DNS 服务器进行解析"));
 expect(screen.queryByLabelText("转发标签（OUTBOUND TAG）")).not.toBeInTheDocument();
 await userEvent.type(screen.getByLabelText("DNS 服务器"),"1.1.1.1");
 await userEvent.click(screen.getByRole("button",{name:/确\s*认/}));
 await waitFor(()=>expect(api.write).toHaveBeenCalled());expect(api.write.mock.calls[0]?.[1]).toMatchObject({action:"dns",action_value:"1.1.1.1",match:[".example.net"]});
});
