---
paths:
  - "panel/frontend/src/portal/screens/account/**"
---

# 门户 · 账号安全

- 版式（10-16 样稿 `#p-security`）：单列分区，从上到下 个人信息卡 → 登录（密码、在新设备上登录两行）→ 登录的设备 → Telegram → 通知方式 → 退出登录。改密码、快捷登录、Telegram 绑定、设备下线都在弹层（kit `Sheet`）里；解除 Telegram 用 `ui/ConfirmModal`。
- 快捷登录（保留规则 1）：只在已登录设备签发、60 秒、一次性；点「在新设备上登录」即打开弹层并签发一次（同一会话再签发时旧令牌作废，所以「重新生成」直接再调）。弹层画 `common/qr.ts` 的 `QrCode`（真编码 `quickLoginLink(token)`，链接格式与登录页识别同源，都在 `portal/entry-links.ts`）、倒计时条、链接与复制、「只发给自己的设备」提醒，过期盖遮罩、链接变灰。倒计时按响应的 `expires_in` 走，不受本机时钟偏差影响。关掉弹层令牌不作废（没有撤销接口），60 秒内仍可用。
- 改密码弹层的两个密码框用共用的 `ui/PasswordInput`（右侧眼睛按钮切换显示明文，缺省隐藏，状态不落 localStorage），autoComplete 照旧 `current-password` / `new-password`。
- 改密码时当前密码错回 401：请求带 `passwordCheck`，错误落到「当前密码」框，不能当作会话失效登出；本地预检与后端 `crypto.ValidatePassword` 同规则（≥ 8 字符、≤ 256 字节、字母与数字）。成功后保留当前会话、其余下线。
- 登录的设备：只列门户会话（网页登录），会话位置与 IP 按 D-F-3 不显示；设备名由 `user_agent` 推出（`deviceName`），图标按系统分手机 / 电脑（`deviceKind`）；当前会话置顶、标「这台设备」、不能下线。点别的设备先弹确认（写清下线影响什么、不影响什么：已添加到 App 的套餐照常能用），确认后底部提示「已将 X 下线」；404 提示「这个会话已经下线」。
- 个人信息不显示「用户组」（`GET v1/me` 没有），用户 ID 显示 uuid 前 8 位（与后台一致，设计稿的数字编号后端没有）。
- Telegram 卡：未绑定「绑定后，到期、流量和工单回复的提醒也能发到 Telegram」+「绑定」；已绑定「已绑定 @x · 提醒会发到这个账号」+「解除绑定」（保留确认框，安全项）；`username` 是 omitempty，没有时写「已绑定 Telegram」（审计 C2）。站点没开 Telegram（`enabled = false`）且未绑定时写「站点未启用 Telegram 通知」、**不给绑定入口**（点了会 422）；已绑定但站点暂停时补一句「恢复前不会推送」。
- Telegram 绑定弹层：点「绑定」即打开并取码（再取会作废旧码）。左边编号步骤、右边 `t.me` 深链二维码（深链前端拼 `telegramDeepLink`，机器人用户名取取码响应的 `bot_username`）；绑定指令是 `/start CODE`（设计稿早期的 `/bind` 会绑定失败），手动发送放在「打不开 Telegram？」折叠里。弹层开着且码没过期时每 3 秒重拉 `GET v1/me/telegram`，绑上了就地变成完成页（没有「我已发送，完成绑定」按钮）。绑定码只回绝对时间 `expires_at`：倒数按本地收到响应的时刻起算，剩余时长夹在 (0, 10 分钟]（`BindCodeTTL`），落在外面就按 10 分钟（`bindDeadline`，审计 C3）；过期后二维码盖遮罩、主按钮就地变「重新获取」。
- 通知方式：后端固定 5 项（交易通知 / 服务提醒 / 活动与群发 × 邮件 / Telegram，活动与群发只有邮件：群发只发邮件，10-21 去掉没有消费方的那格，`PREF_ROWS.channels`，Telegram 列那格留空），交易类锁定开启；没绑 Telegram 或站点没开时 Telegram 一列禁用并说明；每次点击 PUT 一项，乐观更新、失败回滚并提示「已恢复原设置」。
- 本页写操作都没挂幂等中间件，不带幂等键；签发快捷登录、取绑定码都在点击回调里发，不放进 useEffect。
- Telegram 卡的讨论群入口只看 `GET v1/me/telegram` 的 `discuss_link`（恒在，null 不显示），只渲染 https 链接；入群审核开没开门户不知道，文案只说「可能需要」。
- 假后端（`dev/mock/portal/account.ts`）：取码 8 秒后视为已在 Telegram 发送；`multi` 场景已绑定且有用户名，`empty` 场景绑上后没有用户名（演示 C2），`legacy` 场景站点没开 Telegram。
