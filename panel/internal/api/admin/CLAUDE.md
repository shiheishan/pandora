# panel/internal/api/admin/
> L2 | 父级: /panel/internal/api/CLAUDE.md

管理控制台 API：只做路由、鉴权链与 DTO，业务编排在 domain。每条路由的门槛逐条声明在 router_<模块>.go 里：RequirePermission（缺权限回 404，不暴露接口存在）→ RequireRecentReauth（高危写 15 分钟内输过密码）→ Idempotency（重试不重做），reauth 失败不消耗幂等键。来源 IP 在审计里只存密文，这一层用 Deps.Envelope 解开、用 Deps.GeoIP 翻成归属地——解密集中在 profile.go 的 decryptWith，按表区分 AAD。

成员清单
router.go: Deps 与 NewRouter：全局中间件链，/v1 挂 admin.writes 只读门（middleware.AdminWritesGate），根 / 与 /assets/* 经 webapp 下发后台前端，/v1 登录分组与已登录分组；已登录分组按拆分前的原顺序调用各 router_<模块>.go 的 register*Routes，顺序不要重排
router_<模块>.go: 按模块分段的路由表，每个 register*Routes(r, d, h) 声明一段路由的权限、重认证与幂等 scope。dashboard 仪表盘与收入；appearance 主题、插槽、插件钩子；notify Telegram、邮件设置、通知模板；users 批量运营、流量重置、用户状态、用户组、设备数；marketing 礼品卡、优惠券、分销；billing 挂账、订单（人工开单与标记已支付都挂重认证）、支付渠道、余额调账；catalog 套餐（含 registerCatalogPlanUpdate，套餐类新路由加这里）与流量包（registerTrafficPackRoutes，写接口 catalog.publish + 重认证 + 幂等）；security 审计、系统状态、风控、降级开关；nodes 节点分组、节点、服务器（含 nodeBatchStatusIdempotencyScope）；content 公告与知识库；support 工单与快捷回复
handlers.go: handlers 结构与核心处理器：登录、me（追加邮箱、显示名、角色）、用户（替用户重置密码的原因可选、限 500 字，R101）、订阅换链接、订单、节点列表（含 country_code、近 24h 流量、控制节点探针的 CPU / 内存，按 sort_order, node_no 排序）、工单队列与处理、降级开关（切换后向管理端频道发 switches.changed）
helpers.go: 包内共用小工具：域常量、请求级超时
access_log.go: 安全事件明细，audit_events 与 subscription_fetch_log 两路归并，分类规则展示与筛选共用；outcome 筛选（error = 非 success）
audit_log.go: 审计日志列表与 CSV 导出（security.audit.read + ops.export + reauth），导出日期区间格式错回 422，自由文本列做公式防护
risk.go: 风控共享 IP 聚类：列表补明文 IP、归属地与 high/mid/low 分级，标记为正常，批量停用聚类内账号
profile.go: IP 解密助手、用户风控画像（含注册 IP）、注册与活跃时序（含 active_users：成功拉取或有流量的去重用户）
dashboard.go / revenue.go / system_status.go / system_components.go: 仪表盘流量排行、通知积压、「需要处理」汇总（路由挂 ops.dashboard.read，逐项按主体权限过滤）、收入趋势（带上一区间合计 previous_total）与调整、系统状态（备份、数据库，以及 8 个组件的 state / components：postgres、valkey、节点、支付回调、邮件、Telegram、SSE 连接数、备份）
bulk_users.go / usergroup.go / devices.go / traffic_reset.go: 用户批量筛选导出生成群发（筛选含套餐、到期、订阅状态）、用户组（列表带 exclusive_pools；被节点池名单引用时删除回 409 写明池名；换组提交后发 node.users.changed）、设备数限制、流量重置日志
catalog.go: 套餐目录、向导一次建成 / 改完、版本与价格
traffic_packs.go: 流量包目录管理（后台-04 流量包 tab）：列表、新建、修改、上下架，updated_at 作乐观锁由客户端原样传回
manual_order.go / late_payment.go: 人工开单（settlement: grant 赠送 / pending 待用户支付 / offline 线下已收款，带 reference 当场结清）与标记线下已收款、挂账转余额
coupon.go / coupon_batch.go / giftcard.go / commission.go: 优惠券（路径 id 非 UUID 回中性 404）、批量生券、礼品卡与批次一次性导出、分销（总览带累计佣金、邀请数与计佣范围 scope）与提现审批
node_admin.go: 节点新建 / 编辑 / 复制 / 移动 / 排序 / 批量改状态 / 一步退役 nodeRetire（node.lifecycle + 重认证 + node_retire 幂等），节点身份与令牌状态 nodeIdentity
node_routing.go: 全局出站与分流 GET / PUT v1/nodes/routing（revision 乐观并发、持 node-config-release 锁、推进全部未退役节点并逐个通知）；validateRoutingPayload 是单节点与全局路由共用的校验
server.go / pools.go: 服务器（物理宿主）读写与状态、节点分组（列表带组内节点 members、绑定套餐名 plan_names 与「仅限用户组」allowed_user_groups；新建 / 编辑带 allowed_user_group_ids 时经 pool_user_groups.go 处理；套餐版本换绑池、池名单变化提交后发租户级 node.users.changed）
pool_user_groups.go: 节点池「仅限用户组」名单（R104，表 node_pool_user_groups）：请求带了字段才要求近期重认证（路由上的 reauth 中间件只能整条挂，这里按字段挂）、校验格式与租户内存在、整体替换、名单有变化时写 node_pool.user_groups_changed 前后对照审计
announce.go / content.go: 公告（草稿 / 定时 / 撤回，按套餐与用户组定向）、知识库版本
appearance.go: 主题、插槽、Webhook 钩子与投递记录（含 duration_ms）
site_settings.go: 站点时区读写（R49），即 tenants.timezone，按日用量与收入趋势的切日口径；时区名须能被 time.LoadLocation 加载，拒绝空串与 Local，改动写审计
mail.go / mail_template.go / telegram.go: 邮件与注册设置（全部 upsert，SMTP 密码行缺失也写得进，R94）、通知模板（列表带 has_default、草稿预览、草稿实发测试）、Telegram 配置（管理员群组 admin_chat_id 作测试默认目标），三个测试发送挂 ops.notification.write
ticket_macros.go: 工单快捷回复的列表与增改删
events.go: 管理端 SSE
*_test.go: 路由契约与守卫（router_contract、security_guards、step4_test、step5_test 与四份 AST 契约，order_pack_routes_test 另用真实注册函数证明未重认证到不了幂等与处理器；源码级契约经 router_source_test.go 读全部 router*.go）、权限字典契约、处理器单测；*_pg18_test.go 为 PG18 集成测试，announcement、node_config 与 delivery（delivery_pg18_test.go：交付集合变化后通知节点、节点列表交付判定与节点用户列表同口径；pool_user_groups_pg18_test.go：池名单的字段级 reauth、校验、审计、通知、删组被拒与换组后的实际下发；与 domain/subscription 同域）三个域同包，run-pg18-gates.sh 用精确 -run 过滤分开；phase4_pg18_test.go 归 announcement 域，守节点 PATCH 保留敏感键（R78）、钩子数值越界 422（R93）与建租户补种、新租户切开关、SMTP 密码 upsert（R94、R97）；reset_password_reason_test.go 守重置密码原因只限长度（R101）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
