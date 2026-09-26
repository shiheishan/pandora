# panel/frontend/src/admin/screens/
> L2 | 父级: /panel/frontend/src/admin/CLAUDE.md

后台十个模块的页面。Shell 只认 index.ts 这张登记表：按路由取出模块的懒加载组件，传入 { tab, rest }，外面包 shell/ScreenFrame（Suspense 骨架 + 错误边界）；读权限在 Shell 里按 modules.ts 先判过，页面拿到的一定是当前管理员能读的标签，页面内接口的 404 仍按「无权限或不存在」处理。
每个模块一个目录、构建出一个独立块，三个前端会话各改各的目录：后台前端一 dash / tickets / users / plans / billing，后台前端二 marketing / nodes / content / system / security。登记表不再改动；目录里加文件时由该模块的会话补本目录的 L2。十个模块全部接入后（后台前端二第 ⑥ 步）占位页 Placeholder 已删除。
rest 是模块（与标签）之后剩下的路径段，已解码，由页面自己解释（如 #/users/list/<用户 id> 打开详情抽屉）；页面改地址用 core/router 的 navigate / href，查询串用 useHashLocation 读，外框规范化地址时保留查询串。

成员清单
index.ts: 页面登记表 SCREENS（十个 React.lazy）与页面入参类型 AdminScreenProps { tab: string | null; rest: string[] }
dash/: 仪表盘（后台-01），八个只读接口拼成的落地页；见 dash/CLAUDE.md
marketing/: 营销（后台-06）已接入：优惠券、礼品卡、佣金与提现；见 marketing/CLAUDE.md
tickets/: 工单（后台-02），左队列右详情，选中、筛选与搜索都在地址上；见 tickets/CLAUDE.md
users/: 用户（后台-03），五个标签全部接入：列表 + 详情抽屉（第 ③ 步）、用户组 / 批量运营 / 设备策略 / 流量重置（第 ④ 步）；见 users/CLAUDE.md
nodes/: 节点与服务器（后台-07）四个标签已接入：节点（列表 + 五标签详情抽屉）、服务器（卡片 + 四标签详情抽屉）、节点池、全局路由；见 nodes/CLAUDE.md
content/: 内容与外观（后台-08）三个标签已接入：公告（列表 + 编辑区，定时、级别、套餐与用户组定向）、知识库（目录 + 编辑区 + 版本历史）、主题与插槽（生效主题卡、站点时区卡、七个插槽）；见 content/CLAUDE.md
plans/: 套餐（后台-04），两个标签：套餐（左栏卡片 + 详情：价格 / 线路 / 版本、五步向导、销售设置、归档）与流量包（R73）；见 plans/CLAUDE.md
billing/: 订单与收款（后台-05），四个标签：订单（列表 + 抽屉、人工开单、标记已支付、取消）、挂账（保留规则 6）、支付渠道（R66）、收入调整；见 billing/CLAUDE.md
system/: 通知与插件（后台-09 前半）三个标签已接入：通知渠道（SMTP、注册与验证、Telegram）、邮件模板（渠道分段、防抖预览、实发测试信）、Webhook 钩子（新建、启停、编辑、投递记录、测试投递）；见 system/CLAUDE.md
security/: 安全与运维（后台-09 后半）四个标签已接入：审计日志（搜索、筛选、分页、导出）、访问日志（安全事件的实时尾随）、风控（共享 IP 聚类、标记正常、批量停用）、降级开关（确认框带原因、核心项锁定、switches.changed 实时刷新）；见 security/CLAUDE.md

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
