# panel/frontend/src/portal/screens/account/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

账号安全（用户门户-10-账号安全.dc.html；契约门户-10，修订 R15、R28、R62、R114；保留规则 1；D-F-3 已决）。两列卡片流：左列个人信息、修改密码、快捷登录；右列登录会话、Telegram、通知偏好、退出登录。
个人信息读外框的 me（同键 ['portal','me']）：邮箱、用户 ID（uuid 前 8 位，与后台一致；设计稿的数字编号后端没有）、注册时间；设计稿的「用户组」GET v1/me 没有，不显示。
改密码：口令错回 401，api 以 passwordCheck 发请求、落到「当前密码」框，不当作会话失效；本地先按后端同一套规则校验（≥ 8、≤ 256 字节、字母与数字）；成功后当前会话保留、其余下线（R62），会话列表重拉。
快捷登录（保留规则 1）：只在这台已登录设备签发、60 秒、一次性；链接格式与登录页识别同源（entry-links 的 quickLoginLink，#/quick-login/<token>），生成后尝试复制，60 秒倒计时（按 expires_in，不受本机时钟偏差影响）到点隐藏，按钮变回「生成快捷登录链接」。
会话：只列门户会话（R15），当前会话置顶标「当前」、不能下线，其余按最近活跃倒序；设备名由 user_agent 推出；位置与 IP 按 D-F-3（已决，5.A.2）不显示，meta 写「N 分钟前活跃 · 登录于」（lastSeenLabel）（last_seen_at 由认证中间件 5 分钟一次刷新，R62 / R114）。下线照设计直接执行、不弹确认（对方重新登录即可恢复）。
Telegram：站点未启用 / 已绑定（解绑先确认）/ 展示绑定码 / 未绑定四态；指令是 /start CODE（设计稿的 /bind 会绑定失败），10 分钟倒计时，展示期间 3 秒轮询绑定状态，另给 t.me 深链。通知偏好按后端 3 类 × 2 渠道（设计稿的五个事件合并成类），交易类锁定开启，Telegram 列在未绑定或站点未启用时置灰；每次点击 PUT 一项，乐观更新、失败回滚。本页写操作都没挂幂等中间件，不带键。

成员清单
index.tsx: 页面组件——布局、ProfileCard、PasswordCard、QuickLoginCard、SessionsCard / SessionRow、退出登录
Connections.tsx: TelegramCard（Bound / Unbound 与解绑确认）与 NotificationPrefsCard
api.ts: 数据层——会话、改密、快捷登录、Telegram、通知偏好的 schema、查询与 mutation
model.ts: 纯逻辑——deviceName、lastSeenLabel、sortSessions、validateNewPassword、passwordErrors、偏好行、倒计时、shortUserId、telegramDeepLink
clock.ts: useNow 秒级时钟，只在有倒计时时走表
Account.module.css: 页面样式，取自设计稿门户-10
account.test.ts: 第 ⑥ 步账号安全的单元测试（schema、设备名、排序、密码校验与错误落位、倒计时与深链）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
