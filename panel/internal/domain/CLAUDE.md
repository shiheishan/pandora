# panel/internal/domain/
> L2 | 父级: /panel/internal/CLAUDE.md

业务用例层：每个包对应一个业务域，只依赖 platform，不依赖 api 和彼此的内部实现。状态机与账本规则由数据库触发器兜底，这里负责编排事务、幂等键与领域校验。

成员清单
adminops/: 管理后台的读写用例，9 文件
appearance/: 主题与外观配置，service.go 读写、sanitize.go 清洗用户提交的样式
billing/: 订单、支付与复式账本，17 文件；借贷配平与回调幂等在此编排
content/: 版本化知识库与自定义页面投递
dbbackup/: 数据库备份编排，12 文件：config 配置、manifest 签名清单与 linux 锁、retention 保留策略、checkpoint_hook 备份前检查点、file_open/file_owner 的 linux/other 双实现
giftcard/: 礼品卡与卡密，对标 Xboard gift-card，3 文件
identity/: 注册、验证与登录，9 文件
nodefabric/: 节点接入与配置下发（PRD 第 8–9 章），14 文件；enrollment、node/server admin、service
notify/: 站内信、邮件，以及到期与流量预警，7 文件
payment/: 支付渠道适配器接口（PAY-002）与跨渠道通用的金额换算，5 文件
plugin/: 插件钩子 hooks.go 与事件发射 emit.go
subscription/: 订阅分发，3 文件
support/: 工单（OPS-001），2 文件
*_test.go: 各域用例测试随包放置

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
