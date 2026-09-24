# panel/internal/api/admin/
> L2 | 父级: /panel/internal/api/CLAUDE.md

管理控制台 API：只做路由、鉴权链与 DTO，业务编排在 domain。每条路由的门槛全部声明在 router.go：RequirePermission（缺权限回 404，不暴露接口存在）→ RequireRecentReauth（高危写 15 分钟内输过密码）→ Idempotency（重试不重做），reauth 失败不消耗幂等键。来源 IP 在审计里只存密文，这一层用 Deps.Envelope 解开、用 Deps.GeoIP 翻成归属地——解密集中在 profile.go 的 decryptWith，按表区分 AAD。

成员清单
router.go: Deps 与 NewRouter，全部 /v1 路由与逐路由权限、重认证、幂等 scope；根 / 与 /assets/* 经 webapp 下发后台前端
handlers.go: handlers 结构与核心处理器：登录、me、用户、订阅换链接、订单、节点列表（含 country_code）、工单队列与处理、降级开关
helpers.go: 包内共用小工具：域常量、请求级超时
access_log.go: 安全事件明细，audit_events 与 subscription_fetch_log 两路归并，分类规则展示与筛选共用
audit_log.go: 审计日志列表与 CSV 导出（security.audit.read + ops.export + reauth），导出日期区间格式错回 422，自由文本列做公式防护
risk.go: 风控共享 IP 聚类：列表补明文 IP、归属地与 high/mid/low 分级，标记为正常，批量停用聚类内账号
profile.go: IP 解密助手、用户风控画像、注册与活跃时序
dashboard.go / revenue.go / system_status.go: 仪表盘流量排行、收入趋势与调整、系统状态（备份、数据库、后台作业）
bulk_users.go / usergroup.go / devices.go / traffic_reset.go: 用户批量筛选导出生成群发、用户组、设备数限制、流量重置日志
catalog.go: 套餐目录、向导一次建成 / 改完、版本与价格
manual_order.go / late_payment.go: 人工开单（settlement: grant 赠送 / pending 待用户支付）与线下收款、挂账转余额
coupon.go / coupon_batch.go / giftcard.go / commission.go: 优惠券（路径 id 非 UUID 回中性 404）、批量生券、礼品卡与批次一次性导出、分销（总览带累计佣金、邀请数与计佣范围 scope）与提现审批
node_admin.go: 节点新建 / 编辑 / 复制 / 移动 / 排序 / 批量改状态，节点身份与令牌状态 nodeIdentity
server.go / pools.go: 服务器（物理宿主）读写与状态、节点分组
announce.go / content.go: 公告（草稿 / 定时 / 撤回）、知识库版本
appearance.go: 主题、插槽、Webhook 钩子与投递记录（含 duration_ms）
mail.go / mail_template.go / telegram.go: 邮件与注册设置、通知模板、Telegram 配置，三个测试发送挂 ops.notification.write
ticket_macros.go: 工单快捷回复的列表与增改删
events.go: 管理端 SSE
*_test.go: 路由契约与守卫（router_contract、security_guards、step4_test 与三份 AST 契约）、权限字典契约、处理器单测；*_pg18_test.go 为 PG18 集成测试，announcement 与 node_config 两个域同包，run-pg18-gates.sh 用精确 -run 过滤分开

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
