---
paths:
  - "panel/frontend/src/admin/screens/marketing/**"
---

# 后台 · 营销

- 礼品卡明文只在两处出现：生码响应里的前 4 张样例（`sample`）与批次的一次性导出 CSV；列表、使用记录、按筛选导出的报表一律掩码。不要在别处展示或拼出明文。
- 「导出当前筛选」走 `GET v1/gift-cards/codes/report`（要 reauth，无幂等），与卡码列表共用 `codeFilterQuery`；确认框写明明文只在批次的一次性导出里。
- 下载文件名优先取 `Content-Disposition`，幂等重放的响应没有这个头，所以兜底按批次 id 前 8 位自拼（`batchFileName` / `codesReportFileName`）。
- 卡码的行内停用 / 恢复不要 reauth：发现异常要能立刻止血（契约有意设计）。
- 后端恒回的字段在 schema 里必填、不写降级分支：`gift-cards/stats` 的 `balance_issued`，`commission/overview` 的 `total_earned` / `invited_users` / `scope`（后端缺设置时已兜底 `every_order`），`commission/config` 的 `scope`。
- 前端校验的错误键与后端 422 `fields` 同名（`logic.ts` 的表单构建），改字段名两边一起改。
- 佣金设置分两张表单：比例 / 冻结 / 最低提现 / 范围在 `Commission.tsx` 的 `Settings`；多级分销与提现开关、方式在 `CommissionLevels.tsx`（`commissionLevels.ts` 构建请求体，三级之和与开启全 0 的错误都挂 `distribution_l1`）。两张都发 `POST v1/commission/config`，只发自己那几项。
- 单独佣金设置（`CommissionOverrides.tsx`）：按邮箱设，比例或范围留空 = 跟全局，再保存整行覆盖；增删都要 reauth、不带幂等键。
- 打 USDT 提现的款：流水号填链上交易哈希（前端只看形状），实付 USDT 数量可选、十进制字符串；面板不发起链上转账，文案不要暗示会自动打款。
