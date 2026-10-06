---
paths:
  - "panel/frontend/src/admin/screens/billing/**"
---

# 后台 · 订单与收款

- 保留规则 6：设计稿的「欠费单」按后端「挂账」语义做——订单取消后才到账、续费或变更时订阅已结束、超额扣款，是平台欠用户的钱；「转入余额」是贷记，待处理合计按币种分开（分和美分不能相加）。不要改回「欠费 / 催缴」的语义。
- 支付渠道开关只动 `accepting_new`（关 = 结账页不再显示、进行中的回调照常）；「完全停用」才动 `enabled`，放在「更多」菜单里。系统内置的 `offline` 渠道只读，不给开关（`model.ts` 的 `isOffline` / `toggleBody`）。
- 人工开单不提供「从余额扣除」（D-C-3 已决），弹窗里引导先到用户详情调账、再用赠送开单。
- 取消订单、标记已支付的失败文案（凭证号重复、订阅已结束款项进挂账都回 409）是后端中文原文，经 `useFailure` 原样 Toast，不要按状态码改写。
- 会改用户余额或订阅的写（标记已支付、向渠道查单补记、人工开单、转入余额）成功后，除了 `useInvalidateBilling` 还要调 `users/api.ts` 的 `useInvalidateUsers`。
- 「向渠道查单」只对发起过支付的待支付 / 处理中订单出现（`canQueryChannel`：`provider_code` 非空且非 offline）；查单与取消不挂 reauth，人工开单与标记已支付挂 reauth。
- 用户抽屉的「为其开单」跳 `#/billing/orders?new=<用户 id>`，订单页靠 `?new=` 打开人工开单弹窗并预选用户；改这个参数要同步 `users/UserDrawer.tsx`。
