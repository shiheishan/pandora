---
name: add-setting
description: pandora 后台加一个设置项（站点设置、开关、限制或上限、文案、密钥类配置如 SMTP 口令 / 机器人令牌 / 验证码 secret）的完整做法：先分清是设置组的键、降级开关还是部署配置，再按检查表改 settings 的 service（键名、缺省、校验、事务里写与审计、Sealer 封存）、handler 的 DTO 与路由（RequireAuth + RecentReauth）、消费方的注入与快照失效（提交后清进程内快照）、后台页面与 page-help、假后端与各层测试，最后自证。用户或任务说「加一个后台设置」「后台能改 X」「新开关」「站点配置加一项」「把这个常量做成可配置」「加个上限 / 限制」「后台填一个密钥」时使用，即使没提 settings 二字也用；新增或改动设置组的读写、生效机制时同样适用。
---

# 后台加一个设置项

设置表 `settings`（一键一 JSON 值）与降级开关表 `feature_switches` 的属主是 `panel/internal/module/settings`。别的模块要设置值，调它的 service，不直接读表。

动手前用 Read 打开下面几个文件，让按目录生效的规则加载（用 Bash 读不加载）：
`panel/internal/module/settings/service/home.go`（最干净的设置组样板），以及 `.claude/rules/panel-architecture.md`、`panel-conventions.md`、`panel-http.md`、`frontend-mock.md`、`frontend-shells.md`、要放页面的那个 `screens-admin-<模块>.md`、消费方的 `module-<模块>.md`。

## 一、先定是哪一类

| 是什么 | 放哪 | 先例 |
|---|---|---|
| 运营在后台改、改了要生效的值（文案、上限、开关、渠道参数、密钥） | **设置组**：`settings` 表的键，缺行取 Go 里的缺省值，**迁移不种设置行** | `home.*`（`service/home.go`）、`purchase.*`（`service/purchase.go`） |
| 出事时紧急停用一项功能的总闸 | **降级开关**：`feature_switches` 一行，要新迁移种行（见 new-migration skill）、`keys.go` 的 `Switch*` 常量与 `SeedSwitches`（按 code 升序插入，`TestSeedSwitchesMatchMigration` 对照迁移），路由上挂 `k.Switch(code)` | `billing.checkout`（`order/handler/handler.go`） |
| 启动就要、改了要重启、或属于部署的东西（库地址、主密钥、监听端口） | **部署配置**：viper 配置，不进后台 | `public_base_url` 是配置值，后台的 `site.url` 只是覆盖 |

缺省值、可选范围、谁看得到是**产品规则**：任务说明没写就停下问用户，不自己定。工程上的事（键名、存法、生效机制）按下面自己定。

## 二、后端检查表（设置组）

### service：`panel/internal/module/settings/service/`

1. **文件**：新组一个文件 `<组>.go`（单文件 ≤800 行，含测试也一样），已有组就加在它的文件里。`keys.go` 只放 site / registration / mail 和开关，新组的键放自己的文件。
2. **键与缺省**：键名 `<组>.<名>`，常量旁注释写类型与范围（`// int，缺省 0（不限），0–50`）。缺省值只定义一处；消费方也要用时导出（`DefaultHomeEnabled`、`DefaultSiteAllowIndexing` 先例）。
3. **类型**：读的类型 `X`；部分更新用 `XUpdate`（指针字段，nil = 不改，`HomeUpdate` 先例）；整份写就直接收 `X`，由 handler 把每个字段都当必填（`setPurchaseLimitsRequest` 先例）。
4. **读**：`X(ctx)` 用 `s.store.Values(ctx, <组>Keys)` 加 `decodeOr`。库里的值解不开怎么办二选一，在注释里写明：
   - 当内部错误返回（`decodeOr` 的默认做法，适合后台页面和低频读）；
   - Warn 后取缺省值（`decodePurchaseItem`），只用在「读不到也不能让主流程失败」的路径上，比如下单。
5. **校验**：`errs.Invalid(map[string]string{...})`。`fields` 的键就是请求 JSON 的字段名（数组用 `faq.0.q`），文案是给运营看的中文，前端与假后端要同字。文本按字符数（`utf8.RuneCountInString`）限长，拒控制字符；要去首尾空白、统一换行的，先规整再校验（`normalizeMultiline`）。
6. **写**：`SetX` 照 `SetHome` 的顺序：
   `s.tx.Do` → `s.store.LockValues`（锁本组各行）→ 解出旧值 → 合并 → 校验 → 逐键比较（简单值用 `diff`）→ **没变就直接返回，不写、不审计、不通知** → `s.writeChanges` → `s.writeAudit` → 提交后钩子（见第 8 条）。
   - 审计与写在同一个事务里。动作名照近邻写 `<组>.settings_changed`，`resource_type` 用 `auditResourceSettings`，`data` 带 `group`、`before`、`after`（只放改了的键）。
   - 要连外部核对的（主域名保存前回连 `/v1/site-config`），放在事务**外面**先做，不通回 422 `fields.<字段>`（`site_url.go` 先例，HTTP 一律经 `internal/pkg/safehttp`）。
7. **密钥类**（口令、令牌、secret）：
   - 存：`s.sealer.Seal([]byte(明文), []byte(键名))`，AAD 用键名，base64 后存；键名常量加 `//nolint:gosec // 设置键名；值经 Sealer 封存（AAD 为键名）`。
   - 改：请求里 `*string`，nil = 不改，`""` = 清除（`s.store.Delete` 这个键），其余 = 新值（`SetSignup` 的 `CaptchaSecret`、`MailUpdate.Password` 先例）。
   - 读：只有真正要用明文的路径解封（`openSealed`，失败记 Error、返回空串，不把错误往上抛成 500）；给后台展示的读口径不解封，只回 `HasXxx bool`（`SignupWithoutSecret` 先例）。
   - 审计**不写值**，只写 `secret_changed: true`；日志里也不打。
   - 后台回显永远是「已设置 / 未设置」，接口响应里没有明文和密文。
8. **生效机制**：先找改了以后谁要知道。`grep -rn "<键名>\|<读方法名>" panel/internal`，再看消费方有没有缓存（`grep -rn "singleflight\|TTL" panel/internal/app panel/internal/module/<消费方>`）。

   | 读的频率 | 做法 | 先例 |
   |---|---|---|
   | 低频（下单前、后台页面、任务里偶尔读） | 每次现查库，不加缓存 | `PurchaseLimits`（「改了下一单即生效」） |
   | 高频（每个请求、订阅拉取、入口页） | 进程内快照 + **提交后同步失效** + 30 秒 TTL 兜底别的进程 | 站点 → appearance 的 PageMeta；订阅附加设置 → subscription 拉取快照；首页 → `app/portal_home.go` |
   | 只需要知道「变过没有」 | 代号计数，提交后加一 | `signupGen` → `app/portal_csp.go` |

   高频这一档的接法（`148a3481` 是完整先例）：
   - settings 的 `listeners.go` 加 `XChangeListener interface{ XChanged() }`、`UseXChangeListener`，`Service` 加字段，`MissingPorts` 加一项（漏装配启动就报）；
   - `SetX` 有改动时 `tx.AfterCommit(ctx, "settings:<组>_saved", …)` 里调监听方，回滚不调；
   - 消费方实现 `XChanged()` 清快照；快照重读要带失效代号，失效前开始的重读读完不写回（`subscription/service/pull_extras.go`）；
   - `panel/internal/app/wire.go` 里 `settingsSvc.UseXChangeListener(<消费方>)`；
   - 同步 `.claude/rules/panel-architecture.md`「进程内快照的失效同样走注入」那条清单。
   - 不加 Redis 频道：serve 单实例、后台写都在 serve 里（降级开关的跨进程失效是已有的另一套，别照抄）。
9. **消费方怎么拿值**：在消费方 service 里定义只含所需方法的窄接口和 `UseX` 注入（`order/service/limits.go` 的 `PurchaseLimits` 先例），`wire.go` 里接 `settingsSvc`；必需的进消费方的 `MissingPorts`。不跨模块读 `settings` 表，不在 handler、中间件里读设置做业务判断。

### handler：`panel/internal/module/settings/handler/`

10. **DTO**：`dto_<组>.go` 单独定义请求与响应结构，不把 service 类型直接序列化。部分更新的请求字段用指针；数组在响应里永远是 `[]` 不是 `null`。`httpx.BindJSON` 已拒未知字段（多带字段回 400），不用另写。
11. **路由**：`handler.go` 的 `RegisterAdmin`：`GET /settings/<组>` 挂 `k.RequireAuth`；`POST` 挂 `k.RequireAuth, k.RecentReauth`（要幂等时 `RecentReauth` 排在 `Idempotency` 前，见 `panel-http.md`）。POST 成功后回读并返回最新值。`Settings` 接口加新方法。
12. **门户要看的**值走 `RegisterPublic` 的 `/site-config` 等公开接口，只放可以公开的字段，密钥永不出现。
13. **只读模式**：`admin.writes` 关时新 POST 自动 503，这是对的。只有确实要在只读时可写才进 `adminWriteAllowlist`，并同步假后端 `ADMIN_WRITE_ALLOWLIST`（`TestAdminWriteAllowlistMatchesMock` 逐条对照）。

### 测试

14. `service/<组>_test.go`（假 store，单测，本机能跑）：缺省值；规整与审计内容；没变不写不审计不通知；部分更新保留其它字段；每条校验的 `fields` 键与文案；有监听方时查 `MissingPorts` 列出它、回滚不通知。
15. PG 用例（本机跳过，CI 真跑）：在 `settings_pg_test.go` 或 `<组>_pg_test.go` 做真库往返；密钥类照 `TestMailPasswordSealRoundTrip` 验封存往返且库里不是明文。`testkit.TxCtx` 下 `tx.AfterCommit` 的钩子不跑，验通知别用它。夹具里的唯一值带随机后缀。
16. `handler/<组>_test.go`：未登录 401；POST 未重新认证 403 `reauth_required`；部分更新；未知字段 400；响应形状（`home_test.go` 先例）。
17. 消费方的行为用例（上限真的挡住、快照真的失效），放在消费方包里（`order/service/limits_pg_test.go` 先例）。

## 三、前端检查表

18. **schema**：`panel/frontend/src/admin/screens/<模块>/schemas.ts` 加 zod schema，形状照 DTO 的 json tag；密钥类只有 `has_xxx`。注释写上接口路径（`// 首页设置（GET/POST v1/settings/home）`）。
19. **表单逻辑**放纯函数文件（`content/homeForm.ts`、`billing/model.ts` 先例）并写 vitest：上限常量注明「与后端 `<文件>` 一致」；前端校验文案、`fields` 键与后端同字同键；部分更新的组只提交改了的键，没改不发请求；数字输入存原文，清空不悄悄变成 0。
20. **页面**：放在这项设置所属业务的页面里（购买限制在订单页头的弹窗、首页卡在「主题与插槽」），不新开「设置」大页。`useMutation` 成功后 toast + `invalidateQueries`，失败交 `useFailure` 把 `fields` 落到字段上。重新认证不用自己写：接口回 403 `reauth_required` 时 api 客户端会弹框并重放。密钥输入框留空 = 不改，另给一个明确的「清除」动作发 `""`，已设置时显示「已设置」不显示值。
21. **page-help**：`src/admin/page-help.ts` 对应 `模块:页签` 加一条 `{ term, text }`，写清这项管什么、缺省是什么、**多久生效**（「保存后立即生效」只在确实提交后同步失效时写；只靠 TTL 的写「约 30 秒内生效」）。`admin.test.ts` 核对键落在真实页面上。
22. **假后端**：`panel/frontend/dev/mock/admin/<模块>.ts` 加同形状的 GET / POST，校验文案与 `fields` 键照 Go，POST 先 `if (!ctx.requireReauth()) return`；门户能看到的值，门户假后端（`dev/mock/portal/`）同步。`tests/mock-admin-<模块>.test.ts` 加「与后端同规则」的用例（`mock-admin-content.test.ts` 的首页一段先例）。
23. **冒烟读表**：新 GET 加进 `tests/smoke/admin.smoke.ts`（schema 从页面模块导入，见 `frontend-smoke.md`）；写接口要在真栈上验的加进 `writes.smoke.ts`。

## 四、文档与规则

24. 页面规则 `.claude/rules/screens-admin-<模块>.md`、消费方规则 `module-<模块>.md` 写一句：这项设置的键、缺省、生效方式、谁读。
25. 运维要知道的（改了会影响链接、回调、节点、收钱）补进 `panel/deploy/RUNBOOK.md` 对应章节。
26. 用到了新的提交后失效接口，记得第 8 条最后那句的规则同步。

## 五、自证

本机（命令与 Go 版本口径见 verify skill）：

- `panel/` 下：`gofmt -l` 改到的文件；`GOTOOLCHAIN=go1.26.10 go vet` 与 `go test ./internal/module/settings/... ./internal/module/<消费方>/...`；`go test ./tools/refactorcheck/`；golangci-lint（含 depguard）。
- `panel/frontend/` 下：`npm run lint`、`npm run typecheck`、`npm run test`。
- flow-walk 在假后端上点一遍：打开页面看到缺省值 → 改值保存（先 `/__mock/expire-reauth` 验重新认证弹框）→ 错误输入看字段提示 → 刷新后值还在；密钥类再验「已设置」与清除。

CI（本机跑不了，以 GitHub 为准）：panel-newstack 的 test（PG 用例）、panel-smoke（读表与写表）。

改动碰到密钥、认证、钱（支付渠道参数、佣金、价格类上限）或节点通道的设置，合入前走 pre-merge-review。

## 坑

- 迁移里种设置行：不要。缺行就是缺省值，种了以后改缺省值要再写迁移改数据。
- 「没变也写一条审计」：会把审计刷满，也会白白失效快照。比较后没变直接返回。
- 只加了 TTL 不接提交后失效：后台保存后页面上看着「没生效」，运营会再点几次。高频读一律接监听方。
- 在 `tx.Do` 里直接调监听方：回滚了快照也被清，而且别的请求会在提交前读到旧值再缓存回去。只用 `tx.AfterCommit`。
- 改值之前先读了一次快照的 PG 用例：`TxCtx` 不跑提交钩子，快照不会失效，用例看到旧值（`panel-conventions.md` 主域名一节写过）。
- 前端上限与后端不一致：表单放行、后端回 422，运营看到的是两套说法。常量旁注明出处，改一处搜另一处。
- 假后端忘了 `requireReauth`：页面在假后端上「不弹框也能保存」，flow-walk 测不出真实流程。
