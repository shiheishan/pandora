---
paths:
  - "panel/internal/module/giftcard/**"
---

# 礼品卡模块

- 卡密明文保护与掩码：
  - 卡密是等价现金，完整明文只在两处出站：生码响应中的 4 张样例（`GenerateCodes`）、批次一次性导出（`ExportBatch`）。
  - 码表（`gift_card_codes`）只存哈希（`code_hash`）与掩码（`code_mask`）。批次明文封存在批次行上，导出时在同一事务标记 `exported_at` 并清空密文，第二次导出回 409 `ErrBatchAlreadyExported`。
  - 列表与明细仅展示掩码（`code_mask`）。
- 兑换事务与执行顺序：
  - `Redeem` 严格在同一事务中按序执行：拒已注销账号（identity `IsDeleted`，给用户行加共享锁，`ErrAccountDeleted` 409）→ 锁码（`CodeByHash` FOR UPDATE）→ 校验状态（未用）、有效期与模板状态 → 校验模板限制与每人限额（`LockUserTemplate`）→ 发放前判定并校验落点选择（落点失败回 422，事务回滚，卡不被消耗）→ 状态更新为 `used`（`UpdateCodeStatus` 乐观条件 `WHERE status = 'unused'`）→ 执行发放（`grant` 发放余额、时长、套餐、流量等）→ 写兑换流水（`InsertRedemption`）→ 审计与完成幂等。
  - 兑换失败对外统一返回中性错误 `ErrCodeUnusable`，防止利用不同报错探测卡密存在性。
- 模板字段：领取条件只有仅新用户 / 仅付费用户（付费记录经 order 的 `PaidHistory` 端口），使用限制只有每人限兑次数（`max_use_per_user`）。说明、主题色、需邀请、限定套餐（`gift_card_template_plans`）、冷却小时后台建不出，10-21 前后端连库删了（迁移 00065），门户卡面小字只按卡型给固定文案，不要加回。
- 套餐卡与落点机制：
  - 卡提供内容（`offerOf`）涉及订阅时，落点必须由用户选择（`Preview` 下发选项，`Redeem` 传入 choice 并在事务内二次校验）。
  - 套餐卡模板必须有价格档（库里 CHECK `gift_card_templates_plan_needs_price`）：订阅套餐选一档周期价，一次性套餐选它唯一的价；保存时价格必须在售且属于该套餐。
    价格后来被归档（改价或迁移 00056 归档了不合规的价）：兑换拒绝（「这张卡对应的套餐价格已下架，请联系客服」，码不被用掉），后台模板标出「价格已下架」，编辑时必须重选。
  - 套餐卡落点：同套餐的订阅套餐那份 → `subs.Renew`（套餐已归档回 `ErrRenewArchived`）；同套餐的一次性那份 → `subs.Stack`（选项 `stack`，兑换流水 plan_mode 记 `stacked`，00055 加进 CHECK）；
    别的套餐 → `subs.Provision`（新开）或 `subs.ChangePlan`（换掉，可跨类型，剩余价值经 order 的 `RemainingValue` 全额退余额）。一次性同款不套用「只有一份同款就直接续」，叠加与新开都给。
  - 送流量（通用卡、盲盒奖品）经 `subs.GrantBonus` 加到那一份的本期赠送（`bonus_value`；一次性加到总量）。用户没有可加流量的订阅回 `ErrNoTrafficTarget`，不再有「未分配」。
  - 通用卡的落点排除不适用的订阅：含加时长的排除永久的一次性订阅，含加流量的排除不限流量的订阅。盲盒抽中什么说不准，不排除；
    发放时落到永久那份就跳过加时长、落到不限流量那份就跳过流量（回执与审计 `skipped_expire_days` / `skipped_traffic_bytes` 写明），其余奖励照发。
  - 兑换流水 `gift_card_redemptions` 记录落地订阅 ID 及兑换快照。
- 数据巡检（report:integrity，见 `module-report-audit.md`「数据巡检」）：`service/integrity.go` 的 `CheckRedemptions` 找码与兑换流水对不上的卡（码 used 却没有流水，或有流水而码不是 used、`used_by` 不是兑换人），样例是码 id。兑换流水只追加、以它为准；运维手册第 16 章给按流水改回码、或无发放时把码放回未使用的 SQL。
