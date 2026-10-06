# panel/frontend/src/portal/screens/orders/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

我的订单（用户门户-04-订单.dc.html；契约门户-04）
  - 顶部待支付卡片取 draft / pending_payment / processing：待支付可取消（ConfirmModal，先说后果）与去支付（支付弹窗先选方式），处理中不能取消也不能再付
  - 发起过支付（行上 has_payment_intent）的两者都有「我已支付，刷新状态」（POST v1/orders/{id}/query，回调丢了也能让后端去渠道查单补记；没发起过支付的单不显示，渠道无从查起），结果条占卡片一整行，分已到账 / 渠道尚未确认 / 查询失败三种底色
  - 深链展开的待支付明细里也有同一个按钮
  - 下面是已结束订单：「全部 / 已支付 / 已取消 / 已退款」筛选带 counts 计数（已退款为 0 时不显示），按下单月份分组、组头合计已加载行里已支付的金额，「显示更早的订单」每次多取 6 条。
展开由地址驱动：#/orders/<订单 id> 即展开那一行（点行切换，replace 不留历史），不在已加载列表里时单独成卡，不存在按「无权限或不存在」
  - 带 ?paid=1 是收银台回跳，弹支付确认，关掉后抹掉 paid
  - 列表用 ui 的 QueryView
  - 设计是「显示更早」而不是翻页，所以不用 Pager。

成员清单
index.tsx: 页面组件——
  - 待支付卡片（含 PaymentRefresh 按钮与 RefreshNote 结果条，结果按订单 id 记在卡片列表的 state 里）、筛选、分组列表、行展开的明细（GET v1/orders/{id}，末尾「提交工单」链到 #/tickets/new?order=）、深链单独成卡、支付弹窗
Orders.module.css: 页面样式，取自设计稿门户-04

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
