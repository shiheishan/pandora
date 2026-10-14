---
paths:
  - "panel/internal/**"
---

# 面板的统一做法：错误、日志、加密、钱、实时、Redis

## 错误
- 对外错误只有 `panel/internal/pkg/errs` 一套：封闭的码表、HTTP 状态和中性中文文案。service 返回 `*errs.Error`（`errs.New` / `errs.Newf` / `errs.Wrap` / `errs.Invalid`），handler 交给 `httpx` 写信封；未知错误一律 500 `internal_error`，原因只进日志。
- 前端 `panel/frontend/src/core/api.ts` 的 `SERVER_ERROR_CODES` 按同一张表解析，新增码两处同改；守卫是 `errs` 包的 `TestServerErrorCodesMatchFrontend`。
- `reauth_required` 与 `forbidden` 同为 403、码不同，前端靠码弹重认证框。
- 库的约束违例只经 `db.ConstraintErrors`（`panel/internal/pkg/db/constraint.go`）翻译成模块哨兵：各 repo 一张 `constraintErrs` 表（按约束名 ByName、按 SQLSTATE ByCode），不自己 import pgconn 判码、不用 `gorm.ErrDuplicatedKey` 一类逐个判断；要看码用 `db.AsViolation`（只在 repo 里），死锁与序列化失败用 `db.IsRetryable`。service 只用 `errors.Is(err, 本模块哨兵)` 判；翻译后的哨兵用 `%w` 包着原 PgError，对别的模块传上来的错误调 `db.AsViolation` 会把它的违例当成本模块的。
- LIKE / ILIKE 的用户输入一律经 `db.EscapeLike` / `db.ContainsPattern` 转义 `\ % _`，不再各写一份。

## 日志
- zap，字段带 req_id。请求日志只写路由模板（`c.FullPath()`，未匹配写 `-`），不写原始路径、完整 URL、查询串：订阅令牌是路径段。
- 出站 HTTP 失败先过 `log.StripURL(err)`（只留 scheme://host）再返回或记录，渠道密钥、Bot Token 可能在 URL 里。
- 敏感键名脱敏由 `NewRedactingCore` 统一做（键名表在 `panel/internal/pkg/log/redact.go`），加新的敏感字段名加进表里，不要各处手抹。
- 服务日志由进程内 lumberjack 滚动，不要再给它们配 logrotate。

## 加密
- 主密钥只做根：子密钥经 HKDF 派生，用途串是 `panel/internal/pkg/crypto/keys.go` 的 `Info*` 常量（`InfoSeal`、`InfoHMACIP`、`InfoHMACNodeDevice` 等）。新用途新加一个 info 串，不复用已有的；已落了存量哈希或密文的 info 串不能改。

## 钱
- 规则见 `docs/architecture.md` 第 6 节。改钱的代码必须知道：库里 numeric(20,2) 元，repo 写入前 `money.Check` / `money.CheckNonNegative`（PG 会静默四舍五入，库里拦不住）；/v1 JSON 是整数分，经 `money.FromCents` / `money.ToCents` 换算；按比例的金额向零截断、只截一次，只在 service 的领域计算里算，SQL 里不做金额算术。

## 实时推送
- `panel/internal/pkg/realtime`：事件只带 topic 与定位 id，不带业务数据，客户端收到后经 REST 拉，权限过滤留在 REST。topic 常量在 `realtime.go`，新增 topic 同步前端 `core/query.ts` 的 `REALTIME_TOPICS`；守卫是 `realtime` 包的 `TestRealtimeTopicsMatchFrontend`。
- 由属主 service 在提交后发（`Publisher.Changed` / `Publisher.Notify`，配合 `tx.AfterCommit`）。发布失败只记日志，不回滚业务。

## Redis
- 客户端一律经 `redisx.New` / `redisx.Options` 构造：打开 `ContextTimeoutEnabled`，读写、拨号、等池超时收在同一量级，ctx 期限才对命令生效（go-redis 缺省不看 ctx 期限）。
- 会话、鉴权在 Redis 故障时回 503 `service_unavailable`，不放行。
- 限流用 `internal/pkg/ratelimit` 的 `Limiter`；试错计数（先占名额再验、通过后 `Reset`、没结论 `Release`，可选「第 N 次把期限改成暂停时长」）用同包的 `Counter`，不在模块里另写 Lua 计数器。

## 列表分页
- offset 分页的列表接口，limit、offset 一律经 `internal/pkg/pagination.Parse(c, 缺省, 上限)` 解析（游标分页、Top-N、导出上限不在此列）：非法值回 422 `validation_failed`，超上限截到上限。不手写 strconv，不把分页参数当字符串绑定。
