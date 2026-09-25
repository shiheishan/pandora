# panel/frontend/src/portal/screens/overview/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

概览（用户门户-01-概览.dc.html；契约门户-01）。只组合读取，不写：主卡展示 status∈{active,trialing,grace,past_due} 中 current_period_end 最晚的一条订阅；余额与可提佣金直接用外框 queries.ts 的同键查询；待补字段（在线设备、设备上限、重置日）缺席时显示「—」或退回按日用量接口的周期末。
设计稿之外补了三处，均有契约依据：critical 公告顶部横幅（门户-08 建议）、grace / past_due 徽标（门户-02 待补·前端）、流量包余量并入剩余流量并注明「含流量包」（门户-03 我的流量包）。

成员清单
index.tsx: 页面组件——插槽、critical 横幅、待支付条（30 秒刷新倒计时，去支付 → #/orders/<订单 id>）、主卡（导入 → #/subs?sub=、复制订阅地址、续费 → #/checkout?renew=，7 天内转「立即续费」）、三格统计、公告卡与正文弹窗、本期用量卡
Overview.module.css: 页面样式，取自设计稿门户-01；待支付说明在窄屏整句换行

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
