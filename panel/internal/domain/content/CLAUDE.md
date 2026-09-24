# panel/internal/domain/content/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

版本化的知识库与自定义页面。每次保存生成新版本（乐观锁 expected_latest_version），同受众的旧发布版本自动归档；正文按纯文本 / Markdown 源码存取，两端都不渲染可信 HTML，从根上不设 XSS 边界。门户侧的可见性（状态、可见范围、平台、客户端版本、语言、限定套餐）全部在 visible 一处判定，详情与反馈共用。

成员清单
service.go: Service 与 Page 模型（后台列表带版本作者 created_by / created_by_name）；后台 ListAdmin / GetAdmin / PublishVersion / Archive，门户 ListVisible / GetVisible 与唯一的可见性查询 visible
feedback.go: 门户「这篇文章有帮助吗」SubmitFeedback：先按详情的可见性判定（看不到 404），再校验该版本发布过（否则 422 fields.version），按（文章、版本、用户）覆盖写 content_page_feedback（00079）
*_test.go: service_test.go 单元测试；content_pg18_test.go、feedback_pg18_test.go 与 created_by_pg18_test.go 为 PG18 集成测试（run-pg18-gates.sh 的 content 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
