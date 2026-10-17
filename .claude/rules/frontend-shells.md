---
paths:
  - "panel/frontend/src/admin/*"
  - "panel/frontend/src/portal/*"
---

# 面板前端：后台与门户的外框

## 后台（src/admin）

- reauth 只在后台入口接线：`main.tsx` 建 `ReauthController`，作为 `requestReauth` 传给 `createAppRuntime`
  - 常驻的 `ReauthDialog` 接住任何页面写请求的 403 `reauth_required`，验证通过后 `api.ts` 用原幂等键重放
  - 页面自己不弹「重新验证」，也不做「先弹框后执行」：契约 1.6 规定先请求、按需弹框
- 写失败统一交给 `actions.ts` 的 `useFailure`：reauth 被取消时静默，有 `fields` 就标到表单，其余用 Toast
  - 带幂等键的写操作要传 `{ fields, intent }`，键的去留由这一处按 `endsIntent` 决定
  - 自己先处理 4xx 分支、不经 `useFailure` 的写操作，在 catch 开头直接调用 `endsIntent`
- 后台权限只分有和没有：登录了就是管理员，能做全部操作
  - 前端不按权限隐藏任何入口、按钮或页签；侧栏、⌘K、页签栏、仪表盘卡片全部照 `modules.ts` 的登记显示
  - 页面不等 `GET v1/me` 回来再渲染；me 只给头像与名字用（`me.ts`），身份一律显示「管理员」
- 外框布局（样稿 `dev/prototype/admin-v4.html`，av4 落地）：`Shell.tsx` 是两栏网格（侧栏 232 + 主区），页面随文档滚动；**桌面（≥ 960）没有顶栏**
  - 侧栏（`Sidebar.tsx`，`id="admin-sidebar"`，`position: sticky` 贴顶，z-index 20 让侧栏里的弹层盖住内容）自上而下：环形 Logo + 站点名 + 版本徽标（`__APP_RELEASE__`）；搜索框（点开 ⌘K，显示 ⌘K 提示）；`<nav aria-label="主导航">`（浏览器测试的入口，不要改）——第一项「实时动态」（`EventsTrigger variant="nav"`），然后照 `NAV_GROUPS` 分组（第一组标题为 null 不画组名：仪表盘、工单 / 业务 / 网络 / 设置）；工单提醒卡（`tasks.ts` 的 `ticketAlert`，仪表盘与工单页不显示，点进工单）；账户区：头像 + 名字（ui `Menu` 向上弹出：名字 + 邮箱、修改密码、绑定 Telegram、外观「浅色 / 深色」choice、退出登录），旁边「打开用户端」（`../`，新标签）与主题切换两个图标按钮
  - 导航徽标（`navBadges`）：工单（红，`hot`）、订单与收款 `orders_pending_stale`、营销 `withdrawals_pending`、节点 `nodes_offline`；数字 `aria-hidden`，不进链接的无障碍名称（浏览器用例按精确名点导航）
  - 侧栏收起成图标栏的功能与 `pandora-admin-sidebar` 存储键已删，不再恢复
  - 窄于 960（`useMedia('(max-width: 959px)')`）：主区顶上一条 52 高的手机顶栏（`Topbar.tsx`：汉堡「打开导航」+ 页名 + 操作插槽 + `EventsTrigger variant="top"`），侧栏变成 272 宽的抽屉：遮罩、Esc、换页、点遮罩关闭，收着时 `inert`
  - 页头由外框画（`Shell.tsx`）：20px 模块名 + 计数插槽 + 弹性空白 + 「?」（`PageHelp.tsx`，32 的图标按钮，悬停或聚焦展开阴影卡片，手机隐藏）+ 操作插槽；下面是页签栏（ui `Tabs`）。内容列最宽 1120、页边距 28 / 32 / 72（手机 24 / 16）
  - 插槽：外框把计数格与操作格的元素经 `kit/PageActions` 的 `PageActionsProvider` 交下去，页面用 `PageActions` / `PageCount` portal 进来（照门户 `PageHeadAside` 的做法）。手机（≤ 639，`useMedia('(max-width: 639px)')`）页头标题行整行隐藏，操作格换成顶栏右侧那格（`Topbar` 的 `actionsRef`），按钮压到 30 高
  - 三种外框模式（`modules.ts`）：详情 `isDetailRoute(module, tab, rest)`（`DETAIL_ROUTES` 登记的「模块:页签」且 rest 非空；不能写成「任何 rest 非空」，路由页把 rest 当范围用）——不画页头与页签栏；全幅 `fullBleed`（工单）——不画页头与页签栏、内容区无边距，给页面一块 `100vh`（窄屏减 52）的纵向 flex 容器；自画页头 `ownHead`（仪表盘）——不画标题行
  - 模块图标与外框图标在 `nav-icons.ts`（Hugeicons Pro），新模块要同时登记图标
- 新增模块或页签：`modules.ts` 的 `MODULES` 与 `NAV_GROUPS`、`nav-icons.ts`；不再登记权限码
  - 页签已登记、页面还没接上的，放进 `modules.ts` 的 `PENDING_TABS`（外框画空状态，不交给模块页面分发）；接好页面后删掉那一项
- 页面级说明只放页头标题行右侧的「?」（`PageHelp.tsx`，悬停或聚焦展开），数据在 `page-help.ts` 的 `PAGE_HELP`：键是「模块:页签」，没有页签的模块用模块名；值是 `{ label, sections: [{ title, ordered?, items }] }`，要点是字符串或 `{ term, text }`。各模块只改这份数据，不在页面里写说明段落；字段旁的就地提示不受此限
- ⌘K（`CommandPalette.tsx`）：候选来自 `modules.ts` 的 `paletteGroups(query)`——空查询一组「快速跳转」列全部模块，有查询一组「页面」列模块、「模块 › 页签」与 `PALETTE_EXTRAS` 深链；实体搜索经组件的 `sources` 传入，分组接在「页面」之后
  - 实体搜索（10-16）：`entity-search.ts` 的 `useEntitySearch` 在有查询时并行查 `GET v1/users`、`v1/orders`、`v1/nodes`（`q`、`limit=4`），分「用户」「订单」「节点」三组接在「页面」之后、`sources` 之前；防抖 200ms，查询键带搜索词、`signal` 传给 `api.get`，搜索词变了旧请求由 react-query 取消；某组失败就不显示那组，不报错；没有结果且还在查时显示「正在搜索…」
  - 实体候选带 `path`（`EntityItem`），选中直接 `navigate` 到那一条：`/users/list/<用户id>`、`/billing/orders/<订单id>`、`/nodes/nodes/<节点id>`；只用本文件里的最小 schema 与状态文案，不 import `screens/**`（外框不把模块页面拉进主包）
- 实时动态（`EventsCapsule.tsx`，纯函数在 `events.ts`）：`EventsProvider` 在 `Shell.tsx` 里包一次，是唯一的数据源（`useRealtime` 只调一次，每个入口只开一条 SSE）；`EventsTrigger` 有两种外观——侧栏第一项（`nav`，弹层贴侧栏右侧）与手机顶栏按钮（`top`，弹层横铺在顶栏下），共用同一个弹层内容。按 topic + table + op 生成通用条目（事件只说哪一行变了，没有业务内容），每条有未读点、「全部标为已读」，页签 全部 / 订单 / 订阅 / 工单 / 节点 / 其他（按现有 topic 归类，不新增事件类型）；最多留 30 条，已读只在内存，刷新即清（10-16 用户定，不持久化）
  - 点事件标已读并跳到对应页：订单、工单、节点、服务器、套餐有 id 时直接打开那一条（工单消息、订阅、删除事件退回列表页）；底部「审计日志 →」去 `/security/audit`
- 接口回 404 时统一显示「不存在或已删除」（`ui/QueryView`），不提权限
- 与设计稿不同的产品命名：
  - 「欠费单」叫「待处理到账」（10-16 设计稿，取代原「挂账」）
- 改密码成功后，后端已在同一事务里吊销全部会话：前端直接清令牌回登录页，不再发任何请求
- 浏览器标题（`Shell.tsx`、`LoginPage.tsx`）、登录页页脚与侧栏品牌区随站点名：已登录读 `site.ts` 的 `useAdminSiteName()`（`v1/settings/site`，与内容页共用缓存），登录前读服务端写进入口页的 `<meta name="application-name">`（`documentSiteName()`，开发期没有就是 Pandora）；侧栏只画环形标记加站点名文字，不写死 Pandora
- 2 小时无操作自动退出（`idle.ts` 的 `startIdleWatch`，在 `Shell.tsx` 挂上）：后台有定时轮询，服务端按 last_seen 判的空闲在开着的标签页上永远不会触发，所以前端只把点击、按键、滚轮、触摸算作操作，多个标签页经 localStorage 共享最近操作时间
- 侧栏徽标与仪表盘「待办」共用 `tasks.ts` 的同一条查询和同一种缓存形状

## 门户（src/portal）

- 门户没有 reauth，`createAppRuntime('portal')` 不传 `requestReauth`
- 外框布局（样稿 v5，`Shell.tsx`；组件套件见 `portal-kit.md`）：
  - 桌面（≥ 960）：左侧固定窄导航 232 宽（Logo = 站点名展示字体 + 绿点；三组 `pages.ts` 的 `SIDE_GROUPS`：我的套餐 / 选购 / 钱包 ‖ 订单 / 邀请返利 ‖ 消息 / 工单支持 / 帮助中心 / 账号安全；当前项白底浮起；「订单」计数 = 待支付笔数 `usePendingOrderCount`，「消息」计数 = 未读数）。`<nav aria-label="主导航">` 与前三项的名字是浏览器测试的入口，不要改
  - 侧栏底部账户按钮（头像首字、名字、余额），点开在上方弹出账户菜单（`role="menu"`）：名字 + 当前套餐 chip + 邮箱、钱包（余额）、账号安全、外观「浅色 / 深色」（`menuitemradio`）、退出登录；点外面、Esc、Tab、换页收起，方向键在项之间移动
  - 手机（≤ 959）：没有侧栏，内容列顶上只有 Logo；底部悬浮标签栏四项 `TAB_PAGES`（套餐 / 选购 / 钱包 / 我的），订单、邀请、消息、工单、帮助、账号安全都从「我的」页进，这些页上「我的」高亮（`tabOwner`）；「我的」上的小红点 = 有未读消息或待支付订单
  - 内容列 720 宽，`WIDE_PAGES` 1040；页标题 h1 在列里（`HIDDEN_TITLE_PAGES` 只给读屏）；标题行右侧插槽 `PageHeadAside`；换页整列重挂载触发 rise 进场
  - `#/me` 只在手机有：`resolvePage(path, { desktop })` 在 `matchMedia('(min-width: 960px)')` 为真时把它落回 `#/subs`（窗口拉宽同样跳回）；`#/overview` 已删，按未知页面落回 `#/subs`
- 外来链接不能用两段式路径（public 网关根下有订阅通配）
  - 邀请链接用 `/?invite=CODE`，渲染前就取走
  - 快捷登录链接用 `#/quick-login/<token>`，加载时即消费并抹掉
  - 邮件登录链接用 `#/mail-login/<token>`（`entry-links.ts` 的 `mailLoginTokenFromHash`），同样加载即消费并抹掉，失败落到登录页「用邮件里的链接登录」子页并显示原因；带 `?invite=` 打开门户时上报一次邀请访问（`POST v1/invite/visit`，失败忽略）
  - 登录页读公开的 `v1/signup-config`：`mail_link_login` 为 true 才在登录按钮下给「用邮件里的链接登录」入口；`captcha` 非空时注册第 1 步、找回第 1 步、邮件登录第 1 步渲染人机验证（`Captcha.tsx` 按需加载第三方脚本，不写内联脚本），`email_suffixes` 非空时邮箱框提示可用后缀
  - 生成与识别的格式只写在 `entry-links.ts` 一处
- 登录页（10-16 样稿 v5）：站点名与标语、公告、白卡片、页脚写注册方式；卡片里登录 / 注册两个页签（`v1/site-config` 的 `registration_mode` 为 closed 时注册页签整个藏掉，带邀请码打开也落到登录）；邀请码只有 invite_only 必填，open 模式下填了无效码的 403 落在邀请码框；邮件链接登录、快捷登录是登录按钮下的次级入口，找回密码从「忘记密码？」进，三者都是带返回的子页；后台关邮箱验证时注册只有一步
- 快捷登录只收已登录设备在账号安全页生成的链接或令牌（保留规则 1，与设计稿不同）
- 外观：生效主题的颜色经 CSSOM 写到 `<html>`，不注入 `<style>`
- 站点名取 `GET v1/appearance` 的顶层 `site_name`（系统设置 `site.name`），不读主题 branding；标语与 Logo 才取生效主题。服务端下发入口页时已把站点名写进 `<title>`，`useAppearanceTheme` 接口回来后照样同步 `document.title`
  - 键的白名单是 `styles/design-tokens.ts` 的 `COLOR_TOKENS`
  - Logo 只认 `data:image/`，因为 CSP 的 img-src 只放行 `'self' data:`
- 外框与页面共用同一个查询键的读接口，schema 必须是全字段，各处经 `select` 取自己要的部分
  - 涉及 `queries.ts` 的 `SUBSCRIPTIONS_KEY`、`COMMISSION_KEY`、`PENDING_ORDERS_KEY`（`v1/orders?status=pending_payment`，侧栏订单计数与我的套餐页的待支付横条共用）、`['portal','me']`、`['portal','balance']`
  - 页面不要另开键，也不要收窄这些 schema
- 多条订阅时，主订阅取 `pickPrimary`：生效订阅里 `current_period_end` 最晚的那条。账户菜单与「我的」页的套餐 chip、选购页都按它
- 未读数没有 SSE 推送，按契约 60 秒轮询一次 `limit=1`
- 门户外框（`*.module.css`）只用 960 与 640 两个断点；外框的手机布局都在 959 以下
