# panel/frontend/src/admin/screens/system/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

通知与插件（后台-09 前半，归后台前端二）。三个标签全部接入：通知渠道、邮件模板、Webhook 钩子。视觉按 管理后台-09-通知与安全.dc.html，数据与规则按 api-contract.md 后台-09 · 通知与插件（含 R14 R20 R21 R45 R59），并对过 Go 的 mail.go、telegram.go、mail_template.go、notify/template_admin.go、plugin/hooks.go；契约里的待补·后端（admin_chat_id、测试可省 chat_id、草稿预览、has_default、测试信发草稿、sent_count_7d、duration_ms）主线都已实现，照实现写。设计缺、契约标「待补·前端」的都补上了：SMTP 加密方式、「注册与验证」卡、Telegram 启用开关、模板渠道分段、钩子的启用开关 / 编辑 / 排队数。
分层：schemas（zod）→ queries（读 hook，这几张表没有实时通知，写后按 SK 前缀失效；转出 admin/actions.ts 的 useCan / useFailure / useIntentKey）→ logic（纯函数，system.test.ts 守住）→ hookActions（钩子保存）→ 组件。
三条后端事实决定了写法：邮件设置 SMTP 六字段每次整体覆盖，所以注册卡保存带的是「已保存」的 SMTP 字段；钩子按 code upsert、省略的字段写成零值，所以新建时生成避开现有 code 的 code、编辑与启停都回填全字段；钩子超时与重试次数后端只有 DB CHECK（越界 500），前端先拦。三个测试发送用的都是已保存的配置，渠道卡有未保存修改时禁用测试。
权限：通知渠道读挂 security.audit.read、存 platform.settings.write（无 reauth 无幂等）、测试 ops.notification.write；模板读 / 预览 ops.notification.read、存 / 恢复 platform.settings.write、测试信 ops.notification.write + reauth；钩子读 platform.plugin.read、写全部 platform.plugin.write + reauth，保存带幂等键 plugin_hook_save。只读账号（ops.notification.read）只能看模板。
已决（5.A.2）：D-A-4 管理员群组只作测试默认目标；D-A-5 设计里的「重置密码」「礼品卡兑换成功」模板后端没有，按后端现有模板显示；D-A-6 事件只用后端目录，不提供 ticket.replied / node.offline / node.online。

成员清单
index.tsx: 页面入口，按标签分发：notify → ChannelsTab、templates → TemplatesTab、hooks → HooksTab
schemas.ts: 邮件与注册设置、Telegram 设置、三个测试响应、模板（列表行带 has_default / description / preview_*，保存 / 恢复 / 草稿预览 / 测试信响应）、钩子（列表与小写事件目录、保存、删除、投递记录可为 null、测试投递）的 zod schema 与枚举
queries.ts: 查询键前缀 SK、各读 hook（邮件、Telegram、模板、钩子、投递记录——展开时才挂载，null 归一为空数组）、useInvalidateSystem，转出三件通用 hook
logic.ts: 发件人「名称 <地址>」拆拼、SMTP 表单 / 校验 / 请求体、注册卡请求体、SMTP 与 Telegram 状态文字、chat id 解析与「没变不带」、模板中文名 / 状态标记 / 光标处插入变量 / 长度校验、钩子成功率 / 状态点 / 避让现有 code / 主机名 / 表单校验 / 全字段请求体 / 事件文字 / 耗时与投递行
ChannelCard.tsx: 渠道卡外壳（名称 + 状态圆点、两列字段区、浅底底栏）
ChannelsTab.tsx: 通知渠道：SMTP 卡（服务器、端口、加密、用户名、密码留空不改 / 清除、单框发件人、测试收件地址）与「注册与验证」卡（注册模式、邮箱验证、降级开关提示并链到安全与运维），Telegram 卡排在两者之间（与设计稿两张渠道卡并排）
TelegramCard.tsx: Telegram 卡：启用开关、Bot Token 只进不出、管理员群组 chat id、Bot 用户名，测试 chat id 留空发往管理员群组
TemplatesTab.tsx: 邮件模板三栏：渠道分段 + 列表（各模板草稿切换后仍保留），编辑区（主题、正文、变量 chips、白名单外变量提示、恢复默认确认、放弃修改），预览栏（300ms 防抖请后端渲染、邮件渠道才有实发测试信，改过就发草稿）
HooksTab.tsx: Webhook 钩子：新建条（端点 URL、订阅事件、新建后弹一次性密钥）、钩子卡片（状态点、URL、名称 · 事件 · 成功率 · 排队 · 最近送达，启用开关、投递记录展开、测试投递、编辑、删除确认）
HookDialogs.tsx: EventPicker（按后端事件目录的开关菜单，首项全部事件）、HookModal（编辑全字段与范围校验）、SecretModal（一次性签名密钥，可复制、不能点遮罩关掉）
hookActions.ts: useSaveHook：新建、启停、编辑共用的保存写操作（reauth + 幂等，失败 fail(e, { fields, intent })）
system.module.css: 渠道卡网格与卡片、模板三栏（窄于 1180 预览移到编辑区下方）与预览、钩子新建条 / 卡片 / 投递记录四列、编辑弹窗两列
system.test.ts: logic 与 schema 边界的单元测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
