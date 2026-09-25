# panel/internal/domain/nodefabric/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

节点编排（PRD 第 8–9 章）：节点接入、身份、配置签发与下发、数据面兼容接口，以及后台的节点 / 服务器管理。两套状态并存：生命周期 status（node_transitions 触发器强制）与服务状态 serving_status（Go 内强制）。凭据只存哈希；配置与有效发布物用 Ed25519 签名，节点端 fail closed。

成员清单
service.go: Service 与构造；bootstrap 令牌与旧版接入、身份查询、签名请求、心跳、配置签发与回报、指标
enrollment.go: 两阶段接入 Begin/Commit：先占用令牌并落不可用的候选凭据，提交时才激活身份与 server_token（签发记录重置为无签发人）
uniproxy.go: UniProxy 兼容数据面（LoadRouting 与 effective_release_service 的 loadEffectiveRoutingTx 同一口径：节点私有规则在前、全局规则在后）：节点鉴权、server-token 签发（写审计、记 server_token_issued_at/by、拒绝已退出服务的节点）、配置组装与 ETag、用户下发（只给套餐绑定了节点所在池的订阅，无池节点不下发任何人，R104）、流量与在线上报；扣量先吃套餐本周期额度、再按先到先扣吃用户流量包（D-E-1），套餐用完但流量包有剩余的订阅继续下发；逐用户记账委托 usage_daily.go
usage_daily.go: 流量上报的单用户记账 chargeReportEntry：同一事务里扣量（chargeTraffic）并累加 subscription_usage_daily 当日行（00072，重试报文两边都不记）；UsageLocation / UsageDay 是按日流量唯一的日界口径（用户时区 → 租户时区 → UTC；用户为默认 UTC 时视同未设、跟随站点时区（R50）；内嵌 time/tzdata），subscription 的读接口共用
node_admin.go: 后台节点增删改、复制、移动、排序、批量状态，协议白名单与稳定协议 SQL；country_code（00082）只在此写、只进管理端响应（保留规则 3）
node_retire.go: 一步退役 RetireNode：持 node-config-release 锁，生命周期按 node_transitions 合法边推进到 retired（active 等经 draining、canary 经 standby；draft 与接入失败态只改服务状态），服务状态 retired、清 desired_config_version、吊销有效身份、在途任务置 failed，拒绝在役服务器的控制节点
node_identity.go: 节点凭据只读视图 NodeCredentials：当前或最近一份 mTLS 身份、服务端令牌是否存在及签发时间与签发人、未用未过期的安装令牌数
server_admin.go: 后台服务器（物理宿主）读写与状态机
protocol_schema.go: 各协议的配置约束元数据与规范化节点类型
xboard_field_names.go / xboard_validate.go: 后台 xboard 字段名与内核字段名的翻译，校验复用同一翻译
effective_release_codec.go / effective_release_service.go: 按节点物化的不可变有效发布物，签名字段编解码与生成
config_key_transition.go: 配置签名密钥轮换的过渡声明与校验
nodestream.go / nodestream_event.go: 节点长连接推送（内存 StreamHub）与事件定义；NotifyUsersChanged 发租户级 node.users.changed，供改变交付集合的后台写路径（套餐换绑池等）在提交后调
userdelta.go: 用户列表增量下发
testdata/: 生产协议配置样本与 VLESS 迁移往返样本
*_test.go: 单元与契约测试；*_pg18_test.go 为 PG18 集成测试（effective 与 enrollment 两个域，server_token_pg18_test.go 共用 enrollment 的 openEnrollmentPG18；traffic_charge_pg18_test.go 与 usage_daily_pg18_test.go 共用 traffic_charge 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
