---
paths:
  - "panel/internal/domain/giftcard/**"
---

# 礼品卡

- 卡码是等价现金，完整明文只在两处出站：生码响应里的 4 张样例、批次的一次性导出（`exported_at` 只能打一次，第二次 409）。其余读模型（列表、兑换记录、门户）一律经 `MaskCode` 掩码，`MaskCode` 是唯一的 Go 掩码实现
- 按筛选导出的对账报表连明文都不取：掩码由 `maskedCodeSQL` 在 SQL 里算好再扫描，必须与 `MaskCode` 同形。守卫 `codes_export_test.go:TestCodesExportSQLReadsOnlyTheMask`、`TestMaskedCodeSQLMirrorsMaskCode`、`batches_test.go:TestGiftCardReadModelsCarryOnlyMaskedCodes`
- 卡码列表与掩码报表共用 `CodeFilter` 的筛选与 SQL 条件，运营在列表里看到多少行，导出的就是那些行
- 发放余额、流量、延期、开套餐不在本包实现，由 billing 经 `Granter` 接口注入；本包只管模板、码、批次与兑换流水
- 套餐卡（2026-10-07）：`Granter.GrantPlan` 带卡密，回落地方式 mode（new / renewed / changed，常量 `PlanGrant*` 与 billing 一一对应）与换套餐退回余额的金额、币种；兑换流水 granted 记 plan_mode / plan_refund / refund_currency。换套餐的退款证据挂在卡密上，提交时由 00134 的守卫对兑换流水核对，所以兑换流水必须和发放在同一事务
- 落点（购买模型统一 Q8）：这张卡落到哪一份由人选。`cardOffer` 决定要落地的是什么（套餐、加时长、重置、送流量、多项奖励；盲盒按奖池可能抽到的算），选项与默认值由计费域 `Granter.Placements` 按 `purchase.Options` 给出（展示结构 `PlacementView` = `purchase.Placement`）。`PreviewCode` 带 userID 回 `placement {question, options, default_key}`，纯余额卡为 null；`Redeem` 带 `choice`，在兑换事务里重新 Match：失效或多个选项没选回 422，事务回滚，卡不会被用掉；一个选项都没有时送流量记为未分配，其余由计费域拒绝。兑换流水 granted 记 `subscription_id`
- 兑换在一个 Serializable 事务里：锁码 → 校验状态与条件 → 经 Granter 发放 → 写流水 → 标记已用 → 审计，顺序不能调。对外所有「码不能用」的情况回同一句话，不能让人借错误文案区分码是否存在
