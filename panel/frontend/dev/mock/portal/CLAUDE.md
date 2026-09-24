# panel/frontend/dev/mock/portal/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

门户十一个页面的假接口，一个页面一个文件、导出一个 MockModule，归门户前端会话。index.ts 的登记顺序即询问顺序，不再改动。外框顶栏读的余额、订阅、佣金、未读数按契约归属各页面，所以住在对应页面的文件里，页面会话扩充它们时外框照用。

成员清单
index.ts: 登记表 PORTAL_MODULES
fixtures.ts: 共享夹具（不是模块，不进登记表）：按用户建的内存状态（订阅 ID 稳定、订阅地址可换发、每日用量、待支付单、公告、流量包余量）与场景开关——default / empty / multi / legacy（「待补·后端」字段全缺席，对照后端现状）/ error（读接口 500）/ slow（延迟 2.5 秒），经 POST v1/__mock/portal-scenario 切换，切换即重建
overview.ts: 概览（门户-01）；GET v1/me/subscriptions/{id}/usage（修订 R47：days 422、他人订阅 404、无数据补 0）与匿名的场景开关
subs.ts: 我的订阅（门户-02）；GET v1/me/subscriptions（外框头像菜单的套餐徽标也读它，外框只取 plan_name / status）、subscription-links、{id}/nodes（只对 active / trialing / grace 下发，否则 404）、{id}/rotate（无 body、不幂等、统计清零）
plans.ts: 选购套餐（门户-03）；目前只有 GET v1/me/traffic-packs（修订 R30 形状）
orders.ts: 我的订单（门户-04）；目前只有 GET v1/orders（单值 status 过滤、未知状态 400、limit / offset）
wallet.ts: 钱包（门户-05）；GET v1/me/balance（外框余额胶囊也读它）
referral.ts: 邀请返利（门户-06）；GET v1/me/commission（外框菜单的可用佣金提示也读它）
messages.ts: 消息（门户-08）；GET v1/me/notifications（外框铃铛未读数轮询它）、GET v1/me/announcements（置顶优先，最多 20 条）
account.ts: 账号安全（门户-10）；POST v1/me/quick-login 签发快捷登录令牌
checkout.ts / tickets.ts / help.ts: 其余三个页面，目前为空壳
读接口经 fixtures.gate 过一道，error / slow 场景对它们生效；外框的余额与佣金也在内，所以 error 场景下外框同样显示失败态

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
