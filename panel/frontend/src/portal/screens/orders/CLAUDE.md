# panel/frontend/src/portal/screens/orders/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

我的订单（用户门户-04-订单.dc.html；契约门户-04，修订 R69）。顶部待支付卡片取 draft / pending_payment / processing：待支付可取消（ConfirmModal，先说后果）与去支付（支付弹窗先选方式），处理中只展示。下面是已结束订单：「全部 / 已支付 / 已取消 / 已退款」筛选带 counts 计数（已退款为 0 时不显示），按下单月份分组、组头合计已加载行里已支付的金额，「显示更早的订单」每次多取 6 条。
展开由地址驱动：#/orders/<订单 id> 即展开那一行（点行切换，replace 不留历史），不在已加载列表里时单独成卡，不存在按「无权限或不存在」；带 ?paid=1 是收银台回跳，弹支付确认，关掉后抹掉 paid。列表用 ui 的 QueryView；设计是「显示更早」而不是翻页，所以不用 Pager。

成员清单
index.tsx: 页面组件——待支付卡片、筛选、分组列表、行展开的明细（GET v1/orders/{id}，末尾「提交工单」链到 #/tickets/new?order=）、深链单独成卡、支付弹窗
Orders.module.css: 页面样式，取自设计稿门户-04

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
