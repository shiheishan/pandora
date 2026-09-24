# panel/internal/domain/
> L2 | 父级: /panel/internal/CLAUDE.md

业务用例层：每个包对应一个业务域，只依赖 platform，不依赖 api 和彼此的内部实现。状态机与账本规则由数据库触发器兜底，这里负责编排事务、幂等键与领域校验。

成员清单
adminops/: 管理后台的读写用例，11 文件（含审计读模型 audit.go、风控聚类 risk.go）；套餐目录用例拆成「事务外校验 + *Tx 事务体」，向导编辑 plan_wizard_update.go 把资料、价格、版本发布编排进同一事务；见 adminops/CLAUDE.md
appearance/: 主题与外观配置，service.go 读写、tokens.go 设计稿令牌白名单与站点名、sanitize.go 清洗插槽 HTML；见 appearance/CLAUDE.md
billing/: 订单、支付与复式账本，22 文件；traffic_pack.go 是流量包（D-E-1）目录、下单（kind=addon）与用户级余额，plan_change.go / plan_change_quote.go 是变更套餐（D-E-2，kind=upgrade）的剩余价值折算、下单与原地履约，order_holds.go 是各建单路径共用的预留父节点与余额冻结；借贷配平与回调幂等在此编排；commission_available.go 是「可用佣金 = 账本余额 − 未过账在途提现」的唯一口径，提现申请与转余额在同一把科目锁下共用；见 billing/CLAUDE.md
content/: 版本化知识库与自定义页面投递，门户可见性一处判定，「有帮助」反馈按版本记；见 content/CLAUDE.md
dbbackup/: 数据库备份编排，12 文件：config 配置、manifest 签名清单与 linux 锁、retention 保留策略、checkpoint_hook 备份前检查点、file_open/file_owner 的 linux/other 双实现
giftcard/: 礼品卡与卡密，对标 Xboard gift-card，4 文件；明文卡码只在生码样例与批次一次性导出里出站，其余读模型一律经 MaskCode 掩码；见 giftcard/CLAUDE.md
identity/: 注册、验证与登录，9 文件；见 identity/CLAUDE.md
nodefabric/: 节点接入与配置下发（PRD 第 8–9 章），16 文件；usage_daily.go 在流量上报事务内累加按日用量（00072）；enrollment、node/server admin、node_identity 凭据视图、service；见 nodefabric/CLAUDE.md
notify/: 站内信、邮件，以及到期与流量预警，8 文件；见 notify/CLAUDE.md
payment/: 支付渠道适配器接口（PAY-002）与跨渠道通用的金额换算，5 文件；不依赖 httpx，渠道停用以 ErrProviderDisabled 哨兵交给 billing 翻译成 503
plugin/: 出站 webhook 插件钩子 hooks.go 与事件发射 emit.go，投递记往返耗时；见 plugin/CLAUDE.md
subscription/: 订阅分发与门户按日用量读模型，4 文件；见 subscription/CLAUDE.md
support/: 工单（OPS-001）与客服快捷回复，3 文件；见 support/CLAUDE.md
*_test.go: 各域用例测试随包放置

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
