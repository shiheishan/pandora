# panel/frontend/dev/mock/portal/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

门户十一个页面的假接口，一个页面一个文件、导出一个 MockModule，归门户前端会话。index.ts 的登记顺序即询问顺序，不再改动。外框顶栏读的余额、订阅、佣金、未读数按契约归属各页面，所以住在对应页面的文件里，页面会话扩充它们时外框照用。

成员清单
index.ts: 登记表 PORTAL_MODULES
fixtures.ts: 共享夹具（不是模块，不进登记表）：按用户建的内存状态（订阅挂在目录套餐上、ID 稳定、订阅地址可换发，另有余额与流水、订单、公告、流量包余量、礼品卡兑换记录）与场景开关——default / empty / multi（第二条订阅 past_due 且原价格已下架，测续费遇改价）/ legacy（「待补·后端」与修订 R69 字段全缺席，对照旧后端）/ error（读接口 500）/ slow（延迟 2.5 秒），经 POST v1/__mock/portal-scenario 切换，切换即重建
seeds.ts: 历史数据种子（不是模块）：跨四个月的 13 条订单（已支付 / 已取消 / 超时 / 已退款 / 处理中）、余额流水、礼品卡兑换记录
catalog.ts: 商品目录夹具（不是模块）：礼品卡（余额 / 流量 / 套餐 / 盲盒 / 需订阅的延期 / 限新用户）、三个套餐（家庭版 allow_upgrade=false）、四个流量包、覆盖各种拒绝的优惠码（AUTUMN26 / WELCOME / PROYEAR 限年付 / BIG50 有门槛 / EXPIRED / USED）、四种支付方式（含一个 POST 跳转渠道与一个只收 USD 的渠道）
billing.ts: 计费逻辑（不是模块）：请求体拒绝多余字段、优惠码试算、下单即扣余额与 30 分钟过期或取消退回（全部记余额流水）、履约（新购开订阅、续费延期、变更原地换套餐并退余额、流量包加余量）、变更折算（剩余时间比与剩余流量比取小）、订单列表行与明细形状
overview.ts: 概览（门户-01）；GET v1/me/subscriptions/{id}/usage（修订 R47：days 422、他人订阅 404、无数据补 0）与匿名的场景开关
subs.ts: 我的订阅（门户-02）；GET v1/me/subscriptions（外框徽标与页面共用）、subscription-links、{id}/nodes（只对 active / trialing / grace 下发，否则 404）、{id}/rotate（无 body、不幂等、统计清零）
plans.ts: 选购套餐（门户-03）；匿名的套餐目录与流量包目录、我的流量包余量、优惠码试算（套餐 + 价格或 pack_id）、购买流量包（order_create）
checkout.ts: 确认订单（门户-03 结账）；支付方式、新购（order_create）、续费（subscription_renewal_create）、变更试算与下单（subscription_change_plan_create）、发起支付（同渠道复用意图）；匿名的假收银台 GET v1/__mock/cashier（HTML）与 …/complete（履约后 302 回 return_url）
orders.ts: 我的订单（门户-04）；列表（status 逗号多值、未知状态 400、limit / offset、counts、按下单时间倒序）、明细、取消（非 UUID 400、重复取消 already_terminal），读前把超时待支付单转 expired
wallet.ts: 钱包（门户-05）；余额与流水（外框余额胶囊也读它）、充值建单（balance_topup_create、200）、礼品卡预览与兑换（gift_card_redeem，流量进流量包余额、延期要有订阅、盲盒固定抽第二项）、我的兑换记录
referral.ts: 邀请返利（门户-06）；自带按 PortalState 挂的佣金状态（WeakMap，切场景跟着重建）：GET v1/me/invite（没有码时懒生成 8 位码）、GET v1/me/commission（外框菜单的可用佣金提示也读它；可用 = 账本余额 − 未过账在途提现，5.A D-F-1；legacy 去掉 R69 字段）、申请提现（commission_withdrawal_request，检查顺序照 RequestWithdrawal）、转余额（commission_transfer_to_balance，记进钱包余额与流水）；empty 无码无记录，multi 有一笔审核中提现且邀请码已用满
messages.ts: 消息（门户-08）；按 PortalState 挂的站内信（WeakMap）：GET v1/me/notifications（外框铃铛未读数也轮询它；limit 1–100 默认 30、unread=1、unread 计数全量）、单条已读（他人或不存在也 200，非 UUID 500）、全部已读、GET v1/me/announcements（置顶优先，最多 20 条）；empty 无站内信
account.ts: 账号安全（门户-10）；POST v1/me/quick-login 签发快捷登录令牌
tickets.ts: 工单支持（门户-07）；按 PortalState 挂的工单（WeakMap）：分类、列表（不带 messages、related_order 恒 null）、详情（message_count 0、last_reply_at 零值、related_order）、新建（support_ticket_create，201，空标题取正文首行，未结满 5 个 409，订单须是本人的）、回复（_user_reply，resolved 重开、closed 409）、关闭（_user_close，已关闭 404）、撤回（必须带 JSON 体、客服回复过 409）；四张种子覆盖等待回复 / 客服已回复 / 已关闭带关联订单 / 已撤回，multi 另加三张未结凑满 5，empty 无工单，legacy 去掉 R60 字段
help.ts: 帮助（门户-09），目前为空壳
读接口经 fixtures.gate 过一道，error / slow 场景对它们生效；外框的余额与佣金也在内，所以 error 场景下外框同样显示失败态

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
