import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import { expect, it, vi } from "vitest";
import { AuthProvider } from "../src/core/auth";
import { DialogHost } from "../src/core/dialogs";
import { NodeCreate } from "../src/features/admin/Nodes";

it("creates through the original API with the selected server after configuration regrouping", async () => {
 localStorage.setItem("aegis_admin_token", "fixture-token");
 const writes: {body:Record<string,unknown>;key:string|null}[]=[];
 vi.stubGlobal("fetch", vi.fn(async (input:string, options?:RequestInit)=>{
  const path=new URL(input).pathname;let data:unknown={};
  if(path.endsWith('/me'))data={user_id:"operator",permissions:["*"]};
  else if(path.endsWith('/nodes')&&options?.method==='POST'){
   writes.push({body:JSON.parse(String(options.body)),key:new Headers(options.headers).get('Idempotency-Key')});data={id:"019a1234-1234-7123-8123-123456789abc"};
  }else if(path.endsWith('/servers'))data={servers:[{id:"server-a",name:"测试服务器",status:"ready"}]};
  else if(path.endsWith('/node-pools'))data={pools:[]};
  else if(path.endsWith('/node-protocol-schemas'))data={schemas:[{node_type:"shadowsocks",status:"stable",required:["cipher"],allowed_properties:["cipher"],methods:["aes-128-gcm"]}]};
  return new Response(JSON.stringify(data),{status:200});
 }));
 const router=createMemoryRouter([{path:"/nodes/new",element:<NodeCreate/>},{path:"/nodes/:id",element:<p>新节点详情</p>}],{initialEntries:["/nodes/new?server=server-a"]});
 render(<ConfigProvider theme={{token:{motion:false}}}><App><QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}><DialogHost><AuthProvider><RouterProvider router={router}/></AuthProvider></DialogHost></QueryClientProvider></App></ConfigProvider>);
 await userEvent.type(await screen.findByLabelText('节点名称'), '新线路');
 await userEvent.type(screen.getByLabelText('订阅显示名称'), '显示名称');
 await userEvent.type(screen.getByLabelText('节点地址'), 'node.example');
 await userEvent.type(screen.getByLabelText('连接端口'), '8443');
 await userEvent.click(screen.getByLabelText('节点协议'));
 await userEvent.click(await screen.findByText('Shadowsocks',{selector:'.xnode-protocol'}));
 await userEvent.click(await screen.findByLabelText('加密方式'));
 await userEvent.click(await screen.findByText('aes-128-gcm',{selector:'.ant-select-item-option-content'}));
 await userEvent.click(screen.getByRole('button',{name:/提\s*交/}));
 await waitFor(()=>expect(writes).toHaveLength(1));
 expect(writes[0]?.body).toMatchObject({name:'新线路',display_name:'显示名称',server_host:'node.example',server_port:443,client_port:8443,traffic_rate:1,sort_order:0,server_id:'server-a',node_type:'shadowsocks',kernel:'pandora-native',protocol_config:{cipher:'aes-128-gcm'}});
 expect(writes[0]?.key).toBeTruthy();
 expect(await screen.findByText('新节点详情')).toBeVisible();
});
