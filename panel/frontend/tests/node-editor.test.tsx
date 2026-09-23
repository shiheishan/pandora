import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, ConfigProvider } from "antd";
import { expect, it, vi } from "vitest";
import {
  NodeEditDialog,
  editorProtocolPatch,
  editorProtocolValues,
} from "../src/features/admin/NodeEditor";
import { ApiFailure } from "../src/core/api";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

// 路由组绑定属于待接后端契约，测试显式打开。
document.head.insertAdjacentHTML("beforeend", '<meta name="pandora-contracts" content="route-groups-v1">');

const { write, request } = vi.hoisted(() => ({ write: vi.fn(), request:vi.fn() }));
vi.mock("../src/core/auth", () => ({ useAuth: () => ({ api: { write,request },scope:"fixture",principal:{id:"fixture"},can:()=>true }) }));
const schema = {
  node_type: "anytls",
  status: "stable",
  allowed_properties: ["tls", "padding_scheme", "key_path"],
  property_types: { tls: "boolean", padding_scheme: "array" },
  sensitive_properties: ["key_path"],
};
const node = {
  id: "fixture",
  row_version: 8,
  node_type: "anytls",
  name: "原线路",
  traffic_rate: 1,
  server_host: "fixture.example",
  server_port: 443,
  protocol_config: {
    tls: true,
    padding_scheme: ["1=85-420"],
    legacy_option: "preserved",
  },
};
function setup(reject = false) {
  write.mockReset();
  request.mockReset().mockResolvedValue({nodes:[{id:"parent-one",name:"父线路",node_type:"anytls",traffic_rate:2,current_traffic_rate:2,parent_node_id:null},{id:"nested",name:"不应作为父级",node_type:"anytls",parent_node_id:"parent-one"}],route_groups:[{id:"group-one",remarks:"共享规则",action:"block"}]});
  if (reject)
    write.mockRejectedValue(
      new ApiFailure("记录已变更，请重新打开", "conflict", 409),
    );
  else write.mockResolvedValue({ ...node, row_version: 9 });
  const close = vi.fn();
  render(
    <ConfigProvider theme={{ token: { motion: false } }}>
      <QueryClientProvider client={new QueryClient({defaultOptions:{queries:{retry:false}}})}>
      <App>
        <NodeEditDialog
          node={node}
          schemas={[
            schema,
            {
              node_type: "shadowsocks",
              status: "stable",
              allowed_properties: ["cipher"],
              required: ["cipher"],
              methods: ["aes-128-gcm"],
            },
          ]}
          pools={[]}
          close={close}
        />
      </App>
      </QueryClientProvider>
    </ConfigProvider>,
  );
  return close;
}
it("submits shared route bindings atomically with node edits",async()=>{
  setup();
  await userEvent.click(screen.getByLabelText("路由组"));
  await userEvent.click(await screen.findByText("共享规则"));
  await userEvent.clear(screen.getByLabelText("节点名称"));
  await userEvent.type(screen.getByLabelText("节点名称"),"带路由线路");
  await userEvent.click(screen.getByRole("button",{name:/提\s*交/}));
  await waitFor(()=>expect(write).toHaveBeenCalled());
  expect(write.mock.calls[0]?.[1]).toMatchObject({row_version:8,name:"带路由线路",route_group_ids:["group-one"]});
});
it("adds a node tag in the unified editor and submits only the tag delta", async () => {
  setup();
  await userEvent.type(screen.getByLabelText("节点标签"), "专线{Enter}");
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() => expect(write).toHaveBeenCalled());
  expect(write.mock.calls[0]?.[1]).toEqual({ row_version: 8, tags: ["专线"] });
});
it("saves base and padding edits together with CAS without resubmitting hidden keys", async () => {
  const close = setup();
  await userEvent.clear(screen.getByLabelText("节点名称"));
  await userEvent.type(screen.getByLabelText("节点名称"), "新线路");
  await userEvent.clear(screen.getByLabelText("填充方案"));
  await userEvent.type(
    screen.getByLabelText("填充方案"),
    "1=100-200\n2=300-400",
  );
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() =>
    expect(write).toHaveBeenCalledWith(
      "v1/nodes/fixture",
      {
        name: "新线路",
        row_version: 8,
        protocol_patch: { padding_scheme: ["1=100-200", "2=300-400"] },
      },
      { method: "PATCH" },
    ),
  );
  expect(close).toHaveBeenCalledTimes(1);
});
it("requires explicit discard for a changed draft and sends no write", async () => {
  const close = setup();
  await userEvent.type(screen.getByLabelText("节点名称"), "未保存");
  await userEvent.click(screen.getByRole("button", { name: /取\s*消/ }));
  expect(close).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "放弃修改并关闭" }));
  expect(close).toHaveBeenCalledTimes(1);
  expect(write).not.toHaveBeenCalled();
});
it("keeps user input open after a conflict", async () => {
  const close = setup(true);
  await userEvent.type(screen.getByLabelText("节点名称"), "草稿");
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  expect(await screen.findByText("记录已变更，请重新打开")).toBeVisible();
  expect(screen.getByLabelText("节点名称")).toHaveValue("原线路草稿");
  expect(close).not.toHaveBeenCalled();
});
it("closes unchanged without a write and normalizes missing switches", async () => {
  const close = setup();
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() => expect(close).toHaveBeenCalled());
  expect(write).not.toHaveBeenCalled();
  expect(editorProtocolValues(schema, {}).tls).toBe(false);
  const initial = editorProtocolValues(schema, node.protocol_config);
  expect(
    editorProtocolPatch(
      schema,
      initial,
      { ...initial, key_path: "" },
      node.protocol_config,
    ),
  ).toEqual({});
});
it("retains separate protocol drafts and submits only the selected protocol", async () => {
  setup();
  await userEvent.clear(screen.getByLabelText("填充方案"));
  await userEvent.type(screen.getByLabelText("填充方案"), "1=200-300");
  await userEvent.click(screen.getByRole("combobox", { name: "节点协议" }));
  await userEvent.click(
    await screen.findByText("Shadowsocks", { selector: ".xnode-protocol" }),
  );
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  expect(await screen.findByText("请填写加密方式")).toBeVisible();
  expect(write).not.toHaveBeenCalled();
  await userEvent.click(screen.getByLabelText("加密方式"));
  await userEvent.click(
    await screen.findByText("aes-128-gcm", {
      selector: ".ant-select-item-option-content",
    }),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "节点协议" }));
  await userEvent.click(
    await screen.findByText("AnyTLS", {
      selector: ".ant-select-item-option-content .xnode-protocol",
    }),
  );
  expect(screen.getByLabelText("填充方案")).toHaveValue("1=200-300");
  await userEvent.click(screen.getByRole("combobox", { name: "节点协议" }));
  await userEvent.click(
    await screen.findByText("Shadowsocks", {
      selector: ".ant-select-item-option-content .xnode-protocol",
    }),
  );
  await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
  await waitFor(() =>
    expect(write).toHaveBeenCalledWith(
      "v1/nodes/fixture",
      {
        row_version: 8,
        node_type: "shadowsocks",
        protocol_config: { cipher: "aes-128-gcm" },
      },
      { method: "PATCH" },
    ),
  );
});

it("binds a same-protocol root parent without overriding inherited rates",async()=>{
 setup();
 await userEvent.click(screen.getByLabelText("父级节点"));
 await userEvent.click(await screen.findByText("父线路"));
 expect(screen.queryByText("不应作为父级")).toBeNull();
 expect(screen.getByLabelText("基础倍率")).toBeDisabled();
 expect(screen.getByRole("switch",{name:"启用动态倍率"})).toBeDisabled();
 await userEvent.click(screen.getByRole("button",{name:/提\s*交/}));
 await waitFor(()=>expect(write).toHaveBeenCalledWith("v1/nodes/fixture",{row_version:8,parent_node_id:"parent-one"},{method:"PATCH"}));
});
it("submits node alias and converts the GB cap into exact raw bytes", async () => {
  setup();
  await userEvent.type(screen.getByLabelText("自定义节点 ID（选填）"), "107");
  const limit=screen.getByLabelText("流量限制（GB）");
  await userEvent.clear(limit);
  await userEvent.type(limit,"2");
  await userEvent.click(screen.getByRole("button", {name:/提\s*交/}));
  await waitFor(()=>expect(write).toHaveBeenCalled());
  expect(write.mock.calls[0]?.[1]).toEqual({row_version:8,custom_code:"107",transfer_limit_bytes:2147483648});
});

it("changes the client endpoint without changing the listener", async () => {
 setup();
 await userEvent.type(screen.getByLabelText("连接端口"), "8443");
 await userEvent.click(screen.getByRole("button", { name: /提\s*交/ }));
 await waitFor(() => expect(write).toHaveBeenCalledWith("v1/nodes/fixture", {row_version:8,client_port:8443}, {method:"PATCH"}));
});

it("saves dynamic rate ranges in the same versioned node edit",async()=>{
 setup();
 await userEvent.click(screen.getByRole("switch",{name:"启用动态倍率"}));
 await userEvent.click(screen.getByRole("button",{name:"添加时间段"}));
 await userEvent.clear(screen.getByLabelText("时段倍率 1"));
 await userEvent.type(screen.getByLabelText("时段倍率 1"),"0.25");
 await userEvent.click(screen.getByRole("button",{name:/提\s*交/}));
 await waitFor(()=>expect(write).toHaveBeenCalledWith("v1/nodes/fixture",{row_version:8,rate_schedule:{enabled:true,ranges:[{start:"00:00",end:"23:59",rate:0.25}]}},{method:"PATCH"}));
});

it("preserves an explicit zero base rate for free traffic",async()=>{
 setup();
 await userEvent.clear(screen.getByLabelText("基础倍率"));
 await userEvent.type(screen.getByLabelText("基础倍率"),"0");
 await userEvent.click(screen.getByRole("button",{name:/提\s*交/}));
 await waitFor(()=>expect(write).toHaveBeenCalledWith("v1/nodes/fixture",{row_version:8,traffic_rate:0},{method:"PATCH"}));
});
