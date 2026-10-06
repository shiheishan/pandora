---
paths:
  - "panel/frontend/src/admin/screens/users/**"
---

# 后台 · 用户

- 保留规则 2 贯穿整个模块：后台看不到订阅地址。列表、详情、画像都不含令牌；订阅标签用一句说明代替设计稿的「订阅地址 + 复制」；换发订阅链接的响应 schema 用 `.strict()` 守住「不回令牌」（`model.test.ts` 断言带令牌即判不符），成功后提示让用户到门户重新复制。
- 明文注册 IP 只从风控画像接口取（`security.audit.read`），没有该权限整行不显示。
- 产品取舍：重置密码是管理员直接设新密码，不要原因、不发邮件、不回显（D-B-2）；不给全局「默认同时在线设备」滑块；批量生成不带「开通套餐」（引导去人工开单）；设备标签只有在线台数与上限，不列逐台客户端与 IP（节点只上报 IP 哈希，D-B-8）；识别窗口只给 5 / 10 / 30 / 60 分钟（`DEVICE_WINDOWS`），下拉旁说明代价。
- 用户组删除拦截先看节点池名单（`exclusive_pools`），再看成员与引用，与后端 409 同序（`model.ts` 的 `groupBlocker`）。
- 前端口径与后端逐一对齐，改后端这些函数时同步改 `model.ts`：当前订阅挑法 = `currentSubscriptionSQL`；手动重置挑哪条订阅 = `billing.ManualResetTraffic`；批量生成校验 = `adminops.GenerateUsers`；密码预检 = `platform/crypto.ValidatePassword`。
- 设备策略请求体：strict 才带 grace，识别窗口改了才带 `window_minutes`。
- 批量导出没有 cookie，只能 fetch 带 Bearer 取回再存文件，要 reauth。
- 用户相关的地址被别处引用：仪表盘排行、工单「查看用户」、订单抽屉都跳 `#/users/list/<用户 id>`，订单标签跳 `#/billing/orders?user_id=`；改地址格式要同步这些入口。
