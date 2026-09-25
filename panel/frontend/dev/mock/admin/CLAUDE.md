# panel/frontend/dev/mock/admin/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

后台十个模块的假接口，一个模块一个文件、导出一个 MockModule，归属与 src/admin/screens 相同（后台前端一 dash / tickets / users / plans / billing，后台前端二 marketing / nodes / content / system / security）。index.ts 的登记顺序即询问顺序，不再改动。模块状态放在文件内的模块级变量里，vite 重启即复原。

成员清单
index.ts: 登记表 ADMIN_MODULES
users.ts: 用户（后台-03）：48 个确定性种子用户（前 6 个 id 与仪表盘流量排行一致）；列表（q 按邮箱 / 显示名 / 用户 id / 订阅令牌反查，status 逗号多值，group_id 含 none，sub_state，limit/offset）、详情、启停封禁、设新密码、换发订阅链接（不回令牌）、人工调账（reauth + 幂等；不在种子里的 id 回 404，tests/mock-api.test.ts 用真实种子 id 与种子余额）、分配用户组、用户组列表、单订阅设备上限、风控画像；订阅令牌只在内存里用于反查；套餐 id 固定（批量按 plan_id 筛）；第 ④ 步的接口在 users-ops.ts，按同一份用户数组展开进本模块
users-ops.ts: 用户第 ④ 步：用户组增删改（删除按成员 / 套餐 / 价格 / 优惠券回 409）、批量预览 / 导出 CSV / 生成 / 群发（筛选与 buildFilterSQL 同口径，导出与生成 reauth、生成与群发幂等，生成的账号进同一份用户数组）、在线设备与全局模式（grace 参与 exceeded）、流量重置日志 / 统计 / 单用户历史 / 手动重置（72 条确定性种子，omitempty 键省略，重置清零真实种子订阅的本期用量）；按 DisallowUnknownFields 拒绝未知字段
dash.ts: 仪表盘（后台-01）八个只读接口，照契约按权限回 404、tasks 条目按各自读权限过滤（withdrawals_pending 挂 marketing.commission.read）、校验 currency / days / range / limit / snapshot_at 并回契约里的 422；数据按日期确定性生成，概览今日 / 昨日与收入趋势末两天同源；含全部待补·后端字段
marketing.ts: 营销（后台-06）；优惠券、礼品卡（模板 / 统计 / 批次 / 掩码卡码 / 一次性导出 CSV / 使用记录）、佣金总览与提现、分销参数，权限 / reauth / 幂等 scope / 校验文案照契约与 Go 处理器，按 DisallowUnknownFields 拒绝未知字段；另临时挂了 GET v1/plans（营销页读套餐名与价格用，plans 模块在登记表里排在前面，后台前端一补上后自动失效，届时删除）
tickets.ts: 工单（后台-02）：队列（逗号分隔多状态、指派人、q、breached、limit/offset、后端同款排序）、详情、回复与内部备注、指派、改状态（人工升级提到 high、closed_reason）、SLA 扫描、可指派目录（固定三位客服 + 当前管理员）、快捷回复四接口；写接口照契约用幂等 scope，omitempty 字段为空时省略，related_order 恒 null、详情 last_reply_at 零值，与后端现状一致
nodes.ts: 节点与服务器（后台-07）的节点全套接口（列表、新建 / 编辑 / 复制 / 迁移 / 排序 / 批量改状态 / 退役 / 删除、REALITY、一键安装令牌、服务端令牌、吊销身份、发布配置、探针、单节点路由、身份），按合法状态边、保留规则 5、协议 schema 与 DisallowUnknownFields 校验，读接口按名字抹掉敏感键；把 nodes-infra.ts 的路由表并入同一个 MockModule，节点存储以参数传给它
nodes-infra.ts: 节点与服务器（后台-07）第 ③ 步：服务器（列表 status / q、新建、详情、下属节点、PATCH 清空与容量下限、合法边改状态与进入 ready 的前提、删除仅草稿或已退役并级联静默名下节点、安装令牌 reauth + 幂等）、节点池增删改（删除按节点 / 套餐 / 未用令牌 409）、全局出站与分流（revision 为规范 JSON 的 sha256、reauth + 幂等、删除被节点私有规则引用的出站 409）；导出与单节点路由共用的 validateRouting、空体判断 emptyBody、心跳保活 keepAlive（种子里在线的行每 10 秒刷新心跳，定时器 unref）
node-schemas.ts: 协议 schema 夹具，由 Go 的 nodefabric.ProtocolSchemas() 原样导出（含 null 数组与两个 legacy 协议），nodes.ts 与页面单测共用
content.ts: 内容与外观（后台-08）：公告列表（读时把到点的定时公告转发布）/ 新建 / 编辑（全量覆盖、expected_version、撤回终态、已发布不能退回草稿、套餐与用户组 id 与 users.ts 种子一致）/ 撤回；知识库每个版本一行（8 篇确定性种子含作者）/ 单版本含正文 / 保存为新版本（发布时归档同受众旧发布版）/ 归档（重复归档 already_archived）；只有生效的「默认 · 纸白」主题（令牌取自 design-tokens 的 43 个颜色）；七个插槽（粗略净化并回 dropped，空内容为 null）；站点时区（Intl 能加载的 IANA 名）；权限 / reauth / 幂等 scope / 文案照契约与 Go，按 DisallowUnknownFields 拒绝未知字段
system.ts: 通知与插件（后台-09 前半）：邮件设置（SMTP 六字段整体覆盖、密码空不改 / "-" 清空、from_name 空回站点名、开邮箱验证要 host 与发件人）与测试、Telegram（Token 只进不出、admin_chat_id 缺省不改 / null 清空、测试缺 chat 回落管理员群组）、12 个模板（种子、变量白名单、有无内置默认与 Go 一致，保存 / 恢复 / 草稿预览 / 测试信要 reauth 可发草稿）、钩子（按 code upsert、新建无密钥生成 whsec_ 一次性回传、https 与内网校验、超时 / 次数越界模拟 DB CHECK 的 500、删除、投递记录无记录 null、测试投递地址含 crm. 或 fail 时对方回 503）；权限 / reauth / 幂等 scope / 文案照契约与 Go，按 DisallowUnknownFields 拒绝未知字段
plans.ts / billing.ts / security.ts: 其余模块，未接入的为空壳

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
