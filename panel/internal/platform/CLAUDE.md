# panel/internal/platform/
> L2 | 父级: /panel/internal/CLAUDE.md

无业务语义的基础设施层，被 api、domain、middleware 依赖，自身不 import 它们。配置、连接池、加密、令牌、日志、HTTP 模型各只有一个实现，这是 entropy 段"统一范式"的落点：日志只走 logging，响应与错误只走 httpx，配置只走 config。

成员清单
audit/: 不可删审计记录写入（SEC-012）与哈希链。audit.go 的 Write 在同租户 advisory lock 内取链序号 chain_seq（00086）并按第二版口径算 entry_hash：摘要规范化后参与、字段带长度前缀、绑定时间与来源等全部写入列；auth_context（session / reauth）由 Write 从请求主体推出。chain.go 放两版口径与 VerifyChain：第一版存量行（chain_seq 为空）能复算的严格复算，带摘要而复算不出的只核对链接并计数；*_pg18_test.go 由 run-pg18-gates.sh 的 audit 域跑
clientauth/: CLIENT-AUTH 的字节精确、无副作用原语，107 文件；子包 ca42admission、ca42controlv3、ca42execution、evidencecodec 各带 README
config/: 从环境变量加载配置，缺一项拒绝启动，不引入配置框架（NFR-006）
credentialrevocation/: 登录凭据的 fail-closed 集中吊销
crypto/: 口令哈希、令牌生成、签名与信封加密
dashboardmigration/: 只有 migration_contract_test.go，对 00041 dashboard 读模型迁移做字面子句契约，无非测试代码
db/: PostgreSQL 连接池与租户上下文，RLS 变量注入；schema_registry_test.go 按序号重放迁移 Up 段的 CREATE / DROP TABLE，守住现存表、Go 引用与 migrations/RESERVED-TABLES.md 登记簿三者同构
geoip/: IP 画像：地理位置、运营商、网络性质，供风控
httpx/: 统一的响应与错误模型，错误码是封闭列表（新增 reauth_required 403，与 forbidden 同状态不同码），前端 src/core/api.ts 按同一列表解析信封
iamguard/: 租户范围的 IAM 不变量，HTTP 与 CLI 共用
idempotencybind/: 数据库持有的唯一资源绑定器，幂等键与资源一一绑定
logging/: 带脱敏的结构化日志，log/slog
pg18test/: PG18 集成测试打开一次性库的公共护栏，只被 *_pg18_test.go 引用；见 pg18test/CLAUDE.md
realtime/: 服务端推送 SSE，realtime.go 广播与订阅、listener.go 把数据库变更通知转成 topic（quota_balances 已移出监听，00076）、connections.go 经 Valkey 汇总各进程在线连接数；见 realtime/CLAUDE.md
releasejournal/: 发布 journal v3，12 文件，model/receipt/export 通用，store/session/publisher/bootstrap/artifact_boundary/root_capability 为 linux 专用实现
server/: 全部网关共享的 HTTP server 生命周期
token/: 访问令牌签发与校验，每域独立密钥
webapp/: 面板前端的静态下发器，Mount 把 go:embed 的 Vite 产物以 GET/HEAD 挂到网关根 / 与 /assets/*；入口 no-cache + ETag + 严格 CSP，assets/ 一年 immutable，显式 MIME 表，不做 SPA 回退；以最小 Routes 接口接 chi，本层不 import chi；见 webapp/CLAUDE.md
*_test.go: 各包测试随包放置

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
