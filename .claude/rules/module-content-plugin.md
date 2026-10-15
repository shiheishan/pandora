---
paths:
  - "panel/internal/module/content/**"
  - "panel/internal/module/plugin/**"
---

# 知识库内容（content）与插件钩子（plugin）

## content
- 正文只当纯文本 / Markdown 源存储与下发，后端与门户都不渲染成可信 HTML，所以这里不是 XSS 边界；不要引入服务端 HTML 渲染或放行 HTML。
- 门户可见性（状态、可见范围、平台、客户端版本、语言、限定套餐）只在 `panel/internal/module/content/service/portal.go` 的 `visible` 一处判定，列表、详情、反馈共用。反馈先过 `GetVisible`，看不到的文章回 404，免得借反馈接口探测文章是否存在。
- 占位变量与「有效订阅可见」区块只在门户读单篇（`GetVisible`）时按当前用户在服务端渲染（`render.go` 的 `RenderBody`），库里存的是原文，后台与列表看到的也是原文：
  - 变量照 Xboard 名字，白名单只有 `{{siteName}}`、`{{siteUrl}}`、`{{subscribeUrl}}`、`{{urlEncodeSubscribeUrl}}`、`{{safeBase64SubscribeUrl}}`，别的 `{{…}}` 原样保留；订阅链接经 `SubscribeLinks` 端口（app 适配 subscription 的 `MyLinks`，多份生效订阅取凭据最新那份），正文里没有订阅变量时不去取。
  - `<!--access start-->…<!--access end-->` 区块：没有生效订阅时整块换成 `AccessDeniedNotice`；起始标记没闭合时到文末都算区块（宁可多藏）。
  - 列表搜索：SQL 先按原文粗筛；没有在用套餐的用户再取正文、用 `SearchHit` 剔掉区块（连内容，切分与渲染同一个 `processAccessBlocks`）复核，搜不到区块里的字；列表照旧不返回正文。
- 每次保存是一个新版本（同一 slug 版本号递增，乐观锁 `ExpectedLatestVersion`）；发布时只归档「同一受众」（语言、平台、客户端版本范围、限定套餐、可见性都相同）里已发布的旧版本，受众不同的旧版本照常可见。

## plugin
- 插件跑在面板进程外：面板只把业务事件以 HMAC 签名的 HTTP POST 推给插件自己的服务（`Sign` 覆盖时间戳与正文），不在进程内加载插件代码。
- `Emit` 在调用方事务里只写一条 queued 投递（同一钩子同一 dedupeKey 只投一次），提交后入队 `plugin:dispatch`；事件与业务同生共死，`Emit` 出错时调用方要让业务事务失败。各模块的事件拼装放在自己的 `plugin_events.go`。
- 派发全局只许一个实例（库里的咨询锁），认领带租约；失败按 1、2、4…分钟退避、封顶 30 分钟；4xx 或尝试用尽即终态，不再重试。
- 钩子地址保存时 `validateEndpoint` 先查一遍，真正的防线是拨号时的 `checkDialAddr`（按解析后的 IP 拦内网、环回、元数据地址，防 DNS 重绑定）；HTTP 客户端不跟随跳转。`AllowPrivate` 只在开发环境为 true。
- 拦截错误的 `Error()` 不带地址；返回的错误去掉 URL 的路径与查询串（`plog.StripURL`），日志不带密钥与完整地址。
- 可订阅事件是代码里的白名单 `Events`（`KnownEvent`），不接受任意字符串；加事件要同时在白名单登记。
