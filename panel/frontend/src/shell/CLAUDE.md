# panel/frontend/src/shell/
> L2 | 父级: /panel/frontend/CLAUDE.md

两个入口共用的外框底座，夹在 core（无状态原语、不碰组件）与 admin / portal（各自的外框与页面）之间：把 core 的令牌、api 客户端、QueryClient 组装成每个入口一份的运行时，经 context 交出去，并提供登录态、退出、实时事件与页面容器这几件两边都要的事。依赖方向 core → ui → shell → admin / portal，反过来不许。

成员清单
runtime.tsx: createAppRuntime(app, { requestReauth }) 建本入口唯一的 TokenStore + ApiClient + QueryClient，令牌清空即清查询缓存（下一个登录的人看不到上一个人的数据）；RuntimeProvider 同时挂 QueryClientProvider 与 ToastProvider；useRuntime / useApi；useSignedIn 以「令牌是否存在」为登录态（useSyncExternalStore 订阅令牌，401 与其它标签页退出都会翻转它）；signOut 先请求 v1/auth/logout、失败也照样清令牌；useRealtime(enabled, onEvent) 连 v1/events，事件交给失效器并转给调用方，返回 connecting / open / reconnecting / unavailable（4xx，后台无 ops.notification.read 时是 404）
Logo.tsx: 环形标记 + pandora 字标，环色随 currentColor、点默认 --brand（侧栏与后台登录页用 --logo-dot 换成 --sidebar-brand）
Logo.module.css: 环描边 8、缺口虚线 78.5/15.7，与设计稿 svg 逐值一致
ScreenFrame.tsx: 两个外框内容区的页面容器——错误边界（按路由 resetKey 重置，单页崩溃只在内容区显示「页面出错了」+ 重试，发版后旧块加载失败改为「刷新页面」）套 Suspense（ScreenFallback 骨架兜底）；NotFoundScreen 是缺权限地址与接口 404 共用的「无权限或不存在」
ScreenFrame.module.css: 加载骨架的纵向间距
shell.test.ts: 页面块加载失败识别（isChunkLoadError）的单元测试；边界与 Suspense 的界面行为在浏览器里验收

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
