---
paths:
  - "panel/frontend/src/admin/screens/marketing/**"
---

# 后台 · 营销

- 礼品卡明文只在两处出现：生码响应里的前 4 张样例（`sample`）与批次的一次性导出 CSV；列表、使用记录、按筛选导出的报表一律掩码。不要在别处展示或拼出明文。
- 「导出当前筛选」走 `GET v1/gift-cards/codes/report`（要 `marketing.giftcard.read` + `ops.export`，reauth，无幂等），与卡码列表共用 `codeFilterQuery`；确认框写明明文只在批次的一次性导出里。
- 下载文件名优先取 `Content-Disposition`，幂等重放的响应没有这个头，所以兜底按批次 id 前 8 位自拼（`batchFileName` / `codesReportFileName`）。
- 卡码的行内停用 / 恢复不要 reauth：发现异常要能立刻止血（契约有意设计）。
- 后端恒回的字段在 schema 里必填、不写降级分支：`gift-cards/stats` 的 `balance_issued`，`commission/overview` 的 `total_earned` / `invited_users` / `scope`（后端缺设置时已兜底 `every_order`），`commission/config` 的 `scope`。
- 套餐名与套餐卡要 `catalog.read`：没有时不请求目录，退回「N 个套餐」、套餐卡不可编辑；兑换记录另要 `billing.order.read`。
- 前端校验的错误键与后端 422 `fields` 同名（`logic.ts` 的表单构建），改字段名两边一起改。
