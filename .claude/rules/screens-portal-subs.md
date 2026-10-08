---
paths:
  - "panel/frontend/src/portal/screens/overview/**"
  - "panel/frontend/src/portal/screens/subs/**"
  - "panel/frontend/src/portal/screens/plans/**"
---

# 门户 · 概览、我的订阅与选购

- 购买模型（10-07）：每份一张卡（`common/SubCard.tsx`），卡上的按钮自带对象，后面不再问「给哪一份」；只有一份时看不到任何「哪一份」。叫法 sn / dn / who 在 `common/purchase.ts` 的 `makeNaming`（多份用备注名，没备注用「套餐名 ····链接尾号」）
- 手上的份 = 生效中 + 过期 30 天内能救回（`isHeld`）；彻底停用的不列，但它上面没用完的流量包会出转移提示条（`subs/Transfer.tsx`，接口 `POST v1/me/traffic-packs/transfer`）
- `pickPrimary` 只留给外框头像菜单的套餐徽标；概览改成复用 SubCard 的精简列表
- 金额与默认值一律来自报价接口 `POST v1/me/checkout/quote`（`common/quote.ts`），前端不算钱；只做展示用的日期加减（`addPeriod`）
- 到期展示精确到分钟（`common/traffic.ts` 的 `formatMinute` / `expiryInfo`），最后 24 小时写「还剩 X 小时」，过了写「已于 … 到期」
- 保留规则 3：可用节点只有名称 / 协议 / 倍率，删去设计稿的国家徽标与负载列。节点接口对 past_due 等状态回 404，显示「当前没有可用节点」而不是报错。
- 换新链接（`subs/RotateSheet.tsx`）接口不幂等：弹层先说「只换这一份」与后果，请求期间禁用按钮；按份限频（每份 10 分钟 1 次、每天 5 次），429 把服务端的剩余分钟写进按钮「刚换过，N 分钟后才能再换」。换好去 `#/subs/<id>/rotated`，新链接与「添加到 App」放最上面
- 改名（`subs/RenameSheet.tsx`，`PATCH v1/me/subscriptions/{id}`）：候选名排除别的份已用的、不排除自己；撞名 409、不合规 422 落到输入框
- 添加到 App（`common/ImportSheet.tsx`）：先问「就是现在这台 / 别的设备」；深链只带链接与服务端的 `client_name`，没有可靠 scheme 的 App 复制链接手动粘贴；二维码是自写编码器（`common/qr.ts`，字节模式 M 级、1–40 版、SVG 无内联样式）
- 概览在线设备、设备上限、重置日必回：设备上限 null 写「不限」，重置日为 null 时退回额度行或按日用量接口的周期末（`common/traffic.ts` 的 `resetAtOf`）；没有订阅时在线设备显示「—」。
- 设计稿之外补了三处（均有契约依据）：critical 公告顶部横幅、grace / past_due 徽标、流量包余量并入剩余流量并注明「含流量包」。
- 套餐卡特性顺序：设备数、限速「限速 N Mbps」（不限速不显示）、卖点 highlights（按后台给的顺序）；`recommended` 为真时显示「推荐」并用强调样式，流量包的 recommended 显示「最划算」。
- 流量包永不过期、可叠加、挂在买它时选的那一份上（购买模型 10-07 取代 D-E-1 的挂用户）。
- 选购页套餐卡（`plans/labels.ts` 的 `planCardView`，原型 planCard）：同款只有「续费」；不同款能换的只有一份时「换成 X」并写今天付多少；多份时「下一步选换掉哪一份」（不预选）；「另买一份」模式全是「买这个」；`allow_upgrade=false` 或不能续时按钮置灰并给出路。
- 流量包挂在一份上（设计稿 2.4）：选购页流量包标签多份时选「加到哪一份」，预选剩得最少的；没有在用的套餐时不卖。
