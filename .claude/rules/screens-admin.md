---
paths:
  - "panel/frontend/src/admin/screens/**"
---

# 后台页面通用约定

- 页面只改自己模块目录。`admin/screens/index.ts` 的 `SCREENS` 登记表：新建模块在这里登记，其余情况不动；后台权限只分有和没有，页面不按权限隐藏入口或按钮，新模块只在 `admin/modules.ts` 的 `MODULES` 登记（见 frontend-shells.md）。
- 页面内接口回 404 统一显示「不存在或已删除」（`ui/QueryView`），不提权限。
- 选中项、筛选、搜索、分页、抽屉标签都放在地址上（`rest` 路径段 + 查询串），刷新、分享、前进后退要落在同一处；改地址用 `core/router` 的 `navigate` / `href`，筛选类回写用 `replace`。
- 「打开某一条」的地址被外框引用，改路径时同步改外框：`/users/list/<用户id>`、`/billing/orders/<订单id>`、`/nodes/nodes/<节点id>`（⌘K 实体搜索，`admin/entity-search.ts`），再加 `/tickets/<工单id>`、`/nodes/servers/<服务器id>`、`/plans/<套餐id>`（实时事件，`admin/events.ts`）
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
- 10-16 起按设计稿显示效果还原：设计稿没显示的功能前后端一起删，安全项保留（见 redesign-2026-10-16.md）。
- 外壳（10-16）：页头没有面包屑和标题，页签栏直接在顶；页签右端的「?」读 `admin/page-help.ts` 的 `PAGE_HELP`，键是 `模块:页签`（没有页签的模块用模块名）。新页面或改页签时在那里补一条（`{label, sections:[{title, ordered?, items}]}`，items 是字符串或 `{term, text}`），`admin.test.ts` 核对每个键都落在真实页面上。
- `admin/modules.ts` 的 `PENDING_TABS` 是「还没按新设计做出来」的页签，外壳给它们显示空状态；页面接好后从集合里删掉那一项。套餐模块没有页签，详情路径是 `/plans/<id>`。
- 共用组件（都从 `ui/index.ts` 取）：
  - `Modal`：`size` 取 `'sm'|'md'|'lg'`（400 / 560 / 720）或 `ModalWidth` 数值（400、440、480、520、560、640、680、720、760），另有 `subtitle`、`headExtra`、`footerStart`、`tall`、`divided`、`bodyClassName`；结构是头 / 体 / 底，体内滚动
  - `Select`：自绘 listbox，props 与旧的原生版兼容，`onChange` 的 `e.target.value` 照旧可用；不要再往里塞 `<option>`
  - `FilterTabs`（带计数的筛选页签，`items: {value,label,count?}[]`）、`Toolbar` + `SearchField`（列表上方的筛选条，`SearchField` 必须给 `aria-label`）
  - `Table`：列可设 `sortable`，配 `sort` / `onSortChange`；另有 `footer`、`stickyHeader`
- 金额只有人民币：`formatMoney(minor)` 只收一个参数；带币种的两参写法是过渡用的 `@deprecated` 重载，改到哪个模块就去掉哪里的第二个参数，全部改完删重载。
