---
paths:
  - "panel/internal/domain/giftcard/**"
---

# 礼品卡

- 卡码是等价现金，完整明文只在两处出站：生码响应里的 4 张样例、批次的一次性导出（`exported_at` 只能打一次，第二次 409）。其余读模型（列表、兑换记录、门户）一律经 `MaskCode` 掩码，`MaskCode` 是唯一的 Go 掩码实现
- 按筛选导出的对账报表连明文都不取：掩码由 `maskedCodeSQL` 在 SQL 里算好再扫描，必须与 `MaskCode` 同形。守卫 `codes_export_test.go:TestCodesExportSQLReadsOnlyTheMask`、`TestMaskedCodeSQLMirrorsMaskCode`、`batches_test.go:TestGiftCardReadModelsCarryOnlyMaskedCodes`
- 卡码列表与掩码报表共用 `CodeFilter` 的筛选与 SQL 条件，运营在列表里看到多少行，导出的就是那些行
- 发放余额、流量、延期、开套餐不在本包实现，由 billing 经 `Granter` 接口注入；本包只管模板、码、批次与兑换流水
- 兑换在一个 Serializable 事务里：锁码 → 校验状态与条件 → 经 Granter 发放 → 写流水 → 标记已用 → 审计，顺序不能调。对外所有「码不能用」的情况回同一句话，不能让人借错误文案区分码是否存在
