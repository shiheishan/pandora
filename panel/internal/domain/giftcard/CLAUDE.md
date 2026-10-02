# panel/internal/domain/giftcard/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

礼品卡与卡密（对标 Xboard gift-card）。卡码是等价现金，明文只在两处出站：生码响应的 4 张样例与批次的一次性导出；其余读模型一律经 MaskCode 掩码，按筛选导出的对账报表连明文都不取（掩码在 SQL 里算好）。发放余额、流量、套餐不在本包实现，由 billing 经 Granter 接口注入，本包只管模板、码、批次与兑换流水。

成员清单
giftcard.go: Service 与构造、Granter 接口、模板用例（保存时校验奖励、条件与限制）、GenerateCodes（码与批次行同一事务）、奖励与条件类型
batches.go: 批次视图与一次性导出（exported_at 只能打一次，第二次 409），MaskCode 是唯一的掩码实现
codes.go: 卡码读模型与单码启停；CodeFilter（模板 / 状态 / 批次）是列表与掩码报表共用的筛选与 SQL 条件；Stats 含已兑出余额与流量、已发行通用卡面额；门户预览 CardPreview 不带发行量与兑换量，套餐卡补套餐名与周期
codes_export.go: 按 CodeFilter 导出掩码报表（运营对账）：先数后取，超过 CodesExportMax（5 万）回 422；maskedCodeSQL 在库里把码变成掩码再取出，Go 侧从不扫描明文；每次导出写一条只含筛选与行数的审计 gift_card.codes_report_exported
redeem.go: 兑换：锁码、校验条件与限制、按卡型经 Granter 发放、写兑换流水；门户兑换记录带卡码前 12 位提示
*_test.go: 抽奖与掩码单元测试；codes_export_test.go 守导出 SQL 除掩码表达式外不碰 code 列、SQL 掩码与 MaskCode 同形；batches_pg18_test.go、portal_reads_pg18_test.go 与 codes_export_pg18_test.go（掩码逐行一致、无明文、与列表同口径、上限 422、审计、租户隔离）为 PG18 集成测试（run-pg18-gates.sh 的 giftcard 域，共用 openGiftcardPG18 夹具）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
