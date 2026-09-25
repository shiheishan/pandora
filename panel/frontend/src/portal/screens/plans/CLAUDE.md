# panel/frontend/src/portal/screens/plans/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

选购套餐（用户门户-03-选购套餐.dc.html 的列表部分；契约门户-03）。两个标签：订阅套餐与流量包，#/plans?tab=packs 直达后者。只展示 CNY 价格（余额与 epay 只有 CNY）；D-E-3 已决（5.A.2、R100：卖点与「推荐」），第 ② 步接入；在那之前套餐卡的特性只列后端已有事实（流量与重置、设备数、描述），不显示「推荐」。流量包规则按 5.A D-E-1（挂用户、永不过期、可叠加、有生效订阅才消耗），删去设计稿「没有订阅也能买并单独使用」一条。
入口只拼地址不判模式：当前套餐 → #/checkout?renew=&price=，其余 → #/checkout?plan=&price=，由结账页按「无订阅新购 / 同套餐续费 / 换套餐变更」再判；目标套餐 allow_upgrade=false 或当前套餐不可续费时按钮置灰。

成员清单
index.tsx: 页面组件——插槽、商品类型标签（带「¥X 起」）、周期分段（设计稿三档在前，多月档带最小省幅）、套餐卡、流量包卡（recommended 为「最划算」）与规则三格，加载 / 空 / 错误三态
labels.ts: 纯函数 availablePeriods（有哪几档周期及文案）、fromPrice（标签页起价）、planAction（每张卡的入口与置灰理由）
Plans.module.css: 页面样式，取自设计稿门户-03；入口是做成按钮外观的链接

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
