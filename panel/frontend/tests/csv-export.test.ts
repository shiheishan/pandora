import {expect,it,vi} from "vitest";
import {z} from "zod";
import {ApiClient} from "../src/core/api";
it("downloads CSV with account authentication and no cache, preserving structured failures",async()=>{
 const fetchMock=vi.fn().mockResolvedValueOnce(new Response("卡密,状态\r\nABC,unused\r\n",{status:200})).mockResolvedValueOnce(new Response(JSON.stringify({error:{code:"validation_error",message:"请按批次导出"}}),{status:422}));vi.stubGlobal("fetch",fetchMock);
 const api=new ApiClient("https://panel.example/",()=>"fixture-token",vi.fn(),async()=>false);
 expect(await api.request("v1/gift-cards/codes/export?status=unused",z.string(),{responseType:"text"})).toContain("ABC,unused");
 expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({cache:"no-store",method:"GET",headers:{Authorization:"Bearer fixture-token",Accept:"text/csv"}});
 await expect(api.request("v1/gift-cards/codes/export",z.string(),{responseType:"text"})).rejects.toMatchObject({kind:"validation",message:"请按批次导出"});
});
it("does not deliver a CSV after the signed-in account changes",async()=>{
 let finish!:(r:Response)=>void;let generation=0;
 vi.stubGlobal("fetch",vi.fn(()=>new Promise<Response>(resolve=>finish=resolve)));
 const api=new ApiClient("https://panel.example/",()=>"fixture",vi.fn(),async()=>false,()=>generation);
 const pending=api.request("v1/gift-cards/codes/export",z.string(),{responseType:"text"});generation++;finish(new Response("private codes"));
 await expect(pending).rejects.toMatchObject({kind:"cancelled"});
});
