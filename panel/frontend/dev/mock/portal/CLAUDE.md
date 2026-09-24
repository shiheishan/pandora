# panel/frontend/dev/mock/portal/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

门户十一个页面的假接口，一个页面一个文件、导出一个 MockModule，归门户前端会话。index.ts 的登记顺序即询问顺序，不再改动。外框顶栏读的余额、订阅、佣金、未读数按契约归属各页面，所以住在对应页面的文件里，页面会话扩充它们时外框照用。

成员清单
index.ts: 登记表 PORTAL_MODULES
subs.ts: 我的订阅（门户-02）；GET v1/me/subscriptions（外框头像菜单的套餐徽标也读它）
wallet.ts: 钱包（门户-05）；GET v1/me/balance（外框余额胶囊也读它）
referral.ts: 邀请返利（门户-06）；GET v1/me/commission（外框菜单的可用佣金提示也读它）
messages.ts: 消息（门户-08）；GET v1/me/notifications（外框铃铛未读数轮询它）
account.ts: 账号安全（门户-10）；POST v1/me/quick-login 签发快捷登录令牌
overview.ts / plans.ts / checkout.ts / orders.ts / tickets.ts / help.ts: 其余六个页面，目前为空壳

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
