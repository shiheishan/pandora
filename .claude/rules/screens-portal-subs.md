---
paths:
  - "panel/frontend/src/portal/screens/overview/**"
  - "panel/frontend/src/portal/screens/subs/**"
  - "panel/frontend/src/portal/screens/plans/**"
---

# 门户 · 概览、我的订阅与选购

- 主订阅一律用外框 `portal/queries.ts` 的 `pickPrimary`：status ∈ {active, trialing, grace, past_due} 中 `current_period_end` 最晚的一条；没有时回落到能原地续费的已过期订阅（status=expired 且 renewable，`renewableExpired`，w5expiry）；外框徽标、概览、选购页、我的订阅同一口径。已过期的显示「已过期」、链接只读（`isExpiredView`，不给更换），买同套餐走续费、别的套餐走改套餐
- 到期展示精确到分钟（`common/traffic.ts` 的 `formatMinute` / `expiryInfo`），最后 24 小时写「还剩 X 小时」，过了写「已于 … 到期」
- 保留规则 3：可用节点只有名称 / 协议 / 倍率，删去设计稿的国家徽标与负载列。节点接口对 past_due 等状态回 404，显示「当前没有可用节点」而不是报错。
- 更换订阅地址接口不幂等（每调一次再换一次）：用 ConfirmModal 先说后果，请求期间禁用按钮；还没有地址时直接给「重新生成」。
- 概览在线设备、设备上限、重置日必回：设备上限 null 写「不限」，重置日为 null 时退回额度行或按日用量接口的周期末（`common/traffic.ts` 的 `resetAtOf`）；没有订阅时在线设备显示「—」。
- 设计稿之外补了三处（均有契约依据）：critical 公告顶部横幅、grace / past_due 徽标、流量包余量并入剩余流量并注明「含流量包」。
- 套餐卡特性顺序：设备数、限速「限速 N Mbps」（不限速不显示）、卖点 highlights（按后台给的顺序）；`recommended` 为真时显示「推荐」并用强调样式，流量包的 recommended 显示「最划算」。
- 流量包规则按 D-E-1（挂用户、永不过期、可叠加、有生效订阅才消耗），删去设计稿「没有订阅也能买并单独使用」一条。
- 选购入口只拼地址不判模式：当前套餐 → `#/checkout?renew=&price=`，其余 → `#/checkout?plan=&price=`；目标套餐 `allow_upgrade=false` 或当前套餐不可续费时按钮置灰（`plans/labels.ts` 的 `planAction`）。
