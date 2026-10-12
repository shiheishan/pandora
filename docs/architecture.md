# 面板后端架构（换栈设计，P0）

状态：已定稿（2026-10-12）。工程取舍按根 CLAUDE.md 的原则定；第 18 节的产品问题由用户定：Q3 佣金按订单总额（含余额抵扣），其余按建议。
依据：2026-10-12 的六份只读调查（`.claude/p0/` 下的 api、auth、db-money、db-rest、jobs-infra、node，不入库）。
下文的「现状」只写到能说明取舍的程度；细节和出处以调查报告为准。

---

## 1. 一页决定

| # | 决定 | 理由 |
|---|---|---|
| 1 | 只有一个二进制 `pandora`，子命令为 `serve`、`worker`、`migrate`、`admin`、`pay`、`backup` | 根 CLAUDE.md 要求一个进程带子命令。重活放 worker，serve 只管请求 |
| 2 | serve 开三个监听：public HTTP、admin HTTP、节点 gRPC（mTLS 由面板进程自己终结） | public 和 admin 都把 SPA 挂在 `/`，也都有 `/v1/auth/*`，合不进一棵路由树；分两个监听，nginx 几乎不用改 |
| 3 | 每个模块拆成 `model / repo / service / handler / task` 五个子包，用 depguard 钉住分层 | Go 只能按包限制 import。拆到子包，「handler 不碰数据库」才能交给 lint 检查 |
| 4 | 去掉多租户和 RLS；迁移重写为新基线，每个模块一个文件，都有 Down | 产品不做多租户；还没有生产部署 |
| 5 | 库内只保留约束类的东西（CHECK、UNIQUE、FK、追加写触发器、账本配平、认证材料守卫），业务状态机和编排搬进 Go | 规矩 1、4、5：业务规则放 service，数据约束放库 |
| 6 | 钱：库里用 `NUMERIC(20,2)`，单位是元，Go 里用 `decimal.Decimal`；/v1 的 JSON 仍是整数分；所有按比例的计算向零截断，只截一次 | 前端不改；逐分与现状一致 |
| 7 | 鉴权：15 分钟 JWT（HS256 钉死，按域分密钥，带 kid）；会话存 Redis；refresh 放 HttpOnly cookie，带轮换和重放检测；Redis 不可用时回 503 | 栈表要求；吊销保持「下一个请求就生效」 |
| 8 | 后台权限只分有和没有：在管理员名单里且账号 active 就能做全部后台操作；没有角色、权限码、临时提权，后台路由只要求已登录 | 用户 2026-10-12 定（Q8）；非管理员登不进后台，取消管理员时当场吊销其后台会话，不需要每请求查库 |
| 9 | 节点通道：一台服务器一个身份、一条 `Session` 双向流，节点通过消息里的 node_id 区分；`Enroll` 用一次性令牌换客户端证书 | 用户先前定过「无逐节点安装命令、服务器卡片显示会话状态」；一台机器 60 个节点不用 60 份身份 |
| 10 | 删掉 UniProxy、Ed25519 签名通道、配置签名、节点 SSE、nonce；离线发布密钥（签二进制）的位置保留，不在本阶段实现 | mTLS 直达面板进程后，签名和 nonce 防的东西已由 TLS 承担 |
| 11 | 定时和异步工作全部进 asynq，Scheduler 只在 worker 里跑一份；库里的 `SKIP LOCKED`、租约和状态条件原样保留，作为第二道保险 | 库仍是事实来源；任务丢了有对账任务补 |
| 12 | 删掉审计哈希链，审计表保持追加写：触发器加上收回运行角色的 UPDATE/DELETE 权限 | 哈希链没有生产读取方，又是全局串行点和大部分 40001 的来源 |
| 13 | 实时事件由 service 在事务**提交后**发到 Redis，删掉 `notify_change` 触发器和库到 Valkey 的桥 | 「回归普通」；事件源收拢到各表的属主 service |
| 14 | 幂等键留在 PG，与业务写在同一个事务；认领 SQL 从中间件挪进 idempotency 模块的 repo | Redis 做不到和业务同事务；中间件不碰库 |
| 15 | 配置用 viper：`/etc/pandora/config.yaml` 加 `PANDORA_` 前缀的环境变量；秘密走环境变量或 `*_FILE` | 栈表要求；秘密不进配置文件 |
| 16 | 主密钥只做根密钥，按用途用 HKDF 派生子密钥；密文头带 key id | 现在一把密钥同时用作 AES 密钥和 HMAC 盐，是公认的坏做法 |
| 17 | 新旧并存到 P7：新代码在新目录，旧代码保持能编译、只修阻断问题，P7 一次删掉 | 旧包互相 import，按模块逐个删会让旧二进制编译不过；没有部署，并存期只占仓库 |

---

## 2. 进程与运行形态

### 2.1 子命令

| 子命令 | 内容 |
|---|---|
| `serve` | public HTTP（缺省 127.0.0.1:9000）、admin HTTP（127.0.0.1:9001）、节点 gRPC（公网端口，缺省 8444）；SSE hub；节点会话管理；心跳合并；pprof 只监听回环 |
| `worker` | asynq Server 和 Scheduler，跑第 12 节的全部任务 |
| `migrate up / down / status / redo` | goose；使用迁移专用 DSN（表属主角色） |
| `admin …` | 原 adminctl：create、reset-password、grant、revoke、list、has-admin；口令只从 stdin 读 |
| `pay …` | 原 payctl：upsert-epay、list；**密钥改从 stdin 读**（现在走 `--key` 会出现在 argv 里） |
| `backup …` | 原 backup-webdav；使用自己的最小配置加载器，不读完整的 viper 配置 |

命令行框架用 cobra（与 viper 同一作者，社区最常见的组合）。

### 2.2 合成一个 serve 后丢掉的隔离，怎么补

| 原来靠进程隔离得到的 | 现在怎么补 |
|---|---|
| public 被打爆时不拖垮 admin 和节点 | nginx 分区限流（已有）；每个监听各自的 `http.Server` 超时和最大并发（信号量中间件）；serve 设置 GOMEMLIMIT；重活全放 worker |
| Argon2 槽位分开 | 两个信号量：public 一个、admin 一个 |
| 连接池预算分开 | 一个池，上限按原来三者之和或更低；worker 另有一个池 |
| 节点网关不依赖 Redis | 节点会话的鉴权、名单下发、流量入账都不依赖 Redis；只有「变更通知」走 Redis，Redis 断了退回定时比对纪元（第 13 节） |

### 2.3 单实例

serve 按单实例设计：SSE hub、节点会话表、心跳合并都在进程内存里。多实例不在本阶段的范围；需要时，节点会话改为按服务器做一致性哈希，SSE 本来就经 Redis 广播。

---

## 3. 目录与分层

### 3.1 目录

```
panel/
  cmd/pandora/main.go            cobra 根命令，只做装配
  internal/
    app/                         各子命令的装配（serve.go、worker.go …），模块注册
    pkg/                         与业务无关的基础设施
      config/  log/  db/  tx/  redisx/  errs/  httpx/  reqid/
      jwtx/  crypto/  ratelimit/  idgen/  pagination/  testkit/
    module/
      <模块>/
        model/                   GORM 结构体：只有字段和标签
        repo/                    唯一 import gorm 的地方
        service/                 业务规则、状态机、事务边界；对外提供的接口
        handler/                 gin 处理器、每个接口的请求与响应 DTO、路由注册
        task/                    asynq 任务处理器（只调本模块 service）
  db/migrations/                 新基线（goose，embed 进二进制）
proto/                           节点协议源文件（buf）
nodeapi/                         buf 生成的 Go 代码，单独的 Go module，panel 和 pdnd 用 replace 引用
```

旧代码（`internal/api`、`internal/domain`、`internal/middleware`、`internal/platform`、`migrations/`、`cmd/aegis-*`）原地保留到 P7，然后整体删除。

### 3.2 分层规则（depguard 钉住）

| 规则 | depguard 写法 |
|---|---|
| handler 不碰数据库 | `**/module/*/handler/**` 禁止 `gorm.io/gorm`、`internal/module/*/repo`、`internal/pkg/db` |
| task 不碰数据库 | `**/module/*/task/**` 同上 |
| service 不直接用 GORM | `**/module/*/service/**` 禁止 `gorm.io/gorm`；事务通过 `pkg/tx` 的接口 |
| 只 import 本模块的 repo | 每个模块一条规则：`module/<m>/**` 禁止 `module/<其他>/repo`、`module/<其他>/model` |
| 跨模块只调 service | 同上；允许 `module/<其他>/service` |
| 中间件不碰数据库 | `internal/pkg/httpx/**` 禁止 `gorm.io/gorm`、`internal/pkg/db` |
| 模块之间不循环 | Go 编译器本身会拒绝包循环；另外在 service 之间约定依赖方向（第 4.2 节） |

每个模块的规则由一个小脚本从 `internal/module/` 目录生成到 `.golangci.yml`，CI 检查生成结果没有漂移。不再写自研的源码扫描测试。

### 3.3 事务

- `pkg/tx.Manager.Do(ctx, func(ctx) error)` 开事务，把 `*gorm.DB` 放进 ctx；repo 一律通过 `db.From(ctx)` 取连接。这样同一个 ctx 里跨模块调 service 也在同一个事务里。
- 事务边界只在 service 的公开方法上。被别的 service 调用的方法不再自己开事务，而是加入调用方的事务（`Do` 发现 ctx 里已有事务就直接复用）。
- 提交后的动作用 `tx.AfterCommit(ctx, fn)` 登记，包括发事件、入队任务、Kick 投递；回滚时丢弃。
- 隔离级别缺省 READ COMMITTED，加显式行锁（`FOR UPDATE`、`SKIP LOCKED`）。不再缺省走 SERIALIZABLE 和带闸重试。

### 3.4 GORM 用法

- 不用 AutoMigrate；不用 `Save`（它写全部列）。更新一律用 `Model(...).Select(列...).Updates(...)` 或 `UpdateColumn`。
- 每次调用都带 `WithContext`（`db.From(ctx)` 已经带上）。
- 打开 `TranslateError: true`，唯一约束冲突在 repo 里映射成领域错误。
- 复杂的读用 `Raw` 加命名参数，SQL 只写在 repo 里。
- 关联只在需要时 `Preload`，列表页禁止逐行查（N+1）；需要的时候由属主模块提供批量读接口。
- `updated_at` 交给 GORM 的 `autoUpdateTime`，库里不再保留 `set_updated_at` 触发器。

---

## 4. 模块与表属主

### 4.1 属主表

每张表只有一个属主模块。别的模块要数据，调属主模块的 service。

| 模块 | 拥有的表 | 说明 |
|---|---|---|
| identity | users、user_passwords、user_groups、registration_sessions、verification_codes、invite_codes、referrals、user_generation_jobs | 注册、口令、用户资料、批量建用户。user_groups 从 adminops 收回来；referrals 在注册时写入，佣金模块调 identity 读 |
| auth | admins（管理员名单）；Redis 里的会话、refresh、快捷登录 | 登录、刷新、登出、重认证、管理员授予与取消、最后一个管理员保护。原 sessions、refresh_tokens、quick_login_tokens 三张表不再建 |
| settings | settings（键值加类型化访问器）、feature_switches | 原 system_settings 和 tenants.settings 合到这里。各模块通过 settings service 的类型化方法读写自己那组键（如 `settings.Commission()`） |
| catalog | products、plans（含库存三列）、plan_versions、prices、entitlements、quota_definitions、plan_node_pools、traffic_packs | 库存由 catalog service 暴露 `ReserveStock / ReleaseStock / CaptureStock`，order 模块调用，不再直接写 plans |
| order | orders、order_items、order_reservations、order_reservation_events、order_stock_reservations、order_purchase_limit_reservations、plan_purchase_counters | 下单、报价、结算编排、预留与释放 |
| payment | payment_providers、payment_intents、payments、payment_events、late_payment_cases | 渠道适配、回调、查单、挂账。adminops 那套渠道写法删掉 |
| ledger | ledger_accounts、ledger_transactions、ledger_entries、balance_holds | 记账、余额、冻结、调账 |
| promo | coupons、coupon_redemptions | 优惠券 |
| commission | commission_entries、withdrawals | 佣金计提、解冻、提现 |
| giftcard | gift_card_templates、gift_card_prizes、gift_card_template_plans、gift_card_codes、gift_card_redemptions、gift_card_batches | 发放调 subscription、ledger 的 service |
| subscription | subscriptions、subscription_events、subscription_credentials、quota_balances、traffic_pack_grants、traffic_pack_transfers、traffic_reset_logs、subscription_usage_daily、subscription_fetch_log | 状态口径（生效、可救回、已停用）只在这里定义；节点上报的扣量调它的批量扣量方法 |
| node | servers、nodes、node_pools、node_pool_user_groups、server_identities、bootstrap_tokens、node_configs、node_config_applications、node_outbounds、node_routes、route_groups、route_group_members、node_traffic_hourly、node_user_traffic_hourly、node_user_traffic_daily、node_metrics、node_alive_ips、node_traffic_reports、node_tasks；面板 CA | 后台编排、gRPC 会话、接入、流量入账（入账后调 subscription 扣量） |
| certs | dns_credentials、acme_accounts、certificates、certificate_versions、certificate_orders、certificate_issuances | ACME 签发入站 TLS 证书 |
| notify | announcements、notification_templates、notification_preferences、notification_deliveries、telegram_bindings、telegram_bind_codes | 站内信、邮件、Telegram |
| support | tickets、ticket_messages、ticket_macros | 工单 |
| content | content_pages、content_page_feedback | 内容页 |
| appearance | site_themes、site_slots | 主题和槽位 |
| plugin | plugin_hooks、plugin_hook_deliveries | 插件钩子 |
| audit | audit_events、ip_cluster_reviews | 审计写入与查询、风控聚类复核 |
| idempotency | idempotency_keys | Claim、Complete、Replay |
| report | activity_daily、revenue_report_adjustments | 看板与营收报表；其余数据一律调属主 service 的聚合读接口 |

原来的 adminops 不再作为一个模块：用户管理归 identity 和 auth，目录归 catalog，渠道归 payment，看板归 report。

### 4.2 依赖方向

```
report ─┐
giftcard ─► order ─► catalog
           │  ├────► payment ─► ledger
           │  ├────► promo
           │  └────► subscription ─► catalog
commission ─► ledger, identity
node ─► subscription, catalog, settings, certs
notify, support, content, appearance, plugin ─► identity, settings
identity, auth ─► settings, audit
所有模块 ─► audit, idempotency, settings（三者不依赖任何业务模块）
```

需要反向通知的地方（例如订单结算后 subscription 要开通、node 要知道名单变了）用两种普通做法：上游 service 调下游 service（结算编排在 order 里，order 调 subscription 开通），或者下游在 service 构造时注册回调。不引入事件总线。

### 4.3 跨模块读的处理

后台看板和详情页现在直接读十几张表。换栈后由属主模块提供批量读方法（例如 `order.Service.SummaryByUsers(ids)`、`ledger.Service.Balances(ids)`），report 和 handler 拼装。每个方法一条 SQL，不逐行查。

---

## 5. 数据库

### 5.1 去多租户

| 项 | 做法 |
|---|---|
| tenant_id 列、复合外键、`UNIQUE(tenant_id, id)` | 全部去掉，外键改为单列；唯一约束去掉 tenant 前缀（例如 `UNIQUE(email)`） |
| tenants 表 | 删除。时区、币种、站点名进 settings；`settings jsonb` 拆成普通键 |
| RLS、`current_tenant_id`、`set_config`、`db.Scope` | 全部删除 |
| 每租户种子触发器 | 改成基线迁移里的普通 INSERT：降级开关、通知模板、线下收款渠道 |
| 最后一个管理员的锁锚 | 改为 `pg_advisory_xact_lock(常量)` |
| 运行角色 | 保留「应用角色不是超级用户、不是表属主、没有 CREATE/TEMP、search_path 固定」；去掉 BYPASSRLS 和 row_security 两项检查。迁移用单独的属主角色（不是超级用户）；保留期清理函数靠函数定义上的 `SET session_replication_role = replica` 绕开只追加守卫，这个参数要超级用户才能设，所以部署时由超级用户执行一次 `GRANT SET ON PARAMETER session_replication_role TO <属主角色>`（PG15+），P7 的初始化脚本与 CI 都按这个角色跑迁移 |
| RLS 实际承担的安全目的 | 只有一条要换成普通做法保住：幂等键按操作者隔离，改为 `UNIQUE(scope, actor_id, key)`，repo 的查找、认领、完成都带 actor_id。用户之间的隔离本来就不靠 RLS，仍由 repo 的 `WHERE user_id = ?` 保证，门户侧 repo 方法一律把 userID 作为必传参数 |

### 5.2 新基线迁移

- 目录 `panel/db/migrations/`，文件形如 `00001_base.sql`（扩展、通用触发器函数）、`00002_identity.sql`、`00003_auth.sql`……每个模块一个文件，每个都有 `-- +goose Down`。
- 不建的表：RESERVED-TABLES.md 登记的 21 张；从无写入的死表 node_templates、provisioning_runs、runtime_instances、usage_sources、usage_batches、devices；refunds、refund_requests、invoices、payment_webhook_receipts、trial_grants、quota_adjustments；order_transitions、subscription_transitions、node_transitions；audit_chain_heads、audit_chain_gate；node_request_nonces、server_request_nonces、node_enrollments、server_enrollments、node_effective_config_releases、node_identities（并入 server_identities）；sessions、refresh_tokens、quick_login_tokens；tenants。
- 不再建的库内函数：缓存纪元（00155/00156，没有消费方）、节点下发纪元、`notify_change`、审计链、幂等的 legacy 与 actor 策略、`activity_daily_compute`、`node_traffic_payload_entries`、各类 `guard_*_transition`。
- CI 对新基线跑 up → down → up。会锁大表的 DDL 在之后的增量迁移里写明预检、`lock_timeout` 和回滚办法（新基线是空库，不涉及）。

### 5.3 库里保留什么（A 类）

| 类 | 内容 |
|---|---|
| 行级约束 | NOT NULL、CHECK（状态枚举、金额非负、订单恒等式 `payable = total − balance_applied`、`total = greatest(subtotal − discount − proration_credit, 0)`、`proration_credit` 只许 upgrade、人工单原因 ≥5 字、paid/fulfilled ⇒ paid_amount = total 且 paid_at 非空、cancelled ⇒ cancel_reason 非空、券容量、`stock_sold + stock_reserved ≤ stock_total`、`consumed_bytes ≤ granted_bytes`、佣金 `commission = trunc(base × rate_bp / 10000)`、`rate_bp` 在 (0, 5000]） |
| 唯一与外键 | 每单一笔 order_paid / balance_hold / balance_release 的部分唯一索引，late_payment、plan_change_refund 的唯一，`UNIQUE(provider, provider_payment_id)`，`UNIQUE(provider, provider_event_id)`，`UNIQUE(order, referrer)`，`nodes` 端口占用唯一索引等 |
| 追加写触发器 | 一个通用的 `deny_mutation()`，挂在 ledger_transactions、ledger_entries、subscription_events、order_reservation_events、各类预留流水、traffic_reset_logs、traffic_pack_transfers、gift_card_redemptions、referrals、audit_events、node_config_applications、node_traffic_reports、node_metrics、revenue_report_adjustments、certificate_issuances、order_items；另有 `deny_delete()`，挂在 payment_events、commission_entries、withdrawals、late_payment_cases。运行角色同时收回这些表的 UPDATE/DELETE/TRUNCATE |
| 账本配平 | `assert_ledger_balanced` 保留为唯一的提交时（DEFERRABLE）检查：每笔交易借贷和为 0、只有一个币种。这是钱的底线，跨行检查写不成 CHECK |
| 余额不透支 | 新增 CHECK：用户类科目的余额缓存不得为负（现在只靠 Go 判断） |
| 认证材料守卫 | server_identities 的密钥字段不可改，吊销只能 active → revoked 且不可逆 |
| 目录冻结 | 已发布的 plan_version 及其子表不可改，价格不可改（订单快照依赖它） |
| 保留期清理 | 追加写表的清理用一个 SECURITY DEFINER 函数，分批删除超过保留期的行，固定 search_path |

其余库内逻辑（预留守恒、幂等绑定、订单/订阅/意图/节点状态机、挂账形状、改套餐分录形状）搬进 Go service，用单元测试和 PG 集成测试钉住。旧的 `panel/tests/invariants.sql` 里属于 A 类的用例直接复用，其余改写成 Go 测试。

### 5.4 状态机

每个状态机在 service 里写成一张转换表，加一个纯函数 `CanTransition(from, to)`；repo 的状态更新带上 `WHERE status = 旧状态`，影响 0 行就当并发冲突。库里只保留 `CHECK (status IN (…))`。

---

## 6. 钱

| # | 规则 |
|---|---|
| R1 | 库里的金额列一律 `NUMERIC(20,2)`，单位是元；币种列加 `CHECK (currency IN ('CNY','USD'))`，两种都是 2 位小数 |
| R2 | repo 写入前断言 `d.Equal(d.Truncate(2))`（`money.Check`），不满足就报错。PostgreSQL 遇到多余小数会静默四舍五入，必须在写入前拦住；库里拦不住：`NUMERIC(20,2)` 列先舍入再查 CHECK，`CHECK (amount = round(amount, 2))` 永远成立，不要加这种假兜底 |
| R3 | 所有按比例得出的金额（百分比券、换套餐折算、待开始周期额度、佣金）一律**向零截断**，与现状逐分一致 |
| R4 | 只在 service 的领域计算函数里舍入，每个公式只在最后一步舍入一次；handler 只做单位换算，repo 只存不算，SQL 里不做金额算术 |
| R5 | 比值与取最小（换套餐的 min(时间比, 流量比)）用 `big.Rat` 或交叉相乘比较，不先 `Div` 出小数再比 |
| R6 | /v1 的 JSON 仍是整数分（int64）。DTO 输出 `d.Shift(2).IntPart()`，换算前断言它是整数；输入读 int64 分后用 `decimal.New(n, -2)` |
| R7 | 渠道边界：易支付的金额字符串用 `decimal.NewFromString` 解析，超过 2 位小数直接拒绝；输出用 `StringFixed(2)` |
| R8 | 非钱的数量（流量字节、配额、bp、百分比）仍用 int64 和 BIGINT。流量计费倍率从 float64 改为整数千分比 `rate_milli`，计费字节 = bytes × rate_milli / 1000，向零截断 |
| R9 | jsonb 里不放钱：礼品卡余额奖励、奖池、兑换记录、渠道 `min_amount`、`commission.min_withdraw` 改为带类型的列或类型化设置 |
| R10 | 券面值拆成 `amount_off NUMERIC(20,2)` 和 `percent_off_bp INT` 两列，加 CHECK 恰好一个非空 |
| R11 | 账本：分录金额 > 0，配平与单币种在库里检查（5.3）；余额缓存在 Go 里同事务更新；交易 kind 加 CHECK 枚举；礼品卡赠送改记新科目 `platform_promo_expense`，不再和渠道手续费混用一个科目 |
| R12 | 币种小数位只在 `pkg/money` 一处定义；去掉写死的 `/100` |

计费周期加月用 Go 的 `AddDate`（1 月 31 日加一个月得到 3 月 3 日）。周期计算只在 Go 里做，不挪进 SQL，以免两边口径不一致。

---

## 7. 订阅

- 状态口径（生效、可救回、已停用）只在 subscription service 里定义，其他模块调它，不再各写一份 SQL。
- 现在定义了 8 个状态，Go 只写入其中 4 个（pending、active、expired，以及没有入口的 trialing）。用户已定（Q1）：只留 `active`、`expired` 两个状态加 `renewal_closed_at`，建单直接插入 active。
- 到期：`billing:expire_subscriptions` 每分钟跑一次；另外，节点会话管理器按「下一个最早到期时刻」安排一次名单重算，保证到期的用户在秒级内从节点上断开（现在就是这样）。
- 订阅拉取的限流（每个凭据每小时若干次）从 PG 计数改为 Redis 限流；`subscription_fetch_log` 仍留在 PG，作为日志。

---

## 8. 鉴权与会话

### 8.1 令牌

| 项 | 做法 |
|---|---|
| 库 | golang-jwt v5 |
| 算法 | `WithValidMethods([]string{"HS256"})`；header 带 `typ=at+jwt` 和 kid |
| 密钥 | public、admin 各一组，至少 32 字节，两组必须不同；每组可以有多把（按 kid），旧钥只验不签，用于轮换 |
| 选钥 | keyfunc 按**路由组**选本域的密钥集，再按 kid 查找；找不到就拒绝，不信任 header 里的其他字段 |
| 声明 | sub（uid）、sid、aud（public/admin）、iss（pandora-public/pandora-admin）、iat、exp、amr。去掉 tid、did、knd；重认证时间 rat 不再放在令牌里，改记在会话里 |
| 有效期 | 15 分钟（可配，1 分钟到 1 小时） |
| 校验 | `WithExpirationRequired`、`WithIssuedAt`、`WithAudience`、`WithIssuer`，leeway 为 0；exp 的边界在测试里钉住 |

### 8.2 会话（Redis）

| 键 | 类型 | 内容 | 过期 |
|---|---|---|---|
| `sess:{sid}` | hash | uid、aud、amr、created_at、last_seen、reauth_at、abs_exp、ua、ip_hash、rt_cur、rt_prev、rt_rotated_at、epoch | EXPIREAT abs_exp |
| `usess:{uid}` | set | 该用户的全部 sid | 跟随最长的会话 |
| `uepoch:{uid}` | string | 凭据纪元；改密、重置、停用、授予或取消管理员时 INCR | 不过期 |
| `ql:{sha256}` | string | 快捷登录：源 sid、uid | 60 秒，消费时 GETDEL |

- 每个请求的验证：解析 JWT（0 次往返），再用一个 Lua 脚本一次完成三件事：读会话、核对 uid/aud/abs_exp/epoch、按 5 分钟节流更新 last_seen（1 次 Redis 往返）。门户与后台鉴权都从 1 次 DB 往返变成 1 次 Redis 往返（后台不分权限，不再每请求查库）。
- 会话状态不在 PG 里留副本。登录、登出、踢人、刷新重放这些事件写进审计。
- 会话寿命：门户 30 天；后台绝对寿命 7 天、空闲 2 小时失效（Q4），空闲按 last_seen 判断。

### 8.3 refresh

| 项 | 做法 |
|---|---|
| 形态 | `<sid>.<32 字节随机 base64url>`；Redis 只存后半段的 SHA-256，比较用常量时间 |
| 存放 | HttpOnly、Secure、SameSite=Strict 的 cookie，Path=`/v1/auth`；public 叫 `pandora_rt`，admin 叫 `pandora_admin_rt`（admin 经 nginx 的 `proxy_cookie_path` 改成带后台前缀的路径） |
| 轮换 | `POST /v1/auth/refresh` 每次生成新值：`rt_prev ← rt_cur`、`rt_cur ← 新值`，Lua 原子完成 |
| 宽限 | 30 秒内拿 rt_prev 来的请求（多标签页同时刷新）：只发新的 access，不轮换，也不 Set-Cookie |
| 重放 | 超出宽限还拿 rt_prev：吊销整个会话，审计记 `session.refresh_reuse`（记 anonymous，会话主人放资源），回 401。值既不是 rt_cur 也不是 rt_prev：只回 401，不吊销——sid 不保密（JWT 明文、审计、会话列表），按「对不上就吊销」任何拿到 sid 的人都能踢掉别人；重放的信号是用过的旧值再出现 |
| CSRF | cookie 只在 refresh 和 logout 两个接口上读；SameSite=Strict，再加 Go 标准库的 `http.CrossOriginProtection` 做来源检查。下发 cookie 的登录、快捷登录也挂同一个来源检查（防登录 CSRF：跨站 text/plain 表单能拼出合法 JSON，把受害者浏览器登进攻击者账号） |
| 刷新不续 rat | 新 access 沿用会话里的 amr；重认证时间只由 reauth 接口改写 |

### 8.4 吊销

| 场景 | 做法 |
|---|---|
| 登出 | DEL `sess:{sid}`、SREM `usess`；清 cookie；审计 |
| 改密、重置、停用、授予或取消管理员 | 业务事务里先在 Redis 吊销会话（Redis 失败就回滚事务，回 503）；提交后再扫一遍，防止提交前新建的会话漏网；INCR `uepoch:{uid}`，堵住登录与改密之间的竞态（登录建会话前比对纪元） |
| 门户改密 | 保留当前会话，吊销其余会话（产品语义不变） |
| SSE | 吊销时向 `rt:sess:{sid}` 发布消息，SSE 处理器订阅这个频道，收到就关流 |

### 8.5 Redis 不可用

鉴权、刷新、登录一律 fail closed，回 503 `service_unavailable`，不回 401（回 401 会让前端清掉令牌、踢回登录页）。鉴权使用的 go-redis 客户端打开 `ContextTimeoutEnabled`，读写、拨号、池等待的超时都设在 250–500ms。

### 8.6 保留的语义

- public 与 admin 两个域互不相通；管理员不能登录门户（403，提示去后台登录入口）；不是管理员的账号（含被取消的）登后台时，返回与口令错误相同的 401，也计失败次数（IAM-006）；用户不存在时用 DummyVerify 对齐耗时。
- Argon2id 参数不变，事务外计算哈希，public 与 admin 各一个并发闸。
- 重认证窗口 15 分钟，排在幂等之前；reauth 接口改写会话里的 reauth_at（现在不写），并且按 audience 校验会话（现在不校验）。
- 登录限流改为 Strict（Redis 故障时回 503）；同一账号 15 分钟内失败 10 次暂停 15 分钟（Q7），计数放 Redis，按账号跨两个域共用（`lf:{uid 或邮箱哈希}`）；先占名额再验口令（并发请求不能绕过上限），成功清零，结论不明（繁忙、内部错）退回名额。
- 登录失败、刷新重放这类失败事件的审计记 anonymous：被撞账号放资源（user），audience 放 data，不把它记成操作者。
- 后台空闲 2 小时：服务端按 last_seen 判断；后台页面有定时轮询，所以前端另记用户操作，2 小时无操作主动登出（服务端空闲判断兜底关掉的标签页）。

### 8.7 前端要改的

| 位置 | 改什么 |
|---|---|
| `core/api.ts` | 收到 401 时，先单飞调用一次 refresh（跨标签页用 `navigator.locks`，拿到锁后先看别的标签页是否已经刷新过），成功就把原请求重放一次；刷新失败才走 sessionLost |
| `core/api.ts` | refresh、logout 两个请求用 `credentials:'same-origin'`，其余请求仍用 omit |
| 登录页、快捷登录 | 只读 access_token；响应体里不再有 refresh_token |
| `openStream` | 断流后用新令牌重连 |
| `dev/mock-api.ts` 及测试 | 模拟 refresh 接口；补「刷新单飞」「只重放一次」「刷新失败清令牌」三组用例 |

---

## 9. 后台权限

后台权限只分有和没有（用户 2026-10-12 定，Q8）：没有角色分级、权限码、临时提权，管理员之间对等。

- 表：只有 `admins(user_id PK → users ON DELETE RESTRICT（删账号前先取消管理员）, granted_by → users ON DELETE SET NULL, created_at)`，属 auth。「管理员」= 在这张表里且 `users.status = 'active'`。
- 登录：后台登录只接受管理员；不是管理员与口令错完全一样（401，计失败次数，IAM-006）。管理员用正确口令登门户回 403。
- 路由：后台所有业务路由只挂 `RequireAuth`（后台引擎上有效的后台会话就是管理员），不再有缺权限回 404 这一层，也不每请求查库。`app` 的 `TestAdminRoutesRequireLogin` 匿名打遍后台路由（登录、续期、登出、探活除外），漏挂即红。
- 续期兜底：后台 refresh 时再查一次「在名单里且 active」，不是就删会话回 401。吊销万一漏了（如提交后补扫失败），最坏窗口也只是一个 access 寿命（15 分钟）。
- 授予与取消只有 `pandora admin create / grant / revoke`（幂等：已是 / 已不是管理员时直接成功、不写审计），审计动作 `adminctl.admin_granted` / `adminctl.admin_revoked`。授予时同事务吊销门户会话并推进纪元；取消时同事务吊销后台会话，提交后再补扫一遍，所以取消在下一个请求就生效。
- 管理员集合的所有变动（授予、取消、停用或封禁管理员）先取同一把事务级咨询锁；取消或停用后没有可登录的管理员时拒绝（409）：取消是先查后删，被拒时什么都没改。
- identity 经 `StaffGuard.IsStaff`（在名单里，不看状态）给管理员定 12 位口令、拦门户自助找回密码；后台替人改状态、重置口令不做越级检查，只保留「不能对自己」与最后一个管理员保护。
- `pandora admin reset-password` 成功后清掉该账号的登录失败计数，运维重置口令后立刻能登。

---

## 10. HTTP 约定

| 项 | 做法 |
|---|---|
| 框架 | gin。public 和 admin 各一个 `gin.Engine`；中间件链：reqid → 访问日志 → 恢复 → 安全头 → 超时 → 鉴权 → 域守卫 → 限流 → 路由级（登录要求、重认证、开关、幂等） |
| req_id | 接受合法 UUID 形式的 `X-Request-ID`，否则生成 UUIDv7；写进 ctx、响应头、zap 字段、错误信封、gRPC metadata（`x-request-id`）、asynq 任务载荷（`req_id`）、审计行 |
| 成功响应 | 直接返回 DTO（与现在相同，不包信封） |
| 错误 | 一个 `errs.Error{Code, Status, Message, Fields}`。service 返回它或领域错误，handler 用 `c.Error(err)` 交给错误中间件统一写 `{"error":{code,message,fields?,request_id?}}`。未知错误一律回 500 `internal_error`，原因只写日志。15 个错误码不变；加一个测试，对齐 Go 的码表和前端的 `SERVER_ERROR_CODES` |
| DTO | 每个接口单独定义 `XxxRequest` / `XxxResponse`，放在 handler 包里；不把 model 或 service 结构直接序列化出去，也不把请求体直接解码进 service 的输入类型。JSON 字段名和形状与现在一致 |
| 校验 | gin binding（go-playground/validator v10）做格式校验，失败回 422 `validation_failed`，`fields` 写中文提示；业务校验在 service 里做 |
| 请求体 | 上限 1 MiB；`DisallowUnknownFields` |
| 分页 | `limit` / `offset`，统一由 `pkg/pagination.Parse(c, 缺省值, 上限)` 解析；各接口的缺省值和上限保持现状 |
| 幂等 | 中间件只读 `Idempotency-Key` 头，调 idempotency service 认领；需要在业务事务内完成的接口（下单、续费、换套餐、充值、流量包、工单等），service 在同一个事务里完成认领，并绑定到订单。重放规则、409 两种码、租约 5 分钟都不变；新增 `retention:idempotency` 清理任务 |
| 限流 | go-redis/redis_rate（GCRA）。维度保持现状；普通层 Redis 故障时放行，Strict 层回 503；拒绝回 429 并带 `Retry-After`。门户全局层有 IP 与 /24 两个维度，比现在多 1 次 Redis 往返，接受这个代价，换来不再维护自写的 Lua |
| 开关 | feature_switches 进程内缓存 3 秒，变更后经 Redis 失效；只读模式（AdminWritesGate）语义不变 |
| SSE | 端点和帧格式不变（`retry`、25 秒 ping）；频道去掉租户段：`rt:public`、`rt:user:{id}`、`rt:ticket:{id}`、`rt:admin`；会话吊销时关流 |
| 订阅链接 | `GET /{prefix}/{token}` 管线不变（格式判断、诱饵 404、响应头）；渲染逻辑从 handler 挪进 subscription service |
| 访问日志 | 只记路由模板（`c.FullPath()`）、状态码、耗时、req_id；继续记录每个请求的 DB 和 Redis 往返次数（GORM 回调、go-redis hook） |

handler 里现在的业务逻辑（第 4 节附表：公告状态机、访问日志的 IP 哈希与合并分页、节点安装命令拼装、处理器里生成 Reality 密钥、slugify 两份重复实现、`[]map[string]any` 透传、CSV 里用浮点表示钱等）随模块迁移挪进 service。

---

## 11. 节点通道（gRPC + mTLS）

### 11.1 身份与证书

| 项 | 做法 |
|---|---|
| 身份单位 | 一台服务器（机器）一个身份，存于 server_identities；节点（入站）挂在服务器下 |
| CA | 面板自带一个 ECDSA P-256 根 CA，私钥用 HKDF 派生的加密子密钥封存后存库；同一个 CA 签发 gRPC 服务端证书 |
| 安装命令 | 带一次性令牌和 CA 公钥的 SHA-256 指纹（与 kubeadm 的 `--discovery-token-ca-cert-hash` 同一种做法）；节点首次连接时用指纹校验服务端证书链 |
| 接入 | `Enroll(token, CSR, request_id, 主机信息)`：只要求服务端 TLS，不要求客户端证书。令牌只存哈希、单次有效、TTL 20 分钟（沿用 bootstrap_tokens）；成功后回客户端证书和 CA bundle。request_id 用于崩溃后重试（同一个 request_id 重复调用返回同一张证书） |
| 客户端证书 | 有效期 30 天，URI SAN 为 `spiffe://pandora/server/<id>`；剩余不到 10 天时在会话内续期，每次续期生成新私钥 |
| 吊销 | 身份置为 revoked 后，立即关闭该身份的在途流；每次建流都查库确认身份有效，因此吊销立刻生效，不需要 CRL 或 OCSP |
| 过期 | 离线超过 30 天、证书已经过期的：节点继续用磁盘缓存服务现有用户（与现在相同），但连不上面板，要在后台生成新令牌重新接入 |
| 安装证据 | Hello 里带 agent_version 和 binary_sha256，面板记录下来，与当前发布版本不一致时在后台提示。这是节点自报的数据，原来的「两阶段 commit 拒绝」也只是自报，从来不是安全控制，所以改成提示 |
| 离线发布密钥 | 位置保留：以后升级二进制时，用离线密钥签发布清单。不在本阶段实现 |

### 11.2 proto 草案

```proto
syntax = "proto3";
package pandora.node.v1;

service Enrollment {
  rpc Enroll(EnrollRequest) returns (EnrollResponse);           // 服务端 TLS，不要求客户端证书
}
service Control {
  rpc Session(stream AgentMessage) returns (stream PanelMessage); // mTLS
}

message AgentMessage {
  uint64 seq = 1;
  oneof body {
    Hello hello = 10;             // agent_version, binary_sha256, features[], host, applied_config{release_id, content_sha256}, applied_users[{node_id, version}]
    Heartbeat heartbeat = 11;     // metrics（整数口径）, runtime_reason
    TrafficReport traffic = 12;   // report_id, entries[{node_id, user_id, up, down}]
    OnlineIPs online = 13;        // entries[{node_id, user_id, ips[]}]
    ConfigAck config_ack = 14;    // release_id, content_sha256, phase(switched/health_passed/failed), reason_code, detail(≤2KB)
    UsersAck users_ack = 15;      // node_id, version, ok, reason_code
    RenewRequest renew = 16;      // csr
  }
}

message PanelMessage {
  uint64 seq = 1;
  oneof body {
    Welcome welcome = 10;         // session_id, features[], push_interval, heartbeat_interval, server_time
    ServerConfig config = 11;     // release_id, content_sha256, nodes[{node_id, 协议配置, outbounds, routes}]
    UsersFull users_full = 12;    // node_id, version, chunk_index, chunk_total, users[{id, uuid, speed_limit, device_limit}]
    UsersDelta users_delta = 13;  // node_id, from_version, to_version, added[], removed_ids[]
    TrafficAck traffic_ack = 14;  // report_id（落库提交后才发）
    Certificate certificate = 15; // cert_chain_pem, ca_bundle_pem
    Command command = 16;         // reconnect{in_ms, jitter_ms} | tombstone{reason}
  }
}
```

proto 放在仓库根的 `proto/`，用 buf 管理；生成的代码放进单独的 Go module `nodeapi/`，panel 和 pdnd 都用 replace 引用。CI 检查 `buf generate` 之后没有差异，并跑 `buf breaking`。

### 11.3 会话语义

| 项 | 做法 |
|---|---|
| 建流 | 拦截器从客户端证书取 server_id，查库确认身份有效；同一身份的新流一到，就关闭旧流（supersede） |
| 对齐 | Hello 报告已应用的配置版本和各节点的名单版本；面板比较后，相同就不发，不同就发全量（xDS 的 initial_resource_versions 思路） |
| 名单版本 | 沿用内容哈希 `u1-<hex>`。增量只在面板确知节点处于 from 版本时发送，否则发全量。全量按 5000 个用户一块分块发送，单条消息不会超过 gRPC 缺省的 4 MB 上限 |
| 背压 | 每个会话一个有界发送队列；配置和全量名单只保留最新一份；队列满了就断流，让节点重连后重新对齐 |
| 流量 | report_id 必填；面板在 `UNIQUE(node_id, client_report_id)` 下落库，提交后发 TrafficAck；同一个 id 内容不同就报错。节点在收到 ack 之前保留这一批，重连后按原 id 重发。缓冲只在内存里，进程崩溃最多丢一个节拍（60 秒），落盘放到换栈之后再评估 |
| 在线 IP | 只接受该服务器所服务用户的条目（现在不过滤） |
| 心跳 | gRPC keepalive 30 秒 ping、10 秒超时；应用层 Heartbeat 30 秒一次，带指标；「在线」= 有活动的流，最后心跳时间用于展示 |
| 重连 | 全抖动指数退避，1 秒起，封顶 30 秒；面板重启前给每个会话发 reconnect 提示，错峰重连 |
| 下发时效 | 由变更通知驱动，目标是秒级送达（与现在 SSE 连着时相同）。通知丢了，有纪元比对兜底（第 13 节） |
| req_id | 每条流在 metadata 带 `x-request-id`；流内消息带 seq |

### 11.4 网络

gRPC 端口直接对外，由面板进程自己终结 mTLS，不经 nginx（经 nginx 终结的话，信任链会断在 nginx）。nginx 里节点相关的 HTTP location 全部删除；`/pdnd/*` 安装包仍由 public 提供。

### 11.5 pdnd 端

删除 `panel/client.go`（UniProxy）、`signed.go`、`stream.go`（SSE）、`config_key_transition.go`、`effective_release.go`、`bindingcontract`；重写 `enrollment.go` 为 Enroll（保留崩溃安全日志的思路）；新写会话客户端。`node.go` 主循环改为消费会话消息，保留单 goroutine 不变式；`report.go` 改为由 ack 驱动；`users.go` 保留 diff 和「先删后加」，入口改为 proto 消息；`config_apply.go` 保留应用、回滚和坏版本台账，去掉验签。config.json 去掉 token、signed_required，加上证书路径。

---

## 12. 后台任务（asynq）

- Scheduler 只在 worker 里跑一份。周期任务都带 `asynq.Unique`（略短于周期），`MaxRetry(0)`：下一个周期就是重试。
- 队列：`critical`（钱、订阅状态）、`default`、`low`（清理、汇总），按优先级加权。
- 各任务的周期在 cron 表达式里把分钟错开，代替原来的 loopPacer 抖动。
- 每个任务的载荷带 req_id；处理器里先把 req_id 放进 ctx 和 zap 字段。

| 任务 | 调度 | 超时 | 队列 |
|---|---|---|---|
| `order:expire_reservations` | `@every 30s` | 20s | critical |
| `payment:query_patrol` | `@every 1m` | 50s | critical |
| `subscription:expire` | `@every 1m` | 1m | critical |
| `subscription:roll_quota` | `@every 10m` | 1m | critical |
| `commission:settle` | `7 * * * *` | 1m | critical |
| `notify:scan` | `@every 5m` | 2m | default |
| `notify:dispatch` | `@every 30s`，业务提交后也入队（TaskID 按秒分桶去重） | 4m | default |
| `plugin:dispatch` | `@every 1m`，emit 后也入队 | 4m | low |
| `support:escalate_sla` | `*/5 * * * *` | 30s | default |
| `notify:publish_announcements` | `@every 1m` | 20s | default |
| `report:activity_daily`、`node:traffic_rollup` | `*/10 * * * *`（错开 2 分钟） | 2m | low |
| `identity:generate_users` | 建 job 的事务提交后入队，TaskID = `gen_users:<job_id>` | 10m | default |
| `certs:patrol` / `certs:issue` | `@every 1m` / patrol 入队，TaskID = `cert:<order_id>` | 30s / 15m | default |
| `retention:<表>` | 每天一次（03:40 起各表错开几分钟），分批删到删完或到批数上限 | 2–5m | low |
| `reconcile:user_generation` | `@every 1m`，把 queued 或租约过期的 job 重新入队 | 30s | low |

- 保留期清理覆盖的表：原有的 node_metrics、node_traffic_reports、各级流量汇总、node_alive_ips、subscription_fetch_log、activity_daily；新补的 idempotency_keys、registration_sessions、verification_codes、plugin_hook_deliveries、user_generation_jobs（结果清除后 N 天删行）、notification_deliveries。各表的保留期按第 18 节 Q5。
- 「支付开通后发通知」改为在开通的事务里直接写 delivery，不再靠 `ScanPaidOrders` 回看 2 小时（扫描器停机超过 2 小时会漏发）。
- 留在 serve 里、不进 asynq 的：心跳合并、节点会话、SSE hub。删除的：nonce 清理、epoch LISTEN、节点存活巡检（会话在不在就是在线状态）。

---

## 13. 事件、纪元与缓存

| 项 | 做法 |
|---|---|
| 发布时机 | service 用 `tx.AfterCommit` 登记，事务提交后才发布；回滚就不发 |
| 合并 | 一个事务里改了很多行，提交后只发一次（AfterCommit 按键去重） |
| 门户与后台实时推送 | Redis pub/sub `rt:*`，事件带 user_id，由 hub 路由。serve 和 worker 都直接发布，不再经过 PG NOTIFY |
| 节点下发 | 改了名单相关数据（订阅、配额、流量包、用户状态、节点池、用户组、节点配置、服务器）的属主 service，调 `node.Service.MarkUsersChanged(ctx, scope)` 或 `MarkConfigChanged`；提交后 `INCR node:users_epoch` 再 PUBLISH。会话管理器收到通知就重算；另外每 5 秒读一次纪元值，与上次不同也重算。所以丢一条 pub/sub 最多晚 5 秒，Redis 断开时同样退回 5 秒比对（读不到纪元就每 15 秒全量比对一次） |
| 名单重算的成本 | 按「服务器 × 名单内容哈希」共享编码，一次变更每个版本只编码一次 |
| 进程内缓存 | `hashicorp/golang-lru/v2/expirable` 加 `golang.org/x/sync/singleflight`，统一替换四处自写缓存 |
| 手工改库 | 不再有触发器兜底。手工 SQL 改了名单相关的表，要等下一次纪元变化或 15 秒全量比对；运维文档写明这一点 |

---

## 14. 配置、日志、加密

### 14.1 配置

- viper：先读 `/etc/pandora/config.yaml`（root:pandora，0640），再用 `PANDORA_` 前缀的环境变量覆盖（`SetEnvKeyReplacer(".", "_")`）。
- 秘密只能来自环境变量（systemd `EnvironmentFile=`，0600），或者 `*_FILE` 指向的 0600 文件；放在配置文件里会启动失败。
- 启动时 Unmarshal 进结构体，再统一 `Validate()`。现有的校验规则（长度、两个 JWT 密钥不同、生产环境禁用某些项、pprof 只能回环）原样搬过去。
- 段落：`env`、`http.public`、`http.admin`、`grpc.node`、`database`（另有 `migration_url`，只在 migrate 时读）、`redis`、`worker`、`auth`、`ratelimit`、`secrets`、`public_base_url`、`geoip`、`node`、`subscription.singbox`、`acme`、`log`、`backup`。
- `env` 缺省为 `production`（失败即关闭）：注册、找回回显 `dev_code`，以及开发格式的日志，只在显式设成 `development` 或 `test` 时打开；CI 与本地开发显式设 `PANDORA_ENV=development`。
- 只有部署脚本用的变量（POSTGRES_*、后台路径前缀、告警 Telegram、备份钩子）留在 `.env` 给脚本读，不进应用配置。

### 14.2 日志

- zap，生产环境输出 JSON，开发环境输出 console。文件经 lumberjack 写到 `/var/log/pandora/{serve,worker}.log`，自己滚动；删掉 logrotate 配置，避免和 copytruncate 冲突。
- 脱敏：包装一个 `zapcore.Core`，按现有的键名表脱敏（password、secret、token、cookie、authorization、credential、private_key、api_key、session_id、verification_code、otp、seed、card、cvv、signature、phc 等）。
- 固定字段：req_id、route（模板）、uid（有就记）、module。`StripURL`（把 URL 截成 scheme://host 再写日志）保留。

### 14.3 加密

| 用途 | 子密钥（HKDF-SHA256，info 串） |
|---|---|
| 封存（AES-256-GCM，密文头带 key id） | `pandora/seal/v1` |
| IP、标识的 HMAC 哈希 | `pandora/hmac/ip/v1`、`pandora/hmac/identity/v1` |
| 订阅审计盐、通知收件人盐 | `pandora/hmac/sub-audit/v1`、`pandora/hmac/notify/v1` |
| 礼品卡卡密的 HMAC（码表只存它与掩码） | `pandora/hmac/giftcard/v1` |
| 插件钩子签名 | 每个钩子自己的密钥（不变） |

被封存的数据：渠道凭据、DNS 凭据、ACME 账户私钥、证书私钥、面板 CA 私钥、批量建用户的结果、提现收款信息。原来的 `Envelope` 没有 DEK，名不副实，改名为 `Sealer`。

---

## 15. 部署改动（P7 做）

| 项 | 改法 |
|---|---|
| 二进制 | 6 个 `aegis-*` 合成 `/opt/pandora/bin/pandora` |
| systemd | `pandora-serve.service`、`pandora-worker.service`，各自设 MemoryMax 和 GOMEMLIMIT；以专用用户 `pandora` 运行（服务本身不需要 root；入站证书存库，不写文件）。备份和面板自身的 TLS 续期仍由 root 的 timer 执行 |
| Redis | 装 redis-server；`maxmemory-policy noeviction`、`appendonly yes`、`appendfsync everysec`；maxmemory 起步 256 MB；FLUSHALL 和 FLUSHDB 继续禁用；VALKEY_* 改名为 REDIS_* |
| 迁移 | 安装和升级时执行 `pandora migrate up` |
| nginx | public 和 admin 部分基本不变；新增 refresh 接口的独立限流 zone；删除节点相关的 HTTP location；admin cookie 的 `proxy_cookie_path` |
| 端口 | 9003 换成 gRPC 端口（缺省 8444，对外开放） |
| 健康检查 | 服务名、端口改掉；检查 worker 存活（asynq 心跳） |
| 备份 | pg_dump 流程不变；Redis 不进备份（会话丢了只需重新登录，任务丢了靠对账补） |

---

## 16. 测试与 CI

| 层 | 做法 | 在哪跑 |
|---|---|---|
| 纯函数 | 金额公式（F1–F11）、状态机转换表、令牌校验、限流维度、proto 编解码；现有的纯 Go 单测（purchase/balance、money、epay、plan_change_preview 等）原样搬过去，把期望值换算成元 | 本机、CI |
| repo 与 service | 连真实的 PG 18，每个测试一个事务、结束后回滚，或者每个包一个模板库；Redis 用 miniredis 跑单测，集成测试连真实 Redis | CI（本机没有 docker，跳过的不算通过） |
| 迁移 | 新基线 up → down → up；对照 Go 常量检查种子数据（开关、通知模板） | CI |
| 契约 | 错误码表对齐前端；`buf breaking`；指标、设备键、端口声明三项跨 module 对照，改成比较 proto 字段 | CI |
| 黑盒 | `panel/tests/*.sh` 的 e2e（下单、回调重放 10 次只记一笔账、易支付签名与篡改、到期与续费、后台、风控）把目标从旧二进制改成新二进制；只要 /v1 的 JSON 形状不变，就能直接跑 | CI |
| 节点 | 重写 nodesim 和 acceptancepanel 为 gRPC 客户端和服务端；保留「200 用户、5 节点、60 秒，零 5xx」「面板挂掉时用磁盘缓存冷启」「SIGTERM 收尾」等判据；真机矩阵用 node-e2e skill | CI、测试机 |
| 浏览器 | Playwright 购买、余额、礼品卡、到期路径 | CI |
| lint | golangci-lint：depguard（第 3.2 节）、errcheck、govet、staticcheck、gosec、bodyclose、sqlclosecheck、contextcheck | 本机、CI |

旧代码里扫描源码和 SQL 文本的契约测试（`*_contract_test.go`、`handler_sql_guard_test.go`、cmd 契约测试等）随旧代码在 P7 删除，不迁移。

---

## 17. 分步计划与完成标准

| 步 | 内容 | 完成标准 |
|---|---|---|
| P1 骨架 | `cmd/pandora` 与 cobra；`pkg` 下的 config、log、db、tx、redisx、errs、httpx（reqid、恢复、错误中间件、安全头、超时）、jwtx、crypto（HKDF、Sealer）、ratelimit、pagination；asynq 的 Server 和 Scheduler 外壳；goose 与 `00001_base.sql`；`.golangci.yml` 与 depguard 生成脚本；CI 新增 job：lint、迁移往返、新树单测 | `pandora serve` 起得来，`/healthz` 回 200，日志带 req_id；`pandora worker` 能跑一个示例周期任务；`pandora migrate up/down/up` 在 CI 通过；depguard 能拦住一个故意违规的样例（样例不入库） |
| P2 鉴权与用户 | identity、auth、settings、audit、idempotency 五个模块；refresh 接口；前端令牌改造 | e2e 的登录、注册、找回、改密、跨域令牌、IAM-006、重认证、停用即吊销各段全绿；前端 api 测试覆盖刷新单飞 |
| P3 交易 | catalog、order、payment、ledger、promo、commission、giftcard，以及 subscription 的核心（表、开通、续期、换套餐、加流量、状态口径；结算离不开它）；decimal。分四波：catalog、ledger、promo → subscription 核心、payment → order → commission、giftcard。`plan_node_pools` 推到 P5 与 node 一起建 | 下单、结算、释放、换套餐、佣金、挂账、查单各条 PG 集成测试改写后通过；e2e 回调重放只记一笔账；易支付「990 分 ↔ 9.90」与篡改用例通过；账本配平检查在库里生效 |
| P4 订阅 | 订阅链接渲染与拉取、到期与配额滚动任务（订阅的表与开通等核心已在 P3） | expiry_e2e 全绿；订阅格式 UA 分支全覆盖；渲染结果与旧实现逐字节对比一致（用固定种子数据做 golden） |
| P5 节点 | node 模块后台编排；proto 与 nodeapi；Enroll 与 Session；pdnd 客户端改造；nodesim 与 acceptancepanel | 冒烟：200 用户、5 节点、60 秒零 5xx；删人后节点秒级断线（node-e2e 真机）；证书续期、吊销断流、supersede 都有集成测试 |
| P6 其余 | certs、notify、support、content、appearance、plugin、report；看板改为调属主 service | 后台各页面 flow-walk 通过；admin_e2e 全绿 |
| P7 切换与删旧 | 安装脚本（Redis、单二进制、systemd、nginx）；删除旧代码、旧迁移、旧守卫（行数豁免表、RESERVED-TABLES、routebudget）；更新 README 和 `.claude/rules` | 全新测试机上安装成功；全部 e2e、Playwright、节点验收在新二进制上通过；仓库里不再有 `aegis-*` 命令和 `internal/domain` |
| P8 换栈后 | 用旧成绩单作基线复测，再做性能与占用优化 | 另行规划 |

每一步的提交都要求：本机 lint 和单测通过（本机 Go 1.26.9），推送后 CI 全绿。涉及认证、钱、迁移、节点通道的改动，合入前另派一个没参与实现的 opus 子 Agent 做只读审查。

---

## 18. 产品问题与结论（用户 2026-10-12 定）

| # | 问题 | 现状 | 结论 |
|---|---|---|---|
| Q1 | 订阅状态要不要精简为 active、expired 两个，外加「续费窗口已关」标记？ | 定义了 8 个状态，trialing、past_due、grace、paused、cancelled 都没有写入入口 | **精简**。以后真做试用、暂停、取消时再加 |
| Q2 | 退款要不要做？ | 退款表都在，但没有任何写入方，后台和门户也没有退款入口 | **本阶段不做**，相关表和守卫不建；以后做的话按新规矩单独设计 |
| Q3 | 佣金的基数包不包括余额抵扣的部分？ | 包括（基数 = 订单总额，含余额抵扣） | **按订单总额，含余额抵扣**（维持现状） |
| Q4 | 后台会话的寿命和空闲超时？ | 与门户相同，30 天，没有空闲超时 | **后台**：绝对寿命 7 天，空闲 2 小时失效；**门户**维持 30 天 |
| Q5 | 各类数据保留多久？ | 审计永久保留；6 张表从不清理 | **按此执行**：审计 400 天；幂等键 30 天；注册会话和验证码 7 天；插件和通知投递记录 90 天；批量建用户的 job 30 天；流量与指标沿用现有期限（48 小时到 400 天） |
| Q6 | 28 个种了但从不检查的权限码删不删？ | 种子里有，路由不检查 | **删除**；随后 Q8 把权限码整体取消 |
| Q7 | 登录要不要按账号限制失败次数？阈值多少？ | 只按 IP、路由和 /24 网段限流；Redis 故障时放行 | **按此执行**：同一账号 15 分钟内失败 10 次，暂停 15 分钟（期间口令正确也拒绝，提示稍后再试）。已知代价：可能被人拿来锁别人的账号 |
| Q8 | 后台要不要分角色、权限码、临时提权？ | 6 个内置角色、44 个权限码、可临时绑定，缺权限回 404 | **不分**：后台权限只分有和没有，是管理员就能做全部后台操作（第 9 节） |

---

## 19. 换栈时顺带消掉的旧缺陷（不单独修）

| 缺陷 | 在新设计里怎么消失 |
|---|---|
| 没有刷新接口，access 令牌 30 天 | 第 8 节 |
| 登录与改密之间有竞态 | 凭据纪元 `uepoch` |
| SSE 不随吊销断开 | `rt:sess:{sid}` 关流 |
| reauth 不按 audience 校验、不写 last_reauth_at | reauth_at 记在会话里 |
| 设为管理员不踢门户会话 | 授予时吊销 |
| 主密钥直接当 AES 密钥和 HMAC 盐 | HKDF 子密钥 |
| `pay --key` 让密钥出现在 argv 里 | 改从 stdin 读 |
| 节点手工吊销不吊销 UniProxy 令牌，也不断开在途的 SSE | 通道整体替换；吊销即关流 |
| 节点身份 90 天硬过期，不能续期 | 会话内续期 |
| 在线 IP 不按服务用户过滤 | 第 11.3 节 |
| 流量计费用 float64 | 整数千分比倍率 |
| agent_version 写死，HeartbeatOutput 被丢弃 | Hello 带真实版本；会话双向 |
| 幂等键因为禁止删除而永远清理不掉 | 去掉禁删触发器，加保留期任务 |
| 6 张表无限增长 | 第 12 节的保留期任务 |
| 支付开通通知只回看 2 小时 | 在开通事务里直接写 delivery |
| 中间件直接写幂等 SQL | 移进 idempotency 模块的 repo |
| 审计被当作业务数据源（改密失败限流、登录失败去重、同源 IP 判断） | 前两项改用 Redis 限流；同源 IP 改为 audit service 带时间窗的查询 |
| 审计哈希链造成全局串行和 40001 | 删除哈希链 |
| CSV 导出里用浮点表示钱 | decimal |
| 门户余额接口写死 "CNY" | 用账户的币种 |
| 证书相关 15 条路由没有登记在 routes.txt | routebudget 随旧代码删除；新路由表由 gin 导出，测试检查 |
