---
paths:
  - "panel/frontend/src/admin/screens/dash/**"
---

# 后台 · 仪表盘

- 版式（样稿 admin-v4 `#p-dash`，外框 `ownHead` 模式：页面自己画页头）：问候行（日期 +「下午好，<me 的名字>」+ 右侧「?」`PageHelpTip`）→ **要处理** → **今天** → **趋势** → **节点与系统**。分节标题在卡片外（`parts.tsx` 的 `Section`），卡片用 ui 的 `Card`，分段切换用 ui 的 `Segmented`。
- 要处理（`Tasks.tsx`，`model.ts` 的 `todoBoard`）：固定三格等宽——待处理工单、提现待审核、超时未支付订单（条目缺席按 0）；数据巡检（`integrity` 条目，每项 `code / count / checked_at`，后端 `report:integrity` 每 30 分钟写）有违例、或某项 `checked_at` 比接口的 `as_of` 早 2 小时以上（`INTEGRITY_STALE_MS`，巡检没在跑或该项每轮失败）时才出第四格「数据巡检」：标红，数字是出问题的检查项数（各项违例单位不同，不相加），小字列前两项「中文名 违例数」、多的写「等 N 项」，有没更新的再接「巡检没在跑：N 项超过 2 小时没更新」，去处跟第一项有违例的检查走（只有没更新的去账务页）。代号到中文名与去处的表是 `model.ts` 的 `INTEGRITY_CHECKS`，与后端 `report/service/integrity.go` 的常量与中文名逐项对应（加检查两边一起改，前端不认识的代号原样显示、排最后）。工单格与巡检格有事时顶部红线、数字标红。证书异常格（`node_certs`，有才出）的去处：只有 1 个且 sample 带 id 时直接打开那个节点（`#/nodes/nodes/<id>`，小字直接写节点名、不加「如」），多于 1 个去 `#/nodes/nodes?f=cert`（节点列表的证书筛选）。离线节点、通知积压**不进**要处理（归系统状态）。分节小字「N 项 · M 项紧急」（紧急 = 工单与数据巡检），全为 0 时「没有待处理的事」。
- 去处 `Target`（`model.ts`）：`{ module, tab, query?, id? }`，`id` = 打开某一条，由 `targetPath` 拼在 `modulePath(module, tab)` 后面（编码后），只用于 `DETAIL_ROUTES` 登记过的「模块:页签」；`parts.tsx` 的 `targetHref` 经它拼 href。
- 今天（`Kpis.tsx`，`kpiTiles`）：收入 / 付费订单 / 新注册 / 有效订阅四格，各带一行对照（昨天全天、待支付、近 7 天、7 天内到期），全部取 `GET v1/overview`；分节小字「截至 HH:mm」，右侧「看订单 ›」。没有近 24 小时流量格。
- 趋势（`Trend.tsx`，`chart.ts`）：一张卡，分节右侧切「收入 / 注册与日活」，卡内摘要栏右侧切 7 / 30 / 90 天（两种指标共用区间）。收入是蓝色（`--info`）折线 + 渐变面积，摘要「近 N 天净收入 + 较前 N 天 ±x%」；注册画柱、日活画灰色虚线（日活单独一条刻度），摘要「近 N 天新注册 + 日活均值」。不显示上一区间金额、日均、峰值。`GET v1/stats/timeseries` 收 `days`（1–90），与收入同区间。
- 节点与系统：两张卡等高（标题 48 / 表头 34 / 8 行每行 40 / 页脚）；等高靠 ui `Card` 内容区的 `flex: 1` 与网格拉伸，卡内不写最小高度。备份抽屉的文件表在手机上只留文件与时间（其余列 `hideOnMobile`）。
  - 流量排行（`TrafficRank.tsx`，`rank.ts`）：近 24 小时前 8 名（`limit=8`），节点 / 用户分段；每行名字底下铺按第一名比例的浅灰条、流量、占比（节点按 reported、用户按 attributed，BigInt 算）；页脚「合计 X · a/b 节点在线」/「合计 X · 前 N 名占 x%」+「全部节点 / 全部用户 ›」。节点行点进节点详情、用户行点进用户详情。没有排序、均值竖线、Σ 行、数据说明气泡。
  - 系统状态（`SystemStatus.tsx`，`system.ts`）：8 个组件全列，要关注的排前面；每行「状态圆点 + 名字 | 说明 | 指标」，标题右侧「N 项需要关注」/「全部正常」；页脚「每分钟检查 · 刚刚」+「备份详情 ›」。节点调度、邮件、Telegram 行点进对应页（节点调度有节点离线——`online < total`，与节点列表的离线筛选同口径——时去 `#/nodes/nodes?f=offline`，说明写「N 个节点离线」，配置未同步另成一句；系统状态不带节点 id，不直达单个），备份行与「备份详情」打开备份抽屉（`BackupDrawer.tsx`）。没有告警块和刷新按钮。
- 只有人民币：概览的 `revenue` 是单个对象，`GET v1/revenue/timeseries` 只收 `days`，待审提现是单个 `amount`；没有币种切换。
- 仪表盘登录即可进，没有自己的数据：每张卡各自发请求、各自画，各自加载、各自失败、各自重试，一张卡坏了不能牵连别的卡。新增卡片照此办。
- 每张图 / 列表有自己的空状态（「还没有收入 / 用户 / 流量」）并带一个去配置的链接（套餐、注册与邀请、节点）。
- 「要处理」和侧栏徽标共用 `src/admin/tasks.ts` 的同一条 `GET v1/dashboard/tasks` 查询与 schema，不在本目录另起。仪表盘不再请求 `v1/dashboard/backlog/notifications`（`api.ts` 的 `backlogSchema` 只剩 tests/smoke 核对；后端接口保留，容量测试 `panel/tests/perf/k6/bulk_mail.js` 靠它看群发后的积压清空曲线，10-21 核实）。
- 流量接口的字节一律是十进制字符串（numeric 聚合，可能超过 2^53），不是 JSON number；占比用 BigInt 算（`rank.ts`）。
- 分层：数据到卡片、行、文案的映射全部放纯函数（`model.ts` 问候 / 要处理 / 今天，`chart.ts` 趋势图，`rank.ts` 流量排行，`system.ts` 系统状态与备份摘要，都有单测），组件只负责摆放；共用小件（分节、箭头链接、卡内空 / 错 / 骨架）在 `parts.tsx`。
- 图表手写 SVG（`preserveAspectRatio="none"` + `non-scaling-stroke`），柱、圆点、刻度、气泡用 HTML 绝对定位；不引图表库；悬停与方向键逐点查看数值；动态 style 只用于条宽、坐标百分比。
- 颜色：图表与柱用 `--info`，虚线与次要文字 `--text-3`，不写 `--brand*`。
- 格式化（字节、计数、金额、时间）一律取 `core/format.ts`。
- 用户流量排行只显示脱敏邮箱（`email_masked`）。
- 页面说明放 `admin/page-help.ts` 的 `PAGE_HELP['dash']`，不写在页面里。
