# panel/frontend/src/portal/screens/referral/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

邀请返利（用户门户-06-邀请返利.dc.html；契约门户-06，修订 R5、R7、R69，5.A D-F-1）。佣金概况读外框 queries.ts 的 useCommission（与头像菜单的可用佣金同键 ['portal','commission']，schema 已写全）；邀请码与被邀请人读 GET v1/me/invite（没有活动码时后端懒生成）。
可用佣金以账本为准（账本余额 − 在途提现），「全部转入余额」与「申请提现」用同一个数；两个写操作都幂等（commission_transfer_to_balance / commission_withdrawal_request），按请求指纹给键，成功或 4xx 业务拒绝后丢弃键（断网与 5xx 保留，重试拿回同一结果），错误文案照后端原文，成功或失败后都重拉佣金（转余额还重拉余额）。后端同一时间只许一笔在途提现（含打款中），summary.withdrawing > 0 或可用不足最低额时提现表单换成一句说明。
设计稿之外：邀请链接改为 /?invite=<code>（/r/ 会撞订阅通配），邀请码 8 位；横幅按佣金概况的 summary.scope 写（R81 / R114）：every_order 写「邀请好友付费」，first_order 写「好友首单付费」；邀请码有上限时说明用量、用满或站点关闭注册时警示；左列补「邀请记录」卡（契约待补·前端，不展示 risk_flag）；提现占位改「支付宝账号 / 银行卡号」，不做 USDT，「1–3 个工作日」改为「审核通过后打款」（后端无此承诺）。

成员清单
index.tsx: 页面组件——邀请横幅（复制链接 / 复制邀请码）、四格统计（StatStrip，R69 字段缺席显示「—」）、使用佣金卡（转余额 ConfirmModal、冻结中提示、提现表单与锁）、佣金记录（三类合并，先显示 8 条）、邀请记录
api.ts: 数据层——GET v1/me/invite 的 schema 与查询，转余额与申请提现的 mutation
model.ts: 纯映射——inviteLink、headline、inviteUsage、parseWithdrawAmount（元转分与上下限）、withdrawBlock、commissionRecords（佣金 + 且订单号单列成 ref / 转入余额 − / 提现 −，冲销与驳回划掉，驳回附原因）
Referral.module.css: 页面样式，取自设计稿门户-06（网格区域：宽屏右列佣金记录跨两行，< 640 单列）
referral.test.ts: 第 ④ 步的单元测试（佣金与邀请 schema、横幅文案、提现金额与表单锁、记录合并与状态映射、common/intent 的复用与丢弃）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
