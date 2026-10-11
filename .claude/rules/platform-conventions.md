---
paths:
  - "panel/internal/**"
  - "panel/cmd/**"
---

# 平台层的唯一出口

- httpx 的错误码是封闭列表（`panel/internal/platform/httpx/httpx.go` 的 `Code` 常量）。前端 `panel/frontend/src/core/api.ts` 的 `SERVER_ERROR_CODES` 按同一列表解析信封，没有测试对齐两边：新增给后台或门户用的码要两处同改
  - `reauth_required` 与 `forbidden` 同为 403、码不同，前端靠码弹重认证框；`upgrade_required`（426）只给节点网关，前端不登记
  - 对外只给码和中性中文文案，内部详情只进日志
- 日志写路由模板，不写原始路径、完整 URL 或查询串：订阅令牌、Telegram 回调 secret 是路径段，易支付密钥、Bot Token 在出站 URL 里
  - 请求侧取 `httpx.RouteTemplate(r)`（chi 模板，根中间件里按路由树补查，未匹配写 `-`）；`Fail`、`Recovery`、访问日志都按此写。不另维护「哪些路径含秘密」的表
  - 出站 `client.Do` 失败先过 `logging.StripURL(err)`（只留 scheme://host）再往上返回，这类错误会进日志与库里的失败原因
  - 守卫：`platform/routelogtest` 遍历三个网关的全部路由，路径参数换成哨兵各打一次（限流放行、拒绝两遍），日志里出现哨兵或原始路径即红（`api/{public,admin,node}/log_no_raw_path_test.go`）；新路由自动覆盖。处理函数有意把某个公开标识（不是凭证）写成独立字段时，在该网关的测试里用 `routelogtest.PublicParam` 声明
- 审计只经 `platform/audit` 的 `Write` 写入 `audit_events`：它从每租户链头 `audit_chain_heads`（00142）`UPDATE … RETURNING` 取 `chain_seq` 与前驱哈希，写行时同一条语句把链头推到新行。不要在别处直接 INSERT 这张表或改链头；改哈希口径必须保持存量行仍能按 `chain.go` 的 `VerifyChain` 复算
  - 序列化事务的快照早于取号：快照之后别人取过号，取号就报 40001。写审计的序列化事务一律用 `InTxSerializableRetry`：第一次乐观，重试时事务第一条语句 `LOCK TABLE audit_chain_gate`（只用来加锁的空表，LOCK 不取快照）排队；乐观尝试取号前以 NOWAIT 过闸，闸被占就改去排队，不带着业务锁等闸（见 `db.go` 的 `EnterChainGate`）。只用 `InTxSerializable` 的写审计路径在并发下会回 40001
  - 读已提交的审计写入（登录、回调、后台操作）不碰闸，只在链头行上排队。别让它们等任何「序列化事务整段持有」的锁：它们手里常握着业务行锁（`UPDATE users`、按支付单的 advisory lock），会和持锁后要同一行的事务成环（w9audit 曾因此 40P01）
  - 链头缺行时 `Write` 按审计表链尾现建（口径只在 Go 里一处），所以链头不回填、被删也不会分叉
- 会落库的秘密只落哈希或密文。主密钥只做信封加密的根和派生用途专用盐（`crypto.go` 的 `SubscriptionAuditSalt`、`NotifyRecipientSalt`）
  - 新用途就新派生一个盐，带自己的域分隔串（`aegis/<用途>/…/v1`），不要复用已有的盐
  - 已有盐的派生公式落了存量哈希就不能改。守卫：`panel/internal/platform/crypto/salt_test.go` 的 `TestDerivedSaltsAreDomainSeparated`
- 实时推送（`platform/realtime`）只推「什么变了」（topic + 定位 id），不推数据；客户端收到后经 REST 拉，权限过滤留在 REST。不要往事件里塞业务数据
  - 前端主题由 `listener.go` 的 `topicFor` 从 `notify_change` 触发器的表名映射；高频写入的表（`quota_balances`、按日用量）不挂通知，新增高频表也别挂
- 进程内缓存一律用 `platform/cache`（2026-10-09 收拢，w12cache）：`Cache` 有上限、单飞、TTL 兜底，条目是否可用由调用方按纪元判（`Freshness`：监听健康时比 `Watch` 的戳、零往返，不健康时比在本来就要跑的查询里读出的纪元 `EpochSQL`）；没有纪元的急停类缓存用 TTL 加 `Clear`。不要再在域里手写 map + mutex + 过期时间的缓存
  - 纪元的来源是迁移里的触发器（下发纪元 00101/00153，缓存纪元 00155/00156），种类与序列名只在 `platform/cache/epoch.go`；每个 `Watch` 占池里 1 条连接（LISTEN）加池外 1 条（探针），进程要起 Watch 先在 `platform/config` 的连接预算里留位置
  - 还没迁的：`adminops/dashboard_cache.go`（W1-c）、`subscription/cache.go`（W1-a）
- 源码契约测试（断言某函数里必须有 / 不许有某段代码）一律经 `platform/sourcetest` 按「包 + 声明名」取源码，不按文件名读、不用「函数 A 到函数 B」截文本
  - 否定断言用 `Source` 覆盖整个包，函数挪到哪个文件都逃不掉；名字找不到或重名直接失败
  - 只被 `*_test.go` 引用
- go-redis（Valkey 客户端）缺省不看 ctx 期限：`context.WithTimeout(ctx, 250*time.Millisecond)` 传进去，命令照旧等到客户端自己的读写超时（缺省 3 秒）才返回。要让 ctx 期限生效，必须在 `redis.Options` 打开 `ContextTimeoutEnabled`，并把读、写、拨号、等连接池超时一并收到同一量级。
  - 三个网关共用的客户端（`panel/cmd/aegis-{public,admin,node}/main.go` 的 `redis.NewClient(redisOpt)`）没开这个开关。在请求路径上靠 ctx 期限限时的新代码（限流、鉴权 EVAL 等）别直接用它，先实测「挂住时多久返回」，需要就像 nonce 认领那样用独立客户端。
  - 依据：w12nonce 审查第二轮实测，nonce 认领设 250ms、实际等 3 秒；独立客户端打开开关后，挂住、100 路并发、连接被拒三种情形都在 251ms 返回（细节见 w12nonce 的 `.claude/rules/pdnd-node-panel.md`，合入主线后在 `pdnd-node-panel.md` 签名请求一条）。
