# panel/frontend/src/admin/screens/plans/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

套餐（管理后台-04-套餐.dc.html）。两个标签：「套餐」#/plans/catalog/<套餐 id> 是左栏卡片 + 右侧详情（没选中时落到第一张），「流量包」#/plans/packs?s=<状态> 是后端有、设计稿缺、按同一风格补的表格（修订 R73）。
数据流：schemas（纯 zod，Go 的 nil 切片写 nullable 归一成 []，tests/mock-admin-plans.test.ts 也拿它核对假后端）→ api（读 hook 挂 plans.changed，写后按 PK 前缀整体失效）→ model（纯函数，model.test.ts 守住）→ 组件。写失败一律走 failure.ts 的 useCatalogFailure：503 是销售开关 AEGIS_SALES_ENABLED 没开，说清原因不劝重试；只有 422 的 fields 给表单（没有表单可标的确认框直接 Toast 出来），409 的 fields 是乐观锁现值，Toast 信封原文；幂等键去留仍由 actions.ts 的 endsIntent 定。
权限分层：catalog.read 看全部；catalog.write 才能新建版本、改草稿；catalog.publish 才有向导（新建与编辑，5.A D-C-2）、销售设置、归档、价格增删、节点池绑定、发布、流量包写操作。reauth 由外框对话框接管，取消时静默。
后端现状照实处理并在界面上说清：编辑向导（PUT complete）设备数改不回不限、额度或线路一变就开新版本并立即发布且新版本的宽限期与权益回到默认、会清掉上架时间窗；「新建版本」后端不复制，前端 POST versions → PUT 复制当前版本语义 → POST pools 复制绑定。
待决按「未决前」：D-C-1 不做「恢复上架」，归档写明不可恢复、暂停售卖引导到销售设置；D-C-5 向导不放限速（后端必定 422），版本里限速只跟「用完限速」策略走；D-E-3 不加特性列表与「推荐」字段（流量包自带 recommended，不受影响）。

成员清单
index.tsx: 页面入口，按标签分发到 CatalogTab / PacksTab
CatalogTab.tsx: 「套餐」标签：左栏「＋ 新建套餐」（catalog.publish）与套餐卡片（名称 / 代码 / 状态 / 起价 / 流量·设备或「未发布」/ 订阅数，已归档半透明），右侧 PlanDetailView；向导在这一层开合，新建成功后地址跳到新套餐
PlanDetail.tsx: 详情编排：头部（名称、状态、说明、用向导编辑 / 销售设置 / 归档套餐）、四格事实（有效订阅取列表行）、提示条（已发布套餐 node_count = 0 的空订阅警示、停止新购、可见范围、上架时间窗）、价格 / 线路两卡与版本卡；归档确认框（不可逆）
PriceCard.tsx: 「价格」卡：在售在前、已归档淡显在后，行上注明试用、用户组专属、时间窗；归档确认；新增价格（币种 + 金额 + 五档周期或自定义，「高级」里试用天数、用户组专属价、生效时间窗）
PoolCard.tsx: 「线路 · 节点池」卡：有草稿改草稿绑定（POST pools），没草稿走 PUT complete 只带 pool_ids（开新版本并发布，Toast changed）；导出向导也用的 PoolChips
Versions.tsx: 「版本」卡：版本行（摘要、草稿创建人 / 发布日、状态）、展开的编辑器（三项 + 「高级」，权益与其它配额原样回填）、保存草稿 / 另存为新版本、新建版本（建草稿 → 复制语义 → 复制绑定，半途失败说清现状）、发布确认（先列出看得出的前置条件）
Wizard.tsx: 五步向导（基本资料 + 可见范围与排序、用量与设备 + 新建时的流量重置、销售价格、可用线路、确认 + 购买限制与立即发布）；每步只校验本步，提交时前端或后端的 fields 跳到出错的那一步，步骤栏只给过了预检的步打 ✓；新建 POST complete，编辑 PUT complete（没改的额度 / 价格 / 线路发 null）
SalesDrawer.tsx: 「销售设置」抽屉（PUT v1/plans/{id} 整体覆盖）：可见范围与可见用户组、上架时间窗、三个购买开关、每人限购、库存（显示已预留）、排序；导出向导也用的 GroupPicker 与 VISIBILITY_OPTIONS
PacksTab.tsx: 「流量包」标签：状态分段（在地址上）、表格、新建 / 编辑抽屉（updated_at 乐观锁，409 时刷新）、上下架确认
failure.ts: useCatalogFailure 与 SALES_OFF，见上文写失败口径
schemas.ts: 封闭枚举与 zod schema：列表行、价格行、详情、版本行（含 R66 created_by_email）、节点池候选、流量包行、各写响应（R65 向导新建返回完整详情）
api.ts: 读 hook（usePlans、usePlan、usePlanPools、usePoolOptions、useTrafficPacks）、PK 查询键前缀、useInvalidatePlans，转出 schemas；新建向导的节点池候选取 GET v1/node-pools（node.read），只收 id / 名称 / 状态 / 在线数，查询键与节点页分开
model.ts: 纯逻辑：状态 / 可见性 / 重置 / 超额文案，周期映射（五档预设、季付 = month×3、自定义），元 ↔ 分、GB ↔ 字节，起价与额度文案，版本表单与 quotas 同步（traffic.bytes / devices.active），向导两种提交体与校验（键名同 Go），销售设置、新增价格、流量包的表单与校验，datetime-local 互转
model.test.ts: schema 归一与封闭枚举、周期与金额、版本表单、向导新建 / 编辑提交体与后端限制、fields 落步、销售设置、新增价格、流量包的单元测试
Plans.module.css: 唯一样式表，数值取自设计稿；详情栏开 container query，960 宽时版本行与表单改紧凑排布

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
