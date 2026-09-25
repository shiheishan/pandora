# panel/frontend/src/portal/screens/
> L2 | 父级: /panel/frontend/src/portal/CLAUDE.md

门户十一个页面（含结账）。Shell 只认 index.ts 这张登记表：按路由取出页面的懒加载组件，传入 { rest }，外面包 shell/ScreenFrame（Suspense 骨架 + 错误边界）；页头标题与副标题仍由 Shell 按 pages.ts 画，页面只管内容区。
每个页面一个目录、构建出一个独立块，归门户前端会话；登记表与 Placeholder 不再改动，目录里加文件时由门户会话补本目录的 L2。
rest 是页面之后剩下的路径段，已解码（如 #/orders/<订单 id>、#/tickets/<id>）；查询串（如 #/checkout?...）用 core/router 的 useHashLocation 读，外框规范化地址时保留查询串。
页面之间的约定地址：#/subs?sub=<订阅 id>（我的订阅选中哪条）；#/plans?tab=packs（流量包标签）；#/checkout?plan=<套餐>&price=<价格>（无订阅新购、同套餐续费、换套餐变更，由结账页判定）、?renew=<订阅 id>[&price=]（续费）、?pack=<流量包>；#/orders/<订单 id>（订单页展开该行；带 ?paid=1 时是收银台回跳，弹支付确认）；#/messages?tab=announcements（消息页公告标签）；#/tickets/new?order=<订单 id>（订单明细里的「提交工单」预填关联订单）、#/tickets/<id>。
页面数据只经 common/ 与外框 queries.ts 的查询取，同一接口同一查询键；外框 schema 不够用时在 queries.ts 里写全、外框经 select 取自己的部分（订阅列表、佣金概况即如此），不另起查询键。

成员清单
index.ts: 页面登记表 SCREENS（十一个 React.lazy）与页面入参类型 PortalScreenProps { rest: string[] }
Placeholder.tsx: 占位页（第 2 阶段 Shell 的空状态），十一个页面都换成真页面后删除
common/: 多个页面共用的读模型与小块（订阅 / 订单 / 公告 / 商品目录的 schema 与查询、流量与期限计算、客户端深链、插槽、加载失败态、本期用量卡、支付弹窗），不是页面、不进登记表；见 common/CLAUDE.md
overview/: 概览（门户-01），第 ① 步接入；见 overview/CLAUDE.md
subs/: 我的订阅（门户-02），第 ① 步接入；见 subs/CLAUDE.md
plans/: 选购套餐（门户-03 列表），第 ② 步接入；见 plans/CLAUDE.md
checkout/: 确认订单（门户-03 结账），第 ② 步接入；见 checkout/CLAUDE.md
orders/: 我的订单（门户-04），第 ③ 步接入（第 ② 步先接了收银台回跳确认）；见 orders/CLAUDE.md
wallet/: 钱包（门户-05），第 ③ 步接入；见 wallet/CLAUDE.md
referral/: 邀请返利（门户-06），第 ④ 步接入；见 referral/CLAUDE.md
tickets/: 工单支持（门户-07），第 ⑤ 步接入；见 tickets/CLAUDE.md
messages/: 消息（门户-08），第 ⑤ 步接入；见 messages/CLAUDE.md
help/ account/: 其余页面目录，index.tsx 默认导出页面组件，目前渲染 Placeholder，按开工说明第 ⑥ 步接入

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
