import { expect, it } from "vitest";
import { couponInitial, couponPayload } from "../src/features/admin/Coupons";
import { giftRewards, giftRewardFields, giftTrafficBytes } from "../src/features/admin/GiftCards";
it("maps coupon yuan and discount percentages exactly, defaulting to CNY",()=>{
 expect(couponPayload({code:"QA",discount_type:"fixed",discount_input:"0.29"},false)).toMatchObject({discount_value:29,currency:"CNY",min_order_amount:0});
 expect(couponPayload({discount_type:"percent",discount_input:"12.34",max_discount:"9.99",count:3},true)).toMatchObject({discount_value:1234,max_discount:999,count:3});
 expect(()=>couponPayload({discount_type:"percent",discount_input:"100.01"},false)).toThrow(/100/);
 expect(()=>couponPayload({discount_type:"fixed",discount_input:"-1"},false)).toThrow();
 expect(()=>couponPayload({discount_type:"fixed",discount_input:"0.001"},false)).toThrow();
});
it("rejects reversed validity windows before sending",()=>{
 expect(()=>couponPayload({discount_type:"fixed",discount_input:"10",valid_from:"2026-09-08T00:00:00Z",valid_until:"2026-09-07T00:00:00Z"},false)).toThrow(/结束/);
});
it("preserves exact gift money and integral MiB units for each reward type",()=>{
 expect(giftRewards({type:"general",balance:"0.29",traffic_mib:1024,expire_days:7,reset_quota:true})).toEqual({balance:29,traffic_bytes:1073741824,expire_days:7,reset_quota:true});
 expect(giftRewards({type:"plan",plan_id:"p",price_id:"price"})).toEqual({plan_id:"p",price_id:"price"});
 expect(giftRewards({type:"mystery",pool:[{label:"奖励",weight:3,balance:"1.23"}]})).toEqual({pool:[{label:"奖励",weight:3,balance:123,traffic_bytes:0,expire_days:0}]});
});

import { protocolInputConfig } from "../src/core/protocolInputs";
import { protocolField } from "../src/core/protocol";
it("converts structured protocol fields without losing unrelated configuration",()=>{
 expect(protocolInputConfig({network:"xhttp",headers:"Host: example.com\nX-Test: hello: world",sc_max_each_post_bytes:{from:10,to:20},padding_scheme:"64-128\n256-512"})).toEqual({network:"xhttp",headers:{Host:"example.com","X-Test":"hello: world"},sc_max_each_post_bytes:{from:10,to:20},padding_scheme:["64-128","256-512"]});
 expect(()=>protocolInputConfig({headers:"Host: a\nhost: b"})).toThrow(/重复/);
 expect(()=>protocolInputConfig({sc_max_each_post_bytes:{from:20,to:10}})).toThrow(/有序/);
 expect(protocolInputConfig({headers:"",padding_scheme:"",uplink_chunk_size:{}})).toEqual({});
 expect(protocolField({enums:{mode:["auto","packet-up"]}},"network_settings.mode").enums).toEqual(["auto","packet-up"]);
});

it("coupon editing round-trips money, dates and scope without resetting limits",()=>{
 const row={code:"QA",discount_type:"percent",discount_value:1234,min_order_amount:999,max_discount:10001,max_redemptions:8,max_redemptions_per_user:2,applicable_plan_ids:["plan"],valid_from:"2026-09-07T00:12:34.123Z",valid_until:"2026-10-07T00:12:34.123Z"};
 expect(couponPayload(couponInitial(row),false)).toMatchObject(row);
 expect(couponInitial({...row,max_discount:null,valid_until:null})).toMatchObject({max_discount:"",valid_until:""});
});

it("round trips gift rewards without rounding historical fractional MiB or large minor amounts",()=>{
 for(const traffic of [1,123456789,9007199254740991]){
  const fields=giftRewardFields({balance:9007199254740991,traffic_bytes:traffic});
  expect(giftRewards({type:"general",...fields})).toMatchObject({balance:9007199254740991,traffic_bytes:traffic});
 }
 expect(()=>giftTrafficBytes("0.1")).toThrow(/整数 Byte/);
 expect(()=>giftRewards({type:"general",balance:"1.001"})).toThrow(/两位小数/);
 expect(()=>giftRewardFields({balance:9007199254740992})).toThrow(/精确读取/);
});
