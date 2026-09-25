# panel/frontend/src/portal/screens/wallet/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

钱包（用户门户-05-钱包.dc.html；契约门户-05，修订 R31、R68）。余额与流水读外框 queries.ts 的 useBalance（与顶栏余额胶囊同键，schema 已写全）；充值先 POST v1/me/topups 建单（响应 200 不是 201），再走 common/PayFlow 的支付弹窗去收银台；礼品卡先预览卡面再兑换，兑换后失效整个门户前缀（余额、订阅、流量包都可能变，且都没有推送）。
设计稿之外：左列补「余额明细」卡（契约待补·前端，账本类型映射中文，挂账按保留规则 6 称「挂账转入」）；删 USDT 与「试试 GC-…」演示提示；< 640 单列时礼品卡排在余额明细之前。

成员清单
index.tsx: 页面组件——余额与充值（预设金额 / 自定义金额、支付方式下拉、校验）、余额明细（先显示 8 条）、兑换礼品卡（查询 → 卡面 → 立即兑换，失败显示后端原文）、我的礼品卡
api.ts: 数据层——充值建单、礼品卡预览与兑换（幂等 gift_card_redeem）、我的兑换记录的 schema 与 hooks
model.ts: 纯映射——充值金额元转分与 ¥1–¥50000 校验、流水类型名、礼品卡卡面与说明、兑换记录「获得」列、卡码归一
Wallet.module.css: 页面样式，取自设计稿门户-05（网格区域：宽屏右列礼品卡跨两行）
wallet.test.ts: 第 ③ 步的单元测试（钱包映射与订单页映射）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
