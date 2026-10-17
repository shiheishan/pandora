---
paths:
  - "panel/frontend/src/portal/screens/subs/**"
  - "panel/frontend/src/portal/screens/plans/**"
---

# 门户 · 我的套餐与选购（概览已并入我的套餐）

- 我的套餐（`subs/index.tsx` 的 `MySubs`，样稿 v5 `#p-subs`）自上而下：`portal.home.banner` 插件位 → 顶部横条（`subs/TopNotes.tsx`：critical 公告点开看全文、待支付单带 30 秒刷新的倒计时，没有套餐也显示）→ `portal.subscribe.notice` → 空状态（三步说明 + 去选购）或各份 → 「更多」（再买一份，分开用 / 有卡？去兑换）→ `portal.home.aside`。旧概览的佣金、在线设备合计、普通公告已删（用户 10-16 定）。
- 一份 = `subs/Copy.tsx` 的一个 `<article>`：主卡（Hero：眉标、贴纸名 h2、剩余流量大数字、进度条、到期与在线设备、底部「添加到 App」+ 续费 / 叠加）+「这一份」操作行（链接尾号与复制、节点和每天用量、重置本期流量、换个套餐、起名、换新链接）。文字全在 `common/card-text.ts`。多份时顶部切换条（`subs/Copies.tsx`，tablist「你的几份」，方向键可切），所有份都渲染、没选中的 `hidden`，选中记在 `?c=<一份>`；浏览器测试用 fixtures 的 `openCard` 先点 tab。卡上的按钮自带对象，后面不再问「给哪一份」；只有一份时看不到任何「哪一份」。叫法 sn / dn / who 在 `common/purchase.ts` 的 `makeNaming`（多份用备注名，没备注用「套餐名 ····链接尾号」）
- 手上的份 = 生效中 + 过期 30 天内能救回（`isHeld`）；彻底停用的不列。
- 两类套餐（10-16）：订阅套餐（`kind=subscription`，用户端叫「按时间」）按月 / 季 / 半年 / 年付，流量「每期」清零；一次性套餐（`kind=one_time`，叫「一次性」）一个价、总流量、有效 N 天或永久（`permanent` 为真时不写日期，写「永久有效」）。一次性不能续费（`renewable` 恒假），同款再买是「叠加」（`stackable`：总流量加上、天数顺延，`#/checkout?stack=<一份>`）；套餐归档或关了续费时不能续，卡上给「换个套餐 / 再买一份」的出路。没有流量包、没有自动续费（10-16 删）。
- 送的流量（礼品卡、后台加流量）在 `bonus_bytes`：订阅套餐只在本期有效，一次性套餐加在总量上；剩余 = `traffic_limit + bonus_bytes − traffic_used`（`common/purchase.ts` 的 `leftOf`），主卡写「本期剩余 · 共 X，含送的 xG，本期有效」（一次性「剩余 · 共 X，含送的 xG，不清零，用完为止」）。流量快用完 = 已用（含送的）≥ 80%（`isLow`，用户 10-16 定），主卡就地给「重置 · ¥X」与「换更大的 ›」。守卫：`common/common.test.ts`、`subs/copy.test.ts`
- `pickPrimary` 只留给外框头像菜单的套餐徽标
- 金额与默认值一律来自报价接口 `POST v1/me/checkout/quote`（`common/quote.ts`），前端不算钱；只做展示用的日期加减（`addPeriod`）
- 到期展示精确到分钟（`common/traffic.ts` 的 `formatMinute` / `expiryInfo`），最后 24 小时写「还剩 X 小时」，过了写「已于 … 到期」
- 保留规则 3：可用节点只有名称 / 协议 / 倍率，删去设计稿的国家徽标与负载列。节点标签只在后台用（筛选、批量修改），门户不显示（用户 2026-10-14 定）。节点接口对不在生效中的订阅回 404，显示「当前没有可用节点」而不是报错。
- 换新链接（`subs/RotateSheet.tsx`）接口不幂等：弹层先说「只换这一份」与后果，请求期间禁用按钮；按份限频（每份 10 分钟 1 次、每天 5 次），429 把服务端的剩余分钟写进按钮「刚换过，N 分钟后才能再换」。换好去 `#/subs/<id>/rotated`，新链接与「添加到 App」放最上面
- 改名（`subs/RenameSheet.tsx`，`PATCH v1/me/subscriptions/{id}`）：候选名排除别的份已用的、不排除自己；撞名 409、不合规 422 落到输入框
- 添加到 App（`common/ImportSheet.tsx`）：先问「就是现在这台 / 别的设备」；深链只带链接与服务端的 `client_name`，没有可靠 scheme 的 App 复制链接手动粘贴；二维码是自写编码器（`common/qr.ts`，字节模式 M 级、1–40 版、SVG 无内联样式）
- 在线设备、设备上限、重置日必回：设备上限 null 写「不限台数」，重置日为 null 时退回额度行或按日用量接口的周期末（`common/traffic.ts` 的 `resetAtOf`）。
- 样稿 v5 之外保留的（审计「不能丢」）：泄露红条（近 24 小时来源数超过设备上限，给「换新链接」）、不再续费的说明、不限流量、多份没起名时「起个名字，好分清是谁的」+「起名字」、订阅域名探测、「之前的套餐已经停用了」。
- 订阅只有 `active` / `expired` 两态（`subscription-schema.ts`）：「还在用」只认 `queries.ts` 的 `isLive`（`status === 'active'`），过期还能救回的看服务端的 `renewable`。
- 套餐卡（`common/PlanCard.tsx`）特性：订阅套餐「每期 xG」、一次性「共 xG · 有效 N 天 / 永久有效」，设备数、限速「限速 N Mbps」（不限速不显示），开了流量重置时写「流量重置 ¥X / 次，每期（一次性为有效期内）最多 N 次 / 不限次数」（`common/catalog.ts` 的 `resetPriceText`）；没有卖点与线路（10-16 删，审计 #1 #2）。徽标：在用「你在用」白底、推荐深绿底；在用那款换底色。手机上按时间的套餐是单选展开列表（`plans/PickList.tsx`），一次性套餐一行一款（`plans/OnceList.tsx`）。只有人民币，没有币种字段。
- 已过期（还能救回）的那份：主卡写「X月X日到期了」，主按钮「续费，恢复使用 · ¥30/月」，并列「换个套餐 / 换成别的，马上恢复」（去 `#/subs/<id>/change`）与「另买一份」（去 `#/subs/new`）；「这一份」只剩链接、节点和每天用量、起名；开了付费重置时底下注明「重置流量要在用着的套餐上办，先续费就能接着用。」（用户 10-16 定，首次点击测试 s8）
- 换新链接之后的完成页在新链接下写「添加好以后在 App 里点一次更新…原来那条可以删掉」，旁边给按 App 的更新说明链接（`common/UpdateHelp.tsx`）
- 选购页（`plans/index.tsx`）分「按时间」「一次性」两类，切换是页头的 `Segmented`（radiogroup「套餐类型」，`#/plans?kind=one_time`；目录里只有一类时不显示、忽略 `kind`）。套餐卡按钮（`plans/labels.ts` 的 `planCardView`）：没有份时「买这个」去 `?new=<套餐>`；有份时一律去 `#/checkout?plan=<套餐>`，由落点接口 `GET v1/me/checkout/landings?plan_id=` 列出能放到哪里（另开一份 / 续在同款那份 / 叠加到同款那份 / 把某一份换成它），用户自己选、不预选，只有一个选项时直接跳过去（`checkout/Landing.tsx`）。去掉了旧的「给现在的续费或换 / 另买一份」模式切换。
- 关了新购的套餐：目录 DTO 回 `allow_new_purchase` / `allow_renewal`；选购页按 `common/catalog.ts` 的 `listable` 过滤（接新购的都列，关了新购的只在名下有这款且能续时列），「再买一份」只列接新购的。换套餐的候选来自报价接口，后端已按换入开关过滤。
- 「流量不够」页（`#/subs/<id>/traffic`，`subs/Traffic.tsx`；主卡的「重置」与「这一份」的重置行直接去确认页，这一页留给用量图里的「流量不够？」）：上面写剩多少（含送的），下面 `ResetOffer` 与「换成更大的同类套餐」。`ResetOffer`：`reset_price` 为 null 不显示；次数用完（`resetsLeft`，`reset_limit − resets_used`，null 为不限）写「本期（一次性：有效期内）的 N 次已用完」、不给按钮；否则报价 `action: 'reset'` 回一条才显示「重置 · 金额」并跳 `#/checkout?reset=<一份>`（重置单永远按原价、不收券，用户 10-14 定）；409 `order_pending` 显示「有一张待付款的重置订单」引去订单页；409 `conflict` 照后端原话显示、不给按钮。
- 订阅域名探测（`subs/probe.ts`，`MySubs` 里 `useSubDomainProbe()`，用户 10-14 定）：不显示任何东西；每 30 分钟最多一次（`localStorage` 的 `pandora.subProbeAt`，读写包 try/catch，只是省请求，防刷靠服务端）；`api.post('v1/sub-probe/ticket')` 领票据，空串就不探测；否则开隐藏 iframe（`hidden`、`aria-hidden`、`tabIndex=-1`、`referrerPolicy=no-referrer`）指向 `probeFrameURL`（`resolveApiUrl('v1/sub-probe/frame', document.baseURI, {t})`），收到同源、来自这个 iframe 的 `pandora-sub-probe-done` 消息或 30 秒后移除。门户自己不 fetch 任何订阅域名；探测与上报在面板下发的探测页里
