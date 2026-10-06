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
- 权限只看 `GET v1/me` 的 `permissions`，不硬编码角色
  - 新增模块或标签时，在 `modules.ts` 登记读权限码，取契约里该模块主列表接口的权限
  - 侧栏、⌘K、页头标签都据此隐藏入口
  - me 还没回来时一律按无权限处理，宁可少显示
- 缺权限与不存在用同一句「无权限或不存在」（`shell/ScreenFrame.tsx` 的 `NotFoundScreen`、`ui/QueryView`），因为后端缺权限时回 404，不能透露模块是否存在
- 与设计稿不同的产品命名：
  - 「欠费单」叫「挂账」（保留规则 6，契约后台-05）
  - 套餐模块多一个设计稿没有的「流量包」标签，与套餐共用读权限
- 改密码成功后，后端已在同一事务里吊销全部会话：前端直接清令牌回登录页，不再发任何请求
- 侧栏徽标与仪表盘「需要处理」共用 `tasks.ts` 的同一条查询和同一种缓存形状；只在有 `ops.dashboard.read` 时请求
- `Sidebar.module.css` 在侧栏内把 `--surface`、`--text` 等语义令牌重定义成深色，`ui/Menu` 等组件因此能直接用在深底上；侧栏里的组件不要另写一套深色样式

## 门户（src/portal）

- 门户没有 reauth，`createAppRuntime('portal')` 不传 `requestReauth`
- 外来链接不能用两段式路径（public 网关根下有订阅通配）
  - 邀请链接用 `/?invite=CODE`，渲染前就取走
  - 快捷登录链接用 `#/quick-login/<token>`，加载时即消费并抹掉
  - 生成与识别的格式只写在 `entry-links.ts` 一处
- 快捷登录只收已登录设备在账号安全页生成的链接或令牌（保留规则 1，与设计稿不同）
- 外观：生效主题的颜色经 CSSOM 写到 `<html>`，不注入 `<style>`
  - 键的白名单是 `styles/design-tokens.ts` 的 `COLOR_TOKENS`
  - Logo 只认 `data:image/`，因为 CSP 的 img-src 只放行 `'self' data:`
- 外框与页面共用同一个查询键的读接口，schema 必须是全字段，各处经 `select` 取自己要的部分
  - 涉及 `queries.ts` 的 `SUBSCRIPTIONS_KEY`、`COMMISSION_KEY`、`['portal','me']`、`['portal','balance']`
  - 页面不要另开键，也不要收窄这些 schema
- 多条订阅时，主订阅取 `pickPrimary`：生效订阅里 `current_period_end` 最晚的那条。外框徽标、概览、选购页都按它
- 未读数没有 SSE 推送，按契约 60 秒轮询一次 `limit=1`
- 门户外框（`*.module.css`）只用 960 与 640 两个断点
