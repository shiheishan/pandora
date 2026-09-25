# panel/frontend/src/admin/screens/content/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

内容与外观（后台-08，归后台前端二）。三个标签全部接入：公告、知识库、主题与插槽。视觉按 管理后台-08-内容与外观.dc.html，数据与规则按 api-contract.md 后台-08 三节（含 R19、R49）并对过 Go 的 announce.go、domain/content/service.go、appearance.go、site_settings.go；设计缺、契约标「待补·前端」的都补上了：公告的级别、定时发布、自动下线、保存草稿与「套餐 + 用户组」多选可见范围，知识库的类型切换与搜索、新文章标识、发布设置折叠区、历史版本只读查看与恢复，主题区旁的站点时区卡。
分层：schemas（zod；Go 指针字段 nullable、omitempty 字段 optional，插槽保存的 dropped 在空内容时为 null 按代码事实收）→ queries（读 hook，公告挂 announcements.changed，其余写后按 CK 前缀失效；转出 admin/actions.ts 的 useCan / useFailure / useIntentKey）→ logic（纯函数，content.test.ts 守住）→ 组件。写操作的幂等键：成功后 reset，失败交给 fail(e, { intent })，4xx 业务拒绝在 actions.ts 里丢弃（公共规则 10.4、R85；reauth 取消与断网、5xx 保留）。
三条后端事实决定了编辑区写法：公告与知识库的写都是全量覆盖 / 追加新版本，所以表单记下「从哪一版载入」，版本变了而本地没改就换成最新、本地改了就提示「保存会覆盖 / 会在新版本之上再存」并可载入最新，而不是把用户的输入冲掉；公告已撤回是终态、已发布不能退回草稿（D-D-3 已决（5.A.2）：已撤回只读，给「复制为新公告」）；知识库发布只归档「同一受众组合」的旧发布版，受众字段默认沿用上一版，改了就提示旧版不会被自动归档。
主题按 5.A D-D-4 / D-E-4 只画生效的「默认 · 纸白」一张卡片，「保存新主题」、激活、删除都不出现，custom_css 不出现；插槽失焦时内容变了才保存（每次 reauth + 新幂等键），保存后回填净化后的内容、被过滤的片段逐条提示。可见范围里的「即将到期」按 D-D-2（已决，5.A.2）不提供。

成员清单
index.tsx: 页面入口，按标签分发：announce → AnnounceTab（rest = [公告 id | new]）、kb → KbTab（rest = [slug | new]）、theme → ThemeTab
schemas.ts: 公告列表与写响应、内容页（列表行 / 单版本 / 保存 / 归档）、主题（R19 light / dark 两组令牌）、插槽、站点时区、套餐目录子集的 zod schema 与枚举
queries.ts: 查询键前缀 CK、各读 hook（公告挂 announcements.changed；内容页按类型取全部版本；单版本同一篇文章换新版本时保留上一版数据免得编辑区卸载；套餐目录只在有 catalog.read 时请求；站点时区只在有 security.audit.read 时请求）、useInvalidateContent，转出三件通用 hook
logic.ts: datetime-local / date 与 RFC3339 互转（未改动的原样送回）、公告状态文字 / 列表时间 / 可见范围文字 / 表单校验 / 全量请求体 / 按钮取舍、知识库按 slug 聚成文章（最新版、最大已发布版、是否归档）/ 按分类分组与前端搜索 / 表单校验 / 请求体 / 受众改动判断、站点时区选项与 UTC 偏移、插槽脏判断与过滤提示、主题预览色与站点名
AnnounceTab.tsx: 公告左栏（新建、公告卡片：置顶、警告 / 紧急级别、状态彩字、可见范围 · 时间，已撤回淡显）与右栏分发，选中项在地址上，新建建成后换成新 id，「复制为新公告」带预填
AnnounceEditor.tsx: 公告编辑区：标题、正文、可见范围菜单（套餐 · / 用户组 · 开关）、级别、置顶、定时发布、自动下线；保存草稿 / 发布 / 定时发布 / 保存修改 / 撤回（确认框说清后果）/ 复制为新公告，版本变化时的覆盖提示
KbTab.tsx: 知识库三栏：目录（类型分段、搜索、按分类分组、已归档淡显、新文章）、中栏编辑或只读查看历史版本（以此版本恢复、归档仍在发布的旧版）、右栏版本历史（作者、日期、状态）
KbEditor.tsx: 文章编辑区：新文章标识、标题、分类（已有分类 datalist + 自定义）、正文、发布设置（类型、可见性、摘要、语言、复审日期、客户端版本范围、平台、限定套餐菜单）、受众改动提示；保存草稿 / 保存为 v{n} / 归档 / 恢复，新版本出现时的提示
ThemeTab.tsx: 主题与插槽：生效主题卡片（预览、内置、使用中、色块、站点名）、站点时区卡（下拉 + 说明，改要 platform.settings.write + reauth）、七个插槽行（名称 / key / 位置、失焦保存、开关）
content.module.css: 公告两栏（窄于 1180 左栏 260）、知识库三栏（窄于 1180 时版本历史移到编辑区下方）、主题卡片网格与预览、时区卡、插槽三列行；主题预览色经 --pv-* 自定义属性传入
content.test.ts: logic 与 schema 边界的单元测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
