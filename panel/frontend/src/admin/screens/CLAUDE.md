# panel/frontend/src/admin/screens/
> L2 | 父级: /panel/frontend/src/admin/CLAUDE.md

后台十个模块的页面。Shell 只认 index.ts 这张登记表：按路由取出模块的懒加载组件，传入 { tab, rest }，外面包 shell/ScreenFrame（Suspense 骨架 + 错误边界）；读权限在 Shell 里按 modules.ts 先判过，页面拿到的一定是当前管理员能读的标签，页面内接口的 404 仍按「无权限或不存在」处理。
每个模块一个目录、构建出一个独立块，三个前端会话各改各的目录：后台前端一 dash / tickets / users / plans / billing，后台前端二 marketing / nodes / content / system / security。登记表与 Placeholder 不再改动；目录里加文件时由该模块的会话补本目录的 L2。
rest 是模块（与标签）之后剩下的路径段，已解码，由页面自己解释（如 #/users/list/<用户 id> 打开详情抽屉）；页面改地址用 core/router 的 navigate / href，查询串用 useHashLocation 读，外框规范化地址时保留查询串。

成员清单
index.ts: 页面登记表 SCREENS（十个 React.lazy）与页面入参类型 AdminScreenProps { tab: string | null; rest: string[] }
Placeholder.tsx: 占位页（第 2 阶段 Shell 的空状态），十个模块都换成真页面后删除
dash/: 仪表盘（后台-01），八个只读接口拼成的落地页；见 dash/CLAUDE.md
marketing/: 营销（后台-06）已接入：优惠券、礼品卡、佣金与提现；见 marketing/CLAUDE.md
tickets/: 工单（后台-02），左队列右详情，选中、筛选与搜索都在地址上；见 tickets/CLAUDE.md
users/: 用户（后台-03），列表 + 详情抽屉已接入（第 ③ 步），其余四个标签第 ④ 步接入；见 users/CLAUDE.md
nodes/: 节点与服务器（后台-07），第 ② 步接入节点标签（列表 + 五个标签的详情抽屉），服务器 / 节点池 / 路由待第 ③ 步；见 nodes/CLAUDE.md
plans/ billing/ content/ system/ security/: 其余模块目录，index.tsx 默认导出页面组件，未接入的渲染 Placeholder

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
