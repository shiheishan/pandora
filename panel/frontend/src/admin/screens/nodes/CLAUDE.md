# panel/frontend/src/admin/screens/nodes/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

节点与服务器（后台-07，归后台前端二）。第 ② 步接入「节点」标签：列表 + 详情抽屉；服务器、节点池、路由三个标签是第 ③ 步，暂渲染占位页。视觉按 管理后台-07-节点与服务器.dc.html，数据与规则按 api-contract.md 后台-07 · 节点（含 R10 R13 R26 R27 R46 R57）；设计缺、契约标「待补·前端」的都补上了：新建节点弹窗、协议页的基本信息区、单节点路由编辑、交付提示、监控里的运行信息、复制的目标服务器与复制路由、一次性令牌展示框。
分层：schemas（zod；Go 指针字段写 nullable、nil 切片归一成 []）→ queries（读 hook、写后按前缀失效；转出 admin/actions.ts 的 useCan / useFailure / useIntentKey）→ logic（纯函数，nodes.test.ts 守住）→ 组件。
两条后端事实决定了表单写法：protocol_config 写入是整体替换，而读接口按名字抹掉敏感键（password、private_key、psk…），所以 PATCH 只在协议字段真的改了才带 protocol_config，协议改了而敏感字段留空时先确认会被清空；协议 schema 由后端给出（13 个 stable + 2 个 legacy-read-compatible），表单按 allowed_properties 渲染、点号路径展开成嵌套对象，422 的 protocol_config.<键> 按路径再按叶子名落回字段。
保留规则 5：只有从未部署过的草稿（draft / disabled、无心跳、无身份）能迁移，其余引导「复制到新服务器再退役」。列表一次取 1000 条并总带 include_retired=1，「全部」里藏掉已退役，刚退役的节点抽屉仍可继续删除。

成员清单
index.tsx: 页面入口，nodes 标签渲染 NodesTab（rest = [节点 id, 抽屉标签]），其余标签占位
schemas.ts: 列表行、AdminNode、协议 schema、服务器 / 节点池选项、身份、探针、单节点与全局路由、写操作响应的 zod schema
queries.ts: 查询键前缀 NK、各读 hook（列表挂 nodes.changed）、useInvalidateNodes，转出 admin/actions.ts 的三件通用 hook
logic.ts: 状态映射（在线 / 离线 / 排空中 / 草稿 / 已停用 / 已退役）与筛选搜索、心跳与地址文案、迁移资格与 409 资产清单、合法状态边与批量取舍、排序提交项、协议表单模型、基本信息校验与新建体 / PATCH 差量、路由规则行互转与兜底校验、带宽分桶
NodesTab.tsx: 节点列表：搜索、状态分段、勾选批量启停（只交允许的并计数跳过）、调整排序、新建节点弹窗，抽屉在 rest 里
NodeDrawer.tsx: 详情抽屉：头部状态与交付提示，监控 / 协议参数 / 路由 / 身份与令牌 / 操作五个标签，标签记在地址上
NodeForm.tsx: 新建与编辑共用的节点表单：基本信息 + schema 驱动的协议参数、REALITY 密钥生成、敏感字段清空确认
NodeMonitor.tsx: 监控：四个 KPI、近 24 小时带宽柱（手写）、运行信息、单节点路由摘要
NodeRouting.tsx: 单节点路由编辑（规则可指向 direct / block / 私有出站 / 全局出站）与私有出站；RuleRows 规则行编辑器第 ③ 步全局路由复用
NodeIdentity.tsx: 身份与令牌：身份事实、签发一键安装令牌、重签服务端令牌、吊销身份；SecretModal 令牌与命令分两块、仅此一次可见
NodeOps.tsx: 操作：发布配置、复制（目标服务器、复制路由）、迁移（保留规则 5）、启用 / 停用、退役、删除，每项无权限或状态不允许时写明原因
nodes.module.css: 列表八列网格、抽屉 KPI / 带宽柱 / 事实表 / 动作行 / 规则行 / 令牌块、表单网格
nodes.test.ts: logic 与 schema 边界的单元测试，协议部分直接用 dev 里导出的真实 schema

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
