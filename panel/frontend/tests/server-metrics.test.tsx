import { fireEvent,render,screen } from "@testing-library/react";
import { expect,it,vi } from "vitest";
const state=vi.hoisted(()=>({data:{points:[] as Record<string,unknown>[]}}));
vi.mock("../src/components/common",()=>({useData:()=>({data:state.data}),QueryPanel:({children}:{children:React.ReactNode})=>children}));
vi.mock("antd",()=>({Grid:{useBreakpoint:()=>({md:true})},Card:({children}:{children:React.ReactNode})=><div>{children}</div>,Space:({children}:{children:React.ReactNode})=><div>{children}</div>,Empty:({description}:{description:string})=><div>{description}</div>,Select:({options,value,onChange,...props}:{options:{value:string;label:string}[];value:string;onChange:(v:string)=>void;"aria-label":string})=><select aria-label={props["aria-label"]} value={value} onChange={e=>onChange(e.target.value)}>{options.map(o=><option key={o.value} value={o.value}>{o.label}</option>)}</select>}));
import { ServerMetrics } from "../src/features/admin/ServerMetrics";
const point=(at:string,cpu:number)=>({at,cpu_percent:cpu,mem_percent:50,rx_speed:1024,tx_speed:2048});
it("pins selected samples by timestamp as the rolling window changes, then falls back safely",()=>{
 state.data={points:[point("2026-09-07T00:00:00Z",10),point("2026-09-07T00:01:00Z",20),point("2026-09-07T00:02:00Z",30)]};
 const view=render(<ServerMetrics nodeId="a"/>);
 fireEvent.change(screen.getByRole("combobox",{name:"负载采样时间"}),{target:{value:"1"}});
 expect(screen.getByText("CPU 20.0%")).toBeInTheDocument();
 state.data={points:[point("2026-09-07T00:01:00Z",20),point("2026-09-07T00:02:00Z",30)]};view.rerender(<ServerMetrics nodeId="a"/>);
 expect(screen.getByText("CPU 20.0%")).toBeInTheDocument();
 state.data={points:[point("2026-09-07T00:02:00Z",30)]};view.rerender(<ServerMetrics nodeId="a"/>);
 expect(screen.getByText("CPU 30.0%")).toBeInTheDocument();expect(screen.queryByText(/NaN/)).not.toBeInTheDocument();
});
it("does not carry a sample selection into a different node",()=>{
 state.data={points:[point("2026-09-07T00:00:00Z",10),point("2026-09-07T00:01:00Z",20)]};
 const view=render(<ServerMetrics nodeId="a"/>);fireEvent.change(screen.getByRole("combobox",{name:"负载采样时间"}),{target:{value:"0"}});
 view.rerender(<ServerMetrics nodeId="b"/>);expect(screen.getByText("CPU 20.0%")).toBeInTheDocument();
});

