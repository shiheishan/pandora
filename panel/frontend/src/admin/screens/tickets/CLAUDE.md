# panel/frontend/src/admin/screens/tickets/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

工单（管理后台-02-工单.dc.html），客服的主工作台：左队列、右详情的双栏，一整块卡片，高度随视口、两栏各自滚动。状态全在地址上——#/tickets/<工单 id>?f=<筛选>&q=<搜索>，所以刷新、分享、前进后退都落在同一张工单同一个筛选上；列表只拼链接，不持有选中态。
权限分两级：ops.ticket.read 能进来看（外框先判），ops.ticket.write 才出现回复框、状态 / 指派可改、升级、SLA 扫描与快捷回复管理；没有写权限时整页只读。
后端事实以 domain/support/service.go 为准（契约后台-02 + R25 / R42 / R60）：队列与详情是同一个 Go 结构，omitempty 的字段在 schema 里一律可选；后台两条接口都不填 related_order、详情的 last_reply_at 恒为零值（均已报告协调会话），所以页面不依赖它们。写操作的权限判断、幂等键与失败处理取 src/admin/actions.ts；写接口都带幂等键，一次用户意图一把键：「回复并解决」是先 reply 再 status 两把键，第二步失败重试时不再重发回复。

成员清单
index.tsx: 页面入口，从地址读选中工单、筛选与搜索并回写（replace），组装 Queue 与 Detail
api.ts: zod schema 与类型（封闭枚举：6 个状态、4 档优先级、6 个分类、3 种关闭原因）、队列的 useInfiniteQuery（limit 25 / offset，「加载更多」）、详情、可指派目录、快捷回复四个读 hook，写接口响应 schema，useInvalidateTickets
model.ts: 纯逻辑——分段筛选到后端 query（未解决 / 待处理 / 我的 / 超时 / 全部）、状态映射与状态下拉的可设置灰、优先级与分类标签、关闭原因、等待时长与 SLA 两行文案、消息气泡角色与发言人、消息时间、系统消息里状态码的中文化、指派下拉
model.test.ts: model.ts 与 schema 的单元测试
Queue.tsx: 左栏：分段筛选、防抖搜索、「检查 SLA 超时」（租户级扫描，待补·前端）、工单行（优先级点、编号、状态、已升级、未读加粗、等待时长与 SLA 超时标红）与加载更多；导出 useNow 供两栏按分钟刷新时长
Detail.tsx: 右栏：详情头（套餐、关闭原因、已升级徽标、关联订单、SLA）、状态 / 指派下拉、关闭说明对话框、查看用户、升级到 L2 确认（D-B-6 已决：不承诺通知值班）、对话流四种气泡
Composer.tsx: 回复框：快捷回复标签与管理入口、内部备注开关、⌘↵、发送回复 / 回复并解决 / 添加备注
MacroManager.tsx: 快捷回复管理对话框（待补·前端）：列表、新建、编辑、删除确认
Tickets.module.css: 本目录唯一样式表，数值取自设计稿，只引用令牌；左栏宽 min(340px, 38%)，960 宽时给右栏让位

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
