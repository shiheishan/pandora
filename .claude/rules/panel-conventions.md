---
paths:
  - "panel/internal/**"
---

# 面板的统一做法：错误、日志、加密、钱、实时、Redis

## 错误
- 对外错误只有 `panel/internal/pkg/errs` 一套：封闭的码表、HTTP 状态和中性中文文案。service 返回 `*errs.Error`（`errs.New` / `errs.Newf` / `errs.Wrap` / `errs.Invalid`），handler 交给 `httpx` 写信封；未知错误一律 500 `internal_error`，原因只进日志。
- 前端 `panel/frontend/src/core/api.ts` 的 `SERVER_ERROR_CODES` 按同一张表解析。两边没有测试对齐（见 `errs.go` 头注释）：新增码两处手动同改。
- `reauth_required` 与 `forbidden` 同为 403、码不同，前端靠码弹重认证框。

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
- `panel/internal/pkg/realtime`：事件只带 topic 与定位 id，不带业务数据，客户端收到后经 REST 拉，权限过滤留在 REST。topic 常量在 `realtime.go`，新增 topic 同步前端 `core/query.ts` 的 `REALTIME_TOPICS`。
- 由属主 service 在提交后发（`Publisher.Changed` / `Publisher.Notify`，配合 `tx.AfterCommit`）。发布失败只记日志，不回滚业务。

## Redis
- 客户端一律经 `redisx.New` / `redisx.Options` 构造：打开 `ContextTimeoutEnabled`，读写、拨号、等池超时收在同一量级，ctx 期限才对命令生效（go-redis 缺省不看 ctx 期限）。
- 会话、鉴权在 Redis 故障时回 503 `service_unavailable`，不放行。
