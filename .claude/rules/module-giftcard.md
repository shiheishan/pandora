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
- 套餐卡与落点机制：
  - 卡提供内容（`offerOf`）涉及订阅时，落点必须由用户选择（`Preview` 下发选项，`Redeem` 传入 choice 并在事务内二次校验）。
  - 套餐卡兑换（`grantPlan` 调用 `subs.Provision` / `subs.Renew` / `subs.ChangePlan`）：直接走订阅模块的开通、续费或换套餐；兑换流水 `gift_card_redemptions` 记录落地订阅 ID 及兑换快照。
