---
paths:
  - "panel/frontend/src/admin/screens/**"
---

# 后台页面通用约定

- 页面只改自己模块目录。`admin/screens/index.ts` 的 `SCREENS` 登记表：新建模块按 new-admin-module skill 登记，其余情况不动；标签读权限在 `admin/modules.ts` 的 `MODULES` 里由 Shell 先判，页面内只按写权限（`useCan`）隐藏按钮。
- 页面内接口回 404 一律按「无权限或不存在」处理（缺权限的接口回 404），不当作报错。
- 选中项、筛选、搜索、分页、抽屉标签都放在地址上（`rest` 路径段 + 查询串），刷新、分享、前进后退要落在同一处；改地址用 `core/router` 的 `navigate` / `href`，筛选类回写用 `replace`。
- 模块内分层沿用：schemas（zod）→ queries / api（查询键前缀，写后按前缀整体失效）→ logic / model（纯函数，配单测）→ 组件；新逻辑放进纯函数层并补测试，组件只负责摆放。
- zod schema 按 Go 实际编码写严：无 omitempty 的字段必填，指针字段 nullable，omitempty 字段 optional，nil 切片 nullable 后归一成 `[]`；后端恒回的字段不要为它写降级分支，也不要为了「兼容」放宽 schema。
- 各模块的 schema（`schemas.ts` / `api.ts`）同时被 `panel/frontend/tests/mock-admin-<模块>.test.ts`（核对 dev 假后端）与 `tests/smoke/`（对真实网关冒烟）引用，改 schema 要连这两处一起跑、一起改。
- 写失败一律走 `admin/actions.ts` 的 `useFailure`，带幂等键的写把 `intent` 一并交进去；自己先处理某些 4xx（如 409 标到表单）的分支，在 catch 开头用 `endsIntent` 定键的去留。reauth 由外框常驻对话框接管，取消时静默，页面不另写。
- 幂等键一次用户意图一把（`useIntentKey().keyFor(指纹)`），改了请求体自然换新键；没挂幂等中间件的接口不带键。
- 跨模块共用的东西改之前先看引用方：
  - `billing/schemas.ts` 的订单列表行也是用户详情「最近订单」的形状（`users/api.ts` 从这里取）
  - `users/model.ts` 的 `ORDER_STATUS_VIEW` / `orderWhat` / `parseYuan` 与 `plans/model.ts` 的周期文案被订单页复用
  - `plans/api.ts` 的 `planOptionsKey` 给用户、内容两个模块各自查套餐目录，挂在 `PK` 下，套餐页写后一并失效
  - `users/api.ts` 的 `useUserGroups` 被套餐页（可见用户组、组专属价）借用
- 设计稿之外按契约补了不少功能（各模块的筛选、确认框、抽屉、状态等），照设计稿改版时不要把它们删掉。
