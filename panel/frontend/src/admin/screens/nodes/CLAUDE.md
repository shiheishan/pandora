# panel/frontend/src/admin/screens/nodes/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

节点与服务器（后台-07，归后台前端二）。四个标签全部接入：第 ② 步节点（列表 + 五标签详情抽屉），第 ③ 步服务器（卡片 + 四标签详情抽屉）、节点池、全局路由。视觉按 管理后台-07-节点与服务器.dc.html，数据与规则按 api-contract.md 后台-07 的节点 / 服务器 / 节点池 / 路由四节（含 R10 R13 R26 R27 R46 R56 R57 R77–R79）；设计缺、契约标「待补·前端」的都补上了：新建节点弹窗、协议页的基本信息区、单节点路由编辑、交付提示、监控里的运行信息、复制的目标服务器与复制路由、一次性令牌展示框；服务器的添加弹窗、详情 / 下属节点 / 编辑 / 完整状态、在役与容量、从未心跳提示；节点池增删改；全局出站的增改删。
分层：schemas（zod；Go 指针字段写 nullable、nil 切片归一成 []，Go 保证非空的列表写严格数组）→ queries（读 hook、写后按 NK 前缀失效；节点列表与随节点变化的服务器、节点池计数挂 nodes.changed；转出 admin/actions.ts 的 endsIntent / useCan / useFailure / useIntentKey）→ logic（节点与路由）/ infra（服务器与节点池），纯函数，nodes.test.ts / infra.test.ts 守住 → 组件。
节点表单的后端口径：protocol_config 写入是整体替换（普通键缺席即删除），读接口按名字抹掉敏感键（password、private_key、psk、mask_password…），而 PATCH 里缺席的敏感键后端按原路径补回（R106）——所以 PATCH 只在协议字段真的改了才带 protocol_config；编辑同一协议时敏感字段留空 = 不改、不带这个键（必填的也不算缺），选填的敏感字段可点「清空」，保存前确认后显式发 null；换协议后端不补旧密钥，必填照常要填；mKCP 关掉掩码时 mask_password 本来就不带（R107）；协议 schema 由后端给出（13 个 stable + 2 个 legacy-read-compatible），表单按 allowed_properties 渲染、点号路径展开成嵌套对象，422 的 protocol_config.<键> 按路径再按叶子名落回字段。
保留规则 5：只有从未部署过的草稿（draft / disabled、无心跳、无身份）能迁移，其余引导「复制到新服务器再退役」。列表一次取 1000 条并总带 include_retired=1，「全部」里藏掉已退役，刚退役的节点抽屉仍可继续删除。
服务器三条后端事实：状态机 ready 不能直达 maintenance，所以卡片「标记维护」发 draining（卡片显示「维护中」），完整状态走详情里的合法边下拉；进入 ready 要有一个协议校验通过的在役节点，否则 409；删除只许草稿或已退役，名下节点不拒绝而是级联静默（身份吊销、摘掉 server_id），文案按此改写了设计的「先迁移或删除」。安装令牌固定传 ttl_minutes 30（后端缺省 20）。
全局路由：编辑在本地，「发布到全部节点」一次 PUT 带 expected_revision（全局配置没有行版本，用规范 JSON 的 sha256），要 reauth 与幂等键；匹配类型只放后端支持的（D-D-1），节点私有规则排在全局之前、私有兜底会遮住全局；删除仍被节点私有规则引用的全局出站回 409（R56），前端先拦住仍被全局规则引用的出站，改名时规则跟着改。节点池的「仅限用户组」（R104）：弹窗多选、名单变了才带 allowed_user_group_ids（带了就要 reauth，外框对话框接管），卡片在绑定套餐下追加「仅用户组『…』」，存后连用户组列表一起失效。无池节点不服务任何用户，列表与抽屉直接显示后端的 delivery_note（R105），不按 pool_id 另判。新节点接入完成后生命周期停在 attesting，抽屉「操作 › 上线」（R108 activate）一步推到 active 并让服务器就绪；此时「启用」置灰并指向「上线」。

成员清单
index.tsx: 页面入口，按标签分发：nodes → NodesTab（rest = [节点 id, 抽屉标签]）、servers → ServersTab（rest = [服务器 id, 抽屉标签]）、pools → PoolsTab、routing → RoutingTab
schemas.ts: 节点列表行、AdminNode、协议 schema、服务器与下属节点、节点池（members / plan_names / R104 allowed_user_groups）、身份、探针、单节点与全局路由、写操作响应（含 R108 上线只收用得到的字段）的 zod schema
queries.ts: 查询键前缀 NK、各读 hook（节点列表、服务器列表 / 详情 / 下属节点、节点池挂 nodes.changed）、useInvalidateNodes，转出 admin/actions.ts 的三件通用 hook 与 endsIntent（自己先处理 4xx 分支的写操作用它丢弃幂等键）
logic.ts: 节点状态映射（在线 / 离线 / 排空中 / 草稿 / 已停用 / 已退役）与筛选搜索、心跳与地址文案、迁移资格与 409 资产清单、R108 上线资格、合法状态边与批量取舍、排序提交项、协议表单模型（敏感字段留空不带 / 显式清空为 null、要清空的列表）、基本信息校验与新建体 / PATCH 差量、路由规则行互转与兜底校验、新规则插在兜底前、出站被引用计数与改名联动、出站行校验、带宽分桶
infra.ts: 服务器圆点与状态文字、三条占用与三档色、卡片快捷状态切换、合法状态边、删除资格与后果文案、节点按服务器分组、服务器表单校验 / 新建体 / PATCH 差量 / 容量冲突解析；节点池状态文字、删除资格、绑定套餐文字、R104「仅用户组」文字与名单是否改动、新建体与编辑差量（名单变了才带）
NodesTab.tsx: 节点列表：搜索、状态分段、勾选批量启停（只交允许的并计数跳过）、调整排序、新建节点弹窗，抽屉在 rest 里
NodeDrawer.tsx: 节点详情抽屉：头部状态与交付提示，监控 / 协议参数 / 路由 / 身份与令牌 / 操作五个标签，标签记在地址上
NodeForm.tsx: 新建与编辑共用的节点表单：基本信息 + schema 驱动的协议参数、REALITY 密钥生成；敏感字段留空 = 不改，选填的可「清空 / 撤回清空」，保存前确认
NodeMonitor.tsx: 监控：四个 KPI、近 24 小时带宽柱（手写）、运行信息、单节点路由摘要
NodeRouting.tsx: 单节点路由编辑（规则可指向 direct / block / 私有出站 / 全局出站）与私有出站逐行编辑；导出 RuleRows 规则行编辑器，全局路由复用
NodeIdentity.tsx: 身份与令牌：身份事实、签发一键安装令牌、重签服务端令牌、吊销身份；SecretModal 令牌与命令分两块、仅此一次可见（服务器详情的安装令牌复用）
NodeOps.tsx: 节点操作：上线（R108，接入尾段才显示）、发布配置、复制（目标服务器、复制路由）、迁移（保留规则 5）、启用 / 停用、退役、删除，每项无权限或状态不允许时写明原因
ServersTab.tsx: 服务器卡片网格：圆点、名称 / 地区 · IP / agent、三条占用、节点标签、在役 / 容量与从未心跳提示，卡片动作安装令牌（顶部深色命令条）/ 标记维护·恢复服务·投入服务 / 删除；添加服务器弹窗建成后紧接着签发安装令牌
ServerDrawer.tsx: 服务器详情抽屉：概览 / 节点（点行跳节点抽屉）/ 编辑 / 操作（合法边状态下拉、退役确认、安装令牌、删除）；导出 ServerFields 表单字段与 Meters 占用条供卡片与添加弹窗复用
serverActions.ts: 服务器三个写操作 hook：安装令牌（reauth + 幂等）、改状态、删除（reauth、带 JSON 体），卡片与抽屉共用同一套请求与失败处理
PoolsTab.tsx: 节点池三栏卡片（名称 · 节点数 · 状态 | 成员标签跳节点抽屉 | 绑定套餐与「仅用户组『…』」），新建 / 编辑弹窗（含「仅限用户组」多选，要 iam.user.read 才列组名），有节点或套餐时删除禁用并说明，其余阻碍靠 409 文案
RoutingTab.tsx: 全局出站与分流：左栏 RuleRows 规则，右栏内置出站与自定义出站列表（弹窗增改、被引用先拦、改名联动），发布确认「N 条规则会下发到 M 个在线节点」，revision 冲突刷新、引用冲突 409 就地提示
nodes.module.css: 节点列表八列网格、抽屉 KPI / 带宽柱 / 事实表 / 动作行 / 规则行 / 令牌块、表单网格；stack / faint / mono 等通用类服务器页也用
infra.module.css: 服务器卡片网格与占用条、深色安装命令条、服务器详情的下属节点与状态表单、节点池三栏卡片、路由两栏（窄于 1100 堆叠）
nodes.test.ts: logic 与节点 schema 边界的单元测试，协议部分直接用 dev 里导出的真实 schema
infra.test.ts: infra 与服务器 / 节点池 schema 边界的单元测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
