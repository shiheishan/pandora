---
paths:
  - "panel/frontend/src/portal/screens/tickets/**"
  - "panel/frontend/src/portal/screens/help/**"
  - "panel/frontend/src/portal/screens/messages/**"
  - "panel/frontend/src/portal/screens/referral/**"
---

# 门户 · 工单、帮助、消息与邀请返利

工单：
- 按钮可见条件与后端判定一致：撤回要未关闭且客服没回复过，关闭与回复只要未关闭（resolved 仍可回复、会重开）；`closed` 靠 `closed_reason` 分「已撤回」与「已关闭」。
- 新建、回复、关闭幂等（一次意图一把键）；撤回不幂等、请求体必须带 `{}`。撤回与关闭都先 ConfirmModal（关闭不可逆，设计稿没有确认框，按规则补上）。
- 客服按 D-F-2 统一显示「客服」不显示姓名；正文按后端要求 ≥ 10 字；页头副标题不承诺「10 分钟内首次响应」（后端 SLA 普通 12 小时）；回车发送时输入法组字的回车不算。

帮助中心：
- 可见性参数 `platform=any` 在列表、正文、反馈三处必须一致（`help/api.ts` 的 `HELP_QUERY`），不一致反馈会 404。
- 正文是纯文本 / Markdown 源码，只认「## 」行作小标题、空行分段，全部渲染成文本节点，不用 innerHTML。
- 内置一篇「在 App 里点一次更新」（不是后台文章）：`#/help?update=<ios|android|mac|windows>`，走查询参数不占文章 slug；目录最上面固定一组四种设备。内容在 `common/app-update.ts` 的 `APP_UPDATE`，`common/clients.ts` 推荐的每个 App 一段：在哪个页面、点哪个按钮、看到什么算成功，末尾给「删掉再添加一次」的退路；中文界面按钮带禁用词的写英文界面的名字。给 `clients.ts` 加 App 要同时加一段（守卫 `common.test.ts`「clients.ts 推荐的每个 App 都有一段」）
- 搜索交给后端 `q`（要匹配正文，列表拿不到正文）；「有帮助」按用户、文章、版本 upsert，天然幂等不带键；设计稿没有「没帮助」，照设计不加。

消息：
- 新站内信没有 SSE：外框铃铛 60 秒轮询 `['portal','notifications','unread']`（limit=1），消息页用 `['portal','notifications','list']`，写操作失效整个 `['portal','notifications']` 前缀让角标同步。

邀请返利：
- 邀请链接是 `/?invite=<code>`，不能用 `/r/`（会撞 public 网关根下的订阅通配）。
- 可用佣金以账本为准（账本余额 − 在途提现），「全部转入余额」与「申请提现」用同一个数；后端同一时间只许一笔在途提现，`summary.withdrawing > 0` 或可用不足最低额时提现表单换成一句说明。
- 横幅按佣金概况的 `summary.scope` 写（first_order 才说「首单」）；`risk_flag` 不展示给邀请人；提现不做 USDT，不承诺「1–3 个工作日」，写「审核通过后打款」。
