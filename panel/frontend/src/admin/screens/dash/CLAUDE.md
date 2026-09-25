# panel/frontend/src/admin/screens/dash/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

仪表盘（管理后台-01-仪表盘.dc.html），后台的落地页，登录即可进。它没有自己的数据，只把八个只读接口拼成一页：每张卡按自己接口的权限决定发不发请求、画不画（缺权限不发请求，接口 404 也按无权限隐藏），各自加载、各自失败、各自重试，一张卡坏了不牵连别的卡（DASH-01 的局部失败要求）。
分三层：api.ts 是契约的镜像（zod 写全写严，待补·后端字段一律可选，流量与积压三件照 DASH-01 冻结契约；tasks 与侧栏共用，不在这里），model.ts 把数据映射成卡片、行与文案（契约各条目的「映射」一行都落在这里，纯函数、全测），其余组件只负责摆放。图表按规范手写，不引图表库；柱高与条宽是仅有的动态 style。
与外框的关系：「需要处理」和侧栏徽标共用 src/admin/tasks.ts 的同一条 GET v1/dashboard/tasks 查询与严格 schema；格式化（字节、计数、时间）一律取 core/format.ts；卡片与行的链接只在目标页可读时出现，按 modules.ts 判断。

成员清单
index.tsx: 页面入口，按 dashboardAccess 决定哪些卡出现；概览与节点流量两条查询在这里取一次，交给 KPI 与排行共用
api.ts: 其余七个接口的 zod schema、类型与 useXxx 查询 hook（backlog / overview / revenue / traffic nodes·users / system status / stats timeseries），刷新间隔与实时 topic 在这里声明；用户流量以节点流量的 snapshot_at 为锚点
model.ts: 纯逻辑——百分比 / 时长 / 延迟文案、按权限的 dashboardAccess 与 reachable、需要处理卡片（含通知积压合并与待补·前端的账本漂移卡）、KPI 较昨日、收入趋势与注册活跃的柱数据、备份摘要与系统状态行（R52：组件 metrics 缺字段处显示 —）、流量排行与未归属告警
model.test.ts: model.ts 全部分支与 schema 形状的单元测试（待补字段缺失时的退化、字节必须是字符串、R52 缺字段、只读账号只剩通知积压）
Tasks.tsx: 「需要处理」卡片网格（共用的 tasks 查询 + 冻结契约的通知积压明细），目标页不可读时卡片不可点；「超时未支付订单」带待支付筛选跳订单页
Kpis.tsx: 「经营」四格：按币种的今日收入、有效订阅、近 24 小时流量；调账、试用、即将到期、待支付放进 tooltip
RevenueTrend.tsx: 「收入趋势」CNY/USD × 7/30/90 天，区间合计、日均、较上一区间、柱图；切换时保留上一张图
SystemStatus.tsx: 「系统状态」总状态胶囊与组件行（components 未上时只有数据库一行），第 8 行「数据库备份」打开 BackupDrawer
BackupDrawer.tsx: 备份抽屉（待补·前端），backup 段逐项展示与最近 5 份表格，后端 message / identity_hint 原文照登
Activity.tsx: 「注册与活跃 · 近 14 天」成对柱图，active_users 未上时只画注册柱
TrafficRank.tsx: 「流量排行 · 近 24 小时」节点 / 用户两个页签，用户只显示脱敏邮箱（D-A-2 已决，5.A.2），行点进 #/users/list/<id>；底部小字给未归属与质量计数
parts.tsx: 本目录共用的小部件：卡片内错误行与重试、骨架、状态圆点、404 判定、链接拼接
Dash.module.css: 本目录唯一样式表，数值取自设计稿，只引用令牌

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
