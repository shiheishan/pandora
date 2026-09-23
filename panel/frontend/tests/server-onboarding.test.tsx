import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App as AntApp, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { DialogHost } from "../src/core/dialogs";
import { AuthProvider } from "../src/core/auth";
import { ServerDetail } from "../src/features/admin/Nodes";
it("shows the one-time token required by the installer, and clears it on close", async () => {
 localStorage.setItem("aegis_admin_token", "test-token");
 vi.stubGlobal("fetch", vi.fn(async (input: string) => {
 const path = new URL(input).pathname;
 const data = path.endsWith("/v1/me") ? {user_id:"admin",permissions:["node.read","node.provision"]}
 : path.endsWith("/bootstrap-token") ? {token:"one-time-fixture-secret",install_command:"example installer command",expires_at:"2026-09-07T12:00:00Z"}
 : path.endsWith("/nodes") ? {nodes:[{id:"node-a",name:"fixture node",node_type:"vless",identity_active:true,config_validated_at:"2026-09-07T12:00:00Z",last_issued_generation:2,last_applied_generation:1}]} : {id:"server-a",name:"台湾测试机",status:"draft"};
 return new Response(JSON.stringify(data),{status:200});
 }));
 const client=new QueryClient({defaultOptions:{queries:{retry:false}}});
 const router=createMemoryRouter([{path:"/servers/:id",element:<ServerDetail/>}],{initialEntries:["/servers/server-a"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><AntApp><QueryClientProvider client={client}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></AntApp></ConfigProvider>);
 await screen.findByText("待确认 · #2");
 expect(screen.getByText("已接入")).toBeVisible();
 expect(screen.queryByText("已确认 · #2")).not.toBeInTheDocument();
 await userEvent.click(await screen.findByRole("button",{name:"取得接入命令"}));
 // The shared dialog is lazy-loaded; allow its cold module import to finish.
 await userEvent.click(await screen.findByRole("button",{name:"签发命令"}));
 await waitFor(()=>expect(screen.getByText("one-time-fixture-secret")).toBeVisible());
 expect(screen.getByText(/不是管理员密码/)).toBeVisible();
 await userEvent.click(screen.getByRole("button",{name:"已保存并关闭"}));
 await waitFor(()=>expect(screen.queryByText("one-time-fixture-secret")).not.toBeInTheDocument());
 expect(JSON.stringify(localStorage)).not.toContain("one-time-fixture-secret");
});
