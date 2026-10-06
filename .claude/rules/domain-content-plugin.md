---
paths:
  - "panel/internal/domain/content/**"
  - "panel/internal/domain/plugin/**"
---

# 知识库内容与插件钩子

## content
- 正文按纯文本 / Markdown 源码存取，后端与门户都不渲染可信 HTML，从根上不设 XSS 边界；不要引入服务端 HTML 渲染或放行 HTML
- 门户侧可见性（状态、可见范围、平台、客户端版本、语言、限定套餐）只在 `Service.visible` 一处判定，列表、详情、反馈共用。反馈对看不到的文章回 404 而不是 422，免得借反馈接口探测文章是否存在
- 每次保存生成新版本（乐观锁 `expected_latest_version`），发布时同受众的旧发布版本自动归档

## plugin
- 插件跑在面板进程外：面板只把业务事件以 HMAC 签名的 HTTP 请求推给插件自己的服务，不做「上传插件包在进程内加载」
- 事件入队与业务写在同一事务（`Emit` 及各 `Emit*` 都吃调用方的 tx，返回的 error 必须检查）；投递由扫描器异步做，指数退避重试，4xx 不重试
- 钩子地址在保存时和每次发送前都做 SSRF 校验（`validateEndpoint`），HTTP 客户端不跟随跳转
- 可订阅事件是代码里的白名单 `Events`，不接受任意字符串
