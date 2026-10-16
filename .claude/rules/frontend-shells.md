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
  - 前端不按权限隐藏任何入口、按钮或标签；侧栏、⌘K、页头标签、仪表盘卡片全部照 `modules.ts` 的登记显示
  - 页面不等 `GET v1/me` 回来再渲染；me 只给头像与名字用（`me.ts`），身份一律显示「管理员」
  - 新增模块或标签时，只在 `modules.ts` 与侧栏 `NAV_GROUPS` 登记，不再登记权限码
- 接口回 404 时统一显示「不存在或已删除」（`ui/QueryView`），不提权限
- 与设计稿不同的产品命名：
  - 「欠费单」叫「待处理到账」（10-16 设计稿，取代原「挂账」）
- 改密码成功后，后端已在同一事务里吊销全部会话：前端直接清令牌回登录页，不再发任何请求
- 浏览器标题（`Shell.tsx`、`LoginPage.tsx`）、登录页页脚与侧栏品牌区随站点名：已登录读 `site.ts` 的 `useAdminSiteName()`（`v1/settings/site`，与内容页共用缓存），登录前读服务端写进入口页的 `<meta name="application-name">`（`documentSiteName()`，开发期没有就是 Pandora）；站点名是 Pandora 时侧栏照旧画 pandora 字标
- 2 小时无操作自动退出（`idle.ts` 的 `startIdleWatch`，在 `Shell.tsx` 挂上）：后台有定时轮询，服务端按 last_seen 判的空闲在开着的标签页上永远不会触发，所以前端只把点击、按键、滚轮、触摸算作操作，多个标签页经 localStorage 共享最近操作时间
- 侧栏徽标与仪表盘「需要处理」共用 `tasks.ts` 的同一条查询和同一种缓存形状
- `Sidebar.module.css` 在侧栏内把 `--surface`、`--text` 等语义令牌重定义成深色，`ui/Menu` 等组件因此能直接用在深底上；侧栏里的组件不要另写一套深色样式

## 门户（src/portal）

- 门户没有 reauth，`createAppRuntime('portal')` 不传 `requestReauth`
- 外来链接不能用两段式路径（public 网关根下有订阅通配）
  - 邀请链接用 `/?invite=CODE`，渲染前就取走
  - 快捷登录链接用 `#/quick-login/<token>`，加载时即消费并抹掉
  - 邮件登录链接用 `#/mail-login/<token>`（`entry-links.ts` 的 `mailLoginTokenFromHash`），同样加载即消费并抹掉，失败落到登录页「邮件登录」标签并显示原因；带 `?invite=` 打开门户时上报一次邀请访问（`POST v1/invite/visit`，失败忽略）
  - 登录页读公开的 `v1/signup-config`：`mail_link_login` 为 true 才有「邮件登录」标签；`captcha` 非空时注册与找回第 1 步渲染人机验证（`Captcha.tsx` 按需加载第三方脚本，不写内联脚本），`email_suffixes` 非空时邮箱框提示可用后缀
  - 生成与识别的格式只写在 `entry-links.ts` 一处
- 快捷登录只收已登录设备在账号安全页生成的链接或令牌（保留规则 1，与设计稿不同）
- 外观：生效主题的颜色经 CSSOM 写到 `<html>`，不注入 `<style>`
- 站点名取 `GET v1/appearance` 的顶层 `site_name`（系统设置 `site.name`），不读主题 branding；标语与 Logo 才取生效主题。服务端下发入口页时已把站点名写进 `<title>`，`useAppearanceTheme` 接口回来后照样同步 `document.title`
  - 键的白名单是 `styles/design-tokens.ts` 的 `COLOR_TOKENS`
  - Logo 只认 `data:image/`，因为 CSP 的 img-src 只放行 `'self' data:`
- 外框与页面共用同一个查询键的读接口，schema 必须是全字段，各处经 `select` 取自己要的部分
  - 涉及 `queries.ts` 的 `SUBSCRIPTIONS_KEY`、`COMMISSION_KEY`、`['portal','me']`、`['portal','balance']`
  - 页面不要另开键，也不要收窄这些 schema
- 多条订阅时，主订阅取 `pickPrimary`：生效订阅里 `current_period_end` 最晚的那条。外框徽标、概览、选购页都按它
- 未读数没有 SSE 推送，按契约 60 秒轮询一次 `limit=1`
- 门户外框（`*.module.css`）只用 960 与 640 两个断点
