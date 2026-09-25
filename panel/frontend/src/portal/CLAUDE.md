# panel/frontend/src/portal/
> L2 | 父级: /panel/frontend/CLAUDE.md

用户门户入口（用户门户.dc.html 外壳）。未登录是 AuthPage（登录 / 两步注册 / 快捷登录），登录后是 Shell（粘性顶栏、页头、内容区、页脚，< 640 底部标签栏）；内容区按路由从 screens/ 登记表取懒加载的页面，包在 shell/ScreenFrame 里。门户没有 reauth。
路由是 hash：#/<页面>[/<rest>…]，未知页面由 Shell replace 成概览，合法页面后的段作为 rest 交给页面（#/orders/<订单 id>、#/tickets/<id>），查询串（#/checkout?...）不受规范化影响。
两类外来链接在入口处理：邀请链接 /?invite=CODE 在渲染前取走（存 sessionStorage、抹掉查询串、登录页直接落在注册标签），快捷登录 /#/quick-login/<token> 加载即消费并抹掉 hash——两者都不用两段式路径，因为 public 网关根下有订阅通配。
外观：GET v1/appearance 的 theme.tokens 按当前明暗取一组经 CSSOM 写到 <html>（白名单 = styles/design-tokens.ts 的 43 个名字），站点名写进标题，portal.login.notice 插槽渲染在登录页顶部。

成员清单
index.html: 入口页，pandora-app=portal 标记、<html data-app="portal">
main.tsx: 建运行时（无 requestReauth），渲染前 takeInviteFromUrl，挂载 App
App.tsx: RuntimeProvider + Root：应用外观令牌、先消费快捷登录链接，再按登录态切 AuthPage / Shell；快捷登录失败落在快捷登录标签并显示原因
pages.ts: 十一个页面的标题与副标题、顶部导航四项（「订单」短于页标题）、头像菜单五项；resolvePage 拆出 page / rest 并给规范地址，navOwner 让结账页归「选购套餐」，greeting 按时段问候
queries.ts: 外框读接口：site-config 与 appearance（匿名，登录页用）、me（全字段，外框用用户名与邮箱，账号安全页读注册时间）、余额与流水（全字段，外框只用 balance，钱包页用 history；topic orders.changed）、订阅列表（全字段 schema，外框与页面共用 ['portal','subscriptions'] 一个键，套餐徽标经 select 取 pickPrimary 那条，与概览主卡一致）、佣金概况（全字段 schema，外框与邀请返利页共用 ['portal','commission']，头像菜单经 select 取可用佣金）、未读数（无 SSE，60 秒轮询 limit=1）；除 me、订阅、余额与佣金外 schema 只收外框用到的字段；displayName 按契约映射
appearance.ts: pickThemeTokens 取当前明暗那组并按白名单过滤，useAppearanceTheme 写入与撤销 CSS 变量（不注入 <style>，CSP 无 unsafe-inline）
entry-links.ts: takeInviteFromUrl / readStoredInvite 与 quickLoginLink（账号安全页生成，相对入口页的 #/quick-login/<token>）/ quickLoginTokenFromHash / quickLoginTokenFromInput（令牌是 base64url，粘贴整条链接或裸令牌都认），生成与识别同一文件同一格式；环境可注入便于测试
AuthPage.tsx: 三标签卡片：登录（auth:false，401 内联）；注册按 registration_mode 定邀请码必填 / 选填 / 隐藏标签，step1 register/start、step2 register/complete（验证码框以 verification_required 为准，dev_code 显示为提示）后自动登录；快捷登录只收已登录设备生成的链接（保留规则 1）；loginWithPassword / consumeQuickLogin 导出供 App 复用
Shell.tsx: 顶栏（字标、导航、≥ 960 余额胶囊、铃铛未读角标、头像菜单：用户名 + 套餐徽标 + 邮箱、五项带余额 / 佣金提示、深色模式开关、退出）、页头、内容区（screens 登记的页面）、页脚、< 640 五格标签栏（「我的」受控打开头像菜单）；连门户 SSE，只驱动查询失效
*.module.css: 各组件同名样式，断点只用规范的 960 与 640
screens/: 十一个页面与懒加载登记表，归门户前端会话；见 screens/CLAUDE.md
portal.test.ts: 页面路由、rest 子路由与导航归属、邀请码取用与查询串清理、快捷登录链接生成与令牌识别往返、主题令牌白名单、用户名映射的纯逻辑测试；界面交互在浏览器里对 dev/mock-api 验收

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
