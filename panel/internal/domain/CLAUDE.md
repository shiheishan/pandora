# panel/internal/domain/
> L2 | 父级: /panel/internal/CLAUDE.md

业务用例层：每个包对应一个业务域，只依赖 platform，不依赖 api 和彼此的内部实现。状态机与账本规则由数据库触发器兜底，这里负责编排事务、幂等键与领域校验。

成员清单
adminops/: 管理后台的读写用例
  - 含审计读模型 audit.go、风控聚类 risk.go、后台访问明细 access_log.go、用户画像 user_profile.go、系统状态 system_status.go、套餐绑池 plan_pools.go
  - 套餐目录用例拆成「事务外校验 + *Tx 事务体」，向导编辑 plan_wizard_update.go 把资料、价格、版本发布编排进同一事务
  - 见 adminops/CLAUDE.md
appearance/: 主题与外观配置（多套主题、一套生效），service.go 读写、tokens.go 设计稿令牌白名单与站点名、branding.go 站点品牌规则、sanitize.go 清洗插槽 HTML；见 appearance/CLAUDE.md
billing/: 订单、支付与复式账本（结算主链在 settlement.go；主动查单、后台查单审计与定时巡检在 payment_query.go / payment_query_audit.go / payment_query_patrol.go）
  - traffic_pack.go 是流量包（D-E-1）目录、下单（kind=addon）与用户级余额，plan_change.go / plan_change_quote.go 是变更套餐（D-E-2，kind=upgrade）的剩余价值折算、下单与原地履约，order_holds.go 是各建单路径共用的预留父节点与余额冻结
  - 借贷配平与回调幂等在此编排
  - commission_available.go 是「可用佣金 = 账本余额 − 未过账在途提现」的唯一口径，提现申请与转余额在同一把科目锁下共用
  - 见 billing/CLAUDE.md
content/: 版本化知识库与自定义页面投递，门户可见性一处判定，「有帮助」反馈按版本记；见 content/CLAUDE.md
dbbackup/: 数据库备份离机与保留：
  - config 配置与私密文件读取、webdav 上传回读、manifest 签名清单与 linux 锁、retention 保留策略、checkpoint_hook 检查点复制、file_open/file_owner 的 linux/other 双实现（私密路径只信 root）
  - 见 dbbackup/CLAUDE.md
giftcard/: 礼品卡与卡密，对标 Xboard gift-card
  - batches.go 管批次与一次性导出，明文卡码只在生码样例与那一次导出里出站，其余读模型一律经 MaskCode 掩码
  - codes_export.go 按筛选导出掩码对账报表，SQL 里算掩码、不取明文
  - 见 giftcard/CLAUDE.md
identity/: 注册、验证与登录；见 identity/CLAUDE.md
nodefabric/: 节点接入与配置下发（PRD 第 8–9 章），含后台节点列表、旧状态接口、节点分组与池名单、设备数限制的后台用例
  - 路由三个范围（全局 / 路由组 / 节点，00096）的编辑在 routing_admin.go 与 route_group*.go、生效合并只有 routing_merge.go 一处
  - protocol_secrets.go 让节点 PATCH 保留读接口抹掉的敏感键
  - node_retire.go 一步退役、node_activate.go 一步上线
  - usage_daily.go 在流量上报事务内累加按日用量（00072）
  - enrollment、node/server admin、node_identity 凭据视图、service
  - 见 nodefabric/CLAUDE.md
notify/: 站内信、邮件，以及到期与流量预警；见 notify/CLAUDE.md
payment/: 支付渠道适配器接口（PAY-002）与跨渠道通用的金额换算，另含 demo/、epay/ 两个渠道子包；不依赖 httpx，渠道停用以 ErrProviderDisabled、不支持的操作以 ErrNotSupported 哨兵交给 billing 翻译（demo 不支持主动查单）
plugin/: 出站 webhook 插件钩子 hooks.go 与事件发射 emit.go，投递记往返耗时；见 plugin/CLAUDE.md
subscription/: 订阅分发与门户按日用量读模型；current.go 是后台「当前订阅 / 在用」口径的唯一真相源，adminops 与 support 引用；见 subscription/CLAUDE.md
support/: 工单（OPS-001）与客服快捷回复（用户侧、客服侧、超时升级分文件）；见 support/CLAUDE.md

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
