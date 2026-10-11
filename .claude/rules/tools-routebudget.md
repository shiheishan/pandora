---
paths:
  - "panel/tools/routebudget/**"
  - "panel/tools/archguard/**"
  - "panel/internal/platform/roundtrip/**"
  - "panel/internal/platform/db/roundtrip_tracer*.go"
  - "panel/internal/api/*/router*.go"
  - "panel/internal/api/*/*route_budget*_test.go"
---

# 往返记账器、路由登记表与结构守卫（P0 守卫）

## 往返记账器

- 计数器经 context 传（`platform/roundtrip`），访问日志中间件给每个请求挂一个；`platform/db` 的 pgx 追踪器（`roundtrip_tracer.go`，`OpenWithOptions` 注册）与 `roundtrip.ValkeyHook` 按 ctx 加一。不带计数器的 ctx（后台循环、归还钩子）不记
- 口径：语句、管线批次各 1（含 BEGIN、COMMIT），COPY 2；语句准备、取连接探测（空闲 >1 秒）、非受控归还另记。pgx 批次里顺带的语句准备、显式 `Pool.Ping` 没有钩子，不记
- 记在发起往返的 ctx 上：单飞合并读（`platform/cache`，发起者用 `context.WithoutCancel` 跑加载，值保留）记在发起者头上，等结果的请求记 0。预算测试串行发请求、量稳态，数字确定；访问日志在并发下同一路由会 0 / N 不一，看分布别看单条
- 池级 `Pool.SessionResets()` 数真正执行过的会话清理语句：受控路径（InTx / QueryRowScoped / BatchScoped / QueryScoped）归还的连接上必须为 0。换栈接 database/sql 时用它核对 GORM 事务归还有没有多一条清理
- Valkey 钩子要在每个网关建好客户端后挂一次：`rdb.AddHook(roundtrip.ValkeyHook{})`；挂两次重复计数。每个客户端各挂一次：aegis-node 的 nonce 认领专用客户端（`nonceRDB`）也要挂，否则签名请求的 nonce SET 不进 kv_rt，与预算测试（它的 nonce 计 1 次 Valkey 往返）对不上
- 访问日志的字段、节点网关的降级与每分钟统计见 `rules/panel-middleware.md`

## 路由登记表（`panel/tools/routebudget/routes.txt`）

- 三个网关的全部路由，一行一条：网关、方法、路由模板、库语句往返、Valkey 往返。是全仓唯一的路由清单（模块地图直接读它）
- `routes_test.go` 遍历三个真实路由器：加、删、改路由都要同步改表，测试会给出要加的行；行按网关、模板、方法排序
- 预算写「- -」是还没量过。填了数的行由 PG18 走真实路由逐条量：门户 `api/public/route_budget_pg18_test.go`（public_api 域，默认租户里自建夹具），节点 `api/node/route_budget_pg18_test.go`（effective 域，自己接入一个节点，装配照 aegis-node）
- 量法：先热身（语句缓存热起来），再量 3 次必须一致；比的是语句往返（扣掉准备、探测、归还清理），量出的数必须与表相等。多了是超预算；少了是优化落地，照测试给出的行把表改小——只降不升
- 「只升」由 CI 拦：panel-pg18.yml 的 panel-unit 跑 `go run ./tools/routebudget/ratchet`。基点：任务分支一律是与 `origin/feat/panel-redesign`（再 `origin/main`）的合并基点，只有推到主线本身才用推送前的头，PR 用目标分支——所以已经红的放宽不会被下一次不相干的推送洗绿。变宽包括某行变大、退回「- -」、新出现带预算的行（改模板时要和旧行对照）、`pushWALBudget` 变大；每一条都要在区间里的提交信息中点名放行，没点名的照样红：
  - `Budget-Raise: public GET /v1/me <理由>`（网关 方法 模板，再写理由）
  - `Budget-Raise: pushWALBudget <理由>`
  - 当前登记表或 `pushWALBudget` 读不到（挪走、改名）直接报错，改了位置要同步 `routebudget.RoutesFile` / `NodeBudgetFile`
- 门户路由按已登录量（带 Authorization），登录与订阅拉取不带
- 节点 push 另有 WAL 棘轮（`pushWALBudget`，3 个有效用户、带上报编号，取 5 次最小值）。N2 落地后 push 行改成 2、WAL 改成 6KB

## 结构守卫（`panel/tools/archguard/`）

- `TestImportDirection`：platform 不 import domain / api / middleware，domain 不 import api，middleware 不 import domain / api；豁免 `importExemptions`
- `TestNoReadOnlyInTx`：InTx 闭包里只有 SELECT 即红；判定保守（tx 交给认不出的地方、出现 app.* 函数调用、任何写或加锁的字样都不算只读）；豁免 `readOnlyTxExemptions` 按「包:声明 → 个数」，数要相等
