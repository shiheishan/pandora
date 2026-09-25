# panel/frontend/src/portal/screens/orders/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

我的订单（用户门户-04-订单.dc.html；契约门户-04）。第 ② 步只接收银台回跳：return_url 是 #/orders/<订单 id>?paid=1，页面据此弹 common/PayFlow 的确认态轮询订单，关掉后 replace 成 #/orders/<订单 id> 抹掉 paid；列表、明细、取消在第 ③ 步接入。

成员清单
index.tsx: 页面组件——目前渲染占位页，外加回跳时的支付确认弹窗

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
