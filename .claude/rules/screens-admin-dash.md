---
paths:
  - "panel/frontend/src/admin/screens/dash/**"
---

# 后台 · 仪表盘

- 仪表盘登录即可进，没有自己的数据：每张卡按自己接口的权限决定发不发请求、画不画（缺权限不发请求，接口 404 也按无权限隐藏），各自加载、各自失败、各自重试，一张卡坏了不能牵连别的卡。新增卡片照此办。
- 「需要处理」和侧栏徽标共用 `src/admin/tasks.ts` 的同一条 `GET v1/dashboard/tasks` 查询与 schema，不在本目录另起。
- 流量与积压三个接口的字节一律是十进制字符串（numeric 聚合，可能超过 2^53），不是 JSON number。
- 数据到卡片、行、文案的映射全部放 `model.ts`（纯函数、全测），组件只负责摆放。
- 图表手写，不引图表库；柱高与条宽是仅有的动态 style。
- 格式化（字节、计数、金额、时间）一律取 `core/format.ts`。
- 卡片与行的链接只在目标页对当前管理员可读时出现（`model.ts` 的 `reachable`，按 `modules.ts` 判断）。
- 用户流量排行只显示脱敏邮箱（`email_masked`）。
