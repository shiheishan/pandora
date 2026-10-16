---
paths:
  - "panel/frontend/src/portal/screens/overview/**"
  - "panel/frontend/src/portal/screens/subs/**"
  - "panel/frontend/src/portal/screens/plans/**"
---

# 门户 · 概览、我的套餐与选购

- 购买模型（10-07）：每份一张卡（`common/SubCard.tsx`），卡上的按钮自带对象，后面不再问「给哪一份」；只有一份时看不到任何「哪一份」。叫法 sn / dn / who 在 `common/purchase.ts` 的 `makeNaming`（多份用备注名，没备注用「套餐名 ····链接尾号」）
- 手上的份 = 生效中 + 过期 30 天内能救回（`isHeld`）；彻底停用的不列。
- 两类套餐（10-16）：订阅套餐（`kind=subscription`，用户端叫「按时间」）按月 / 季 / 半年 / 年付，流量「每期」清零；一次性套餐（`kind=one_time`，叫「一次性」）一个价、总流量、有效 N 天或永久（`permanent` 为真时不写日期，写「永久有效」）。一次性不能续费（`renewable` 恒假），同款再买是「叠加」（`stackable`：总流量加上、天数顺延，`#/checkout?stack=<一份>`）；套餐归档或关了续费时不能续，卡上给「换个套餐 / 再买一份」的出路。没有流量包、没有自动续费（10-16 删）。
- 送的流量（礼品卡、后台加流量）在 `bonus_bytes`：订阅套餐只在本期有效，一次性套餐加在总量上；剩余 = `traffic_limit + bonus_bytes − traffic_used`（`common/purchase.ts` 的 `leftOf`），卡片用量里注明「含送的 xG」。守卫：`common/subcard.test.ts`
- `pickPrimary` 只留给外框头像菜单的套餐徽标；概览改成复用 SubCard 的精简列表
- 金额与默认值一律来自报价接口 `POST v1/me/checkout/quote`（`common/quote.ts`），前端不算钱；只做展示用的日期加减（`addPeriod`）
- 到期展示精确到分钟（`common/traffic.ts` 的 `formatMinute` / `expiryInfo`），最后 24 小时写「还剩 X 小时」，过了写「已于 … 到期」
- 保留规则 3：可用节点只有名称 / 协议 / 倍率，删去设计稿的国家徽标与负载列。节点标签只在后台用（筛选、批量修改），门户不显示（用户 2026-10-14 定）。节点接口对不在生效中的订阅回 404，显示「当前没有可用节点」而不是报错。
- 换新链接（`subs/RotateSheet.tsx`）接口不幂等：弹层先说「只换这一份」与后果，请求期间禁用按钮；按份限频（每份 10 分钟 1 次、每天 5 次），429 把服务端的剩余分钟写进按钮「刚换过，N 分钟后才能再换」。换好去 `#/subs/<id>/rotated`，新链接与「添加到 App」放最上面
- 改名（`subs/RenameSheet.tsx`，`PATCH v1/me/subscriptions/{id}`）：候选名排除别的份已用的、不排除自己；撞名 409、不合规 422 落到输入框
- 添加到 App（`common/ImportSheet.tsx`）：先问「就是现在这台 / 别的设备」；深链只带链接与服务端的 `client_name`，没有可靠 scheme 的 App 复制链接手动粘贴；二维码是自写编码器（`common/qr.ts`，字节模式 M 级、1–40 版、SVG 无内联样式）
- 概览在线设备、设备上限、重置日必回：设备上限 null 写「不限」，重置日为 null 时退回额度行或按日用量接口的周期末（`common/traffic.ts` 的 `resetAtOf`）；没有订阅时在线设备显示「—」。
- 设计稿之外补了两处（均有契约依据）：critical 公告顶部横幅、送的流量并入剩余流量并注明「含送的」。设计稿早于 10-16，还画着流量包、没有一次性套餐：页面结构照稿，两类套餐与叠加按 10-16 改。
- 订阅只有 `active` / `expired` 两态（`subscription-schema.ts`）：「还在用」只认 `queries.ts` 的 `isLive`（`status === 'active'`），过期还能救回的看服务端的 `renewable`。
- 套餐卡（`common/PlanCard.tsx`）特性：订阅套餐「每期 xG」、一次性「共 xG · 有效 N 天 / 永久有效」，设备数、限速「限速 N Mbps」（不限速不显示），开了流量重置时写「流量重置 ¥X / 次，每期（一次性为有效期内）最多 N 次 / 不限次数」（`common/catalog.ts` 的 `resetPriceText`）；没有卖点（10-16 删）。`recommended` 为真时显示「推荐」并用强调样式。只有人民币，没有币种字段。
- 已过期（还能救回）的那份：主按钮「续费，恢复使用」旁边并列次一级的「换个套餐」（`flowCss.twoLine`，写「换成别的，马上恢复」），底部那行不再出现「换个套餐」；生效中的卡片仍是底部一行（首次点击测试 s8）
- 换新链接之后的完成页在新链接下写「添加好以后在 App 里点一次更新…原来那条可以删掉」，旁边给按 App 的更新说明链接（`common/UpdateHelp.tsx`）
- 选购页（`plans/index.tsx`）分「按时间」「一次性」两个标签（`#/plans?kind=one_time`；目录里只有一类时不显示标签、忽略 `kind`）。套餐卡按钮（`plans/labels.ts` 的 `planCardView`）：没有份时「买这个」去 `?new=<套餐>`；有份时一律去 `#/checkout?plan=<套餐>`，由落点接口 `GET v1/me/checkout/landings?plan_id=` 列出能放到哪里（另开一份 / 续在同款那份 / 叠加到同款那份 / 把某一份换成它），用户自己选、不预选，只有一个选项时直接跳过去（`checkout/Landing.tsx`）。去掉了旧的「给现在的续费或换 / 另买一份」模式切换。
- 关了新购的套餐：目录 DTO 回 `allow_new_purchase` / `allow_renewal`；选购页按 `common/catalog.ts` 的 `listable` 过滤（接新购的都列，关了新购的只在名下有这款且能续时列），「再买一份」只列接新购的。换套餐的候选来自报价接口，后端已按换入开关过滤。
- 「流量不够」页（`#/subs/<id>/traffic`，`subs/Traffic.tsx`；卡上「流量不够」按钮写重置价与剩几次）：上面写剩多少（含送的），下面 `ResetOffer` 与「换成更大的同类套餐」。`ResetOffer`：`reset_price` 为 null 不显示；次数用完（`resetsLeft`，`reset_limit − resets_used`，null 为不限）写「本期（一次性：有效期内）的 N 次已用完」、不给按钮；否则报价 `action: 'reset'` 回一条才显示「重置 · 金额」并跳 `#/checkout?reset=<一份>`（重置单永远按原价、不收券，用户 10-14 定）；409 `order_pending` 显示「有一张待付款的重置订单」引去订单页；409 `conflict` 照后端原话显示、不给按钮。
- 订阅域名探测（`subs/probe.ts`，`MySubs` 里 `useSubDomainProbe()`，用户 10-14 定）：不显示任何东西；每 30 分钟最多一次（`localStorage` 的 `pandora.subProbeAt`，读写包 try/catch，只是省请求，防刷靠服务端）；`api.post('v1/sub-probe/ticket')` 领票据，空串就不探测；否则开隐藏 iframe（`hidden`、`aria-hidden`、`tabIndex=-1`、`referrerPolicy=no-referrer`）指向 `probeFrameURL`（`resolveApiUrl('v1/sub-probe/frame', document.baseURI, {t})`），收到同源、来自这个 iframe 的 `pandora-sub-probe-done` 消息或 30 秒后移除。门户自己不 fetch 任何订阅域名；探测与上报在面板下发的探测页里
