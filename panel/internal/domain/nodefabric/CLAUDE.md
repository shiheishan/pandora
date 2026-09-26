# panel/internal/domain/nodefabric/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

节点编排（PRD 第 8–9 章）：节点接入、身份、配置签发与下发、数据面兼容接口，以及后台的节点 / 服务器管理。两套状态并存：生命周期 status（node_transitions 触发器强制）与服务状态 serving_status（Go 内强制）。凭据只存哈希；配置与有效发布物用 Ed25519 签名，节点端 fail closed。

成员清单
service.go: Service 与构造（GeoIP、上一把配置签名密钥的注入）、节点身份查询（吊销即失效，NODE-014）与签名请求（V2 规范化载荷、nonce 防重放、5 分钟接受窗口），以及 canonicalJSON 等共用助手
bootstrap.go: 一次性 bootstrap 令牌（NODE-008，只存绑定节点名的哈希，接入命令模板占位符经 shellQuote 转义）与旧版接入 Bootstrap（持 node-config-release 锁，令牌消费与身份签发同事务，终态节点只有服务器删除级联静默的才许同名重装）
heartbeat.go: 心跳（AGT-004，指标一律放大成整数）与探针指标的查询、清理
config_delivery.go: 旧版配置签发 FetchConfig（全局 → 池 → 节点分层合并、规范化后 Ed25519 签名，RLS 未命中回中性 404，退役节点拒绝）、VerifyConfigSignature（面板与节点端共用口径）与配置回报（要求唯一匹配的已发布配置），有效发布物的回报 ReportEffectiveConfigApplied
config_publish.go: 旧版配置发布 PublishConfig：锁序为 node-config-release 发布锁 → 目标池 / 节点行 FOR SHARE → 受影响节点行 → 全租户版本分配 → 取代旧层 → 写新层；lockLegacyConfigRelease / syncLegacyDesiredConfigVersion 与接入、后台建节点、退役、上线共用
enrollment.go: 两阶段接入 Begin/Commit：先占用令牌并落不可用的候选凭据，提交时才激活身份与 server_token（签发记录重置为无签发人）
uniproxy.go: UniProxy 兼容数据面：节点鉴权、server-token 签发（写审计、记 server_token_issued_at/by、拒绝已退出服务的节点）、用户下发（只给套餐绑定了节点所在池的订阅，无池节点不下发任何人；池限定用户组时只给名单内组的用户，谓词 PoolAdmitsUserSQL 与订阅下载共用，R104；套餐用完但流量包有剩余的订阅继续下发）、在线与运行状态上报（在线数窗口按租户设置，DeviceWindowMinutes 为可选值，PurgeStaleAlive 截止 70 分钟，R103）
uniproxy_config.go: UniProxy 配置组装与 ETag（LoadRouting 与 effective_release_service 的 loadEffectiveRoutingTx 同一口径：节点私有规则在前、全局规则在后），路由匹配条件翻成节点端 qnode 形状
uniproxy_traffic.go: 流量上报：按用户排序逐个记账，扣量先吃套餐本周期额度、再按先到先扣吃用户流量包（D-E-1），先锁配额行再锁流量包；逐用户记账委托 usage_daily.go
usage_daily.go: 流量上报的单用户记账 chargeReportEntry：同一事务里扣量（chargeTraffic）并累加 subscription_usage_daily 当日行（00072，重试报文两边都不记）；UsageLocation / UsageDay 是按日流量唯一的日界口径（用户时区 → 租户时区 → UTC；用户为默认 UTC 时视同未设、跟随站点时区（R50）；内嵌 time/tzdata），subscription 的读接口共用
node_admin.go: 后台节点新建、读取与 PATCH，协议白名单与稳定协议 SQL（stableProtocolTypes 必须留在本文件，协议对齐门按文件名读）；PATCH 的 protocol_config 整体替换，但请求里缺席的敏感键经 protocol_secrets 补回（R78）；country_code（00082）只在此写、只进管理端响应（保留规则 3）
node_admin_placement.go: 后台节点复制（发布锁下物化当前适用配置）、移动到另一台服务器、排序，均带 row_version 乐观锁与审计
node_admin_lifecycle.go: 服务状态迁移表（只在 Go 内强制）与批量改服务状态（先取发布锁再锁节点行，退役同事务清 desired_config_version、吊销有效身份）、删除节点的三道守卫
node_retire.go: 一步退役 RetireNode：持 node-config-release 锁，生命周期按 node_transitions 合法边推进到 retired（active 等经 draining、canary 经 standby；draft 与接入失败态只改服务状态），服务状态 retired、清 desired_config_version、吊销有效身份、在途任务置 failed，拒绝在役服务器的控制节点
node_activate.go: 一步上线 ActivateNode（R108，与 node_retire.go 对称）：持 node-config-release 锁，接入尾段（attesting 至 canary）按 node_transitions 合法边逐条推到 active（每步过触发器），服务状态按 ProjectNodeLifecycle 投影（旧状态接口同一份映射），服务器按服务器状态机同事务进 ready；前置条件为有效未过期身份、协议就绪、绑着未删除且能进 ready 的服务器，不满足回 409；已 active 幂等不改；返回 AdminNode 与无池 / 池未绑套餐的 warnings
node_refusal.go: NodeStatusRefusal 改节点生命周期时数据库拒绝的统一翻译（后台改状态、一步上线、一步退役三处共用）：状态机触发器的中文原样透传，nodes 表 CHECK 按约束名译中文、认不出的写通用中文，英文原句只进日志；三处 UPDATE 实际只撞得到触发器，约束翻译是兜底（⑪）
node_identity.go: 节点凭据只读视图 NodeCredentials：当前或最近一份 mTLS 身份、服务端令牌是否存在及签发时间与签发人、未用未过期的安装令牌数
server_admin.go: 后台服务器（物理宿主）读写与状态机
protocol_schema.go: 各协议的配置约束元数据与规范化节点类型；RedactProtocolConfig 按键名表 sensitiveProtocolKey 抹掉敏感值，读接口共用；键名表必须覆盖每个 schema 的 SensitiveProperties（含 mask_password），单测守住两份名单不分叉；ProtocolSchemas 必须留在本文件、node_admin.go 的 stableProtocolTypes 必须留在 node_admin.go：pdnd/release/check_native_panel_parity.py（CI 的协议对齐门）按文件名读这两处
protocol_validate.go: 协议配置的内核形状校验入口 ValidateProtocolConfig（逐协议逐字段，返回字段级中文错误）与严格 JSON 解码（拒绝重复键与尾随内容）
protocol_validate_transport.go / protocol_validate_reality.go / protocol_validate_mkcp.go: 校验分项：传输层（WS / HTTP / XHTTP / Hysteria2 / AnyTLS / ShadowTLS / 服务器名）、REALITY（字段与 X25519 密钥，GenerateRealityKeypair）、mKCP（MTU 与掩码开销，与节点端 kernel/mkcp_transport.go 同值）
protocol_secrets.go: RedactProtocolConfig 的逆运算 PreserveRedactedProtocolSecrets：只补抹掉的那几条路径，显式给值（含空串、null）以请求为准，数组长度变化不补，换协议类型不补；挂在开关上的密钥（secretGates：mask_password 跟 mask）开关变了不补，关掉 mKCP 掩码不必显式清口令
xboard_field_names.go / xboard_validate.go: 后台 xboard 字段名与内核字段名的翻译，校验复用同一翻译
effective_release_codec.go / effective_release_service.go: 按节点物化的不可变有效发布物，签名字段编解码与生成
config_key_transition.go: 配置签名密钥轮换的过渡声明与校验
nodestream.go / nodestream_event.go: 节点长连接推送（内存 StreamHub）与事件定义；NotifyUsersChanged 发租户级 node.users.changed，供改变交付集合的后台写路径（套餐换绑池等）在提交后调
userdelta.go: 用户列表增量下发
testdata/: 生产协议配置样本与 VLESS 迁移往返样本
*_test.go: 单元与契约测试（pool_admission_test.go 钉住 PoolAdmitsUserSQL 的白名单与唯一用法；device_window_test.go 钉住设备窗口可选值与迁移 00094 一致、清理截止大于最大窗口、后台节点列表不写死窗口；node_activate_test.go 钉住上线路径只走 00005 的边且经 canary 进 active；node_refusal_test.go 钉住约束名翻译并守住全仓不再把 db.Message 直接塞进 httpx 错误）；*_pg18_test.go 为 PG18 集成测试（effective 与 enrollment 两个域，server_token_pg18_test.go 共用 enrollment 的 openEnrollmentPG18；traffic_charge_pg18_test.go 与 usage_daily_pg18_test.go 共用 traffic_charge 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
