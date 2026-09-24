# 面板重构 · 接口契约

> 第 2 阶段第 ② 步产物，写于 2026-09-23，代码基线 `feat/panel-redesign-p2 @ 54cec13`。
> 第 3 阶段前后端并行的唯一依据：前端照它写页面，补后端照它实现，合并时以它对账。与 docs/CONSTRAINTS.md 冲突时以铁律为准。
> 文中「后台-05」= `管理后台-05-订单与收款.dc.html`，「门户-03」= `用户门户-03-选购套餐.dc.html`，「外壳」= `管理后台.dc.html` / `用户门户.dc.html`；设计稿在协调会话本机 `~/Desktop/Pandora前端代码-20260923/设计稿/`。
> 行号是基线提交上的位置，代码改动后以函数名为准。

## 0. 怎么读这份契约

### 0.1 状态标记

- **现有**：路由已在 router.go，条目写出处理函数位置，请求/响应字段已按 Go 的 json tag / map key 核对。
- **现有；待补·后端（扩展/改…）**：路由已在，但设计要的字段、参数或行为需要在原接口上补，不另开路由。条目里「响应（现有）」与「响应（待补·后端）」分开写，前端可以先按现有形状做，zod 里把待补字段标为可选。
- **待补·后端**：设计要、后端没有的新接口，形状已定；「需迁移」一行说明是否要新表/改表。
- **待补·前端**：后端有、设计没有，写明补进哪个页面哪个位置。
- **待决**：不属于 6 条保留规则、也不能按「设计优先」直接定的冲突，统一收在第 5 节，编号 `D-<分段>-<n>`，正文里出现的 D-x-n 都指向那里。待决未定之前，前端按条目里写的「未决前」处理，后端不动。

- **分段**：本契约由 6 个分段并行核对后合并，正文里的「本分段」「见核对笔记」指该模块所属分段，核对笔记在第 8 节：后台外壳 / 01 / 09 → A；02 / 03 → B；04 / 05 / 06 → C；07 / 08 → D；门户外壳 / 01–05 → E；门户 06–10 → F。

### 0.2 条目格式

```
#### METHOD v1/path — 用途
- 状态：…
- 权限：perm.code｜reauth：是/否｜幂等：是 scope / 否
- 请求 / 响应 / 错误 / 设计（含「映射：后端 x → 设计 y」）
```

路径一律写相对形式 `v1/...`。通用的 401、429 不在条目里重复。

## 1. 横切约定（两个网关通用，已按代码核实）

### 1.1 路径与部署

- 前端所有请求用相对路径（`v1/...` 相对入口页解析），不得写死 `/v1`：后台部署在 nginx 高熵前缀后面（`panel/deploy/nginx-aegis.conf`，前缀被剥掉再转发）。
- 网关只下发 `/`（入口）与 `/assets/*`，其余非 API 路径 404，不回退入口；前端用 hash 路由。public 网关根下还有订阅通配 `/{prefix}/{token}`，所以**任何两段式前端链接都会撞上它**：邀请链接用 `/?invite=CODE`（不用 `/r/CODE`），快捷登录链接用 `/#/quick-login/<token>`（不用 `/q/<token>`）。
- 入口页 CSP：`default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`（`platform/webapp/webapp.go:42`）。外链图片不显示；不能注入 `<style>`，主题的 custom_css 因此无法按旧方式生效（见 D-E-4）。支付跳转是 GET 顶层导航，不受 form-action 影响（门户-03 支付条目）。

### 1.2 认证与会话

- `Authorization: Bearer <access_token>`，没有 cookie；admin 与 public 令牌互不通用（DomainGuard）。
- 访问令牌默认 720 小时（`AEGIS_ACCESS_TOKEN_TTL`），会话过期同为 720 小时（`AEGIS_REFRESH_TOKEN_TTL`，access ≤ refresh）。**两个网关都没有 refresh 接口**：public 登录虽然返回 `refresh_token`，全仓没有消费方，前端不要保存它；admin 登录不返回 refresh_token。令牌失效即 401，前端清令牌回登录页。
- **401 不一定是会话失效**。以下接口在口令错误时也回 401，前端的全局「401 → 登出」拦截必须排除它们，改为表单内联报错：admin `POST v1/me/password`、`POST v1/auth/reauth`；public `POST v1/me/password`。登录接口本身的 401 也是表单错误。
- 缺权限：admin 的 RequirePermission 回 **404 not_found**（NotFoundOrForbidden），不是 403。前端据 `login`/`GET v1/me` 返回的 `permissions` 预先隐藏入口，页面级 404 当作「无权限或不存在」。
- 改密码：admin 与 public 目前都吊销该用户**全部**会话（含当前）。admin 保持（保留规则 4）；public 按设计改为保留当前会话（门户-10 `POST v1/me/password`）。

### 1.3 请求体

- 所有 JSON 请求体经 `httpx.DecodeJSON`：**DisallowUnknownFields**，多传一个字段就是 400 bad_request；上限 1 MiB。前端 zod 的请求 schema 要与条目逐字段一致，不要带多余字段。少数处理器例外（条目里注明，如 `POST v1/late-payments/{id}/apply-to-balance` 用 json.NewDecoder，不拒绝多余字段、上限 4 KiB）。
- 部分 DELETE 与无参数 POST 必须带 `{}` 请求体，空体回 400（条目里注明，如 `DELETE v1/nodes/{id}`、`DELETE v1/servers/{id}`、`POST v1/support/tickets/{id}/withdraw`）。

### 1.4 错误信封与错误码

```
{"error":{"code":"…","message":"…","fields?":{"字段":"原因"},"request_id?":"…"}}
```

错误码是 `platform/httpx/httpx.go` 的封闭列表：bad_request 400、unauthorized 401、forbidden 403、not_found 404、conflict 409、validation_failed 422（带 fields）、rate_limited 429、idempotency_key_reuse 409、service_unavailable 503、internal_error 500。第 ⑤ 步新增 **reauth_required 403**。

- `message` 是可直接展示的中文；前端按 `code` 分支，按 `fields` 标红表单项。注意 `fields` 的键名不一定等于请求字段名（例：改密码的新密码错误键是 `password` 不是 `new_password`），条目里逐一写明。
- 若干接口对非法 UUID 的路径参数回 500 而不是 404（各分段核对笔记列出）；前端在调用前不必自行校验，把 500 当普通失败处理即可，后端在补接口时顺手修。

### 1.5 幂等
- **修订 R10（2026-09-24，后端二 62f7283）**：`POST v1/nodes/status:batch` 与 `POST v1/nodes/batch/status` 共用幂等 scope `node_status_batch`；同一个 key 换条路径重放回 409 `idempotency_key_reuse`。

- 挂了 `middleware.Idempotency` 的路由（条目里「幂等：是 `scope`」）必须带请求头 `Idempotency-Key`（1–255 字节可见字符），缺失或非法回 400。
- 同一次用户意图的所有重试（网络重试、reauth 后重放）必须复用同一个 key；key 在用户发起动作时生成（UUID v4），动作结束（成功或用户放弃）后丢弃。
- 同 key 不同请求体（方法+路径+query+body 的哈希）→ 409 idempotency_key_reuse；同 key 在途 → 409 conflict「同一请求正在处理」；同 key 同请求 → 原样重放第一次的响应（状态码与响应体都一样）。
- admin 路由的中间件顺序是 RequirePermission → RequireRecentReauth → Idempotency（`reset-password` 与 `rotate` 两条例外，先 reauth 后权限，见 B 段核对笔记），所以 reauth 失败**不消耗**幂等键。

### 1.6 重新验证身份（reauth，仅 admin）
- **修订 R9（2026-09-24，后端二 62f7283）**：挂 reauth 的路由由 40 条增至 45 条（新增用户批量导出 / 生成 / 群发、改用户状态、全局设备模式）；`reset-password`、`rotate` 改为先查权限、后 reauth，原先的例外取消。

- 40 条 admin 路由挂 `RequireRecentReauth`（`router.go` 中 40 处，含 `registerCatalogPlanUpdate` 注册的 `PUT v1/plans/{id}`）；开工说明与 `handlers.go:63` 注释里的「53 条」与代码不符。条目里「reauth：是」即这 40 条，另有若干条标了「待补·后端改为是」。
- 令牌里的 rat 在 15 分钟内即通过。登录时 rat=登录时刻。`GET v1/me` 的 `reauthed` 反映当前状态。
- 现状：未通过回 403 forbidden「此操作需要重新验证身份」，与其它 403 无法区分。第 ⑤ 步改为 403 **reauth_required**。
- 前端流程：先发请求 → 收到 reauth_required → 弹「重新验证身份」框 → `POST v1/auth/reauth {password}` → **用返回的新 access_token 替换本地令牌** → 用原 Idempotency-Key 重放原请求。设计稿是「先弹框后执行」，实现改为「先请求、按需弹框」；`reauthed=true` 时不弹框。

### 1.7 实时事件（SSE）

- admin `GET v1/events`（要 `ops.notification.read`，没有则 404，前端隐藏事件胶囊）与 public `GET v1/events`（登录即可）。Bearer 认证，**只能用 fetch 流读取，不能用 EventSource**。路径以 events 结尾，豁免 25 秒请求超时。
- 帧格式：首帧 `retry: 5000`；事件帧 `id: <本连接内序号>` / `event: <topic>` / `data: <单行 JSON>`；每 25 秒注释帧 `: ping`。id 只在本连接内有意义，不支持 Last-Event-ID 续传：断线重连后把当前页相关查询全部失效重拉。重连间隔取 retry 值并加抖动。
- topic（`platform/realtime/listener.go topicFor`）与 data：表变更事件 data 为 `{table, op: "INSERT"|"UPDATE"|"DELETE", id?}`——`orders.changed`、`subscriptions.changed`、`tickets.changed`、`plans.changed`、`nodes.changed`、`announcements.changed`、`data.changed`（兜底）；工单回复事件 `ticket.updated`，data `{ticket_id}`（只推给工单所有人）。带 user_id 的行只推给本人，其余推全租户；admin 频道收到全部。
- 事件只是「哪块该刷新」的信号，不带业务内容；前端据此让 react-query 失效。设计里带标题正文的可读事件流见 D-A-1。已知放大问题：每次节点上报流量都会给全租户在线用户推一条 `subscriptions.changed`（`quota_balances` 无 user_id），前端对同一 topic 做 2 秒节流；后端修复见第 7 节。

### 1.8 数据约定

- 金额：币种最小单位 int64（分），前端展示 ÷100、输入 ×100 取整；币种只有 `CNY`、`USD`（门户余额固定 CNY）。
- 流量：字节 int64；仪表盘冻结契约（DASH-01）里的 `*_bytes` 是十进制字符串，条目注明。
- 时间：多数为 RFC3339；少数接口返回数据库会话时区的无时区字符串（条目注明）。收入类按租户时区切日，部分计数按数据库时区切日（A 段核对笔记）。
- 列表：各接口分页方式不统一（limit/offset、固定条数、无分页），以条目为准；不存在全局分页约定。
- 空列表：若干接口在无数据时返回 `null` 而不是 `[]`（条目注明），前端 zod 用 `.nullable()` 后归一为空数组。
- 乐观锁：节点、服务器用 `row_version`，公告用 `expected_version`，知识库用 `expected_latest_version`；不符回 409，前端提示「已被他人修改」并重拉。

## 2. 非前端接口（前端不调用，列出以免误用）

| 网关 | 路由 | 用途 |
|---|---|---|
| public | `GET /{prefix}/{token}` | 订阅分发（客户端拉取），`api/public/subscribe.go` |
| public | `GET /pdnd/install.sh`、`/pdnd/bin/{name}`、`/pdnd/sha256/{name}` | NativeCore 一键安装引导，`api/public/pdnd_install.go` |
| public | `GET/POST v1/webhooks/payments/{provider}` | 支付回调（签名鉴权） |
| public | `POST v1/webhooks/telegram/{secret}` | Telegram Bot 更新回调 |
| 两者 | `GET /healthz`、`GET /readyz` | 探针 |
| node | `api/node/router.go` 全部 | 节点 agent 通道，与面板前端无关 |

## 3. admin 网关

### 本网关通用事实

> 本分段的共同事实（引用 COMMON 横切约定之外的补充）：
> - 金额一律是**最小货币单位**的 int64（分/美分），前端自行格式化；币种只有 `CNY`、`USD`。
> - 缺权限统一回 404 not_found（RequirePermission 走 NotFoundOrForbidden），前端应据 `login`/`me` 返回的 `permissions` 预先隐藏入口，并把页面级 404 当作「无权限」。
> - 本分段所有挂 reauth 的路由，中间件顺序都是 RequirePermission → RequireRecentReauth → Idempotency：reauth 失败**不消耗**幂等键，重新认证后必须用**同一个** Idempotency-Key 重放。
> - 处理器里凡是 `httpx.OK` 都是 200，包括新建类接口。

> 代码行号以 feat/panel-redesign-p2 @ 54cec13 为准。handler 文件都在 `panel/internal/api/admin/`，路由在 `router.go`。
> 金额一律为最小货币单位（分，int64）；流量为字节（int64）；时间为 RFC3339。
> 用户、订阅、工单、分组的 ID 都是 uuid。设计稿里的 `#10482`、`#4821` 这类数字编号在后端不存在：用户显示 uuid 前 8 位，工单显示 `ticket_no`（形如 `TK20260923-ABCDEFGH`）。

> 金额一律是币种最小单位整数（分）；前端展示 ÷100，输入 ×100。币种只有 `CNY` / `USD`。
> 文件简写：后台-04 = 管理后台-04-套餐，后台-05 = 管理后台-05-订单与收款（tab：订单 / 欠费单 / 支付渠道 / 收入调整），后台-06 = 管理后台-06-营销（tab：优惠券 / 礼品卡 / 佣金与提现）。
> 状态行里「现有；待补·后端（扩展）」表示路由已在，但设计要的字段 / 参数需要后端在原接口上补，不另开新路由。

> 路由全部定义在 `panel/internal/api/admin/router.go`（节点/服务器块 584–690，节点池 497–504，公告/知识库 522–558，外观 182–204）。
> 本分段通用事实（不再逐条重复）：
> - 节点/服务器/公告/知识库的写接口普遍带乐观锁：节点、服务器用 `row_version`（int64），公告用 `expected_version`，知识库用 `expected_latest_version` / `expected_version`。版本不符一律 409 conflict，节点/服务器的 409 带 `fields.row_version = "current=N"`。**每次成功写入后行版本 +1，前端必须用响应或重新拉取的新版本号继续操作。**
> - 所有 DELETE 路由里凡是调用了 `httpx.DecodeJSON` 的（`DELETE v1/nodes/{id}`、`DELETE v1/servers/{id}`），**必须带 JSON 请求体**（至少 `{}`），空体会 400 bad_request。
> - 路径里的 `{id}` 多数处理器先做 UUID 校验回 404；未做校验的在「核对笔记」列出（非法 UUID 会变成 500）。
> - 「节点状态」有两套：生命周期 `status`（NODE-010，draft/provisioning/…/active/draining/maintenance/retired/destroyed，由 DB 触发器 `node_transitions` 强制）与服务状态 `serving_status`（draft/active/draining/disabled/retired，Go 内 `servingTransitions` 强制）。设计稿只有一套「在线/离线/已停用/已退役」，映射见 GET v1/nodes。

### 后台外壳（管理后台.dc.html：登录、侧栏、顶栏、⌘K、二次认证、改自己密码、身份展示）

#### POST v1/auth/login — 管理员登录
- 状态：现有 `panel/internal/api/admin/handlers.go:100 login`
- 权限：匿名（admin 域；无任何角色的账号口令正确也拒）｜reauth：否｜幂等：否。限流：`adm_auth` 按路由 `AEGIS_RATE_LIMIT_AUTH_PER_MINUTE`/分钟，另有网关级 `adm_ip` 240/分钟
- 请求：`{ email: string, password: string }`
- 响应：200 `{ access_token: string, token_type: "Bearer", expires_in: int(秒，= AEGIS_ACCESS_TOKEN_TTL，默认 2592000), user_id: uuid, permissions: string[] }`。**不返回 refresh_token**（库里建了，但 admin 不下发）；令牌的 rat=登录时刻，所以登录后 15 分钟内天然处于「已二次认证」状态
- 错误：401 unauthorized「邮箱或密码不正确」（用户不存在/口令错/账号非 active/无角色，四种一致）；400 bad_request（请求体多字段）
- 设计：后台外壳登录页（邮箱+密码+错误行）。登录页上的版本号「r55」由前端构建时注入，不走接口

#### POST v1/auth/logout — 退出当前会话
- 状态：现有 `handlers.go:133 logout`
- 权限：登录即可（RequireAuth）｜reauth：否｜幂等：否
- 请求：无 body
- 响应：204
- 错误：无特有
- 设计：侧栏账户菜单「退出登录」。成功后前端清令牌回登录页

#### GET v1/me — 当前管理员身份
- 状态：现有 `handlers.go:146 me`；**扩展字段 待补·后端**
- 权限：登录即可｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ user_id: uuid, kind: "user", permissions: string[] | null, reauthed: bool }`。`permissions` 在角色被全部撤销后会是 `null`；`kind` 恒为 `"user"`（admin 令牌也签 `knd=user`），前端不要用它判断身份
- 响应（待补·后端，追加字段，不改已有字段）：`email: string, display_name: string | null, roles: [{ code: string, name: string }]`。来源 `users.email/display_name` 与 `role_bindings→roles`（与 Login 相同的 tenant 级、未过期绑定过滤）。需迁移：否
- 错误：无特有
- 设计：侧栏底部账户块「运维 · 林舟 / linzhou@…」、头像首字。映射：角色标签 ← `roles[0].name`；姓名 ← `display_name ?? email 的 local 部分`；头像字 ← 角色名首字（设计即如此）

#### POST v1/me/password — 修改自己的密码
- 状态：现有 `handlers.go:159 changePassword`；**新密码长度规则 待补·后端**
- 权限：登录即可｜reauth：否｜幂等：否。**没有**认证类限流（只有网关级 240/分钟），与 reauth 不对称，见核对笔记
- 请求：`{ old_password: string, new_password: string }`
- 响应：200 `{ ok: true, reauthenticate: true }`。同一事务内吊销该用户**全部**会话与 refresh 令牌（含当前）——保留规则 4：前端收到 200 立即清令牌回登录页，不要再发任何请求（下一个请求必然 401）
- 错误：422 validation_failed `fields.old_password/new_password`（任一为空时两个键**都会**出现）；401 unauthorized「当前密码不正确」——**这个 401 不是会话过期**，前端在此接口上不得触发全局登出；400 bad_request「新密码不能与当前密码相同」；422 `fields.password`（注意键名是 `password` 不是 `new_password`）「密码至少需要 8 个字符 / 密码必须同时包含字母和数字 / 密码过长」
- 待补·后端：admin 域新密码最少 **12** 个字符（仍要求字母+数字、≤256 字节），错误仍走 `fields.password`；门户侧规则不变。需迁移：否
- 设计：账户菜单「修改我的密码」对话框（当前密码、新密码、4 段强度条、「至少 12 位」）。映射：设计文案「修改后其他会话会被登出」→ 改为「修改后所有会话（包括当前）都会登出，需要重新登录」（保留规则 4）；成功 toast 后直接跳登录页

#### POST v1/auth/reauth — 二次认证（换发 rat 刷新的令牌）
- 状态：现有 `handlers.go:71 reauth`
- 权限：登录即可（刻意不挂 RequireRecentReauth）｜reauth：否｜幂等：否。限流：`adm_reauth` 按账号 + `adm_reauth_ip` 按 IP，各 `AEGIS_RATE_LIMIT_AUTH_PER_MINUTE`/分钟
- 请求：`{ password: string }`
- 响应：200 `{ access_token: string, token_type: "Bearer", expires_in: int }`。同一会话、不轮换 refresh；新令牌 rat=now、exp=now+TTL（顺带把访问令牌寿命向后推，但不超过会话 `expires_at`，中间件每次请求都查会话）。**前端必须用新令牌替换本地令牌**，然后以原 Idempotency-Key 重放被拦下的请求
- 错误：422 `fields.password`「请输入当前密码」；401 unauthorized「密码不正确」——对话框内联显示，不触发全局登出；401「会话已失效，请重新登录」只在会话恰好被吊销的竞态下出现（正常情况下中间件先挡），前端无法按 code 区分这两种 401，按「对话框内联报错；若随后任意请求 401 再全局登出」处理；400 bad_request「缺少会话信息，请重新登录」
- 设计：外壳全局确认框 `ask({reauth:true})`（「敏感操作 · 需要重新认证」+ 当前登录密码）。映射：设计是「先弹框后执行」，实现改为「先发请求，收到 reauth 错误再弹框、认证后重放」；已知必定需要 reauth 的动作（本分段 5 条 + 全表 40 条，见核对笔记）也可以预先弹框，但只要 `GET v1/me.reauthed` 为 true 就跳过密码输入。reauth 错误码当前是 403 forbidden「此操作需要重新验证身份」，第 ⑤ 步改为 403 `reauth_required`

#### GET v1/events — 管理端实时事件流（SSE）
- 状态：现有 `panel/internal/api/admin/events.go:26 events`；新增 topic `switches.changed` 待补·后端（见降级开关）
- 权限：`ops.notification.read`｜reauth：否｜幂等：否。路径以 `events` 结尾，豁免 25 秒请求超时
- 请求：无；必须用 fetch 流读取（Bearer 头，不能用 EventSource）
- 响应：200 `text/event-stream`。首帧 `retry: 5000`；事件帧 `id: <本连接内递增序号>\nevent: <topic>\ndata: {"table": string, "op": "INSERT"|"UPDATE"|"DELETE", "id"?: string}\n\n`；每 25 秒一个注释帧 `: ping`。id 只在本连接内有意义，**不支持 Last-Event-ID 续传**——断线重连后前端要把当前页相关查询全部失效重拉。topic 与来源表（`platform/realtime/listener.go:43 topicFor`；触发器表见迁移 00021/00022）：`orders.changed`←orders；`subscriptions.changed`←subscriptions/subscription_credentials/quota_balances；`tickets.changed`←tickets；`plans.changed`←plans/plan_versions/prices；`nodes.changed`←nodes；`announcements.changed`←announcements；`data.changed` 兜底。admin 频道收到租户内全部上述变更
- 错误：500 internal_error（实时推送未启用 / 响应不支持流式）
- 设计：顶栏「实时事件」胶囊（脉冲点、未读数）与下拉列表（标题、正文、相对时间、点击跳转模块）。映射：事件只携带「哪张表哪一行变了」，**没有标题/正文/金额**；设计里的「新用户注册、提现申请、礼品卡兑换、节点负载告警」来源表（users、withdrawals、gift card 使用、节点指标）根本不在监听列表里。可读事件流见待决 D-A-1；在 D-A-1 定下之前前端按 topic+op 生成通用条目（如「订单变更 · 新建」），点击跳到对应模块，并用事件触发 react-query 失效。无 `ops.notification.read` 的管理员此接口 404，前端隐藏胶囊

> ⌘K 命令面板、主题切换、「打开用户门户 ↗」（同域，链接 `../`）均为纯前端，无接口。侧栏徽标（工单 7、营销 3）取自 `GET v1/dashboard/tasks`。

### 后台-01 仪表盘（管理后台-01-仪表盘.dc.html；收入调整见后台-05）

#### GET v1/dashboard/tasks — 「需要处理」卡片与侧栏徽标的计数
- 状态：待补·后端
- 权限：新权限：`ops.dashboard.read`（路由层声明，满足 IAM-009）；处理器内再按各条目的原读权限逐条过滤，没有对应权限的条目直接不出现：tickets_open→`ops.ticket.read`、withdrawals_pending→`billing.order.read`（与 GET v1/withdrawals 一致）、nodes_offline→`node.read`、orders_pending_stale→`billing.order.read`、notifications_backlog→`ops.notification.read`、ledger_drift→`billing.ledger.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ as_of: rfc3339, items: Item[] }`，`Item` 按 `kind` 区分：
  - `{ kind: "tickets_open", count: int, high_priority: int, oldest_wait_seconds: int | null }`（status ∈ open/pending_agent/escalated；high_priority = priority ∈ high/urgent）
  - `{ kind: "withdrawals_pending", count: int, amounts: [{ currency: string, amount: int }] }`（status ∈ requested/reviewing，按币种合计）
  - `{ kind: "nodes_offline", count: int, sample: [{ id: uuid, name: string }] (≤3), longest_offline_seconds: int | null }`（口径与 GET v1/nodes 的 `stale` 一致：非退役、心跳超过 90 秒）
  - `{ kind: "orders_pending_stale", count: int, threshold_seconds: 1800 }`（pending_payment 且创建超过 30 分钟仍未被过期作业处理）
  - `{ kind: "notifications_backlog", queued: int, failed_total: int, backlog_state: "clear" | "backlogged" }`（与 dashboard/backlog/notifications 同源）
  - `{ kind: "ledger_drift", count: int }`（= overview.ledger_drift_accounts）
- 错误：无特有
- 需迁移：权限字典新增 `ops.dashboard.read` 并授予现有内置管理角色（数据迁移，无表结构变更）
- 设计：后台-01「需要处理」5 张卡（待处理工单、提现待审核、离线节点、超时未支付订单、邮件投递积压）；外壳侧栏「工单」「营销」徽标。映射：卡片副标题「最久已等 3 小时 · 2 个高优先级」←oldest_wait_seconds/high_priority；「合计 ¥1,280.00」←amounts；「JP-TYO-03 等 · 最长 26 分钟」←sample/longest_offline_seconds；「邮件投递积压」改称「通知投递积压」（该计数含全部渠道）。待补·前端：ledger_drift count>0 时追加一张红色卡「账本漂移」（设计没有，但它是必须立刻查的严重信号），点击去订单与收款

#### GET v1/overview — 经营总览
- 状态：现有 `handlers.go:198 overview`（数据 `domain/adminops/service.go:127 Overview`）；**扩展字段 待补·后端**
- 权限：`billing.ledger.read`｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ users: { total, active, today, last_7_days }, subscriptions: { active, trialing, expiring_7_days, expired }, revenue: [{ currency, today, last_7_days, last_30_days, total, actual_today, actual_7_days, actual_30_days, actual_total, adjustment_today, adjustment_7_days, adjustment_30_days, adjustment_total }]（恒为 CNY、USD 两行，展示值 = actual + adjustment，按租户时区切日）, orders: { paid_today, pending, failed_today }, ledger_drift_accounts: int }`，数值全是 int
- 响应（待补·后端，追加）：`revenue[].yesterday: int` 与 `revenue[].actual_yesterday: int`（租户时区昨日，含/不含调整）；`subscriptions.new_7_days: int`（近 7 天新建且当前 active/trialing）；`nodes: { total: int, online: int }`（非退役节点；online = 心跳 ≤90 秒，与 GET v1/nodes 的 stale 取反一致）。需迁移：否
- 错误：无特有
- 设计：后台-01「经营」4 格 KPI。映射：「收入 · CNY / USD」←`revenue[currency].today`，「较昨日 ±x%」←today 与 yesterday；「有效订阅」←`subscriptions.active`（trialing 放 tooltip），「本周新增」←new_7_days；「流量 18.6 TB」取 `GET v1/dashboard/traffic/nodes?range=24h` 的 `totals.reported_bytes`（标签改「近 24 小时」），「41 / 44 节点在线」←`nodes.online/total`。待补·前端：KPI 的 tooltip 或副行里标出 `adjustment_today`（有调账时展示「含调整 ±x」），`subscriptions.expiring_7_days`、`orders.pending` 在「需要处理」之外无处展示，放进「有效订阅」「收入」卡的 tooltip

#### GET v1/revenue/timeseries — 收入趋势
- 状态：现有 `panel/internal/api/admin/revenue.go:14 revenueTimeseries`；**上一区间合计 待补·后端**
- 权限：`billing.ledger.read`｜reauth：否｜幂等：否
- 请求：query `currency: "CNY" | "USD"`（必填，大小写不敏感）、`days?: 7 | 30 | 90`（缺省或非数字按 30）
- 响应（现有）：200 `{ currency: string, days: int, points: [{ date: "YYYY-MM-DD", actual_credit: int, actual_debit: int, adjustment: int, displayed_net: int }] }`，points 按日补齐、升序，按租户时区
- 响应（待补·后端，追加）：`previous_total: int`（紧邻的前一个等长区间 displayed_net 合计）。需迁移：否
- 错误：422 `fields.currency`「仅支持 CNY 或 USD」（缺 currency 也是它）；422 `fields.days`「仅支持 7、30 或 90 天」
- 设计：后台-01「收入趋势」（CNY/USD 分段、7/30/90 天分段、区间合计、日均、较上一区间、柱图）。映射：柱高←displayed_net；区间合计/日均前端求和；较上一区间←(合计−previous_total)/previous_total；柱 tooltip 可展示 actual 与 adjustment 拆分

#### GET v1/dashboard/traffic/nodes — 节点流量排行
- 状态：现有 `panel/internal/api/admin/dashboard.go:27 dashboardNodeTraffic`（DASH-01 冻结契约）
- 权限：`metering.read` + `node.read`｜reauth：否｜幂等：否
- 请求：query `range?: "24h" | "7d" | "30d"`（默认 7d）、`limit?: 5 | 10 | 20`（默认 10）、`snapshot_at?: RFC3339 UTC`（31 天内，默认现在）
- 响应：200 `{ range, snapshot_at, from, to（均为 "YYYY-MM-DDTHH:MM:SS.ffffffZ"）, basis: "strict_raw_report_entries", items: [{ node_id, name, display_name: string|null, upload_bytes, download_bytes, total_bytes, contributing_entry_count: int, report_count: int, last_report_at }], totals: { reported_bytes, attributed_bytes, unattributed_bytes }, ranking: { returned_bytes, other_node_bytes }, quality: { duplicate_report_count, invalid_report_count, invalid_entry_count } }`。**所有 *_bytes 都是十进制字符串**（numeric），前端用 BigInt 或确认 < 2^53 后转 number
- 错误：422 validation_failed（range/limit/snapshot_at 非法、snapshot_at 超范围；无 fields）
- 设计：后台-01「流量排行」节点页签（近 24 小时、前 5）。映射：请求 `range=24h&limit=5`；名称←`display_name ?? name`；设计里「· VLESS Reality」协议后缀 DTO 没有，前端从已缓存的 GET v1/nodes 按 node_id 取 `node_type` 拼接（取不到就不显示）。待补·前端：卡片底部一行小字展示 `totals.unattributed_bytes` 与 quality 计数非零时的提示

#### GET v1/dashboard/traffic/users — 用户流量排行
- 状态：现有 `dashboard.go:41 dashboardUserTraffic`（DASH-01 冻结契约）
- 权限：`metering.read` + `iam.user.read`｜reauth：否｜幂等：否
- 请求：同上；带 `identity` 参数（任何值）直接 422
- 响应：200 同上结构，items 为 `[{ user_id, email_masked: string（"a***@example.com" 或 "***"）, upload_bytes, download_bytes, total_bytes, subscription_count: int, contributing_entry_count: int, last_report_at }]`，`ranking.other_user_bytes`
- 错误：422 validation_failed（含 identity 参数）
- 设计：后台-01「流量排行」用户页签。冲突：设计显示完整邮箱，后端冻结契约只给脱敏邮箱且明文禁止 UI 用其它已加载对象补回——见待决 D-A-2；未决前显示 `email_masked`，行点击跳用户详情（按 user_id）

#### GET v1/dashboard/backlog/notifications — 通知投递积压
- 状态：现有 `dashboard.go:59 dashboardNotificationBacklog`（DASH-01 冻结契约）
- 权限：`ops.notification.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ as_of, backlog_state: "clear" | "backlogged", processor_state: "unobservable", scanner_interval_seconds: 300, counts: { ready, ready_retry, scheduled, scheduled_retry, sending_unobservable, failed_total, suppressed_total, bounced_total }, oldest_ready_at: string | null, max_ready_lag_seconds: int, last_sent_at: string | null, assessment: { threshold_seconds: 600, reason: "no_due_backlog" | "within_threshold" | "lag_exceeded" } }`。统计**不分渠道**（邮件、Telegram、站内信合计）
- 错误：无特有
- 设计：后台-01「邮件投递积压」卡与「系统状态 · 邮件投递」行；后台-09 SMTP 卡状态「已连接 · 重试 3 封」。映射：积压数←ready+scheduled，「重试中」←ready_retry+scheduled_retry，「失败」←failed_total，颜色←backlog_state。分渠道数字由 GET v1/system/status 的 components 提供（不改冻结 DTO）

#### GET v1/system/status — 系统状态
- 状态：现有 `panel/internal/api/admin/system_status.go:36 systemStatus`；**components 待补·后端**；**backup 部分 待补·前端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ backup: { dir: string, readable: bool, message?: string（不可读时）, count?: int, total_bytes?: int, latest?: { name, size, created_at, has_checksum }, latest_age_hours?: int, stale?: bool(>48h 或一份都没有), missing_checksum?: int, recent?: BackupFile[≤5], identity_configured?: bool, identity_hint?: string, offsite_configured?: bool }, database: { size_bytes: int, connections: int, max_connections: int } | { error: string } }`
- 响应（待补·后端，追加）：`state: "ok" | "degraded"`，`components: Component[]`，`Component = { key: "postgres" | "valkey" | "node_fabric" | "payment_callbacks" | "mail" | "telegram" | "sse" | "backup", state: "ok" | "warn" | "down" | "unknown", latency_ms?: number, metrics: object, message?: string }`，各 key 的 metrics：postgres `{ size_bytes, connections, max_connections }`（latency=一次 `SELECT 1` 往返）；valkey `{}`（latency=PING）；node_fabric `{ total, online, config_lagging }`（config_lagging = applied_config_version < desired_config_version 的节点数）；payment_callbacks `{ pending }`（payment_webhook_receipts 中 parse_status='unattempted' 且超过 1 分钟）；mail / telegram `{ queued, retrying, failed_total }`（notification_deliveries 按 channel='email'/'telegram'）；sse `{ connections }`（各网关进程每 30 秒把 Hub.Count 写入 Valkey 带 TTL 的键，这里求和；进程内 Hub.Count 只看得到本进程）；backup `{ latest_age_hours, stale, identity_configured, offsite_configured }`（由现有 backup 派生，warn 条件：stale 或 identity 未配或 offsite 未配或 missing_checksum>0）。需迁移：否
- 错误：无特有（数据库读失败时 `database.error`，不报错）
- 设计：后台-01「系统状态」卡（总状态胶囊「全部正常/n 项降级」+ 7 行组件：PostgreSQL 主库、Redis、NativeCore 调度、支付回调队列、邮件投递 · SMTP、Telegram、SSE 推送）。映射：「NativeCore 调度 r53」→ node_fabric 行，meta 显示「在线 x/y · z 个节点配置未同步」（版本号不由后端提供）；「Redis」→ valkey。待补·前端：卡片末尾加第 8 行「数据库备份」（meta：最近一份 x 小时前 / 过期 / 未配解密私钥 / 未配异地），点击打开抽屉展示 `backup` 全部字段与 `recent` 列表、`message`/`identity_hint` 原文；PostgreSQL 行 meta 追加库大小与连接数

#### GET v1/stats/timeseries — 注册与活跃（按天）
- 状态：现有 `panel/internal/api/admin/profile.go:228 statsTimeseries`；**active_users 待补·后端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：query `days?: int(1–90，默认 14，非法按 14)`
- 响应（现有）：200 `{ points: [{ day: "MM-DD", registered: int, logins: int, orders: int, unique_ips: int }] }`，按数据库会话时区切日（与收入的租户时区不一致，见核对笔记）
- 响应（待补·后端，追加）：每点 `active_users: int`（当日有成功订阅拉取或有流量归属的去重用户数）。需迁移：否
- 错误：无特有
- 设计：后台-01「注册与活跃 · 近 14 天」双柱 +「14 日注册」「日活均值」。映射：注册柱←registered，活跃柱←active_users；两个合计前端计算。待补·前端：tooltip 附 logins、orders、unique_ips

### 后台-02 工单

**状态映射（以后端为准，前端做映射）**
后端 `tickets.status` 有 6 个值（`migrations/00008_ops_marketing.sql:280`）：`open`、`pending_user`、`pending_agent`、`escalated`、`resolved`、`closed`。客服能直接设置的只有 `resolved`、`closed`、`escalated`、`pending_agent`（`panel/internal/domain/support/service.go:1002 agentSettableStatus`）。
| 后端 | 设计标签 / 色 | 能否在「状态」下拉里设置 |
|---|---|---|
| open | 待处理 open | 否（新建时的初始状态），下拉项置灰 |
| pending_user | 等待用户 pending | 否（客服发出非内部回复后自动进入），下拉项置灰 |
| pending_agent | 处理中 progress | 是 |
| escalated | 处理中 progress，并显示「已升级」徽标 | 只能通过「升级」按钮设置，不在下拉里出现 |
| resolved | 已解决 closed | 是 |
| closed | 「已关闭」，设计稿没有这一项，沿用 closed 的绿色 | 是（待补·前端：下拉加「已关闭」） |
「已升级 · L2」徽标以 `escalated_at != null` 为准，不看 status。工单升级后被回复会离开 escalated 状态，但 `escalated_at` 保留，所以徽标仍然显示。
优先级：后端有 `low`、`normal`、`high`、`urgent` 四档，设计稿只有 low、mid、high 三档。映射为 low→low（灰），normal→mid（橙），high→high（红），urgent→high（红，可以加粗）。
分类：后端是封闭枚举（`service.go:76 Categories`），前端写死中文标签：general 一般咨询、billing 账单与支付、subscription 订阅与套餐、technical 连接与技术、account 账号与安全、abuse 举报与投诉。设计稿里「线路质量」「客户端」这类自由文本分类不采用。
等待时长：前端用 `now − last_reply_at` 计算。`last_reply_at` 是所有消息里最晚的时间，包括 system 消息和内部备注，只是近似值。

#### GET v1/tickets/assignees — 可指派客服目录
- 状态：现有 `panel/internal/api/admin/handlers.go:547 ticketAssignees`
- 权限：`ops.ticket.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ assignees: [{ id: uuid, email: string, display_name?: string }] }`。只包含状态为 active、并且在有效角色里持有 `ops.ticket.write` 的账号
- 错误：无特有错误
- 设计：后台-02 详情头的「指派」下拉。映射：选项文字用 `display_name ?? email`，选项值用 `id`；「未指派」对应空串

#### GET v1/tickets — 工单队列
- 状态：现有 `handlers.go:557 ticketQueue`；待补·后端（字段与筛选，见下）
- 权限：`ops.ticket.read`｜reauth：否｜幂等：否
- 请求（现有）：query `status?: string`（单值等值匹配，不校验取值，传未知值只会得到空结果），`priority?: low|normal|high|urgent`，`category?: string`，`assigned_to?: uuid`，`q?: string`（对 ticket_no、subject、用户邮箱做 ILIKE 模糊匹配），`breached?: "1"`，`limit?: int`（默认 25，最大 100），`offset?: int`
- 响应（现有）：200 `{ tickets: [Ticket], total: int }`。Ticket 结构为 `{ id, ticket_no, subject, category, priority, status, created_at, updated_at, resolved_at: time|null, user_id, user_email, assigned_to?: uuid, assignee_email?: string, sla_first_response_due?, sla_resolution_due?, first_responded_at?, escalated_at?, sla_breached?: bool, message_count: int, last_reply_at }`。带 `?` 的字段为 null 或 false 时会被 omitempty 省略。排序规则：escalated 排最前，然后按优先级从 urgent 到 low，再按 created_at 升序
- 待补·后端（需迁移：否）：
  1. `status` 改为接受逗号分隔的多个值（例如 `open,pending_agent,escalated`），用于实现设计稿的「未解决」和「我的」筛选
  2. 每条工单增加 `last_message_author_kind: "user"|"agent"|"system"`，取最后一条非内部备注消息的作者类型，供前端判断未读加粗：值为 user 就加粗。这样可以避免为每个客服单独建一张已读表
- 错误：无特有错误
- 设计：后台-02 左侧列表与分段筛选。映射如下：
  - 「未解决」→ `status=open,pending_user,pending_agent,escalated`
  - 「待处理」→ `status=open,pending_agent,escalated`，即需要客服动作的工单。设计稿只筛 open，但后端的「待客服处理」语义包含这三个状态
  - 「我的」→ `assigned_to=<GET v1/me 返回的 user_id>` 加上「未解决」那组状态
  - 「全部」→ 不带任何筛选
  - 列表项的 `pri` 圆点来自 priority；等待时长按上面的等待时长规则计算；`unread` 来自上面的 `last_message_author_kind`
  - 待补·前端：`sla_breached` 为 true 时，等待时长显示为红色并加「SLA 超时」标记；分段筛选新增「SLA 超时」（对应 `breached=1`）

#### GET v1/tickets/{id} — 工单详情（含内部备注）
- 状态：现有 `handlers.go:577 ticketDetail`；待补·后端（字段）
- 权限：`ops.ticket.read`｜reauth：否｜幂等：否
- 请求：path `id: uuid`
- 响应：200，结构是 Ticket 加 `messages: [{ id, author_kind: user|agent|system, author_name: string|null, body, internal_note?: true, created_at }]`，消息按时间升序
- 待补·后端（需迁移：否）：增加 `user_active_plan: string|null`，口径与用户列表的 `active_plan` 一致，取 status 为 active 或 trialing 的最新订阅的套餐名。设计稿的详情头要显示用户套餐，而客服角色不一定有 `iam.user.read` 权限，不能再去调用户接口
- 错误：not_found 404「工单不存在」（用的是 CodeNotFound，不是 NotFoundOrForbidden）
- 设计：后台-02 右侧详情。映射：
  - `who` 取 `author_name ?? (author_kind==='user' ? user_email : '客服')`
  - `me` 为 `author_kind==='agent'`
  - 待补·前端：`author_kind==='system'` 的消息（状态变更、SLA 自动升级）居中显示为灰色小字；`internal_note` 为 true 的消息用黄色虚线气泡，并标注「仅内部可见」
  - 待补·前端：详情头显示 `sla_first_response_due`、`sla_resolution_due` 和 `first_responded_at`，形式为倒计时或「已超时」
  - 「查看用户」直接打开用户抽屉，参数为 `user_id`。这需要 `iam.user.read` 权限，没有权限时按钮置灰

#### POST v1/tickets/{id}/reply — 客服回复 / 内部备注
- 状态：现有 `handlers.go:592 ticketReply`
- 权限：`ops.ticket.write`｜reauth：否｜幂等：是 `admin_ticket_reply`
- 请求：`{ body: string(1–5000，trim 后计数), internal_note?: bool }`
- 响应：200 `{ ok: true }`。副作用如下：
  - 非内部回复会把状态改为 `pending_user`，同时清空 resolved_at 和 closed_at；如果是第一次回复，还会记下 first_responded_at。对 resolved 或 closed 的工单回复同样会把它重新打开
  - 内部备注不改状态，也不计入首次响应
  - 回复成功后会实时推送 `ticket.updated` 事件给工单所属用户
- 错误：validation_failed 422 `fields.body`；not_found 404
- 设计：后台-02 回复框的「发送回复」和 ⌘↵。「回复并解决」由前端串行完成：先调 reply（幂等键 A），再调 `status=resolved`（幂等键 B），两次请求用不同的幂等键。待补·前端：回复框加一个「内部备注」开关，开启时传 `internal_note: true`，按钮文案改为「添加备注」

#### POST v1/tickets/{id}/assign — 指派 / 取消指派
- 状态：现有 `handlers.go:633 ticketAssign`
- 权限：`ops.ticket.write`｜reauth：否｜幂等：是 `admin_ticket_assign`
- 请求：`{ assigned_to: uuid | "" }`，传空串表示取消指派
- 响应：200 `{ ok: true }`
- 错误：validation_failed 422 `fields.assigned_to`，目标账号没有 `ops.ticket.write` 或未激活时触发；not_found 404。传非 uuid 字符串时推测会返回 500（INFERENCE：SQL 里有 uuid 比较，handler 没有做格式校验）
- 设计：后台-02「指派」下拉

#### POST v1/tickets/{id}/status — 改状态（含人工升级）
- 状态：现有 `handlers.go:659 ticketStatus`；待补·后端（行为）
- 权限：`ops.ticket.write`｜reauth：否｜幂等：是 `admin_ticket_status`
- 请求：`{ status: "resolved"|"closed"|"escalated"|"pending_agent", reason?: string(≤500) }`
- 响应：200 `{ ok: true }`。副作用：写入一条 system 消息「客服将工单状态改为 X：reason」；设为 escalated 时，如果 escalated_at 为空则记录当前时间
- 待补·后端（需迁移：否）：设为 `escalated` 时，把 priority 提升到至少 `high`。设计稿要求「升级后标记为高优先级」，而现有的人工升级只改状态、不改优先级；SLA 自动升级会逐级提一档，这里与之保持一致
- 错误：validation_failed 422，`fields.status`（取值不在允许范围内）或 `fields.reason`（超过 500 字）；not_found 404
- 设计：后台-02「状态」下拉（映射见本节开头的状态映射表）；「升级到 L2」按钮调用本接口，传 `status=escalated`，按钮在 `escalated_at != null` 时禁用。设计稿里「通知二线值班」一项见 D-B-6

#### POST v1/tickets/escalate — 立即执行 SLA 超时扫描
- 状态：现有 `handlers.go:682 ticketEscalate`；待补·前端（→ 补进后台-02 列表顶部筛选区右侧，作为「检查 SLA 超时」次级按钮）
- 权限：`ops.ticket.write`｜reauth：否｜幂等：是 `admin_ticket_escalate`
- 请求：无 body（请求体为空；带 `{}` 也能通过解码，因为 handler 根本不解码）
- 响应：200 `{ escalated: int }`。扫描所有首次响应已超时、尚未回复、且不在 resolved、closed、escalated 状态的工单，把它们置为 escalated，优先级提一档，并写入 system 消息。后台任务每 5 分钟也会自动跑一次
- 错误：无特有错误
- 设计：设计稿没有这个动作。注意它是租户级的批量扫描，不是单张工单的升级，不能用来实现「升级到 L2」按钮

#### GET v1/ticket-macros — 快捷回复列表
- 状态：待补·后端（需迁移：新表 `ticket_macros`）
- 权限：`ops.ticket.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ macros: [{ id: uuid, title: string, body: string, sort_order: int, updated_at }] }`，按 sort_order、created_at 排序
- 错误：无特有错误
- 设计：后台-02 回复框上方的快捷回复标签（`macros`）。点击后把 body 填进回复框，不会自动发送。路径不采用 `v1/tickets/macros`，以免和 `tickets/{id}` 在语义上混淆（chi 本身会优先匹配静态段），沿用 `v1/user-groups` 这种独立资源的风格

#### POST v1/ticket-macros — 新建快捷回复
- 状态：待补·后端（需迁移：同上）
- 权限：`ops.ticket.write`｜reauth：否｜幂等：否（沿用 user-groups 配置类写操作的惯例）
- 请求：`{ title: string(1–20), body: string(1–5000), sort_order?: int }`
- 响应：200 `{ id: uuid }`（沿用 saveUserGroup 的写法）
- 错误：validation_failed 422 `fields.title|body`
- 设计：设计稿没有管理入口。待补·前端：在快捷回复标签行末尾加「管理」按钮，打开一个小对话框，在里面列出、新建、编辑、删除快捷回复
- 表结构：`ticket_macros(id uuid pk default uuidv7(), tenant_id uuid not null → tenants, title text not null, body text not null, sort_order int not null default 0, created_by uuid → users, created_at, updated_at)`，启用 RLS，写操作审计动作为 `ticket_macro.saved` 和 `ticket_macro.deleted`

#### POST v1/ticket-macros/{id} — 编辑快捷回复
- 状态：待补·后端（需迁移：同上）
- 权限：`ops.ticket.write`｜reauth：否｜幂等：否
- 请求：与新建相同
- 响应：200 `{ id: uuid }`
- 错误：not_found 404（NotFoundOrForbidden）；validation_failed 422
- 设计：同上，入口在「管理」对话框里

#### DELETE v1/ticket-macros/{id} — 删除快捷回复
- 状态：待补·后端（需迁移：同上）
- 权限：`ops.ticket.write`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ ok: true }`
- 错误：not_found 404
- 设计：同上，入口在「管理」对话框里

### 后台-03 用户

**用户状态映射**
后端 `users.status` 有 6 个值（`migrations/00002_identity.sql:23`）：`pending`、`active`、`suspended`、`banned`、`deletion_scheduled`、`anonymized`。设计稿的「已过期」不是账号状态，而是订阅状态，按下面的订阅态口径处理。
| 后端 | 设计标签 |
|---|---|
| active | 正常 |
| suspended、banned | 已禁用（banned 在详情页另外标注「封禁」） |
| pending | 待验证（设计稿没有，灰色） |
| deletion_scheduled | 注销中（设计稿没有，灰色） |
| anonymized | 已匿名（设计稿没有，灰色） |

**订阅态口径**（本分段的待补项共用这一定义）
- 当前订阅：status 为 active 或 trialing、按 created_at 取最新的一条。这与现有 `active_plan` 的口径一致（`panel/internal/domain/adminops/service.go:280`）
- `sub_state=active`：存在当前订阅
- `sub_state=expired`：不存在当前订阅，但存在任何一条状态为 expired、cancelled，或 `current_period_end < now()` 的订阅
- `sub_state=none`：从来没有订阅

**保留规则 2（后台看不到订阅地址）**
设计稿用户抽屉「订阅」tab 里的「订阅地址 + 复制」整块由前端删除，改为一行说明：「订阅地址仅用户本人可见；如疑似泄露，请点『更换订阅地址』后让用户在门户重新复制」。已核实，列表、详情、画像、导出这些接口都不返回令牌或订阅 URL。唯一的例外是换发接口会一次性回传新令牌，见 D-B-1。

#### GET v1/users — 用户列表
- 状态：现有 `handlers.go:211 listUsers` → `adminops/service.go:243 ListUsers`；待补·后端（字段、筛选、缺陷修复）
- 权限：`iam.user.read`｜reauth：否｜幂等：否
- 请求（现有）：query `q?: string`（对 email、display_name 做 LIKE 模糊匹配），`status?: string`（SQL 里用的是 `status::text LIKE $3`，所以只能传单值），`limit?: int`（默认 25，最大 100），`offset?: int`
- 响应（现有）：200 `{ users: [{ id, email, display_name: string|null, status, risk_level: trusted|normal|elevated|high, group_name: string, created_at, last_login_at: time|null, subscription_count: int, active_plan: string|null, balance: int64, currency: string }], total: int }`，按 created_at 倒序
- 待补·后端（需迁移：否）：
  1. **缺陷修复**：列表的 `group_name` 永远是空串。原因是 SELECT 语句和 Scan 都没有这一列（`service.go:274–307`），需要补上 JOIN user_groups
  2. 每条记录增加 `group_id: uuid|null`
  3. 每条记录增加 `current_subscription`，结构为 `{ id: uuid, plan_name, status, current_period_end: time|null, traffic: { limit: int64|null, consumed: int64 }, device_limit: int, online_devices: int } | null`
     - `traffic` 取 quota_balances 里 metric 为 `traffic.bytes` 的那一行
     - `device_limit` 是生效值，计算方式为 `COALESCE(s.device_limit, pv.max_devices, 0)`，0 表示不限
     - `online_devices` 取自 `subscription_online_devices.device_count`
  4. 新增筛选：`group_id?: uuid|"none"`、`sub_state?: active|expired|none`；`status` 改为接受逗号分隔的多值，并换成等值匹配（例如 `suspended,banned`）
  5. `q` 的匹配范围扩展：先按 uuid 精确匹配 `users.id`；再按订阅令牌匹配，用与 `subscription_credentials.token_hash` 相同的哈希函数计算后精确比较（范围包括 active 和 grace 状态的凭据）。这是反查，不会把订阅地址暴露给管理员
- 错误：无特有错误
- 设计：后台-03「用户列表」。映射：
  - 「套餐 · 到期」取 `current_subscription.plan_name` 和 `current_period_end`（前端计算剩余天数）
  - 「本期流量」取 `traffic.consumed / traffic.limit`，limit 为 null 时显示「不限」
  - 「余额」取 `balance/100`，加 currency 符号
  - 「设备」取 `online_devices/device_limit`，device_limit 为 0 时显示「n/不限」
  - 「最近活跃」取 `last_login_at`，标签改为「最近登录」（后端没有更细的活跃时间）
  - 状态分段：「全部」不筛；「正常」→ `status=active`；「已禁用」→ `status=suspended,banned`；「已过期」→ `sub_state=expired`
  - 「全部用户组」下拉 → `group_id`
  - 计数文案「显示 n / 共 total」
  - 待补·前端：底部加分页（limit/offset）

#### GET v1/users/{id} — 用户详情
- 状态：现有 `handlers.go:227 getUser` → `service.go:337 GetUser`；待补·后端（字段）
- 权限：`iam.user.read`｜reauth：否｜幂等：否
- 请求：path `id: uuid`
- 响应（现有）：200，结构是列表行（不含 subscription_count 和 active_plan，这两个字段零值输出；group_name 在这里是有值的）加上：
  - `email_verified: bool`
  - `subscriptions: [{ id, plan_name, plan_version: int, status, current_period_end: time|null, amount: int64, currency, auto_renew: bool }]`，包含全部订阅，按 created_at 倒序
  - `recent_orders: [OrderRow]`，最多 20 条。OrderRow 的字段为 `id, order_no, user_email, kind, status, currency, total_amount, payable_amount, paid_amount, refunded_amount, created_at, paid_at, plan_name, interval, interval_count, item_count`
  - `roles: [string]`
- 待补·后端（需迁移：否）：
  1. `recent_orders` 里的 `plan_name`、`interval`、`interval_count`、`item_count` 现在永远是零值，因为 GetUser 的 SQL 没选这几列（`service.go:389–395`）。需要复用 ListOrders 里取首项的 LATERAL 子查询
  2. `subscriptions[]` 每条增加：
     - `current_period_start`
     - `quotas: [{ metric, limit: int64|null, consumed: int64, remaining: int64|null }]`，与 public `v1/me/subscriptions` 同形
     - `device_limit_override: int|null`
     - `plan_max_devices: int|null`
     - `online_devices: int`
  3. 顶层增加：
     - `group_id: uuid|null`
     - `stats: { paid_total: int64, order_count: int, referral_count: int }`。其中 `paid_total` 的口径与导出一致，即 status 为 paid 或 fulfilled 的订单的 paid_amount 之和；`referral_count` 取 referrals 表里 referrer 为该用户的行数
     - `referrer: { id, email } | null`，来自 referrals 表
     - `telegram: { username, bound_at } | null`，来自 telegram_bindings 表
- 错误：404 NotFoundOrForbidden。传非 uuid 的 id 时推测会返回 500（INFERENCE：没有格式校验，uuid 列和 text 参数比较会报错）
- 设计：后台-03 用户抽屉。映射：
  - 头部的「注册于」取 `created_at`
  - 「画像」tab：`stats` 对应 累计消费、订单数、邀请人数三张卡片；facts 行依次为 余额=`balance`、邀请人=`referrer.email`、最近活跃=`last_login_at`、Telegram=`telegram.username`。「注册 IP」来自画像接口的 `registered_ip`，见下一条
  - 「订阅」tab：取当前订阅，显示 plan、到期时间、`quotas[traffic.bytes]`，以及设备上限的 − / +。设备上限显示值为 `device_limit_override ?? plan_max_devices ?? 不限`
  - 「订单」tab：用 `recent_orders`，映射为 no=`order_no`，what=`plan_name · interval`，amt=`paid_amount||payable_amount`，st=`status`（订单状态标签以分段 C 的映射为准）
  - 待补·前端：
    - 「画像」facts 增加 显示名、邮箱已验证、风险等级、角色
    - 「订阅」tab 列出全部订阅（设计稿只画了一条），每条显示状态、版本、价格、自动续费
    - 「订单」tab 底部加「在订单页查看全部」链接。跳转时需要按用户精确筛选，这依赖分段 C 给 `GET v1/orders` 增加 `user_id` 参数：现有的 listOrders 不支持 user_id，`q` 只对 order_no 和邮箱做 LIKE 模糊匹配，按邮箱搜会误中邮箱相似的其他人（`handlers.go:342`，`service.go:567`）
  - 按保留规则 2，删除订阅地址整块（见上文）

#### POST v1/users/{id}/status — 启用 / 停用 / 封禁
- **修订 R9（2026-09-24，后端二 62f7283）**：reauth 改为「是」。
- 状态：现有 `handlers.go:322 setUserStatus` → `service.go:450 SetUserStatus`
- 权限：`iam.user.write`｜reauth：否（router 注释说要 reauth，代码没挂，设计稿也不要求，契约以代码为准）｜幂等：否
- 请求：`{ status: "active"|"suspended"|"banned", reason: string }`。status 不是 active 时 reason 必填
- 响应：200 `{ ok: true, status }`。停用或封禁时会同时吊销该用户的全部会话和 refresh token
- 错误：
  - bad_request 400：不支持的状态
  - validation_failed 422：停用或封禁时没填原因（这个错误没有 fields）
  - conflict 409：操作对象是自己，或者这次变更会让租户失去最后一个有效管理员
  - forbidden 403：iamguard 的 ErrNotEffectiveAdministrator
  - 404
- 设计：后台-03 抽屉的「禁用账号 / 启用账号」按钮。映射：禁用 → `suspended`，启用 → `active`。待补·前端：禁用确认框加必填的「原因」输入框；另加一个次级的「封禁」选项（`banned`）

#### POST v1/users/{id}/reset-password — 管理员替用户设新密码
- **修订 R9（2026-09-24，后端二 62f7283）**：先查权限、后 reauth（无权限直接 404，不再先要求输密码）。
- 状态：现有 `handlers.go:293 resetUserPassword`
- 权限：`iam.user.write`｜reauth：是（中间件顺序是先检查 reauth、再检查权限）｜幂等：否
- 请求：`{ new_password: string, reason: string(≥5 字) }`
- 响应：200 `{ ok: true, sessions_revoked: true }`，不回显新密码
- 错误：
  - validation_failed 422 `fields.reason`
  - validation_failed 422 `fields.password`：密码不符合策略。注意字段名是 password，不是 new_password
  - bad_request 400：目标是自己
  - 404
- 设计：后台-03 抽屉的「重置密码」。设计稿要的是「发一次性重置链接邮件」，与后端现状冲突，见 D-B-2。在 D-B-2 定下来之前，前端的重认证确认框里需要增加「新密码」和「原因」两个输入框

#### POST v1/subscriptions/{id}/rotate — 管理员换发订阅链接
- **修订 R11（2026-09-24，后端二 62f7283）**：响应改为 `{ user_email, old_revoked: true }`，不再含 `token`（D-B-1）；先查权限、后 reauth。
- 状态：现有 `handlers.go:250 rotateSubscriptionLink` → `subscription/admin_rotate.go:44 AdminRotate`
- 权限：`iam.user.write`｜reauth：是（先检查 reauth、再检查权限）｜幂等：否
- 请求：path `id`，是**订阅** id，不是用户 id；body `{ reason: string(≥5 字) }`
- 响应：200 `{ token: string, user_email: string, old_revoked: true }`。`token` 是新订阅令牌的明文，只返回这一次，见 D-B-1
- 错误：
  - validation_failed 422 `fields.reason`
  - 404：订阅不存在，或 id 不是 uuid
  - internal_error 500：换发已经成功，但审计写入失败
- 设计：后台-03 抽屉的「更换订阅地址」。映射：前端传当前订阅的 id；用户有多条订阅时先弹出选择。待补·前端：确认框加必填的「原因」。按保留规则 2，前端不展示 `token`，toast 提示「已换发，请让用户到门户重新复制订阅地址」

#### POST v1/users/{id}/balance — 人工调账
- 状态：现有 `commission.go:405 adjustBalance` → `billing/topup.go:311 AdjustBalance`
- 权限：`billing.provider.write`｜reauth：是｜幂等：是 `admin_user_balance_adjust`
- 请求：`{ amount: int64（单位为分，可正可负，不能为 0）, currency?: string（默认 CNY）, reason: string(≥5 字) }`
- 响应：200 `{ balance: int64 }`，即调整后的余额
- 错误：
  - validation_failed 422：金额为 0（没有 fields）
  - validation_failed 422 `fields.reason`
  - conflict 409：余额不足，扣减失败
- 设计：后台-03 抽屉的「调整余额」行内表单。映射：设计稿输入的「+50 / -20」是元，前端乘以 100 后传给后端；「调整原因」对应 reason

#### GET v1/users/{id}/profile — 风控画像
- 状态：现有 `profile.go:47 userProfile`；待补·前端（→ 补进用户抽屉，新增「风控」tab，只在持有 `security.audit.read` 时显示）；待补·后端（字段）
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：path `id`
- 响应（现有）：200，结构如下
  - `events: [{ action, outcome, ip, ua, domain, at }]`：最近 80 条，ip 为解密后的明文
  - `ips: [{ ip, count, first, last, accounts: int }]`：最多 20 条
  - `related: [{ id, email }]`：与该用户共用 IP 的其他账号
  - `fetches: [{ ip, ua, family, result, format, at }]`：订阅拉取记录，最近 50 条，ip 和 ua 为明文
  - `fetch_sources_7d: int`
- 待补·后端（需迁移：否）：增加 `registered_ip: string`，取 audit_events 里 action 为 `user.registered` 的那条记录，解密 source_ip_enc；没有记录时为空串。注册 IP 是明文 IP，不放进 `iam.user.read` 就能看到的 GET v1/users/{id}
- 错误：无特有错误。解密失败的 IP 返回空串
- 设计：设计稿抽屉的「画像」tab 其实是资料卡，不包含风控数据。「注册 IP」这一行由前端在持有 `security.audit.read` 时从本接口取值，没有权限时隐藏。风控 tab 的内容为：IP 聚合表（`accounts > 1` 的行高亮）、关联账号列表（可点击跳转到对应用户）、订阅拉取记录，以及用 `fetch_sources_7d` 给出的「疑似分享」提示

#### POST v1/users/{id}/group — 分配用户组
- 状态：现有 `usergroup.go:208 assignUserGroup`
- 权限：`iam.user.write`｜reauth：否｜幂等：否
- 请求：`{ group_id: uuid | "" }`，传空串表示移出分组
- 响应：200 `{ ok: true }`
- 错误：validation_failed 422「用户或分组不存在」（没有 fields）
- 设计：后台-03 抽屉「画像」tab 的「用户组」下拉。映射：选项值为 `group_id`。设计稿的「默认」组在后端对应 `user_group_id = NULL`，新注册用户就是这种情况，后端没有叫「默认」的组。待补·前端：下拉第一项改为「未分组（默认）」，对应传空串

#### GET v1/user-groups — 用户组列表
- 状态：现有 `usergroup.go:41 listUserGroups`
- 权限：`iam.user.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ groups: [{ id, code, name, description: string, users: int, plans: int, prices: int, coupons: int }] }`，按 created_at 排序。plans、prices、coupons 是引用计数，表示分别有多少套餐可见性、专属价格、优惠券限定引用了这个组
- 错误：无特有错误
- 设计：后台-03「用户组」tab。映射：名称=`name`，说明=`description`，成员=`users`。设计稿的「可用节点池」列后端没有，见 D-B-3，在它定下来之前隐藏这一列。「查看成员」跳到用户列表，带 `group_id` 参数（依赖列表的待补筛选）。待补·前端：增加「被引用」列，显示 `plans`、`prices`、`coupons`

#### POST v1/user-groups — 新建用户组
- 状态：现有 `usergroup.go:82 saveUserGroup`
- 权限：`iam.user.write`｜reauth：否｜幂等：否
- 请求：`{ name: string（必填）, code?: string, description?: string }`。不传 code 时由 name 生成；name 是纯中文时会生成 `group-<8 位十六进制>`
- 响应：200 `{ id: uuid }`
- 错误：validation_failed 422 `fields.name`；conflict 409：分组标识已存在
- 设计：后台-03「新建用户组」表单（名称、说明）。待补·前端：增加一个可选的「标识（英文）」输入框，放在折叠的高级选项里

#### POST v1/user-groups/{id} — 编辑用户组
- 状态：现有 `usergroup.go:82 saveUserGroup`；待补·前端（→ 补进后台-03「用户组」表格每行的「编辑」按钮，行内编辑名称和说明）
- 权限：`iam.user.write`｜reauth：否｜幂等：否
- 请求：`{ name: string, description?: string, code?: string }`。code 会被忽略，不允许修改
- 响应：200 `{ id }`
- 错误：404 NotFoundOrForbidden；validation_failed 422 `fields.name`；传非 uuid 的 id 时推测会返回 500（INFERENCE）
- 设计：设计稿没有这个入口

#### DELETE v1/user-groups/{id} — 删除用户组
- 状态：现有 `usergroup.go:151 deleteUserGroup`；待补·前端（→ 补进后台-03「用户组」表格每行的「删除」按钮）
- 权限：`iam.user.write`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ ok: true }`
- 错误：conflict 409，以下任一情况触发：组内还有用户；还有套餐按这个组控制可见性；还有该组的专属价格；还有优惠券限定了这个组。message 会写明具体是哪一种。另有 404
- 设计：设计稿没有这个入口。待补·前端：删除按钮在 users、plans、prices、coupons 任一不为 0 时置灰，悬停提示具体原因

#### POST v1/users/bulk/preview — 批量筛选预览
- 状态：现有 `bulk_users.go:35 previewBulkUsers` → `adminops/bulk_users.go PreviewBulk`；待补·后端（筛选与字段）
- 权限：`iam.user.read`｜reauth：否｜幂等：否
- 请求（现有）：`{ status?: ""|pending|active|suspended|banned|deletion_scheduled, group_id?: uuid, query?: string（邮箱模糊匹配）, has_active_sub?: bool }`。注意 has_active_sub 只认 status 为 `active` 的订阅，不包括 trialing
- 响应（现有）：200 `{ total: int, samples: [email] }`，samples 最多 10 条
- 待补·后端（需迁移：否）：
  1. BulkFilter 增加 `plan_id?: uuid`（当前订阅的套餐）、`expires_within_days?: int(1–365)`（当前订阅在 N 天内到期）、`sub_state?: active|expired|none`。预览、导出、群发三处共用 buildFilterSQL，必须同步改；导出的 query 参数同名增加
  2. 响应增加 `sample_rows: [{ email, plan_name: string|null, current_period_end: time|null }]`，保留原有的 `samples` 字段
- 错误：validation_failed 422 `fields.status|group_id`
- 设计：后台-03「批量运营」按条件筛选。映射：
  - 套餐 → `plan_id`：下拉选项来自分段 C 或 D 的套餐列表接口
  - 状态：正常 → `status=active`；已禁用 → 需要多值，但 bulk 的 status 只接受单值，前端只能传 `suspended`，封禁用户需要另选；已过期 → `sub_state=expired`
  - 到期时间 → `expires_within_days`
  - 用户组 → `group_id`
  - 命中数取 `total`，预览列表取 `sample_rows`

#### GET v1/users/bulk/export — 导出 CSV
- **修订 R9（2026-09-24，后端二 62f7283）**：权限改为 `iam.user.write`，reauth 改为「是」。
- 状态：现有 `bulk_users.go:54 exportUsers`
- 权限：`iam.user.read`｜reauth：否（router 注释说要写权限 + 重认证，代码是读权限、不要 reauth，契约以代码为准）｜幂等：否
- 请求：query `status?`、`group_id?`、`query?`、`has_active_sub?: "true"|"false"`、`limit?: int`（默认 10000，最大 50000，超出会被静默截断），外加待补的 `plan_id`、`expires_within_days`、`sub_state`
- 响应：200，`text/csv; charset=utf-8`，带 BOM，`Content-Disposition: attachment; filename="users.csv"`。列依次为：邮箱、状态、分组、生效订阅、订单数、累计实付（元）、注册时间、最近登录。不包含 IP、设备、订阅凭据
- 错误：validation_failed 422（同预览）
- 设计：后台-03「导出 CSV」。映射：没有 cookie，不能用 `<a href>` 直接下载。前端要用 fetch 带 Bearer 头取回 blob，再触发下载。请求前先用 preview 的 `total` 提示「将导出 N 人（上限 10000）」

#### POST v1/users/bulk/generate — 批量生成账号
- **修订 R9（2026-09-24，后端二 62f7283）**：reauth 改为「是」。
- 状态：现有 `bulk_users.go:106 generateUsers` → `adminops/bulk_users.go GenerateUsers`
- 权限：`iam.user.write`｜reauth：否（注释说要，代码没挂，以代码为准）｜幂等：是 `user_bulk_generate`
- 请求：`{ count: int(1–500), email_prefix: string（1–20 位，只能用小写字母、数字、-）, email_domain: string, group_id?: uuid, reason: string(5–500 字) }`
- 响应：200 `{ count: int, users: [{ email, password }], warning: string }`。邮箱格式为 `<prefix>-<8 位随机>@<domain>`，口令明文只返回这一次
- 错误：validation_failed 422 `fields.count|email_prefix|email_domain|reason|group_id`
- 设计：后台-03「批量生成用户」。映射：数量 → count（设计稿写的上限是 200，按后端改为 500）；邮箱后缀 → email_domain；用户组 → group_id。待补·前端：增加必填的「邮箱前缀」和「生成原因」。「开通套餐」这一项后端没有，见 D-B-7，在它定下来之前隐藏。「下载」按钮由前端把 `users` 拼成 CSV 在本地下载，不再请求服务器

#### POST v1/users/bulk/mail — 群发邮件
- **修订 R9（2026-09-24，后端二 62f7283）**：reauth 改为「是」。
- 状态：现有 `bulk_users.go:135 sendBulkMail` → `adminops/bulk_mail.go SendBulkMail`；待补·后端（变量替换）
- 权限：`ops.notification.write`｜reauth：否（注释说要，代码没挂，以代码为准）｜幂等：是 `user_bulk_mail`
- 请求：`{ status?, group_id?, query?, has_active_sub?,（外加待补的 plan_id?, expires_within_days?, sub_state?）, subject: string(1–200), body: string(1–20000) }`。筛选字段直接放在 body 顶层，不嵌套在子对象里
- 响应：200 `{ queued: int, skipped: int }`。skipped 表示关闭了营销邮件偏好的人数。邮件是异步投递的，走的是 notification_deliveries 队列
- 待补·后端（需迁移：否）：正文支持 `$email`、`$plan`、`$expire` 三个变量。做法是在 INSERT…SELECT 入队时，逐个用户对 body 执行 `replace()`：plan 取当前订阅的套餐名，expire 取 current_period_end，格式为 YYYY-MM-DD，没有则为空。模板 `admin.broadcast` 保持 `{{subject}}` / `{{body}}` 不变，不改 allowed_variables
- 错误：
  - validation_failed 422 `fields.subject|body|status|group_id`
  - validation_failed 422：没有命中任何用户，或命中超过 20000 人（这两种没有 fields）
- 设计：后台-03「群发邮件」。映射：发送前的确认框显示 preview 的 `total`；成功后 toast 显示「已排队 queued 封，跳过 skipped 封（用户退订）」

#### GET v1/devices — 在线设备与全局策略
- 状态：现有 `devices.go:18 listOnlineDevices`
- 权限：`iam.user.read`｜reauth：否｜幂等：否
- 请求：无，也不分页
- 响应：200 `{ devices: [{ subscription_id, email, plan, limit: int（生效值，0 表示不限）, online: int, nodes: int, overridden: bool, exceeded: bool, last_seen_at: time|null }], mode: "loose"|"strict", grace: int }`
  - 只包含状态为 active、trialing、grace 的订阅，按在线数倒序，最多 200 条
  - `exceeded` 的计算方式是 `limit > 0 && online > limit + grace`
  - 在线数按 5 分钟窗口内去重后的 IP 哈希计算
- 错误：无特有错误
- 设计：后台-03「设备策略」。映射：
  - 「接近或超出上限的订阅」列表由前端过滤出 `limit > 0 && online >= limit` 的行，pips 取 `online` 和 `limit`，点击后按 email 打开用户抽屉的「订阅」tab。注意这里只有 subscription_id，没有 user_id，见核对笔记
  - 模式映射（以后端为准）：设计稿的「拒绝新设备」对应 `loose`，文案改为「节点本地判定，超限的新连接被拒」；设计稿的「踢下最早的设备」后端没有，改为 `strict`，文案为「面板跨节点汇总，超出上限 + 宽容值后停止向该订阅下发」
  - 待补·前端：增加宽容值 `grace` 输入框（0–5），只在 strict 模式下显示
  - 「默认同时在线设备」滑块和「设备识别窗口」下拉见 D-B-5、D-B-4

#### POST v1/subscriptions/{id}/device-limit — 单个订阅的设备数覆盖
- **修订 R12（2026-09-24，后端二 62f7283）**：写审计；订阅不存在或 id 非法回 404（原为 200）。
- 状态：现有 `devices.go:89 setDeviceLimit`
- 权限：`iam.user.write`｜reauth：否｜幂等：否
- 请求：path `id`，是**订阅** id；body `{ limit: int(0–1000) | null }`。null 表示恢复套餐规定，0 表示不限
- 响应：200 `{ ok: true }`
- 错误：validation_failed 422，超出 0–1000 时触发（没有 fields）。另外，订阅不存在时也会返回 200 ok，因为代码没检查影响的行数；传非 uuid 时推测会返回 500（INFERENCE）
- 设计：后台-03 抽屉「订阅」tab 的「本订阅设备上限」− / + 控件。设计稿的范围是 1–20，后端允许 0–1000，前端按 1–1000 放开。待补·前端：增加「恢复套餐默认」（传 null）和「不限」（传 0）两个快捷按钮

#### POST v1/settings/device-limit — 全局设备判定模式
- **修订 R9 / R12（2026-09-24，后端二 62f7283）**：reauth 改为「是」；写审计。
- 状态：现有 `devices.go:130 setDeviceMode`
- 权限：`iam.user.write`｜reauth：否（注释说「要求近期重认证」，代码没挂，以代码为准）｜幂等：否
- 请求：`{ mode: "loose"|"strict", grace?: int(0–5) }`
- 响应：200 `{ ok: true }`
- 错误：validation_failed 422，mode 或 grace 超出范围时触发（没有 fields）
- 设计：后台-03「保存策略」。映射见 GET v1/devices

#### GET v1/traffic-resets — 流量重置日志
- 状态：现有 `traffic_reset.go:13 listTrafficResets` → `billing/traffic_reset.go:61`
- 权限：`metering.reset.read`｜reauth：否｜幂等：否
- 请求：query `user_id?: uuid`、`reason?: renewal|cycle_roll|manual|gift_card`、`limit?: int`（默认 50，最大 200）、`offset?: int`
- 响应：200 `{ logs: [{ id, user_email, plan_name?: string, metric, reason, consumed_before: int64, actor_email?: string, note?: string, created_at }], total: int64 }`
- 错误：bad_request 400：reason 取值不支持，或 user_id 不是 uuid
- 设计：后台-03「流量重置」表格。映射：
  - 方式：renewal、cycle_roll 显示为「自动」，manual 显示为「手动」，gift_card 显示为「礼品卡」（设计稿没有这一项，新增）
  - 重置前用量取 `consumed_before`，转换为 GB 显示
  - 操作人：`actor_email` 为空时显示为「系统 · 续费」或「系统 · 周期滚动」；不为空时显示 `actor_email`，有 note 再追加「 · note」
  - 待补·前端：增加按 reason 筛选的分段控件和分页

#### GET v1/traffic-resets/stats — 重置统计（近 30 天）
- 状态：现有 `traffic_reset.go:29 trafficResetStats`
- 权限：`metering.reset.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ last_30_days: int, by_reason: { renewal?: int, cycle_roll?: int, manual?: int, gift_card?: int }, freed_bytes: int64, manual_count: int }`
- 错误：无特有错误
- 设计：后台-03 流量重置的四张 KPI 卡。映射：
  - 「本月重置」取 `last_30_days`，标签改为「近 30 天重置」
  - 「自动 · 账单周期」取 `by_reason.renewal + by_reason.cycle_roll`
  - 「手动」取 `manual_count`
  - 「重置前累计用量」取 `freed_bytes`，按 TB/GB 换算显示

#### GET v1/users/{id}/traffic-resets — 单个用户的重置历史
- 状态：现有 `traffic_reset.go:39 userTrafficResetHistory`
- 权限：`metering.reset.read`｜reauth：否｜幂等：否
- 请求：path `id`。不接受分页参数，固定返回最近 50 条
- 响应：200 `{ logs: [ResetLog], total: int64 }`，结构同上
- 错误：bad_request 400：id 不是 uuid
- 设计：后台-03 抽屉「流量重置」tab。映射同 GET v1/traffic-resets

#### POST v1/users/{id}/traffic-reset — 手动重置本期流量
- 状态：现有 `traffic_reset.go:55 manualResetTraffic` → `billing/traffic_reset.go:174 ManualResetTraffic`；待补·后端（挂 reauth）
- 权限：`metering.reset.write`｜reauth：否（现状）；待补·后端改为「是」｜幂等：是 `traffic_manual_reset`
- 请求：`{ note: string(5–500 字) }`
- 响应：200 `{ reset: true, freed_bytes: int64 }`。只对 status 为 `active` 的最新订阅生效（按 current_period_end 倒序取第一条，不包括 trialing），只把 `traffic.bytes` 的 consumed 清零
- 待补·后端（需迁移：否）：在 router 上挂 `RequireRecentReauth`。设计稿的两处入口都标了 `reauth:true`，router 注释也写了「写权限 + 近期重认证」，只有代码漏掉了
- 错误：
  - validation_failed 422 `fields.note`
  - validation_failed 422：该用户没有生效中的订阅，或该订阅没有流量配额（这两种没有 fields）
  - 404：id 不是 uuid
- 设计：后台-03 流量重置页顶部的「按邮箱手动重置」，以及抽屉里的「立即重置本期」。映射：按邮箱重置时，前端先调 `GET v1/users?q=<email>`，在结果里取 email 完全相等的那一条拿到 id，找不到就提示「用户不存在」。待补·前端：两处确认框都增加必填的「重置原因」（对应 note）

### 后台-04 套餐

公共映射：
- 套餐状态：后端 `draft` → 设计「草稿」，`active` → 「在售」，`archived` → 「已归档」。
- 版本状态：`draft` → 「草稿」；`published` 且 `id == plan.current_version_id` → 「当前发布」；其余 `published` / `retired` → 「历史」。版本号取 `version`。「发布于」取 `frozen_at`。
- 价格周期：设计的 `1m` → `{billing_interval:"month", interval_count:1}`，`3m` → month×3，`6m` → month×6，`12m` → `year`×1，`once` → `one_time`×1（后端注释的约定：季付就是 month×3，不是单独的周期类型）。后端另外支持 `day`、`week`、`quarter`，设计没有。
- 流量：后端存在版本的 `quotas[]` 里，`metric:"traffic.bytes"`、`unit:"bytes"`、`period:"cycle"`；GB = limit ÷ 1024³。列表直接给 `traffic_limit`（字节）。设备数取版本的 `max_devices`；向导同时写一条 `devices.active` quota。
- 限速：设计「限速 Mbps」→ 后端 `throttle_kbps`（Mbps × 1000）。数据面 pdnd 把它当作始终生效的按用户限速；但后台校验要求它只能配 `overage_policy:"throttle"`，见 D-C-5。
- 乐观锁：写操作都要带从 GET 拿到的 `row_version`；版本号对不上时回 409 conflict，`fields.row_version = "current=N"`。
- `catalog.publish` 类写接口（新增价格、改价、发布）受一个默认关闭的销售开关控制（环境变量 `AEGIS_SALES_ENABLED=1`），开关没开时回 503 service_unavailable「服务暂时不可用」。前端对这个 503 要给出明确提示，不要让用户反复重试。

#### GET v1/plans — 套餐列表（左栏卡片）
- 状态：现有 `panel/internal/api/admin/handlers.go:405 listPlans`
- 权限：`catalog.read`｜reauth：否｜幂等：否
- 请求：无参数（不分页，按 sort_order、created_at 排序，包含 archived）
- 响应：200 `{ plans: [{ id, product_id, row_version:int, code, name, description:string|null, status:"draft"|"active"|"archived", visibility, sort_order:int, current_version_id:uuid|null, draft_version_id:uuid|null, version:int|null, max_devices:int|null, traffic_limit:int64|null(字节), prices:[PriceRow], active_subscriptions:int, node_count:int }] }`；`PriceRow = { id, currency, unit_amount:int64, billing_interval, interval_count:int, trial_days:int, status:"active"|"archived", user_group_id:uuid|null, valid_from|null, valid_until|null, row_version }`（已归档价格也在里面）
- 错误：无
- 设计：后台-04 左栏卡片（名称 / 代码 / 状态 / 起价 / 流量·设备 / 订阅数）；详情里的「有效订阅」也取这里的 `active_subscriptions`（GET v1/plans/{id} 没有这个字段）。起价 = 价格中 `status=active` 的最低一档。`node_count = 0` 时建议加一个「无可用节点」警示（待补·前端：设计里没有，但这是后端专门留出来防止「买了空订阅」的字段）。人工开单弹窗的「套餐与周期」下拉也用这个接口。

#### GET v1/plans/{id} — 套餐详情（含全部版本与价格）
- 状态：现有 `panel/internal/api/admin/catalog.go:12 getPlan`
- 权限：`catalog.read`｜reauth：否｜幂等：否
- 请求：路径 `id: uuid`
- 响应：200 `{ plan: { id, product_id, current_version_id|null, row_version, code, name, description|null, status, visibility:"public"|"authenticated"|"group"|"invite_only"|"hidden", visible_group_ids:uuid[], visible_from|null, visible_until|null, allow_new_purchase:bool, allow_renewal:bool, allow_upgrade:bool, purchase_limit_per_user:int|null, stock_total:int|null, stock_reserved:int, sort_order:int, versions:[VersionRow] (version 降序), prices:[PriceRow] (created_at 降序，含已归档) } }`；`VersionRow = { id, version:int, status:"draft"|"published"|"retired", frozen_at|null, row_version, quota_reset_strategy:"never"|"natural_month"|"billing_cycle"|"fixed_day", quota_reset_day:int|null, grace_period_hours:int, grace_keeps_service:bool, renewal_extends_period:bool, renewal_resets_quota:bool, renewal_keeps_addons:bool, max_devices|null, max_concurrent|null, device_release_hours:int, overage_policy:"suspend"|"throttle"|"metered_billing", throttle_kbps|null, notes|null, entitlements:[{code, value:json}], quotas:[{metric, limit:int64|null, unit, period:"total"|"cycle"|"day"|"month"}], pool_ids:uuid[], created_at }`
- 错误：id 不是 uuid 或不存在 → 404
- 设计：后台-04 右侧详情（facts、价格卡、版本列表、版本编辑表单的初值）。设计版本行的「草稿 · 周敏 · 09-22」要显示创建人：待补·后端（扩展），在 VersionRow 上加 `created_by_email: string|null`（plan_versions.created_by 已有，只需 JOIN users，不需迁移）。

#### POST v1/plans — 只建套餐壳（draft，无版本 / 价格）
- 状态：现有 `panel/internal/api/admin/catalog.go:21 createPlan`；设计不直接用（设计的新建走向导 POST v1/plans/complete）
- 权限：`catalog.write`｜reauth：是｜幂等：是 `catalog_plan_create`
- 请求：`{ code:string(^[a-z0-9][a-z0-9_-]{1,63}$), name:string(1..120), description?:string|null, visibility?:"public"(默认)|"authenticated"|"group"|"invite_only"|"hidden", visible_group_ids?:uuid[](仅 group 时且必填), visible_from?:RFC3339, visible_until?:RFC3339, allow_new_purchase?:bool(默认 true), allow_renewal?:bool(默认 true), allow_upgrade?:bool(默认 true), purchase_limit_per_user?:int>0|null, stock_total?:int>=0|null, sort_order?:int }`
- 响应：201 `{ plan: <同 GET v1/plans/{id}.plan> }`
- 错误：422 字段校验（code / name / visibility / visible_group_ids / visible_until / purchase_limit_per_user / stock_total）；409 code 已存在
- 设计：无。前端不需要单独入口。

#### POST v1/plans/complete — 向导一次建成套餐（资料 + 额度 + 价格 + 线路 + 可选发布）
- **修订 R1（2026-09-24，后端一 0651cb2）**：权限改为 `catalog.publish`，reauth 改为「是」（D-C-2）。以下原文中的权限行作废。
- 状态：现有 `panel/internal/api/admin/catalog.go:41 createPlanComplete`
- 权限：`catalog.write`｜reauth：否（见 D-C-2）｜幂等：是 `catalog_plan_create_complete`
- 请求：`{ code, name, description?:string|null, visibility?:string, sort_order?:int, allow_new_purchase?:bool, allow_renewal?:bool, allow_upgrade?:bool, visible_group_ids?:uuid[], purchase_limit_per_user?:int|null, stock_total?:int|null, traffic_gb?:int64|null(null 或 0 = 不限), max_devices?:int|null(null = 不限), throttle_kbps?:int|null, quota_reset_strategy?:"never"|"natural_month"|"billing_cycle"(默认)|"fixed_day", quota_reset_day?:1..28, pool_ids?:uuid[], prices?:[{ billing_interval, interval_count:int>0, unit_amount:int64>0, currency:"CNY"|"USD", trial_days?:int }], publish:bool }`
- 响应：201 `{ plan: <创建套餐壳那一刻的详情，不含后续建出的版本 / 价格>, version_id:uuid, price_ids:uuid[], published:bool }`，前端成功后应重新拉 GET v1/plans/{id}
- 错误：422 `code`/`name` 必填；`publish=true` 时 `prices` 为空或 `pool_ids` 为空；`prices.{i}.unit_amount|interval_count|currency|billing_interval`（同周期同币种重复）；`traffic_gb`/`max_devices` 为负；发布前置条件不满足（`prices`：没有当前有效价格；`pool_ids`：没有池或池里没有可服务节点；`visibility`：invite_only 禁止发布）。中途任何一步失败都会自动归档已建出的套餐并返回那一步的错误；回滚本身也失败时回 500，message 写明要手工归档哪个套餐。503 销售开关未开。**`throttle_kbps` 非空时必定 422**（向导把 overage_policy 写死为 suspend），见 D-C-5
- 设计：后台-04「新建套餐」向导 5 步。映射：第 1 步 name / code / description；第 2 步 traffic_gb / max_devices（留空 = 不限）；第 3 步 prices（设计只让填「价格（元）」+ 周期，前端固定 `currency:"CNY"` 并 ×100；想卖 USD 需要补币种选择，见下文「后端有、设计缺」）；第 4 步 pool_ids（chip 的名称和数量来自 GET v1/plans/{id}/pools 或节点池列表）；第 5 步开关「保存后立即发布上架」→ `publish`。

#### PUT v1/plans/{id}/complete — 向导一次改完套餐
- **修订 R1（2026-09-24，后端一 0651cb2）**：权限 `catalog.publish`、reauth「是」；整个编辑在一个事务里完成，发布失败时资料、价格、新草稿版本全部回滚；`prices` 只同步本次清单里出现的币种的公开价，用户组价与其他币种不动，`[]` 等同 `null`。原文「`[]` = 全部归档」「不是原子操作」作废。
- 状态：现有 `panel/internal/api/admin/catalog.go:64 updatePlanComplete`
- 权限：`catalog.write`｜reauth：否（见 D-C-2）｜幂等：是 `catalog_plan_update_complete`
- 请求：`{ expected_row_version:int64, code, name, description?:string|null, visibility:string(必须传当前值，空串会 422), sort_order:int, allow_new_purchase?:bool|null(null = 不动), allow_renewal?:bool|null, allow_upgrade?:bool|null, visible_group_ids?:uuid[], purchase_limit_per_user?:int|null, stock_total?:int|null, traffic_gb?:int64|null(null = 不动), max_devices?:int|null(null = 不动), throttle_kbps?:int|null(null = 不动), prices?:[PlanPriceInput]|null(null = 不动；[] = 全部归档), pool_ids?:uuid[]|null(null = 不动) }`。注意基本资料字段（code / name / description / visibility / visible_group_ids / purchase_limit_per_user / stock_total / sort_order）**没有**「不动」语义，每次都整体覆盖，所以必须回填当前值
- 响应：200 `{ plan: <更新后的详情>, changed: string[] }`；`changed` 是中文的生效说明（例如「流量与设备数已更新；新购买的用户按新额度，已经买了的用户仍按原额度」），前端原样展示
- 错误：409 row_version 冲突；409「已归档套餐不能恢复或编辑」；422 同 PUT v1/plans/{id} 的字段校验与价格校验；503 销售开关未开。**不是原子操作**：资料先提交，额度 / 线路再开新版本**并立即发布**，价格最后同步，后面任一步失败时前面已生效的部分不会回滚
- 设计：后台-04「用向导编辑」。映射与限制：
  - 额度或线路变了 → 后端自动开新版本并立即发布（设计 toast 写的是「额度变更请新建版本」，实际并不需要手动新建版本；文案改成直接展示 `changed`）。
  - `prices` 按「周期 + 币种」整组同步：清单里没有的在售价格会被**归档**，包括 USD 价格和用户组专属价。设计的 editPlan 只回填了 CNY 价格，照着做会把 USD 价格和用户组专属价全部归档。前端必须回填全部在售价格，或者没改价格时传 `prices: null`。

#### PUT v1/plans/{id} — 改套餐资料与销售设置（不动版本、价格）
- 状态：现有 `panel/internal/api/admin/catalog.go:80 updatePlan`（路由由 `router.go registerCatalogPlanUpdate` 注册）
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_plan_update`
- 请求：`{ expected_row_version:int64, code, name, description?:string|null, visibility, visible_group_ids:uuid[], visible_from?:RFC3339|null, visible_until?:RFC3339|null, allow_new_purchase:bool, allow_renewal:bool, allow_upgrade:bool, purchase_limit_per_user:int|null, stock_total:int|null, sort_order:int }`（整体覆盖，三个 allow_* 是 bool 而不是可空）
- 响应：200 `{ ok:true, row_version:int64 }`
- 错误：409 row_version 冲突 / 已归档；422 字段校验，另有 `stock_total`「不能低于已预留库存」；active 套餐把某个 allow_* 从 false 改回 true 时受销售开关控制（503）
- 设计：设计没有独立入口。待补·前端 → 在后台-04 详情顶部「用向导编辑」旁补一个「销售设置」抽屉（可见性 / 可见用户组 / 上架时间窗 / 三个购买开关 / 限购 / 库存 / 排序），用这个接口；D-C-1 选方案 a 时，「下架 / 重新上架」也走这个接口。

#### POST v1/plans/{id}/versions — 新建草稿版本
- 状态：现有 `panel/internal/api/admin/catalog.go:95 createPlanVersion`
- 权限：`catalog.write`｜reauth：否｜幂等：是 `catalog_plan_version_create`
- 请求：无 body（不解码）
- 响应：201 `{ version: VersionRow }`，只有 `id / version / status / frozen_at / row_version / created_at` 有值，其余字段是**库默认值或零值**，不会从当前版本复制；`entitlements / quotas / pool_ids` 为 null
- 错误：404；409 已归档；每个套餐只允许一个草稿（唯一索引 `plan_versions_one_draft_per_plan`），已有草稿时回 409「目录对象已存在或活动报价发生冲突」
- 设计：后台-04「新建版本」。设计里新版本会继承当前版本的流量和设备数，后端不复制。前端流程：POST 建草稿 → PUT 版本写入从当前版本复制的全部语义 → POST v1/plans/{id}/pools 复制池绑定 → 可选发布。已有草稿（`draft_version_id != null`）时禁用按钮。

#### PUT v1/plans/{id}/versions/{versionID} — 编辑草稿版本的额度与语义
- 状态：现有 `panel/internal/api/admin/catalog.go:105 updatePlanVersion`
- 权限：`catalog.write`｜reauth：否｜幂等：否
- 请求：`{ expected_row_version:int64, quota_reset_strategy:"never"|"natural_month"|"billing_cycle"|"fixed_day", quota_reset_day?:1..28(仅 fixed_day，其它策略必须为 null), grace_period_hours:int>=0, grace_keeps_service:bool, renewal_extends_period:bool, renewal_resets_quota:bool, renewal_keeps_addons:bool, max_devices?:int>0|null, max_concurrent?:int>0|null, device_release_hours:int>=0, overage_policy:"suspend"|"throttle"|"metered_billing", throttle_kbps?:int>0|null(仅 throttle 时必填，其余策略必须为 null), notes?:string|null, entitlements:[{code, value?:json}], quotas:[{metric, limit:int64|null, unit, period}] }`。`pool_ids` **必须省略**，传了（包括 []）就回 422。全量覆盖：entitlements / quotas 会先删后插
- 响应：200 `{ ok:true, row_version:int64 }`
- 错误：404；409 row_version 冲突 /「只有未发布草稿版本可以编辑」；422 语义校验（字段名如上，另有 `entitlements.{i}`、`quotas.{i}`、`quotas.{i}.period`、`quotas.{i}.limit`）
- 设计：后台-04 版本行展开的「每周期流量 GB / 设备上限 / 限速 Mbps + 保存草稿」。流量改的是 `quotas` 里 traffic.bytes 那一条（同步维护 devices.active 那条）；其余字段必须原样回填 GET 拿到的值。已发布版本点「查看」只读；设计的「已发布版本另存为 vN」= 先调 POST versions 再调本接口。

#### POST v1/plans/{id}/versions/{versionID}/publish — 发布草稿版本
- 状态：现有 `panel/internal/api/admin/catalog.go:120 publishPlanVersion`
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_plan_version_publish`
- 请求：`{ expected_plan_row_version:int64>0, expected_version_row_version:int64>0 }`
- 响应：200 `{ ok:true, plan_row_version:int64, version_row_version:int64 }`
- 错误：409 已归档 / 不是草稿 / 任一 row_version 冲突；422 前置条件：`prices`（没有覆盖可见范围的当前有效 CNY / USD 价格）、`pool_ids`（没有启用的节点池，或池里没有可服务节点）、`visibility`（invite_only 禁止发布）；503 销售开关未开
- 设计：后台-04 版本行「发布」确认框。发布后套餐 status 从 draft 变为 active；旧的已发布版本状态**不变**（仍是 published），靠 `current_version_id` 区分当前版本。

#### POST v1/plans/{id}/prices — 新增价格
- 状态：现有 `panel/internal/api/admin/catalog.go:138 createPlanPrice`
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_price_create`
- 请求：`{ currency:"CNY"|"USD", unit_amount:int64>=0, billing_interval:"day"|"week"|"month"|"quarter"|"year"|"one_time", interval_count:int>0, trial_days?:int>=0, user_group_id?:uuid|null, valid_from?:RFC3339|null, valid_until?:RFC3339|null }`
- 响应：201 `{ price: PriceRow }`
- 错误：404；409 已归档套餐 / 同一产品下活动报价冲突（唯一约束）；422 字段校验，`user_group_id`「用户组不存在」；503 销售开关未开
- 设计：后台-04 价格卡底部「币种 + 金额 + 周期 + 新增价格」，周期映射见上文。

#### POST v1/plans/{id}/prices/{priceID}/archive — 归档价格
- 状态：现有 `panel/internal/api/admin/catalog.go:153 archivePlanPrice`
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_price_archive`
- 请求：`{ expected_row_version:int64>0 }`（取该价格的 row_version）
- 响应：200 `{ ok:true, row_version:int64 }`
- 错误：404；409 row_version 冲突 /「价格已经归档」；422 expected_row_version
- 设计：后台-04 价格行「归档」。归档不可逆（库里的触发器只允许 active → archived），对应设计里「已归档」禁用态。

#### POST v1/plans/{id}/archive — 归档套餐（不可逆）
- 状态：现有 `panel/internal/api/admin/catalog.go:170 archivePlan`
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_plan_archive`
- 请求：`{ expected_row_version:int64>0 }`
- 响应：200 `{ ok:true, row_version:int64 }`；副作用是 `allow_new_purchase=false`、product 也标为 archived；`allow_renewal` 不动，所以已有订阅照常续费
- 错误：404；409 冲突 /「套餐已经归档」
- 设计：后台-04 详情「归档套餐」。设计的「恢复上架」见下一条与 D-C-1。

#### POST v1/plans/{id}/unarchive — 恢复上架
- 状态：待决（D-C-1）。库里 `app.guard_plan_catalog_transition` 触发器规定套餐生命周期单调（archived 不能回到 active），`UpdatePlan` 也显式拒绝「已归档套餐不能恢复或编辑」
- 权限：（若选 b）`catalog.publish`｜reauth：是｜幂等：是 `catalog_plan_unarchive`
- 请求：（若选 b）`{ expected_row_version:int64 }`
- 响应：（若选 b）200 `{ ok:true, row_version:int64, status:"active" }`
- 错误：（若选 b）409 没有已发布的 current_version；409 冲突
- 设计：后台-04 已归档套餐的「恢复上架」按钮。需迁移：选 b 要改 00035 的触发器（新迁移 CREATE OR REPLACE FUNCTION）；选 a 不需要迁移、也不需要这条路由。

#### GET v1/plans/{id}/pools — 节点池绑定候选（chip 列表）
- 状态：现有 `panel/internal/api/admin/pools.go:295 planPools`
- 权限：`catalog.read`｜reauth：否｜幂等：否
- 请求：路径 `id: uuid`
- 响应：200 `{ version_id:uuid|"", version_status:string|"", row_version:int64, editable:bool, pools:[{ id, name, active_nodes:int, bound:bool }] }`。有草稿时返回草稿的绑定（`editable=true`）；没有草稿时返回当前发布版本的绑定，只读（`editable=false`）；两者都没有时 version_id 为空串。pools 只列未禁用的池
- 错误：404
- 设计：后台-04「线路 · 节点池」chip（名称 + 节点数）；向导第 4 步的 chip 也可用它（新建时没有 plan id，改用节点池列表接口，那条属于节点分段）。

#### POST v1/plans/{id}/pools — 替换草稿版本的节点池绑定
- 状态：现有 `panel/internal/api/admin/pools.go:433 setPlanPools`
- 权限：`catalog.publish`｜reauth：是｜幂等：是 `catalog_plan_pools_update`
- 请求：`{ version_id:uuid, expected_version_row_version:int64>0, pool_ids:uuid[](不重复，最多 500 个) }`
- 响应：200 `{ bound:int, row_version:int64, version_id:uuid }`
- 错误：404；409「只有未发布的草稿版本可以修改节点分组」/ 冲突；422 `version_id`、`expected_version_row_version`、`pool_ids`（格式不对、有重复、不存在或已禁用）
- 设计：后台-04「保存绑定」。GET 返回 `editable=false`（没有草稿）时，本接口用不了。前端两条路任选：① 调 PUT v1/plans/{id}/complete 只传 `pool_ids`（外加必须回填的基本资料），后端开新版本并立即发布，符合设计「保存即生效」；② 先建草稿、复制语义、绑池、再发布。推荐 ①。

#### 后端有、设计缺：套餐高级设置（待补·前端）
设计向导只覆盖名称 / 代码 / 说明 / 流量 / 设备 / CNY 价格 / 线路 / 是否发布。下表列出后端其余可写字段，以及建议补进的位置：

| 字段 | 所属 | 补进哪里 | 用哪个接口写 |
|---|---|---|---|
| visibility、visible_group_ids | plan | 向导第 1 步「可见范围」折叠区 | POST/PUT complete |
| sort_order | plan | 向导第 1 步 | POST/PUT complete |
| visible_from / visible_until | plan | 详情「销售设置」抽屉（complete 接口不收这两个字段） | PUT v1/plans/{id} |
| allow_new_purchase / allow_renewal / allow_upgrade、purchase_limit_per_user、stock_total（只读显示 stock_reserved） | plan | 向导第 5 步「购买限制」折叠区 + 销售设置抽屉 | complete / PUT v1/plans/{id} |
| throttle_kbps（设计已有，只在版本行里） | version | 向导第 2 步补「限速 Mbps」 | complete（需先解决 D-C-5） |
| quota_reset_strategy / quota_reset_day | version | 向导第 2 步「流量重置」（设计只写了「按账单周期自动重置」这句说明）；PUT complete 不收这两个字段，编辑时走版本编辑 | POST complete / PUT versions |
| grace_period_hours、grace_keeps_service、renewal_extends_period、renewal_resets_quota、renewal_keeps_addons、max_concurrent、device_release_hours、overage_policy、notes、entitlements、其它 quotas | version | 版本行展开后的「高级」折叠区（只对草稿可编辑） | PUT versions |
| currency（USD） | price | 向导第 3 步每档价格加币种下拉（详情页价格卡已有） | complete / POST prices |
| trial_days | price | 向导第 3 步每档「试用天数」+ 详情页价格卡新增行 | complete / POST prices |
| user_group_id、valid_from / valid_until | price | 详情页价格卡新增行的「高级」（complete 不收这几个字段） | POST prices |
| billing_interval day / week、interval_count 任意值 | price | 周期下拉补「自定义」 | 同上 |

### 后台-05 订单与收款（tab：订单 / 挂账 / 支付渠道 / 收入调整）

公共映射：
- 订单状态（后端 9 个 → 设计 3 个 + 补 1 个）：`draft` / `pending_payment` / `processing` → 「待支付」；`paid` / `fulfilled` → 「已支付」；`cancelled` → 「已取消」，`expired` → 「已过期」（放在「已取消」筛选组里）；`refunded` / `partially_refunded` → 「已退款 / 部分退款」（设计没有这个状态，待补·前端：补一个状态标签，筛选不单列）。
- 订单 30 分钟过期（`orders.expires_at = now() + 30 min`，checkout.go:383），到期由后台任务转为 `expired`。支付是 epay 收银台跳转，没有二维码。这两条主要影响门户；后台只需要把 `expired` 映射好，并在详情里展示 `expires_at`。
- 人工单**不是** `kind='manual'`：后端用 `kind='new'` + `created_by` 非空 + `manual_reason` 非空来标识（迁移 00044 的 CHECK）。「来源：人工开单」按 `manual_reason != null` 判断。
- 设计 mock 里的渠道（支付宝当面付 / 微信 Native / USDT / Stripe）后端都没有。后端适配器只有 `epay`（聚合收银台）和 `demo`，外加系统内置的 `offline`（线下收款，`accepting_new=false`，结账页选不到）。不做 USDT。

#### GET v1/orders — 订单列表
- 状态：现有 `panel/internal/api/admin/handlers.go:342 listOrders`；待补·后端（扩展）
- 权限：`billing.order.read`｜reauth：否｜幂等：否
- 请求（现有 query 参数，逐条写清，供分段 B 引用）：
  - `q?: string`：对 `order_no` 和用户 `email` 做小写的子串 LIKE，**没有**按用户精确筛选
  - `status?: string`：原样作为 `o.status::text LIKE $3` 的模式。只能传一个值；传 `pending` 之类的设计值匹配不到任何订单，必须用后端值（例如 `pending_payment`）。值里的 `%` / `_` 会被当作通配符
  - `from?: YYYY-MM-DD`、`to?: YYYY-MM-DD`：按 `created_at` 过滤，from 是当天 00:00（UTC）起、to 包含当天（到次日 00:00 UTC 为止）；格式不对时**静默忽略**
  - `limit?: int`：默认 25，取值不在 1..100 时回落到 25
  - `offset?: int`：默认 0；**不做下限钳制**，传负数会让 SQL 报错，回 500
  - 待补·后端，新增参数：`user_id?: uuid`（精确匹配 `o.user_id`，供用户详情 › 订单 tab 使用）；`status` 改为支持逗号分隔多值 `status=pending_payment,processing,draft`，按枚举白名单精确匹配（不再走 LIKE，未知值回 400）；`offset < 0` 按 0 处理
- 响应：200 `{ orders: [OrderRow], total:int64 }`；`OrderRow = { id, order_no, user_email, kind:"new"|"renewal"|"upgrade"|"downgrade"|"addon"|"topup"|"manual", status, currency, total_amount, payable_amount, paid_amount, refunded_amount, created_at, paid_at|null, plan_name, interval, interval_count:int, item_count:int }`（plan_name / interval 是第一个订单项的快照；topup 等没有订单项的单这几项为空串 / 0）。待补·后端（扩展）加 `provider_code: string|null, provider_name: string|null`（取最近一笔 payment，没有则取最近一个 payment_intent 的渠道）和 `balance_applied: int64`，不需迁移
- 错误：offset 为负时 500（修复前）
- 设计：后台-05「订单」tab。搜索框 → `q`；状态分段 → `status` 多值（「待支付」= `draft,pending_payment,processing`，「已支付」= `paid,fulfilled`，「已取消」= `cancelled,expired`）；列映射：订单号 `order_no`、用户 `user_email`、内容 `plan_name` + 周期（`kind=topup` 时显示「余额充值」，`item_count>1` 时显示「等 N 项」）、金额 `total_amount` + currency、渠道 `provider_name`（为空时：`balance_applied == total_amount` 显示「余额」，人工单显示「人工」，否则显示「—」）、状态按上面的映射、创建时间 `created_at`。

#### GET v1/orders/{id} — 订单详情（快照）
- 状态：现有 `panel/internal/api/admin/handlers.go:360 getOrder`；待补·后端（扩展）
- 权限：`billing.order.read`｜reauth：否｜幂等：否
- 请求：路径 `id: uuid`
- 响应：200 `{ order: OrderRow + { user_id, organization_id|null, state_version:int64, subtotal_amount, discount_amount, tax_amount, balance_applied, coupon_id|null, manual_reason|null, subscription_id|null, expires_at|null, fulfilled_at|null, cancelled_at|null, expired_at|null, cancel_reason|null, updated_at, items:[{ id, product_id|null, price_id|null, plan_id|null, plan_version_id|null, product_name, plan_name|null, plan_version|null, interval|null, interval_count|null, snapshot_entitlements:json, snapshot_quotas:json, quantity, unit_amount, line_amount, currency, created_at }] } }`。待补·后端（扩展）加 `created_by: uuid|null, created_by_email: string|null`（orders.created_by 已有，不需迁移）
- 错误：404（id 不是 uuid 也回 404）
- 设计：后台-05 订单抽屉 facts：用户 / 内容 / 金额 / 币种 / 渠道 / 来源（`manual_reason != null` →「人工开单 · {created_by_email}」，否则「门户下单」）。「取消订单」要用的 `state_version` 从这里取。

#### GET v1/orders/{id}/payments — 订单的支付尝试、入账与退款
- 状态：现有 `panel/internal/api/admin/handlers.go:370 getOrderPayments`
- 权限：`billing.payment.read`（比读订单高一级）｜reauth：否｜幂等：否
- 请求：路径 `id: uuid`
- 响应：200 `{ payment_intents:[{ id, provider_code, provider_name, currency, amount, status:"created"|"requires_action"|"processing"|"succeeded"|"failed"|"cancelled"|"expired", provider_ref|null, failure_code|null, failure_message|null, expires_at|null, created_at, updated_at }], payments:[{ id, payment_intent_id|null, provider_code, provider_name, provider_payment_id, currency, amount, fee_amount, refunded_amount, status:"succeeded"|"refunded"|"partially_refunded"|"disputed"|"reversed", method|null, paid_at }], refunds:[{ id, payment_id|null, provider_refund_id|null, currency, amount, reason, status, entitlement_revoked, commission_reversed, failure_message|null, succeeded_at|null, created_at }] }`
- 错误：404
- 设计：后台-05 抽屉「支付记录」。映射：intent `created`/`requires_action`/`processing` →「等待回调」，`succeeded` →「成功」，`failed` →「失败」，`cancelled`/`expired` →「已关闭」（设计没有这一项，待补·前端补标签）；`provider_code=offline` 的 payment →「人工确认」，流水号取 `provider_payment_id`（格式 `offline:<凭证号>`）。设计每行只有一个状态：以 intent 为行，有对应 payment 的显示 payment 状态。没有 `billing.payment.read` 权限时回 404，前端要隐藏这一块而不是报错。

#### POST v1/orders/{id}/cancel — 管理员取消待支付订单
- 状态：现有 `panel/internal/api/admin/handlers.go:380 cancelOrder`
- 权限：`billing.order.write`｜reauth：否｜幂等：是 `admin_order_cancel`
- 请求：`{ expected_state_version:int64>0, reason:string(5..500 字) }`
- 响应：200 `{ order: { order_id, status, state_version, cancelled_at?, cancel_reason?, already_terminal:bool }, already_terminal:bool }`
- 错误：400 `expected_state_version` 不是正数、`reason` 长度不对（注意这里回的是 **400 bad_request**，不是 422）；404；409 状态版本冲突或已有支付证据（有入账的单不能取消）
- 设计：后台-05 抽屉「取消订单」确认框。待补·前端：设计确认框里没有理由输入框，要补一个必填的「取消原因」（5 字起）。

#### POST v1/orders/manual — 人工开单
- 状态：现有 `panel/internal/api/admin/manual_order.go:22 createManualOrder`；待补·后端（扩展：结算方式）
- 权限：`billing.order.write`｜reauth：否（路由注释写了要重认证，实际代码没挂）｜幂等：是 `order_create`（与用户结账共用 scope，`billing.CheckoutIdempotencyScope`）
- 请求（现有）：`{ user_id:uuid, plan_id:uuid, price_id:uuid, reason:string(5..500 字) }`
- 请求（扩展后）：`{ user_id, plan_id, price_id, reason, settlement?: "grant"(默认，即现有行为) | "pending" | "offline" | "balance", reference?: string(settlement=offline 时必填，≤128 字) }`。`pending` = 建一张 `pending_payment` 单交给用户去付（走 CreateOrder，不带 ManualGrant，但写入 manual_reason / created_by）；`offline` = 同一事务链上先建待支付单，再走 MarkOrderPaid（offline 渠道，凭证号 = reference）；`balance` = CreateOrder 时 `UseBalance = 应付全额`，余额不足回 409（是否提供这一项见 D-C-3）
- 响应：201，body 是业务层预先写入幂等记录的那一份：`{ discount_amount, order_id, order_no, currency, total_amount, balance_applied, payable_amount, status }`（重放同一 key 得到完全相同的 201 响应）
- 错误：400 id 格式不对 / 缺字段；422 `reason`；409 / 422 CreateOrder 的库存、限购、价格失效等错误；500「已创建并履约，但审计写入失败」（订单已生效，message 里带单号）
- 设计：后台-05「人工开单」弹窗。映射：用户邮箱 → 要先用用户搜索（GET v1/users?q=，分段 B）解析出 `user_id`，前端改成可搜索选择器；「套餐与周期」→ `plan_id` + `price_id`（选项来自 GET v1/plans 的在售价格）；「备注」→ `reason`（改成必填，5 字起，文案改为「开单原因（写入审计）」）；「结算方式」→ `settlement`：「赠送（0 元）」= grant，「待用户支付」= pending，「线下已收款」= offline（待补·前端：选这项时出现「凭证号」输入），「从余额扣除」= balance（取决于 D-C-3）。

#### POST v1/orders/{id}/mark-paid — 手工标记已支付（线下收款）
- **修订 R2（2026-09-24）**：响应已改为 snake_case `{ processed, already_handled, payment_id, subscription_id, ledger_txn_id }`，不再返回 `signature_failed`。
- 状态：现有 `panel/internal/api/admin/manual_order.go:62 markOrderPaid`
- 权限：`billing.order.write`｜reauth：是｜幂等：是 `admin_order_mark_paid`
- 请求：`{ reason:string(5..500 字), reference:string(1..128 字，线下凭证号) }`；金额由服务端从订单读取，不接受传入
- 响应：200 `{ Processed:bool, AlreadyHandled:bool, SignatureFailed:bool, PaymentID:string, SubscriptionID:string, LedgerTxnID:string }`。**字段名是 PascalCase**：`billing.PaymentWebhookOutput` 没有 json tag。待补·后端（小改）：加 json tag，改成 `{ processed, already_handled, payment_id, subscription_id, ledger_txn_id }`，前端按改后的 snake_case 写
- 错误：404；409 订单不是 `pending_payment` / `processing`，或 payable 为 0；422 `reason` / `reference`；同一凭证号重复入账按 AlreadyHandled 处理；500「已入账，但审计写入失败」
- 设计：后台-05 抽屉「手工标记已支付」。输入框「渠道流水号 / 转账凭证」→ `reference`；待补·前端：补必填的「收款说明」→ `reason`。

#### GET v1/late-payments — 挂账列表（设计里的「欠费单」）
- **修订 R3（2026-09-24）**：响应新增 `pending_amounts`（按币种分开的待处理合计）；旧的 `pending_amount` 保留一个版本后删除，前端只用 `pending_amounts`。
- 状态：现有 `panel/internal/api/admin/late_payment.go:14 listLatePayments`；待补·后端（扩展）
- 权限：`billing.ledger.read`｜reauth：否｜幂等：否
- 请求：`status?: ""|"suspense"|"applied"|"refunded"|"manual_review"|"refund_pending"`（空 = 全部，排序时 suspense 排最前），`limit?:1..100`（默认 25），`offset?:int>=0`
- 响应：200 `{ cases:[{ id, case_kind:"released_order"|"excess_capture", status, amount:int64, currency, order_no, order_status, user_id, user_email, received_at, resolved_at?, resolution_reason? }], total:int64, pending_amount:int64 }`。**`pending_amount` 把所有币种的 suspense 金额直接相加**（CNY 分 + USD 分），是 bug。待补·后端（扩展）：改成 `pending_amounts: { CNY?: int64, USD?: int64 }`，保留 `pending_amount` 一个版本后删除
- 错误：400 status 不在白名单
- 设计：后台-05「欠费单」tab。**保留规则 6：按后端「挂账」语义做**。挂账是「用户已经付了钱、但订单在到账前已被取消或释放（`released_order`）或多扣了款（`excess_capture`）」的待处理款项，是平台欠用户的钱；设计里的「欠费单」写成了用户欠平台的钱，方向正好相反。文案改法：tab 名「欠费单」→「挂账」；说明改为「订单取消后才到账、或超额扣款的款项暂记在挂账科目。「转入余额」会把这笔钱记入用户余额（贷记），挂账随之关闭。」；「未结清合计」→「待处理合计」（按币种分开显示）；列「原因」→ `case_kind`（released_order「订单取消后到账 · {order_no}」、excess_capture「超额扣款 · {order_no}」）；「金额」→ amount + currency；「账龄」→ now − received_at（天）；按钮「转为余额欠款」→「转入余额」，已处理的显示「已转入余额」，其他终态按 status 显示。设计里「余额可为负、下次充值自动抵扣」这段删掉，后端没有这个语义。

#### POST v1/late-payments/{id}/apply-to-balance — 挂账转入用户余额
- 状态：现有 `panel/internal/api/admin/late_payment.go:35 applyLatePayment`
- 权限：`billing.adjustment.write`｜reauth：是｜幂等：是 `late_payment_apply`
- 请求：`{ reason:string(5..500 字) }`。注意这里**没有用** httpx.DecodeJSON：body 上限 4 KiB，多传字段不报错；body 为空或不是 JSON 时回 400「请求体无法解析」
- 响应：200 `{ ledger_txn_id: uuid }`
- 错误：404；409「这笔挂账已经处理过了」；422 `reason`
- 设计：后台-05 挂账行「转入余额」（设计确认框本来就标了 reauth）。待补·前端：确认框补必填的「处理原因」。确认文案改为「{user_email} 的余额将**增加** {amount}，挂账关闭」。

#### GET v1/payment-providers — 支付渠道列表
- 状态：现有 `panel/internal/api/admin/handlers.go:418 listProviders`；待补·后端（扩展：统计）
- 权限：`billing.payment.read`｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ providers:[{ id, code, adapter, display_name, enabled:bool, accepting_new:bool, has_credentials:bool, base_url:string, currencies:string[] }] }`（按 code 排序，包括 `offline`）
- 响应（扩展后，每个元素追加）：`today: { [currency]: int64 }`（租户时区今天 succeeded 的 payments 金额合计）、`success_rate_24h: number|null`（近 24 小时 payment_intents 中 succeeded ÷ 进入终态的数量，没有样本为 null）、`last_callback_at: RFC3339|null`（最近一条 payment_events 的时间）。不需迁移
- 错误：无
- 设计：后台-05「支付渠道」tab 卡片：名称 `display_name`、key `code`、今日成交 `today`、成功率 `success_rate_24h`、币种 `currencies`、备注 → 由 `last_callback_at`、`has_credentials=false`（「未配置凭据」）、`accepting_new` 拼出。`offline` 不展示卡片，或展示为不可切换的只读卡。

#### POST v1/payment-providers/{code}/toggle — 启停渠道
- 状态：现有 `panel/internal/api/admin/handlers.go:432 toggleProvider`
- 权限：`billing.provider.write`｜reauth：是｜幂等：否
- 请求：`{ enabled:bool, accepting_new:bool }`（两个都要传，漏传按 false 处理）
- 响应：200 `{ ok:true }`
- 错误：404 code 不存在
- 设计：后台-05 渠道卡的开关。设计停用时的说明是「结账页不再显示，进行中的支付仍会回调」，这正是 `accepting_new=false` 的语义（PAY-009）。映射：开 → `{enabled:true, accepting_new:true}`，关 → `{enabled:true, accepting_new:false}`；`enabled:false`（连回调也停）不放在开关上。待补·前端：在卡片「更多」菜单里放「完全停用（回调也不处理）」，二次确认后发 `enabled:false`。

#### GET v1/revenue/adjustments — 收入调整列表
- 状态：现有 `revenue.go:27 revenueAdjustments`；**登记人展示字段 待补·后端**
- 权限：`billing.ledger.read`｜reauth：否｜幂等：否
- 请求：query `currency?: "CNY" | "USD"`（空=全部）
- 响应（现有）：200 `{ adjustments: [{ id: uuid, currency, amount: int(可负), reason, effective_on: "YYYY-MM-DD", reversal_of?: uuid, created_by: uuid, created_at: rfc3339, reversed: bool }] }`，按 created_at 倒序，**最多 200 条、无分页**
- 响应（待补·后端，追加）：每行 `created_by_email: string | null`（join users）。需迁移：否
- 错误：422 `fields.currency`
- 设计：后台-05「收入调整」列表（原因、币种、金额、登记人 · 时间、冲销按钮）。映射：「冲销单」←`reversal_of != null`；「已冲销」←`reversed`；「登记人」←created_by_email。待补·前端：增加「生效日」列（`effective_on`，设计缺）

#### POST v1/revenue/adjustments — 登记收入调整
- 状态：现有 `revenue.go:43 createRevenueAdjustment`
- 权限：`billing.adjustment.write`｜reauth：是｜幂等：是 `revenue_adjustment_create`（域层另以同一 key 做业务幂等）
- 请求：`{ currency: "CNY" | "USD", amount: int(非零，|x| ≤ 1e12), reason: string(5–500 字), effective_on?: "YYYY-MM-DD"(缺省=租户今天，不得晚于租户今天) }`
- 响应：200 RevenueAdjustment（同列表行结构，`reversed` 恒 false）
- 错误：422 `fields.currency|amount|reason|effective_on|idempotency_key`；409 idempotency_key_reuse「该幂等键已用于不同的报表调整」
- 设计：后台-05「登记调整」行内表单（币种、金额、原因）+ reauth 确认。待补·前端：表单加「生效日」日期框（可选，默认今天，最大值今天）；金额输入按元填写、提交时×100 取整；原因前端先校验 ≥5 字

#### POST v1/revenue/adjustments/{id}/reverse — 冲销一条调整
- 状态：现有 `revenue.go:62 reverseRevenueAdjustment`
- 权限：`billing.adjustment.write`｜reauth：是｜幂等：是 `revenue_adjustment_reverse`
- 请求：`{ reason: string(5–500 字) }`
- 响应：200 RevenueAdjustment（新追加的反向记录：amount 取反、同 effective_on、`reversal_of = id`）
- 错误：422 `fields.reason|idempotency_key`；404 not_found（id 不存在）；409 conflict「反向记录不能再次撤销」/「该调整已撤销」；409 idempotency_key_reuse
- 设计：后台-05 行尾「冲销」+ 确认框。待补·前端：确认框里加「冲销原因」必填输入（设计缺，后端强制 5–500 字）；若不加输入框，可默认填「冲销：」+ 原 reason（原 reason ≥5 字，拼接后必然满足长度）

### 后台-06 营销（tab：优惠券 / 礼品卡 / 佣金与提现）

#### GET v1/coupons — 优惠券列表
- **修订 R6（2026-09-24）**：权限改为 `marketing.coupon.read`（迁移 00068 新增，授给所有持有 `marketing.coupon.write` 的角色）。
- 状态：现有 `panel/internal/api/admin/coupon.go:49 listCoupons`
- 权限：`marketing.coupon.write`（没有只读权限）｜reauth：否｜幂等：否
- 请求：`q?:string`（对 code、name 小写子串匹配），`status?:string`（LIKE 模式：`active`/`paused`/`expired`/`exhausted`），`limit?:1..200`（默认 25），`offset?:int>=0`
- 响应：200 `{ coupons:[{ id, code, name, discount_type:"percent"|"fixed", discount_value:int64, currency:string(可能为空串), max_discount|null, min_order_amount, max_redemptions:int|null, max_redemptions_per_user:int, redeemed_count:int, applicable_plan_ids:uuid[], valid_from|null, valid_until|null, status, created_at, discounted_total:int64 }], total:int64 }`
- 错误：无
- 设计：后台-06「优惠券」tab。分段「全部 / 启用中 / 已停用」→ 不传 / `active` / `paused`；`expired`、`exhausted` 在「全部」里显示为「已过期」「已用完」标签（设计缺，待补·前端）。列：优惠码 `code`，批次标签在 `name != code` 时显示 `name`（后端没有 batch 概念，靠同名归批；「· N 张」需要按 name 再查一次 total，建议去掉），优惠（percent：`discount_value/100` %，fixed：`discount_value/100` 元），适用套餐（`applicable_plan_ids` 为空 →「全部套餐」，否则映射成套餐名），使用 `redeemed_count / max_redemptions`（null →「不限」），有效期至 `valid_until`（null →「长期」），启用开关 `status=active`。

#### GET v1/coupons/{id}/redemptions — 单张券的兑换记录
- 状态：现有 `panel/internal/api/admin/coupon.go:315 couponRedemptions`
- 权限：`marketing.coupon.write` + `billing.order.read`｜reauth：否｜幂等：否
- 请求：路径 `id: uuid`（最近 200 条）
- 响应：200 `{ redemptions:[{ email, order_no, discount:int64, currency, at, reverted:bool }] }`
- 错误：id 不是 uuid 时数据库转换报错，回 500（不是 404）
- 设计：后台-06 优惠码行展开「兑换记录」：用户 / 订单号 / −优惠金额 / 时间；`reverted=true` 显示「已回滚」（设计缺，待补·前端）。

#### POST v1/coupons — 新建单张优惠券
- 状态：现有 `panel/internal/api/admin/coupon.go:132 createCoupon`
- 权限：`marketing.coupon.write`｜reauth：是｜幂等：否（靠 code 唯一约束防重）
- 请求：`{ code:string(自动转大写，必填), name?:string(默认 = code), discount_type:"percent"|"fixed", discount_value:int64(percent 用万分比 1..10000；fixed 用分，≥1), currency?:"CNY"(默认)|"USD", max_discount?:int64|null, min_order_amount?:int64, max_redemptions?:int|null(null = 不限), max_redemptions_per_user?:int(默认 1), applicable_plan_ids?:uuid[], valid_from?:RFC3339|"YYYY-MM-DDTHH:MM", valid_until?:同上 }`
- 响应：**200**（不是 201）`{ id, code }`
- 错误：422 `code` / `discount_type` / `discount_value`；422「时间格式不正确」/「结束时间必须晚于开始时间」（不带 fields）；409「这个优惠码已经存在」
- 设计：后台-06「新建优惠券」内联表单：优惠码 → `code`；类型 pct / off → `percent` / `fixed`；数值：pct 填 15 → `discount_value: 1500`，off 填 ¥10 → `1000`；「每码可用次数」→ `max_redemptions`。待补·前端：补「有效期至」（列表有这一列，表单没有）、「适用套餐」多选、「每人限用」、「门槛金额」、「封顶优惠」（percent 时）。

#### POST v1/coupons/batch — 批量生成优惠券
- **修订 R5（2026-09-24）**：幂等改为「是」，scope `coupon_batch_generate`。
- 状态：现有 `panel/internal/api/admin/coupon_batch.go:76 generateCoupons`
- 权限：`marketing.coupon.write`｜reauth：是｜幂等：否（批量写操作按惯例应该幂等，见核对笔记）
- 请求：`{ count:1..1000, prefix?:string(大写字母数字，≤8 位), name:string(必填，同批共用), 以及 POST v1/coupons 除 code 以外的全部字段 }`；`max_redemptions` 默认 1（每码一次）
- 响应：200 `{ count:int, codes:string[], name }`
- 错误：422 `count` / `prefix` / `name` 及折扣校验；500 反复撞码
- 设计：后台-06「批量生成」表单：码前缀 → `prefix`，数量 → `count`（设计上限 500，后端 1000，前端按 500 限制即可），类型 / 数值 / 每码可用次数同上。待补·前端：补必填的「活动名称」→ `name`（批次靠它找回）。生成后可以把 `codes` 下载成 CSV（优惠码不是等价现金，没有一次性可见的要求）。

#### POST v1/coupons/{id}/status — 启停优惠券
- 状态：现有 `panel/internal/api/admin/coupon.go:267 setCouponStatus`
- 权限：`marketing.coupon.write`｜reauth：是｜幂等：否
- 请求：`{ status:"active"|"paused" }`
- 响应：200 `{ ok:true }`
- 错误：422 status 不合法；404 不存在（id 不是 uuid 时是 500）
- 设计：后台-06 行内启用开关。设计里点开关直接切换，没有 reauth 确认；后端要求 reauth，前端要接入统一的重认证流程。

> 优惠券「编辑」：设计 06 里**没有**编辑入口（只有新建、批量、启停、兑换记录），后端不需要补。

#### GET v1/gift-cards — 礼品卡模板列表
- 状态：现有 `panel/internal/api/admin/giftcard.go:15 listGiftTemplates`
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：无（不含 archived）
- 响应：200 `{ templates:[{ id, name, description, type:"general"|"plan"|"mystery", status:"active"|"paused"|"archived", rewards:{ balance?, traffic_bytes?, expire_days?, reset_quota?, plan_id?, price_id?, pool?:[{label, weight, balance?, traffic_bytes?, expire_days?}] }, conditions:{ new_user_only?, paid_user_only?, allowed_plan_ids?, require_invite? }, limits:{ max_use_per_user?, cooldown_hours? }, theme_color, code_total:int, code_used:int, created_at }] }`
- 错误：无
- 设计：后台-06 礼品卡「模板」卡片：面额（general：balance → ¥，traffic_bytes → GB，expire_days → 天；plan：套餐名；mystery：「盲盒」）、类型标签、名称、「兑换后」说明（balance「直接入账」/ plan「立即开通」/ traffic「当期有效」）、「已发 `code_total` 张」。

#### GET v1/gift-cards/stats — 礼品卡统计
- 状态：现有 `panel/internal/api/admin/giftcard.go:163 giftCardStats`；待补·后端（扩展）
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ templates:int, codes_total:int, codes_used:int, codes_unused:int, balance_out:int64(已兑出的余额，CNY 分), traffic_out:int64(已兑出的流量，字节) }`。待补·后端（扩展）加 `balance_issued:int64`（已发行的 general 卡码面额合计 = Σ 每批码数 × 模板 rewards.balance，CNY 分），不需迁移
- 错误：无
- 设计：后台-06 礼品卡四个统计：已发行 `codes_total`、已兑换 `codes_used`、兑换率 = used ÷ total（前端算）、面额合计 `balance_issued`（扩展前可以先用 `balance_out`，同时把标签改成「已兑出余额」）。

#### GET v1/gift-cards/codes — 卡码列表
- 状态：现有 `panel/internal/api/admin/giftcard.go:92 listGiftCodes`；待补·后端（改：掩码）
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：`template_id?:uuid, status?:""|"unused"|"used"|"disabled"|"expired", batch_id?:uuid, limit?:1..5000(默认 50), offset?:int`
- 响应（现有）：200 `{ codes:[{ id, code(**完整明文**), status, batch_id?, expires_at?, used_email?, used_at?, created_at, template_id }], total:int64 }`
- 响应（改后）：把 `code` 换成 `code_masked: string`（保留前缀 + 随机段前 4 位，其余用 `•` 代替，例如 `GCH2K9••••••••`），接口不再返回明文；其余字段不变
- 错误：400 status 不在白名单或 uuid 格式不对
- 设计：后台-06「批次与卡码」右侧码列表：码 `code_masked`、状态（unused「可用」/ used「已兑换」/ disabled「已停用」/ expired「已过期」，最后一项设计缺，待补·前端）、兑换人 `used_email`、行内启停。

#### GET v1/gift-cards/codes/export — 导出卡码 CSV（现状：可反复导出明文）
- 状态：现有 `panel/internal/api/admin/giftcard.go:113 exportGiftCodes`；待补·后端（下线，由 POST v1/gift-cards/batches/{batch_id}/export 取代）
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：`template_id?, status?, batch_id?`（最多 5000 行）
- 响应：200 `text/csv; charset=utf-8`，带 UTF-8 BOM，`Content-Disposition: attachment; filename="gift-codes.csv"`，`Cache-Control: no-store`；表头「卡密,状态,有效期,使用者,使用时间」，卡密是完整明文
- 错误：400 同上
- 设计：现有行为是任何有只读权限的管理员都能无限次导出全部明文码，没有重认证，也不写审计。这与设计「一次性导出、导出后只能看掩码」直接冲突。按设计改：新接口上线后这条路由删除（或固定回 404），前端不再调用。

#### GET v1/gift-cards/batches — 批次列表
- 状态：待补·后端
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：`template_id?:uuid, limit?:1..200(默认 50), offset?:int>=0`
- 响应：200 `{ items:[{ id:uuid(= gift_card_codes.batch_id), template_id, template_name, prefix:string, count:int, used:int, disabled:int, expires_at|null, created_by_email|null, created_at, exported_at|null, exported_by_email|null }], total:int }`
- 错误：400 uuid 格式不对
- 设计：后台-06「批次与卡码」左栏：批次号（前端取 id 前 8 位，或按 `created_at` 生成 `GB-MMDD` 显示名）、`used/count`、模板名 · 日期、`exported_at` 为空 →「未导出 · 完整卡码仍可一次性导出」，否则「已导出」。
- 需迁移：新表 `gift_card_batches(id uuid PK, tenant_id, template_id FK, prefix text, count int, expires_at, created_by uuid, created_at, exported_at timestamptz NULL, exported_by uuid NULL)`，开启租户 RLS；按 `gift_card_codes.batch_id` 分组回填存量批次（存量批次的 `exported_at` 怎么填见 D-C-4）；GenerateCodes 同一事务里写批次行；gift_card_codes.batch_id 补指向新表的外键。

#### POST v1/gift-cards/batches/{id}/export — 一次性导出批次明文卡码
- 状态：待补·后端
- 权限：`marketing.giftcard.write`｜reauth：是｜幂等：是 `giftcard_batch_export`（同 key 重放必须返回同一份 CSV，也就是在幂等窗口内允许重新下载同一次导出的结果）
- 请求：`{}`
- 响应：200 `text/csv; charset=utf-8`（带 BOM，`Content-Disposition: attachment; filename="gift-codes-<batch前8位>.csv"`，`Cache-Control: no-store`），列：卡密（明文）、状态、有效期、模板名；同一事务里设置 `exported_at = now()`、`exported_by`，并写审计 `gift_card.batch_exported`
- 错误：404 批次不存在；409「该批次已导出，完整卡码不可再次获取」（`exported_at` 已有值）
- 设计：后台-06 批次右上「一次性导出」按钮（reauth），以及生成后的「仅此一次可见」弹窗里的「导出 CSV」（直接调本接口，成功后批次标记为已导出）。
- 需迁移：用上一条的 `gift_card_batches.exported_at` / `exported_by`（不另外计数）。

#### GET v1/gift-cards/usages — 兑换记录
- 状态：现有 `panel/internal/api/admin/giftcard.go:172 listGiftUsages`；待补·后端（改：掩码）
- 权限：`marketing.giftcard.read`｜reauth：否｜幂等：否
- 请求：`template_id?:uuid`（最近 200 条）
- 响应：200 `{ usages:[{ template_name, code(明文 → 改为 code_masked), user_email, granted:{ balance?, traffic_bytes?, expire_days?, reset_quota? }, prize_label?, redeemed_at }] }`
- 错误：400 uuid 格式不对
- 设计：后台-06「使用记录」：码 / 用户 / 获得内容（由 granted 拼：「余额 +¥100」「流量 +100 GB」「+30 天」；plan 卡 granted 里没有套餐信息，用 template_name 代替；mystery 显示 `prize_label`）/ 时间。

#### POST v1/gift-cards — 新建或保存礼品卡模板
- **修订 R4（2026-09-24）**：reauth 改为「是」。
- 状态：现有 `panel/internal/api/admin/giftcard.go:36 saveGiftTemplate`
- 权限：`marketing.giftcard.write`｜reauth：否｜幂等：否
- 请求：`{ id?:uuid(留空 = 新建，有值 = 更新), name:string(1..120), description?:string, type:"general"|"plan"|"mystery"(更新时不能改), status?:"active"(默认)|"paused"|"archived", rewards:{…同上}, conditions:{…}, limits:{…}, theme_color?:string }`
- 响应：200 `{ template: Template }`
- 错误：422 `name`（同名也在这里报）/ `status` / `rewards`（例如「通用卡至少要送一样东西…」「盲盒至少要有 2 个奖品」）/ `limits` / `conditions`（新用户与付费用户互斥）；404 更新的 id 不存在；409 修改卡型
- 设计：后台-06「＋ 新建模板」。设计点一下就建出一个固定内容的占位模板，这样不行（后端必须有真实奖励才能保存）。待补·前端：补一个模板编辑抽屉，包含类型、名称、说明、奖励（general：余额 / 流量 / 延长天数 / 重置流量；plan：套餐 + 价格；mystery：奖池的名称、权重、奖励）、领取条件（仅新用户 / 仅付费用户 / 限定套餐 / 必须被邀请）、限制（每人次数 / 冷却小时）、主题色、状态（暂停 / 归档）。卡片上的「编辑」也打开这个抽屉。

#### POST v1/gift-cards/{id}/codes — 为模板生成一批卡码
- 状态：现有 `panel/internal/api/admin/giftcard.go:63 generateGiftCodes`；待补·后端（改响应）
- 权限：`marketing.giftcard.write`｜reauth：是｜幂等：是 `giftcard_codes_generate`
- 请求：`{ count:1..5000, prefix?:string(大写字母数字，≤8 位), expires_at?:RFC3339(不能早于现在) }`
- 响应（现有）：200 `{ batch_id:uuid, count:int, codes:string[](全部明文) }`
- 响应（改后）：200 `{ batch_id, count, sample:string[](前 4 张的明文，只在这次响应里出现), batch:<GET batches 的 item> }`，不再返回全部 `codes`；完整明文只能通过一次性导出获得
- 错误：422 `count` / `prefix` / `expires_at`；404 模板不存在；409「已归档的礼品卡不能再生成新码」
- 设计：后台-06 模板卡「生成一批码」。待补·前端：补数量、前缀、有效期三个输入（设计固定写死 100 张）。生成后弹出「仅此一次可见」：显示 `sample` 加「…」；「导出 CSV」调 POST v1/gift-cards/batches/{batch_id}/export；「已保存，关闭」只是关闭弹窗，批次保持未导出，之后在批次列表里还能导出一次。

#### POST v1/gift-cards/codes/{id}/toggle — 停用 / 恢复单个卡码
- 状态：现有 `panel/internal/api/admin/giftcard.go:148 toggleGiftCode`
- 权限：`marketing.giftcard.write`｜reauth：否（有意设计：发现异常时要能立刻止血）｜幂等：否
- 请求：`{ disabled:bool }`
- 响应：200 `{ disabled:bool }`
- 错误：404；409 已兑换（「要收回权益请走调账或工单」）/ 已过期 / 当前状态不需要该操作
- 设计：后台-06 码行「停用 / 启用」；已兑换的码禁用按钮（与设计一致）。

#### GET v1/commission/overview — 分销总览与当前配置
- **修订 R6（2026-09-24）**：权限改为 `marketing.commission.read`。
- 状态：现有 `panel/internal/api/admin/commission.go:251 commissionOverview`；待补·后端（扩展）
- 权限：`billing.order.read`｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ pending:int64, available:int64, paid_out:int64, this_month:int64, entries:int, need_review:int, waiting_withdrawals:int, rate_percent:int, freeze_days:int, min_withdraw:int64(分) }`（金额不分币种，直接跨币种相加，与挂账是同类问题；佣金实际上基本是 CNY）
- 响应（扩展后）：追加 `total_earned:int64`（status ∉ {reversed, rejected} 的 commission_amount 合计）、`invited_users:int`（referrals 表行数）、`scope:"first_order"|"every_order"`
- 错误：无
- 设计：后台-06「佣金与提现」四个统计：累计佣金 `total_earned`、冻结中 `pending`、已提现 `paid_out`、邀请注册 `invited_users`；设置表单的初值：`rate_percent`、`scope`、`freeze_days`、`min_withdraw ÷ 100`。

#### GET v1/withdrawals — 提现申请列表
- **修订 R6（2026-09-24）**：权限改为 `marketing.commission.read`。
- 状态：现有 `panel/internal/api/admin/commission.go:28 listWithdrawals`
- 权限：`billing.order.read`｜reauth：否｜幂等：否
- 请求：`status?: "requested"|"reviewing"|"approved"|"rejected"|"processing"|"paid"|"failed"|"returned"`（精确匹配，不分页，最近 200 条）
- 响应：200 `{ withdrawals:[{ id, email, user_id, amount:int64, currency, status, payout_detail:string(解密后的收款信息), reject_reason:string, requested_at, completed_at|null, earned_total:int64 }] }`
- 错误：无
- 设计：后台-06 提现列表。状态映射：`requested`/`reviewing` →「待审核」，`approved`/`processing` →「待打款」，`paid` →「已打款」，`rejected` →「已拒绝」，`failed`/`returned` →「打款失败 / 已退回」（设计缺，待补·前端）。收款信息 `payout_detail`；建议在金额旁显示 `earned_total`（后端专门留出来核对提现是否合理的字段，设计缺，待补·前端）。

#### POST v1/withdrawals/{id}/review — 审批提现
- **修订 R6（2026-09-24）**：权限改为 `marketing.withdrawal.approve`。
- 状态：现有 `panel/internal/api/admin/commission.go:89 reviewWithdrawal`
- 权限：`billing.provider.write`｜reauth：是｜幂等：否
- 请求：`{ action:"approve"|"reject", reason?:string(reject 时必填；approve 时必须为空) }`
- 响应：200 `{ status:"approved"|"rejected" }`
- 错误：422 action 不合法（不带 fields）；422 `reason`；409「该提现申请已被处理过」
- 设计：后台-06「通过 / 拒绝」。设计里「通过」没有确认框，后端要求 reauth，前端要走重认证；「拒绝」确认框补必填的拒绝理由（待补·前端）。设计写的「金额退回用户佣金余额」：拒绝时只改状态、不动账（钱在打款之前一直没离开账本），文案可以保留。

#### POST v1/withdrawals/{id}/paid — 记录已打款（动账）
- **修订 R6（2026-09-24）**：权限改为 `marketing.withdrawal.approve`。
- 状态：现有 `panel/internal/api/admin/commission.go:165 markWithdrawalPaid`
- 权限：`billing.provider.write`｜reauth：是｜幂等：是 `commission_withdrawal_mark_paid`
- 请求：`{ payout_reference:string(必填，转账流水号) }`
- 响应：200 `{ status:"paid" }`
- 错误：422 `payout_reference`；409「只有已批准的提现才能标记为已打款」/「提现状态已变化」
- 设计：后台-06「打款」确认框。待补·前端：补必填的「转账流水号」。

#### POST v1/commission/config — 修改分销参数
- **修订 R4 / R6（2026-09-24）**：reauth 改为「是」；权限改为 `marketing.commission.write`（迁移 00068 新增，授给当前持有 `billing.provider.write` 的角色）。
- 状态：现有 `panel/internal/api/admin/commission.go:312 setCommissionConfig`；待补·后端（扩展：计佣范围）
- 权限：`billing.provider.write`｜reauth：否｜幂等：否
- 请求（现有）：`{ rate_percent?:0..50, freeze_days?:0..90, min_withdraw?:int64>=0(分) }`（字段可省，省略的不改）
- 请求（扩展后）：追加 `scope?: "first_order"|"every_order"`，存为 system_settings `commission.scope`（字符串 jsonb；现有写入函数固定写 `to_jsonb(bigint)`，需要分开处理），没有这条设置时代码按 `every_order` 兜底（与现行为一致）。`accrueCommission` 里判断：scope 为 first_order 且被推荐人此前已有计提记录（或已支付订单）时不计提
- 响应：200 `{ ok:true }`
- 错误：422 `rate_percent` / `freeze_days` / `min_withdraw` / `scope`
- 设计：后台-06「邀请与佣金设置」：返佣比例滑块 0–50 → `rate_percent`（范围与后端一致）；计佣范围「仅首单 / 每笔订单」→ `scope`；结算冻结期 → `freeze_days`；最低提现 ¥ → `min_withdraw ×100`。
- 需迁移：否（新 key 走代码兜底；如果要让 value_schema 出现在系统设置页，加一条种子数据迁移，不建表不改表）。

### 后台-07 节点与服务器 · 节点（节点 tab + 节点详情抽屉）

#### GET v1/nodes — 节点列表（含运营聚合）
- 状态：现有 `panel/internal/api/admin/handlers.go:703 nodeList`；**待补·后端（字段扩展 + 排序，无迁移，cc 除外）**
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：query `include_retired?: "1"`（默认不含 serving_status=retired；永远不含 status=destroyed）。无分页、无服务端筛选，硬上限 200 条。
- 响应：200 `{ nodes: Node[], total: int }`，`Node = { id: uuid, node_no: int, row_version: int64, name, status: 生命周期态, serving_status: "draft"|"active"|"draining"|"disabled"|"retired", server_id?: uuid, server_name?: string, pool_id?: uuid, pool_name?: string, agent_version?, hostname?, public_ipv4?, cpu_cores?, memory_mb?, disk_gb?, health_score?: int, applied_config_version?: int, desired_config_version?: int, last_heartbeat_at?: time, stale: bool(心跳>90s或从未心跳), delivered_to_users: bool, delivery_note: string, identity_serial?: int, created_at, node_type?: string, server_host?: string, server_port?: int, traffic_rate: float, display_name?: string, kernel: string, protocol_config: object(敏感键已脱敏), protocol_schema_version: int, config_validated_at?: time, sort_order: int, online_users: int, online_ips: int, traffic_bytes: int64(近30天), granted_plans: string[] }`
- 待补·后端：
  - 加字段 `traffic_bytes_24h: int64`（node_traffic_reports 近 24h 上下行合计，原始量）——设计列是「24h 流量」。
  - 加字段 `cpu_percent?: float`、`mem_percent?: float`、`metrics_at?: time`：取该节点所在服务器 control_node 最近一条 node_metrics（与 `servers.cpu_bp` 同源）；无探针为 null。
  - 加字段 `country_code?: string(2)`（见迁移；管理员在创建/编辑表单填写，可空）。**只进 admin 响应，public 任何接口不得输出（保留规则 3）。**
  - 排序改为 `ORDER BY sort_order, node_no`（现为 created_at DESC，与「调整排序」模式不一致）。
- 错误：无特有（查询失败统一 500）。
- 设计：后台-07 节点表格与抽屉头。映射：`n.cc` → `country_code`；`n.name` → `name`（`display_name` 是给用户看的名字，放进编辑表单）；`n.addr` → `server_host:server_port`；`n.proto` → `node_type`；`n.server` → `server_name`；`n.online` → `online_users`（可在悬停中补 `online_ips`）；`n.cpu` → `cpu_percent`；`n.traffic` → `traffic_bytes_24h`；心跳 → `last_heartbeat_at`。状态映射：`serving_status=active && !stale` → 在线；`active && stale` → 离线；`draining` → 在线（标「排空中」）；`draft|disabled` → 已停用（draft 标「草稿」）；`retired` → 已退役（需 `include_retired=1`）。筛选/搜索按设计在前端做。**待补·前端**：在行尾或抽屉头展示 `delivered_to_users=false` 时的 `delivery_note`（交付提示），抽屉「监控」补 `online_ips`、`health_score`、`applied/desired_config_version`、`agent_version`、`granted_plans`、`pool_name`。

#### POST v1/nodes — 新建节点（草稿）
- 状态：现有 `panel/internal/api/admin/node_admin.go:20 createAdminNode` → `nodefabric/node_admin.go:286 CreateAdminNode`
- 权限：`node.provision`｜reauth：否｜幂等：是 `node_create`
- 请求：`{ name: string(1–120), server_id: uuid, node_type: string(13 个稳定协议之一), server_host: string(IP 或 ASCII 主机名), server_port: int, protocol_config: object(按 node-protocol-schemas), pool_id?: uuid, kernel?: string(默认 "auto"), traffic_rate?: float(<=0 视为 1), display_name?: string, sort_order?: int }`；待补·后端：加 `country_code?: string(2)`。
- 响应：201 `AdminNode = { id, row_version, name, server_id, pool_id, status:"draft", serving_status:"draft", node_type, server_host, server_port, kernel, traffic_rate, display_name, protocol_config(脱敏), protocol_schema_version, config_validated_at, sort_order, created_at, updated_at, warnings?: string[] }`
- 错误：422 name/server_id/pool_id/server_host/node_type/协议字段（fields 键为协议字段名）；409 服务器 retired/quarantined「目标服务器当前不接受节点」；409 容量满（fields.capacity_nodes="used/cap"）；409 节点名称已存在。
- 设计：后台-07「新建节点」。映射：设计是先生成空白草稿再填协议，后端不允许无协议的草稿。**待补·前端**：「新建节点」改为弹窗/抽屉表单，一次收集 名称、服务器（GET v1/servers）、协议类型、地址、端口、协议参数（schema 驱动）、资源池、内核、倍率、展示名、国家；提交成功后打开抽屉，并提示「节点已创建为草稿，签发安装令牌并启用后才会下发」。

#### PATCH v1/nodes/{id} — 编辑节点基本信息与协议参数
- 状态：现有 `panel/internal/api/admin/node_admin.go:35 patchAdminNode` → `nodefabric/node_admin.go:369 PatchAdminNode`
- 权限：`node.write`｜reauth：否｜幂等：否
- 请求：`{ row_version: int64, name?: string, pool_id?: uuid|null(显式 null 或 "" = 清空), node_type?: string, server_host?: string, server_port?: int, kernel?: string, traffic_rate?: float(>0), display_name?: string, protocol_config?: object }`（省略=不改）；待补·后端：加 `country_code?: string|null`。
- 响应：200 `AdminNode`（同上）
- 错误：422 row_version/name/pool_id/traffic_rate/协议字段；409 版本冲突；409 名称重复；404 节点不存在。
- 设计：后台-07 抽屉「协议参数」→「保存协议参数」。映射：设计字段 `uuid`/`password`（每节点凭据）后端没有——用户凭据按订阅下发，协议表单只渲染 schema 的 `allowed_properties`，`sensitive_properties` 用 password 输入并标「敏感」；设计「schema v3」→ 后端 `protocol_schema_version`（当前稳定版为 1）。设计确认框写「保存后需要发布配置」——实际 PATCH 成功后自动推送到在线节点（`notifyNodeChanged`），前端文案改为「保存后自动下发」。**待补·前端**：「协议参数」tab 顶部补「基本信息」区：名称、展示名、资源池、内核、流量倍率、地址、端口、国家（后端可编辑字段全集）。

#### POST v1/nodes/{id}/copy — 复制节点
- 状态：现有 `panel/internal/api/admin/node_admin.go:55 copyAdminNode` → `nodefabric/node_admin.go:472 CloneAdminNode`
- 权限：`node.provision`｜reauth：否｜幂等：是 `node_copy`
- 请求：`{ row_version: int64(源节点), name?: string(默认 "<原名>-copy"), target_server_id?: uuid(默认原服务器), pool_id?: uuid(默认原池), copy_routing?: bool, sort_order?: int(默认原值+1) }`
- 响应：201 `AdminNode`（新节点，status/serving_status 均为 draft）；源为 legacy v0 协议时 `warnings` 提示需重选协议。
- 错误：409 版本冲突；409「原节点未绑定服务器」；409 容量满/服务器不接受；409 名称重复；422 target_server_id/pool_id/name。
- 设计：后台-07 抽屉「操作 › 复制节点」。映射：设计副本名「X 副本」→ 前端传 `name: "<原名> 副本"`；设计「停用节点」→ 后端 draft。**待补·前端**：复制确认框补「目标服务器」「复制路由规则」两个选项（这也是已部署节点换机器的正确路径，见 move）；展示 `warnings`。

#### POST v1/nodes/{id}/move — 迁移到其他服务器（仅未部署草稿）
- 状态：现有 `panel/internal/api/admin/node_admin.go:75 moveAdminNode` → `nodefabric/node_admin.go:576 MoveAdminNode`
- 权限：`node.provision`｜reauth：否｜幂等：是 `node_server_move`
- 请求：`{ server_id: uuid, row_version: int64, reason?: string(≤500) }`
- 响应：200 `AdminNode`（目标即当前服务器时直接返回原节点，不报错）
- 错误：422 row_version/server_id/reason；409 版本冲突；409「活动或排空中的节点不能移动」（serving_status active/draining）；409「节点仍绑定控制面或 Agent 资产」，fields 逐项计数：control_node, active_identities, runtime_instances, metrics, pending_tasks, provisioning, bootstrap_tokens, config_applications, usage_sources, usage_batches, traffic_reports, active_server_token；409 目标容量满/不接受节点。
- 设计：后台-07 抽屉「操作 › 迁移到其他服务器」。**保留规则 5**：只能迁移未部署的草稿节点。前端规则：仅当 `serving_status ∈ {draft, disabled}` 且 `last_heartbeat_at == null` 且 `identity_serial == null` 时启用「迁移」按钮，否则禁用并显示提示「只能迁移从未部署过的草稿节点。已部署节点请用『复制节点』选目标服务器，再退役原节点」；后端仍回 409 时把 fields 里非 0 的项列给管理员。设计文案「保留节点 ID 与用户订阅」「迁移期间约 30 秒不可用」删除（草稿节点没有用户）。

#### PUT v1/nodes/order — 批量排序
- 状态：现有 `panel/internal/api/admin/node_admin.go:95 reorderAdminNodes` → `nodefabric/node_admin.go:678 ReorderAdminNodes`
- 权限：`node.write`｜reauth：否｜幂等：否
- 请求：`{ items: [{ id: uuid, row_version: int64, sort_order: int }] }`（1–200 项，id 唯一）
- 响应：200 `{ ok: true, updated: int }`（每个节点 row_version +1，前端需重新拉 GET v1/nodes）
- 错误：422 items；409 任一版本冲突（整批不落库）；404 任一节点不存在。
- 设计：后台-07「调整排序 / 完成排序」。点「完成排序」时一次提交整页顺序（sort_order 用 10、20、30…）。

#### POST v1/nodes/status:batch — 批量改服务状态（启用/停用/退役）
- **修订 R10（2026-09-24，后端二 62f7283）**：幂等 scope 改为 `node_status_batch`（与别名路由共用）。
- 状态：现有 `panel/internal/api/admin/node_admin.go:109 batchAdminNodeStatus` → `nodefabric/node_admin.go:737 BatchAdminNodeLifecycle`
- 权限：`node.lifecycle`｜reauth：否｜幂等：是 `node_status_batch`
- 请求：`{ items: [{ id: uuid, row_version: int64 }](1–100), serving_status: "draft"|"active"|"draining"|"disabled"|"retired", reason?: string(≤500) }`
- 响应：200 `{ ok: true, updated: int, serving_status: string }`
- 错误：422 items/serving_status/reason；409 版本冲突；409「不允许的节点服务状态转换」fields.serving_status="from -> to"（合法边：draft→active/disabled/retired；active→draining/disabled；draining→active/disabled/retired；disabled→draft/active/retired；retired 终态）；409「节点协议或服务器状态未满足启用条件」（启用要求服务器 status=ready 且协议通过稳定 schema 校验）；409 退役时仍有控制面/有效身份/在途任务。任一失败整批不落库。
- 设计：后台-07 批量条「启用 / 停用」、抽屉「操作 › 启用节点 / 停用节点」（单节点也走本接口，items 只放一个）。映射：设计「启用」→ `active`；「停用」→ `disabled`。**前端统一用本路径**，不用下面的别名。

#### POST v1/nodes/batch/status — 同上（别名）
- **修订 R10（2026-09-24，后端二 62f7283）**：幂等 scope 改为 `node_status_batch`（与 `status:batch` 共用）。
- 状态：现有，与 status:batch **同一处理器** `node_admin.go:109 batchAdminNodeStatus`，路由 router.go:664
- 权限：`node.lifecycle`｜reauth：否｜幂等：是 `node_batch_status`（与 status:batch 的 scope **不同**，同一 Idempotency-Key 跨两条路径不会去重）
- 请求/响应/错误：与 `POST v1/nodes/status:batch` 完全相同。
- 设计：不使用。status:batch 是 e2e（`panel/tests/uniproxy_e2e.sh`）使用的正式路径；本别名是后补的重复入口。

#### GET v1/node-protocol-schemas — 协议表单 schema
- 状态：现有 `panel/internal/api/admin/handlers.go:1428 nodeProtocolSchemas` → `nodefabric/protocol_schema.go:59 ProtocolSchemas`
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ schemas: [{ node_type: string, version: int, status: "stable", required: string[], allowed_properties: string[](点号路径如 "obfs.password"), methods?: string[], enums?: {字段: string[]}, property_types?: {字段: "number"|"boolean"|"json"}, sensitive_properties?: string[] }] }`
- 错误：无
- 设计：后台-07 抽屉「协议参数」表单「字段来自后端协议定义」。映射：设计 `f.req` → `required`；`f.sens` → `sensitive_properties`；选择型字段用 `enums`/`methods` 渲染下拉；点号路径提交时展开为嵌套对象。设计 SCHEMA 里的 port 不在 schema 内，对应节点顶层 `server_port`。

#### POST v1/nodes/bootstrap-token — 签发一键安装令牌
- 状态：现有 `panel/internal/api/admin/handlers.go:882 nodeIssueToken` → `nodefabric/service.go:197 IssueBootstrapToken`
- 权限：`node.provision`｜reauth：是｜幂等：是 `node_bootstrap_token_issue`
- 请求：`{ node_name: string(1–120，已存在同名节点则绑定到该节点), server_id?: uuid, pool_id?: uuid, ttl_minutes?: int(1–30，越界或缺省=20) }`
- 响应：201 `{ token: string(仅此一次), expires_at: time, install_command: string }`
- 错误：422 node_name/pool_id/server_id；409「已退役或销毁节点不能签发引导令牌」；409「引导令牌资源池与目标节点不匹配」；422 pool 不存在或已禁用；404「服务器不存在、已删除或已退役」。
- 设计：后台-07 抽屉「身份与令牌 › 签发一键安装令牌」。前端传 `node_name: 节点 name, server_id: 节点 server_id, ttl_minutes: 30`（设计文案「30 分钟内有效」）。映射：设计把令牌拼在命令里；后端命令从终端读令牌（不进 argv/history），前端要**分两块显示**：令牌（可复制，仅显示一次）+ 安装命令，并提示「执行命令后粘贴上面的令牌」。

#### POST v1/nodes/reality-keypair — 生成 REALITY 密钥对
- 状态：现有 `panel/internal/api/admin/handlers.go:1484 nodeRealityKeypair`
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无请求体
- 响应：200 `{ private_key: string, public_key: string, short_id: string(8 位 hex), hint: string }`
- 错误：无特有
- 设计：后台-07 抽屉「协议参数 › 生成 REALITY 密钥对」。仅当 node_type=vless 且 tls=2(REALITY) 时启用。映射：设计 short_id 16 位 → 后端 8 位；生成结果只填进表单，保存才落库；私钥保存后读接口不回显。

#### DELETE v1/nodes/{id} — 删除（销毁）节点
- 状态：现有 `panel/internal/api/admin/handlers.go:1511 nodeDelete` → `nodefabric/node_admin.go:876 DeleteNode`
- 权限：`node.lifecycle`｜reauth：是｜幂等：否
- 请求：body 必带 `{ row_version?: int64(0 或省略=不校验), reason?: string }`
- 响应：200 `{ deleted: true }`
- 错误：404「节点不存在」；409 版本冲突；422「节点还在服务中」（serving_status active/draining）；422 在役服务器的控制节点；422 节点端仍有 active 运行实例；422「已经销毁过了」；422「节点当前状态不能直接销毁，请先将其退役」（生命周期 status 不在 draft/provisioning_failed/bootstrap_failed/retired/destroy_failed）。
- 设计：后台-07 抽屉「操作 › 删除节点」。映射：设计说「彻底删除，历史数据一并移除」——后端是**转终态**（status=destroyed，改名为 `name#destroyed-时间戳` 让出名字，流量/审计保留），前端文案改为「从列表移除，名字可复用；历史流量与审计保留」。

#### POST v1/nodes/{id}/status — 旧生命周期状态机推进（兼容）
- 状态：现有 `panel/internal/api/admin/handlers.go:969 nodeSetStatus`
- 权限：`node.lifecycle`｜reauth：否｜幂等：是 `node_legacy_status`
- 请求：`{ row_version: int64, status: 生命周期态, reason?: string }`
- 响应：200 `{ ok: true, row_version: int64 }`
- 错误：404「节点不存在」；422 row_version；409 版本冲突；409 非法跳转（DB 触发器 check_violation 的消息）。status 为 retired/destroyed 时会顺带吊销有效身份，并按映射改服务器状态。
- 设计：不直接使用。新前端的「退役」走下面的待补接口；本接口只在「服务器详情」高级区保留（不做）。

#### POST v1/nodes/{id}/retire — 退役节点（两套状态机一步到位）
- 状态：**待补·后端**（新接口，放 handlers.go 节点区或 node_admin.go）
- 权限：`node.lifecycle`｜reauth：是｜幂等：是 `node_retire`（不可逆）
- 请求：`{ row_version: int64, reason?: string(≤500) }`
- 响应：200 `AdminNode`（serving_status=retired，status=retired）
- 行为：在一个事务里：持 `node-config-release` 锁 → 校验版本 → 拒绝在役服务器的控制节点（409）→ 生命周期按合法边推进到 retired（active/canary/unhealthy/upgrade_failed 先经 draining；draft 直接置 serving retired、生命周期保持 draft 以便 DELETE 直接销毁）→ serving_status=retired、desired_config_version=NULL → 吊销有效身份、在途任务置 failed → 审计 `node.retire`。
- 错误：404；409 版本冲突；409 控制节点；409 已退役。
- 需迁移：否。
- 设计：后台-07 抽屉「操作 › 退役节点」（「永久下线但保留历史流量与审计」「退役后不可再启用」）。原因：现有 status:batch 退役遇到有效身份会 409，且只改 serving_status，之后 DELETE 仍因生命周期未退役而 422——设计「退役→删除」两步走不通。

#### POST v1/nodes/{id}/revoke-identity — 吊销节点身份
- 状态：现有 `panel/internal/api/admin/handlers.go:1082 nodeRevokeIdentity`
- 权限：`node.identity.revoke`｜reauth：是｜幂等：否
- 请求：无请求体
- 响应：200 `{ ok: true }`
- 错误：404「该节点没有有效身份」
- 设计：后台-07 抽屉「身份与令牌 › 吊销节点身份」。映射：设计吊销后把节点标「离线」——后端只吊销身份，不改 serving_status；前端吊销后重新拉列表，由心跳 stale 自然变离线。

#### POST v1/nodes/config/publish — 发布一层配置（global/pool/node）
- 状态：现有 `panel/internal/api/admin/handlers.go:1120 nodePublishConfig` → `nodefabric/service.go:1354 PublishConfig`
- 权限：`node.config.publish`｜reauth：是｜幂等：是 `node_config_publish`
- 请求：`{ scope: "global"|"pool"|"node", scope_ref?: uuid(pool/node 必填且须规范小写，global 必须为空), payload: object }`（payload 不得含保留键 protocol/server_port/host/server_name/kernel/kernel_type/base_config/outbounds/routes/custom_outbounds/custom_route_rules——这些由节点字段与路由表生成）
- 响应：201 `{ config_id: uuid, version: int, scope: string, affected_nodes: int }`
- 错误：422 scope/scope_ref/payload；404 pool/node 不存在或已退役；409「节点配置版本状态异常」。
- 设计：后台-07 抽屉「操作 › 发布配置」→ 前端传 `{ scope: "node", scope_ref: 节点 id, payload: {} }`，toast 用返回的 `version`（设计「版本 #N」）。注意：它会**替换**该节点已有的 node 层叠加配置（当前没有任何界面写入 node 层 payload，所以传 `{}` 等于「强制重新下发当前协议与路由」）。设计路由页「发布到全部节点」**不用**本接口，见 PUT v1/nodes/routing。

#### GET v1/nodes/{id}/metrics — 节点探针曲线
- 状态：现有 `panel/internal/api/admin/handlers.go:1469 nodeMetrics` → `nodefabric/service.go:1541 FetchMetrics`
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：query `minutes?: int`（1–1440，越界或缺省=60）
- 响应：200 `{ points: [{ at: time, cpu_percent: float, mem_percent: float, load1: float, rx_speed: int64(B/s), tx_speed: int64(B/s), tcp_conns: int }], latest: null | { cpu_percent, mem_used_mb, mem_total_mb, disk_used_gb, disk_total_gb, load1, load5, load15, tcp_conns, uptime_sec, rx_total, tx_total } }`
- 错误：节点不存在返回 200 空 points（不回 404）；非法 UUID 回 500（见核对笔记）。
- 设计：后台-07 抽屉「监控」。映射：KPI「CPU」→ `latest.cpu_percent`，「内存」→ `mem_used_mb/mem_total_mb`；「带宽 · 近 24 小时（Mbps）」→ `minutes=1440`，前端按小时分桶取 `(rx_speed+tx_speed)*8/1e6` 的均值画 24 根柱；「在线用户」「24h 流量」取 GET v1/nodes 的 `online_users` / `traffic_bytes_24h`。探针挂在服务器控制节点上；普通协议节点无自身探针时，前端回退显示所在服务器的数据（GET v1/servers/{id}）。

#### POST v1/nodes/{id}/protocol — 改协议参数（兼容入口）
- 状态：现有 `panel/internal/api/admin/handlers.go:1394 nodeSetProtocol`（内部转调 PatchAdminNode）
- 权限：`node.write`｜reauth：否｜幂等：否
- 请求：`{ row_version: int64, node_type: string, server_host: string, server_port: int, traffic_rate?: float, display_name?: string, kernel?: string, protocol_config?: object }`（全量覆盖；traffic_rate<=0→1，kernel 空→auto）
- 响应：200 `AdminNode`
- 错误：同 PATCH v1/nodes/{id}。
- 设计：不使用，前端统一用 PATCH v1/nodes/{id}。

#### GET v1/nodes/{id}/routing — 读单节点出站与分流
- 状态：现有 `panel/internal/api/admin/handlers.go:1188 nodeGetRouting`；**待补·前端**（→ 抽屉新增「路由」tab）
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ row_version: int64(节点版本), outbounds: [{ tag, type, settings: object }], routes: [{ priority: int, matcher: object, outbound_tag: string, enabled: bool, note: string }] }`（只含节点私有项，不含全局项）
- 错误：404 节点不存在。
- 设计：后台-07 抽屉「监控 › 单节点路由（覆盖全局）」只读列表。映射：`r.m` → matcher 的值拼接，`r.o` → `outbound_tag`；routes 为空时显示「沿用全局规则」。

#### PUT v1/nodes/{id}/routing — 全量替换单节点出站与分流
- 状态：现有 `panel/internal/api/admin/handlers.go:1251 nodeSetRouting`；**待补·前端**（→ 抽屉「路由」tab 的编辑器，复用路由页的规则行组件）；**待补·后端（校验修正）**
- 权限：`node.config.publish`｜reauth：否｜幂等：否
- 请求：`{ row_version: int64(节点), outbounds: [{ tag: string(1–64，不得为 direct/block，不得重复), type: "direct"|"block"|"socks"|"http"|"shadowsocks"|"vmess"|"vless"|"trojan"|"hysteria"|"hysteria2"|"tuic"|"anytls"|"shadowtls"|"wireguard", settings?: object }], routes: [{ priority?: int(0=按序号*10), matcher: object, outbound_tag: string, enabled: bool, note?: string }] }`。matcher 只支持键：`domain|domains`、`domain_suffix|domain_suffixes`、`ip|ip_cidr|ip_cidrs`、`port|ports`、`network|networks`、`source|source_ip_cidr|source_cidrs`、`source_port|source_ports`；值为字符串/整数或其数组；`{}` 为兜底且必须是最后一条启用规则。
- 响应：200 `{ ok: true, row_version: int64 }`
- 错误：422 outbounds/routes（「第 N 条规则指向不存在的出站」「无法跨内核下发」「空匹配兜底规则必须放在最后」）/row_version；409 版本冲突；404。
- 待补·后端：出站引用校验目前只认 direct/block 与本次提交的私有出站，**要把全局出站（node_outbounds.node_id IS NULL）的 tag 也纳入**，否则单节点规则无法指向「US-LAX-01」这类全局出站；保存后调用 `notifyNodeChanged`（现只递增 config_source_generation，不主动推送）。
- 设计：设计无单节点编辑界面（后端有、设计缺）。

#### POST v1/nodes/{id}/server-token — 重签服务端（UniProxy）令牌
- **修订 R13（2026-09-24，后端二 62f7283）**：id 非法或节点不存在回 404；已退役 / 已销毁（含 serving_status=retired）回 409；签发写审计（不记令牌）。签发时间与签发人仍按 M8 在后端二第 ④ 步补。
- 状态：现有 `panel/internal/api/admin/handlers.go:1433 nodeIssueServerToken` → `nodefabric/uniproxy.go:148 IssueServerToken`
- 权限：`node.provision`｜reauth：是｜幂等：是 `node_server_token_issue`
- 请求：无请求体
- 响应：201 `{ token: string(仅此一次), node_type: string, panel_url: string, install_command: string, hint: string }`
- 错误：404「节点不存在」
- 待补·后端：写审计 `node.server_token.issue`（现在完全不留审计）；同时写 `server_token_issued_at/by`（见迁移），供身份 tab 展示；拒绝 retired/destroyed 节点（409）。
- 设计：后台-07 抽屉「身份与令牌 › 重签服务端令牌」。映射：设计只 toast，后端返回一次性令牌和命令——前端必须弹出一次性展示框（令牌 + 命令 + `hint`），关闭后不可再看。

#### GET v1/nodes/{id}/identity — 节点身份与令牌状态
- 状态：**待补·后端**（新接口）
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ identity: null | { serial: int, status: "active"|"rotating"|"revoked"|"expired", spiffe_id: string, fingerprint_sha256: string(hex), issued_at: time, expires_at: time, revoked_at?: time, revoked_reason?: string }, server_token: { present: bool, issued_at?: time, issued_by?: uuid, issued_by_name?: string }, bootstrap_tokens_pending: int(未用未过期) }`
- 错误：404 节点不存在（UUID 先校验）
- 需迁移：改表 nodes 加列 `server_token_issued_at timestamptz`, `server_token_issued_by uuid`（identity 部分读现有 node_identities，无迁移）。
- 设计：后台-07 抽屉「身份与令牌」上半部：节点身份 → `identity.spiffe_id`（或 serial）；服务端令牌 → `server_token.present ? "已签发（仅签发时可见）" : "未签发"`（后端只存哈希，不存前缀，设计的 `srv_•••1a2b` 显示不了）；签发时间 → `server_token.issued_at · issued_by_name`。

### 后台-07 节点与服务器 · 服务器

#### GET v1/servers — 服务器列表
- 状态：现有 `panel/internal/api/admin/server.go:53 serverList` → `nodefabric/server_admin.go:195 ListServers`
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：query `status?: "draft"|"ready"|"draining"|"maintenance"|"unhealthy"|"quarantined"|"retired"`, `q?: string(≤120，匹配 name/hostname)`；上限 500。
- 响应：200 `{ servers: Server[], total: int }`，`Server = { id, name, status, status_reason?, row_version, region?, hostname?, public_ipv4?, public_ipv6?, private_ipv4?, architecture?, os_name?, agent_version?, last_heartbeat_at?, heartbeat_online: bool(ready 且 90s 内心跳), cpu_cores?, memory_mb?, disk_gb?, capacity_nodes: int, notes?, control_node_id?, node_count, active_node_count, serving_node_count, never_seen_node_count, created_at, updated_at, cpu_bp?: int(万分比), mem_used_mb?, mem_total_mb?, disk_used_gb?, disk_total_gb?, metrics_at? }`
- 错误：422 status/q。
- 设计：后台-07 服务器卡片。映射：`v.host` → `name`；`v.region` → `region`（首次接入由 GeoIP 自动填）；`v.ip` → `public_ipv4`；`v.agent` → `agent_version`；CPU → `cpu_bp/100`；内存 → `mem_used_mb/mem_total_mb`；磁盘 → `disk_used_gb/disk_total_gb`（null 显示「—」）；圆点：`status=ready && heartbeat_online` 绿、`status=ready && !heartbeat_online` 红（设计 down）、其他灰；`v.nodes` 标签由 GET v1/nodes 按 `server_id` 分组得到（不逐台调 /servers/{id}/nodes）。「N 台服务器」→ `total`。**待补·前端**：卡片补 `serving_node_count / capacity_nodes`，`never_seen_node_count>0` 时提示「有 N 个节点标了在役但从未心跳（多半没装 agent）」。

#### POST v1/servers — 添加服务器
- 状态：现有 `panel/internal/api/admin/server.go:67 serverCreate` → `nodefabric/server_admin.go:303 CreateServer`；**待补·前端**（→ 「添加服务器」弹窗表单）
- 权限：`node.write`｜reauth：否｜幂等：否
- 请求：`{ name: string(1–120), region?: string(≤64), hostname?: string(≤253), public_ipv4?: string, public_ipv6?: string, private_ipv4?: string, architecture?: string(≤32), os_name?: string(≤120), capacity_nodes?: int(>0，缺省 32), notes?: string(≤2000) }`
- 响应：201 `Server`（status=draft）
- 错误：422 各字段（长度、IP 格式、capacity）；409「服务器名称已存在」。
- 设计：后台-07「添加服务器」。映射：设计点一下就生成 `new-edge-N` 并直接给命令；前端改为弹窗收集 名称（必填）、容量、备注（其余可空，接入后自动回填），提交后**紧接着**调 POST v1/servers/{id}/bootstrap-token 展示安装命令。

#### GET v1/servers/{id} — 服务器详情
- 状态：现有 `panel/internal/api/admin/server.go:89 serverGet` → `server_admin.go:221 GetServer`；**待补·前端**（→ 点击服务器卡片打开「服务器详情」抽屉）
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `Server`
- 错误：404。
- 设计：设计缺详情（功能对照列了「服务器（详情、下属节点…）」但原型没画）。抽屉分「概览」（规格/探针/状态/心跳/备注）与「节点」（下一接口）两块。

#### GET v1/servers/{id}/nodes — 服务器下属节点
- 状态：现有 `panel/internal/api/admin/server.go:182 serverNodes` → `server_admin.go:571 ListServerNodes`；**待补·前端**（→ 服务器详情抽屉「节点」区）
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ nodes: [{ id, name, status, serving_status, node_type?, display_name?, server_host?, server_port?, kernel, traffic_rate, config_validated_at?, last_heartbeat_at?, created_at }], total: int }`（含 retired/destroyed，按 sort_order）
- 错误：404。
- 设计：同上。点行跳到节点抽屉。

#### PATCH v1/servers/{id} — 编辑服务器
- 状态：现有 `panel/internal/api/admin/server.go:103 serverPatch` → `server_admin.go:338 PatchServer`；**待补·前端**（→ 服务器详情抽屉「编辑」表单）
- 权限：`node.write`｜reauth：否｜幂等：否
- 请求：`{ row_version: int64, name?: string, region?: string, hostname?: string, public_ipv4?: string, public_ipv6?: string, private_ipv4?: string, architecture?: string, os_name?: string, capacity_nodes?: int, notes?: string }`（省略=不改，"" = 清空；name 传 "" 不清空）
- 响应：200 `Server`
- 错误：422 字段；409 版本冲突；409「服务器容量不能低于当前节点占用」fields.capacity_nodes="minimum=N"；409 名称重复；404。
- 设计：设计缺（后端有）。

#### POST v1/servers/{id}/status — 改服务器状态（维护/恢复/退役）
- 状态：现有 `panel/internal/api/admin/server.go:136 serverSetStatus` → `server_admin.go:411 SetServerStatus`
- 权限：`node.lifecycle`｜reauth：否｜幂等：否
- 请求：`{ status: 服务器状态, row_version: int64, reason?: string(≤500) }`
- 响应：200 `Server`
- 错误：422 status/row_version/reason；409 版本冲突；409「不允许的服务器状态转换」fields.status="from -> to"（合法边：draft→ready/maintenance/retired；ready→draining/unhealthy/quarantined；draining→ready/maintenance/retired；maintenance→ready/retired；unhealthy→draining/maintenance/quarantined/retired；quarantined→draining/maintenance/retired）；409 进入 ready 需至少一个协议校验通过且 active 的节点（fields.nodes="requires_valid_active_node"）。
- 设计：后台-07 卡片「标记维护 / 恢复服务」。映射：「标记维护」在 status=ready 时发 `draining`（ready 不能直达 maintenance；draining 即「停止新分配、已有连接保持」，与设计「节点暂停下发」一致），卡片标签显示「维护中」；status 已是 draining 时菜单再提供「转入维护（maintenance）」；「恢复服务」发 `ready`。服务器详情抽屉补完整状态下拉（含「退役」，删除前置条件）。

#### DELETE v1/servers/{id} — 删除服务器
- 状态：现有 `panel/internal/api/admin/server.go:160 serverDelete` → `server_admin.go:478 DeleteServer`
- 权限：`node.lifecycle`｜reauth：是｜幂等：否
- 请求：body `{ row_version: int64(>0) }`
- 响应：200 `{ ok: true, id: uuid }`
- 错误：422 row_version；409 版本冲突；409「服务器仍在服务，请先在『状态』里退役」（只有 draft/retired 可删）；404。
- 设计：后台-07 卡片「删除」。映射：设计确认文案「服务器上的节点需要先迁移或删除」与后端不符——后端**不拒绝**，而是把名下节点级联静默（serving_status=retired、吊销身份、作废任务、摘掉 server_id；同名重装可复活）。前端文案改为「只能删除草稿或已退役的服务器；名下 N 个节点会一起下线，身份立即吊销」，N 取 `node_count`；非 draft/retired 时按钮禁用并提示先退役。

#### POST v1/servers/{id}/bootstrap-token — 服务器安装令牌
- 状态：现有 `panel/internal/api/admin/handlers.go:915 serverIssueToken` → `service.go:197 IssueBootstrapToken`
- 权限：`node.provision`｜reauth：是｜幂等：是 `server_bootstrap_token_issue`
- 请求：`{ node_name?: string(缺省=服务器 name), pool_id?: uuid, ttl_minutes?: int(1–30，缺省 20) }`（请求体里的 `server_id` 字段会被忽略，以路径为准）
- 响应：201 `{ token, expires_at, install_command }`
- 错误：同 POST v1/nodes/bootstrap-token；另 404 服务器不存在/已删除/已退役。
- 设计：后台-07 卡片「安装令牌」与顶部黑色命令条（「令牌 30 分钟内有效 · 仅显示一次」）。前端传 `ttl_minutes: 30`，令牌与命令分开显示（同节点令牌）。

### 后台-07 节点与服务器 · 节点池

#### GET v1/node-pools — 节点池列表
- 状态：现有 `panel/internal/api/admin/pools.go:42 listNodePools`；**待补·后端（字段扩展，user group 部分需迁移）**
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ pools: [{ id, code, name, region: string, status: "active"|"draining"|"disabled", nodes: int, active_nodes: int, plans: int(绑定的套餐版本数) }] }`
- 待补·后端：每项加 `members: [{ id, name, node_no }]`（不含 destroyed）、`plan_names: string[]`（去重的套餐名）、`allowed_user_group_ids: uuid[]` 与 `allowed_user_groups: [{ id, name }]`（用户组限制这一项取决于待决 D-B-3，未决前不做）。
- 错误：无。
- 设计：后台-07 节点池卡片。映射：`p.name` → `name`；`p.n` → `nodes`；节点标签 → `members[].name`；「绑定套餐」→ `plan_names` 用「、」连接；「— · 仅用户组『内测』」→ `plan_names` 为空显示「—」，`allowed_user_groups` 非空时追加「仅用户组『…』」。

#### POST v1/node-pools — 新建节点池
- 状态：现有 `panel/internal/api/admin/pools.go:85 createNodePool`；**待补·前端**（→ 节点池 tab 右上「新建节点池」）
- 权限：`node.provision`｜reauth：否｜幂等：否
- 请求：`{ name: string(必填), code?: string(缺省由 name 派生), region?: string, status?: string(被忽略，新建一律 active) }`；待补·后端（取决于 D-B-3）：加 `allowed_user_group_ids?: uuid[]`。
- 响应：**200**（不是 201）`{ id: uuid }`
- 错误：422 name；409「这个分组标识已存在」。
- 设计：设计缺（后端有）。

#### POST v1/node-pools/{id} — 编辑节点池
- 状态：现有 `panel/internal/api/admin/pools.go:128 updateNodePool`；**待补·前端**（→ 节点池卡片「编辑」）
- 权限：`node.provision`｜reauth：否｜幂等：否
- 请求：`{ name?: string, region?: string, status?: "active"|"draining"|"disabled", code?: string(被忽略) }`（空字符串=不改，region 无法清空）；待补·后端（取决于 D-B-3）：加 `allowed_user_group_ids?: uuid[]`（省略=不改，`[]`=不限制）。
- 响应：200 `{ ok: true }`
- 错误：422 status（无 fields）；404。
- 设计：设计缺。节点归属池的变更不在这里，走 PATCH v1/nodes/{id} 的 `pool_id`。

#### DELETE v1/node-pools/{id} — 删除节点池
- 状态：现有 `panel/internal/api/admin/pools.go:169 deleteNodePool`；**待补·前端**（→ 节点池卡片「删除」）
- 权限：`node.provision`｜reauth：否｜幂等：否
- 请求：无请求体
- 响应：200 `{ ok: true }`
- 错误：404；409 池下还有节点 / 还有套餐绑定 / 还有节点模板默认池 / 还有 pool 层配置发布记录 / 还有未用引导令牌（各自一条中文消息）。
- 设计：设计缺。`nodes>0 || plans>0` 时按钮直接禁用并说明原因。

### 后台-07 节点与服务器 · 路由

#### GET v1/nodes/routing — 读全局出站与分流
- 状态：**待补·后端**（新接口；静态段 `routing` 与 `/nodes/{id}` 同级，chi 静态优先，与现有 `/nodes/order`、`/nodes/bootstrap-token` 同一模式）
- 权限：`node.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ revision: string(全局出站+规则的规范 JSON 的 sha256 hex，空集也有值), outbounds: [{ tag, type, settings: object }], routes: [{ priority, matcher: object, outbound_tag, enabled, note }], online_nodes: int(serving_status=active 且未 stale，供发布确认框) }`
- 需迁移：否（node_outbounds / node_routes 的 `node_id` 本就可为 NULL，全局出站已被 LoadRouting 合并）。
- 设计：后台-07 路由 tab 的「分流规则」列表与「出站」列表。

#### PUT v1/nodes/routing — 保存并发布全局出站与分流到全部节点
- 状态：**待补·后端**（新接口）
- 权限：`node.config.publish`｜reauth：是｜幂等：是 `node_routing_global_publish`（批量影响全部节点）
- 请求：`{ expected_revision: string, outbounds: [...同单节点], routes: [...同单节点] }`（校验规则与 PUT v1/nodes/{id}/routing 完全一致，复用同一函数）
- 响应：200 `{ ok: true, revision: string, affected_nodes: int }`
- 行为：一个事务内持 `node-config-release` 锁 → 比对 revision（不符 409）→ 删除并重写 `node_id IS NULL` 的出站与规则 → 对全部未退役节点 `config_source_generation+1` → 审计 `node.routing.global_publish` → 提交后逐节点 `notifyNodeChanged`。同时修改 `loadEffectiveRoutingTx` 与 `LoadRouting`：生效规则 = 节点私有规则（按 priority）在前 + 全局规则在后（节点规则「覆盖全局」；节点私有兜底规则会遮住全局规则，编辑器需提示）。**若全局出站被某节点私有规则引用，删除该出站要 422 并列出节点**。
- 错误：422 outbounds/routes；409 revision 冲突；409 删除仍被引用的全局出站。
- 需迁移：否。
- 设计：后台-07 路由 tab「添加 / 移除」（本地编辑）+「发布到全部节点」（一次 PUT）。确认框「N 条规则会下发到 M 个在线节点」→ N=routes.length，M=GET 返回的 `online_nodes`。映射：规则类型「域名后缀」→ `domain_suffix`，「IP 段」→ `ip_cidr`，「端口」→ `port`，「兜底」→ `{}`；「geosite」「geoip」→ 后端不支持，见待决 D-D-1；出站「直连」→ 内置 `direct`，「拦截」→ 内置 `block`，自定义出站如 US-LAX-01 → `{tag, type:"trojan", settings}`；「代理 · selector」→ 不支持，见 D-D-1。**待补·前端**：出站列表要能新增/编辑/删除（tag、type 下拉、settings JSON），设计只有只读列表。

### 后台-08 内容与外观 · 公告

#### GET v1/announcements — 公告列表（附可选套餐）
- 状态：现有 `panel/internal/api/admin/announce.go:44 listAnnouncements`；**待补·后端（字段扩展，无迁移）**
- 权限：`ops.announcement.write`（没有单独的读权限）｜reauth：否｜幂等：否
- 请求：无（上限 200，置顶在前，按 published_at/created_at 倒序）
- 响应：200 `{ announcements: [{ id, title, body, severity: "info"|"notice"|"warning"|"critical", pinned: bool, status: "draft"|"scheduled"|"published"|"withdrawn", version: int, target_plan_ids: uuid[], plan_targets: [{ id, name, status("missing"=已删) }], publish_at?: time, expires_at?: time, created_at }], plans: [{ id, name, status }] }`
- 待补·后端：每项加 `target_user_group_ids: uuid[]`、`user_group_targets: [{ id, name }]`，顶层加 `user_groups: [{ id, name }]`（表里已有 `target_user_group_ids` 列，public 端 `domain/notify/announce.go` 已按它过滤，只是 admin 读写都没暴露）。
- 错误：无。
- 设计：后台-08 公告左侧列表。映射：`a.st` live → `published`，draft → `draft`，withdrawn → `withdrawn`；**`scheduled` 设计缺**，待补·前端：显示「定时 · MM-DD HH:mm」（取 publish_at）；`a.target` → 由 `plan_targets` + `user_group_targets` 名称拼接，都为空显示「全部用户」；`a.at` → published 用 `publish_at`、draft 显示「草稿」。

#### POST v1/announcements — 新建公告（草稿/立即发布/定时发布）
- 状态：现有 `panel/internal/api/admin/announce.go:220 saveAnnouncement`（无 id 分支）
- 权限：`ops.announcement.write`｜reauth：是｜幂等：是 `announcement_save`
- 请求：`{ title: string(2–160), body: string(2–20000，Markdown 纯文本), severity?: "info"|"notice"|"warning"|"critical"(缺省 info), pinned?: bool, target_plan_ids?: uuid[], publish_at?: RFC3339(带时区), expires_at?: RFC3339(须晚于 publish_at), publish?: bool, expected_version?: 0 }`；待补·后端：加 `target_user_group_ids?: uuid[]`（同样校验属于本租户）。
- 响应：200 `{ id: uuid, status: string, version: int }`（publish=false → draft；publish=true 且 publish_at 在未来 → scheduled；否则 published）
- 错误：422 title/body/severity/expected_version/target_plan_ids（fields）；422 时间格式、下线时间早于发布时间（无 fields）。
- 设计：后台-08「＋ 新建公告」→「发布」。映射：设计新建时先插一条本地草稿，前端在首次「发布」或「保存草稿」时才 POST。**待补·前端**：编辑区底栏补「级别」下拉（severity 四档）、「定时发布」时间选择（publish_at）、「自动下线」时间选择（expires_at）、「保存草稿」按钮（publish=false）；「可见范围」从单选改为「套餐 + 用户组」多选（数据来自响应的 `plans`、`user_groups`），全不选=全部用户。

#### POST v1/announcements/{id} — 编辑/发布已有公告
- 状态：现有 `panel/internal/api/admin/announce.go:220 saveAnnouncement`（有 id 分支）
- 权限：`ops.announcement.write`｜reauth：是｜幂等：是 `announcement_save`
- 请求：同新建（全量覆盖），`expected_version: int(>0，当前 version)`
- 响应：200 `{ id, status, version }`
- 错误：同新建；404；409 版本冲突；409「已撤回公告不可重新编辑，请新建公告」；409「已发布公告只能保持发布状态」（published 再传 publish=false）。
- 设计：后台-08 编辑区「发布 / 保存修改」。映射：已发布公告「保存修改」→ `publish: true`；草稿「发布」→ `publish: true`。设计允许对已撤回公告点「发布」——后端拒绝，见待决 D-D-3。

#### POST v1/announcements/{id}/withdraw — 撤回公告
- 状态：现有 `panel/internal/api/admin/announce.go:390 withdrawAnnouncement`
- 权限：`ops.announcement.write`｜reauth：是｜幂等：是 `announcement_withdraw`
- 请求：`{ expected_version: int(>0) }`
- 响应：200 `{ ok: true, version: int }`
- 错误：422 expected_version；404；409 版本冲突；409「这条公告已经撤回过了」。
- 设计：后台-08「撤回」（设计只在 live 时显示；后端对 draft/scheduled 也允许撤回，前端对 scheduled 也显示「撤回」=取消定时）。

### 后台-08 内容与外观 · 知识库

#### GET v1/content-pages — 内容页列表（每个版本一行）
- 状态：现有 `panel/internal/api/admin/content.go:13 listContentPages` → `domain/content/service.go ListAdmin`；**待补·后端（字段扩展，无迁移）**
- 权限：`ops.content.write`｜reauth：否｜幂等：否
- 请求：query `kind?: "page"|"kb_article"|"tutorial"|"legal"`, `status?: "draft"|"published"|"archived"`, `q?: string(匹配 slug/标题)`, `limit?: int(1–500，缺省 200)`
- 响应：200 `{ pages: Page[] }`，`Page = { id, slug, kind, category?, version: int, title, summary?, locale, sanitizer_version, target_platforms: string[], min_client_version?, max_client_version?, target_plan_ids: uuid[], visibility: "authenticated"|"internal", status, review_due_at?, published_at?, created_at, updated_at, latest_version?: int(同 slug 最大版本), is_latest_in_audience?: bool }`（列表不含 body；按 slug、version 倒序）
- 待补·后端：`Page` 加 `created_by?: uuid`、`created_by_name?: string`（content_pages.created_by 列已存在）。
- 错误：无。
- 设计：后台-08 左栏按分类分组、右栏「版本历史」。映射：前端按 `slug` 分组，每组取 `version == latest_version` 那行作为文章条目（`v{version}`），`status=archived` 淡显；按 `category` 分组；「版本历史」= 同 slug 所有行，`h.by` → `created_by_name`，`h.at` → `created_at`，当前 = `latest_version`。**待补·前端**：左栏顶部补 `kind` 切换（知识库/教程/页面/法律条款，设计只有知识库）与搜索。

#### GET v1/content-pages/{id} — 读单个版本（含正文）
- 状态：现有 `panel/internal/api/admin/content.go:27 getContentPage` → `ListAdmin` 同文件 `GetAdmin`
- 权限：`ops.content.write`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ page: Page(含 body) }`
- 错误：404（含非法 UUID）。
- 设计：后台-08 点文章或点历史版本时加载正文。**待补·前端**：历史版本行可点开只读查看。

#### POST v1/content-pages — 保存为新版本
- 状态：现有 `panel/internal/api/admin/content.go:37 publishContentVersion` → `service.go PublishVersion`
- 权限：`ops.content.write`｜reauth：是｜幂等：是 `content_page_version_create`
- 请求：`{ slug: string(小写字母数字单连字符，≤80), kind?: string(缺省 kb_article), category?: string(≤80), title: string(2–160), summary?: string(≤500), body: string(10–100000), locale?: string(缺省 zh-CN), target_platforms?: ("web"|"windows"|"macos"|"linux"|"android"|"ios")[], min_client_version?: string, max_client_version?: string, target_plan_ids?: uuid[], visibility?: "authenticated"|"internal"(缺省 authenticated), status?: "draft"|"published"(缺省 draft), review_due_at?: time, expected_latest_version: int(新 slug 为 0) }`
- 响应：201 `{ page: { id, slug, version, status } }`
- 错误：422 各字段（fields）；409「内容已产生新版本，请刷新后重试」/「内容版本已变化」。
- 设计：后台-08「保存为 v{n}」「＋ 新文章」「恢复」。映射：「保存为 v{n}」→ `status: "published"`，`expected_latest_version = latest_version`；**必须原样带上一版的受众字段**（locale/target_platforms/min/max_client_version/target_plan_ids/visibility），否则旧的已发布版本不会被自动归档、两版同时对用户可见。「恢复」（archived → 可见）→ 用该版本内容再 POST 一次 `status: "published"`（得到新版本号，符合「每次保存生成新版本」）。**待补·前端**：「＋ 新文章」要先填 `slug`（中文标题派生不出，给输入框并校验格式）；编辑区右侧补「发布设置」折叠区：类型、摘要、语言、平台、客户端版本范围、限定套餐、可见性（internal=仅后台）、复审日期、「保存草稿」（status=draft）；分类下拉改为「已有分类 + 自定义输入」（后端是自由文本）。

#### POST v1/content-pages/{id}/archive — 归档某个版本
- 状态：现有 `panel/internal/api/admin/content.go:52 archiveContentPage` → `service.go Archive`
- 权限：`ops.content.write`｜reauth：是｜幂等：是 `content_page_archive`
- 请求：`{ expected_version: int(该行 version) }`
- 响应：200 `{ page: { id, version, status: "archived", already_archived: bool } }`（已归档重复调用返回 already_archived=true，不报错）
- 错误：404（非法 UUID 或 expected_version<=0 也回 404）；409 版本不符。
- 设计：后台-08「归档」。前端对当前文章的**最新已发布版本**调用；归档后门户不再展示该 slug（前提是没有其他受众组合的已发布版本）。

### 后台-08 内容与外观 · 主题与插槽

#### GET v1/themes — 主题列表
- 状态：现有 `panel/internal/api/admin/appearance.go:20 listThemes` → `domain/appearance/service.go:122 ListThemes`
- 权限：`platform.appearance.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ themes: [{ id, code, name, is_builtin: bool, is_active: bool, tokens: object(扁平 CSS 变量名→值), branding: object, custom_css: string }] | null }`（无主题时是 `null` 不是 `[]`）
- 错误：无。
- 设计：后台-08 主题卡片。映射：`t.name` → `name`；`t.active` → `is_active`；色块 `t.bg/t.fg/t.accent` → `tokens.bg / tokens.fg(或 text) / tokens.brand`（缺省回退到前端默认 token）。**待补·前端**：`is_builtin` 卡片显示「内置」标签、隐藏「删除」、把「编辑」换成「另存为」；自定义主题卡片补「编辑」。token 键名问题见待决 D-D-4。

#### POST v1/themes — 保存主题（新建或覆盖同 code 的自定义主题）
- 状态：现有 `panel/internal/api/admin/appearance.go:37 saveTheme` → `service.go:159 SaveTheme`
- 权限：`platform.appearance.write`｜reauth：是｜幂等：是 `appearance_theme_save`
- 请求：`{ code: string(^[a-z][a-z0-9_-]{1,38}$，同 code 即覆盖), name: string(1–60), tokens?: object, branding?: object({ site_name?, tagline?, logo?: data URI 或 URL, … }), custom_css?: string(净化后 ≤64KB) }`
- 响应：200 `{ saved: true, dropped: string[](净化时被删掉的 CSS 片段说明) }`（不回主题本体，前端需重新 GET）
- 错误：422 code/name 缺失；422 tokens/branding 非法 JSON；422 custom_css 超 64KB；422「内置主题不能直接改」；DB CHECK 不满足（code 格式、name 长度）目前会变 500（见核对笔记）。
- 设计：后台-08「保存新主题」（名称、主色、背景）。映射：主色 → `tokens.brand`，背景 → `tokens.bg`；`code` 由前端从名称派生（中文名时用 `theme-<时间戳base36>`），并在高级区可改。**待补·前端**：「保存新主题」卡片扩成编辑器：code、品牌区（站点名 site_name、标语 tagline、Logo）、更多 token、自定义 CSS 文本框；保存后若 `dropped` 非空，逐条提示被过滤的内容。

#### POST v1/themes/{code}/activate — 激活主题
- 状态：现有 `panel/internal/api/admin/appearance.go:58 activateTheme` → `service.go:216 ActivateTheme`
- 权限：`platform.appearance.write`｜reauth：是｜幂等：否
- 请求：无请求体
- 响应：200 `{ activated: true }`
- 错误：404「主题不存在」。
- 设计：后台-08 主题卡片「激活」（确认框需走 reauth 流程，设计没标）。

#### DELETE v1/themes/{code} — 删除主题
- 状态：现有 `panel/internal/api/admin/appearance.go:68 deleteTheme` → `service.go:244 DeleteTheme`
- 权限：`platform.appearance.write`｜reauth：是｜幂等：否
- 请求：无请求体
- 响应：200 `{ deleted: true }`
- 错误：422「删不掉：内置主题和正在生效的主题都不能删」（不存在也是这条）。
- 设计：后台-08 主题卡片「删除」（设计只对 active 禁用，前端还要对 is_builtin 禁用）。

#### GET v1/slots — 插槽列表
- 状态：现有 `panel/internal/api/admin/appearance.go:82 listSlots` → `service.go:273 ListSlots`
- 权限：`platform.appearance.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ slots: [{ key: string, label: string, where: string, content: string, enabled: bool, updated_at?: "YYYY-MM-DD HH24:MI" }] }`（以代码里的 7 个插槽位为准：portal.login.notice、portal.home.banner、portal.home.aside、portal.sidebar.extra、portal.subscribe.notice、portal.plans.notice、portal.footer）
- 错误：无。
- 设计：后台-08「前端插槽」。映射：`s.name` → `label`（下方小字补 `where`），`s.key` → `key`，`s.val` → `content`，`s.on` → `enabled`。设计 mock 的 key（portal.login.banner、portal.subscription.notice）不存在，以接口返回为准。新门户必须在这 7 个位置挂载插槽（与 public 段对齐）。

#### POST v1/slots/{key} — 保存插槽
- 状态：现有 `panel/internal/api/admin/appearance.go:96 saveSlot` → `service.go:315 SaveSlot`
- 权限：`platform.appearance.write`｜reauth：是｜幂等：是 `appearance_slot_save`
- 请求：`{ content: string(HTML 白名单净化后 ≤32KB), enabled: bool }`（全量覆盖，切换开关也要带 content）
- 响应：200 `{ saved: true, dropped: string[] }`
- 错误：422「未知的插槽位」；422 content 超 32KB。
- 设计：后台-08 插槽「失焦保存」与开关。映射：每次保存都要 reauth + 新 Idempotency-Key；前端改为「内容有变化才在失焦时保存」，开关切换带上当前 content；`dropped` 非空时提示被过滤的标签/属性，并用返回后重新 GET 的 content 回填（显示净化后的真实内容）。

### 后台-09 通知与安全 · 通知与插件（通知渠道 / 邮件模板 / Webhook 钩子）

#### GET v1/settings/telegram — 读取 Telegram 配置
- 状态：现有 `panel/internal/api/admin/telegram.go:17 getTelegramSettings`；**admin_chat_id 待补·后端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ enabled: bool, bot_username: string, has_token: bool }`（Token 永不回显）；待补·后端追加 `admin_chat_id: int | null`（system_settings 键 `telegram.admin_chat_id`，KV 表，需迁移：否）
- 错误：无特有
- 设计：后台-09 通知渠道「Telegram」卡。映射：状态点「已连接 @bot」← `enabled && has_token` 显示「已启用 @bot_username」，`!has_token` 显示「未配置 Token」，`!enabled` 显示「已停用」（后端不做连通性探测）；Bot Token 输入框为空占位「已设置，留空不修改」

#### POST v1/settings/telegram — 保存 Telegram 配置
- 状态：现有 `telegram.go:39 setTelegramSettings`；**admin_chat_id 待补·后端**
- 权限：`platform.settings.write`｜reauth：否｜幂等：否
- 请求：`{ enabled: bool, bot_username: string（可带 @，后端去掉）, bot_token: string（空=不修改） }`；待补·后端追加 `admin_chat_id?: int | null`
- 响应：200 `{ ok: true, enabled: bool }`
- 错误：422 `fields.bot_username`（启用却没填用户名）
- 设计：Telegram 卡字段 Bot Token、管理员群组 Chat ID、Bot 用户名、保存。待补·前端：卡片标题栏加「启用」开关（`enabled`，设计缺）。「管理员群组」除测试默认目标外还用来做什么见待决 D-A-4

#### POST v1/settings/telegram/test — 发送 Telegram 测试消息
- **修订 R14（2026-09-24，后端二 62f7283）**：权限改为 `ops.notification.write`。
- 状态：现有 `telegram.go:117 testTelegram`；**chat_id 可选 待补·后端**
- 权限：`billing.provider.write`（与保存用的权限不同，见核对笔记）｜reauth：否｜幂等：否
- 请求（现有）：`{ chat_id: int }`（必填非 0；JSON 数字，负数群 ID 在 JS 安全整数内）；待补·后端：`chat_id?` 可省略，省略时发往 `admin_chat_id`，两者都没有回 422 `fields.chat_id`
- 响应：200 `{ sent: true }`
- 错误：422 `fields.chat_id`；422 validation_failed「Telegram 还没配置好…」/「Bot Token 无效」/「发送失败：<Telegram 原始报错>」（无 fields）
- 设计：Telegram 卡底部测试输入框「留空发送到管理员群组」+「发送测试」。映射：测试用的是**已保存**的配置，前端在有未保存修改时先提示保存

#### GET v1/settings/mail — 读取邮件与注册设置
- 状态：现有 `panel/internal/api/admin/mail.go:23 getMailSettings`
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ smtp_host: string, smtp_port: int(默认 465), encryption: "ssl" | "tls" | "none"(默认 ssl), smtp_username: string, has_password: bool, from_address: string, from_name: string(默认 "AegisPanel"), email_verification: bool, registration_mode: "closed" | "invite_only" | "open"(默认 closed) }`
- 错误：无特有
- 设计：后台-09「邮件 · SMTP」卡（服务器、端口、用户名、密码、发件人）。映射：设计单框「发件人：Pandora <noreply@…>」← `from_name <from_address>` 拼接显示，提交时拆回两个字段（解析不了就整串当 from_address）。待补·前端：①SMTP 卡加「加密方式」下拉（ssl/tls/none）；②同页签新增第三张卡「注册与验证」：注册模式（关闭/仅邀请/开放）+ 邮箱验证开关，并注明「实际能否注册还受降级开关 auth.registration 控制」

#### POST v1/settings/mail — 保存邮件与注册设置
- 状态：现有 `mail.go:82 setMailSettings`
- 权限：`platform.settings.write`｜reauth：否｜幂等：否
- 请求：`{ smtp_host: string, smtp_port: int(1–65535), encryption: "ssl"|"tls"|"none", smtp_username: string, smtp_password: string（空=不改，"-"=清空）, from_address: string, from_name: string, email_verification?: bool, registration_mode?: "closed"|"invite_only"|"open" }`。**SMTP 六个字段每次都整体覆盖写入**，只有 password/email_verification/registration_mode 可省略——「注册与验证」卡保存时也必须带上当前全部 SMTP 字段
- 响应：200 `{ ok: true }`
- 错误：422 `fields.smtp_port|encryption|registration_mode|email_verification`（开启邮箱验证但 host 或 from 为空）
- 设计：SMTP 卡「保存」、新增的「注册与验证」卡「保存」（同一接口）

#### POST v1/settings/mail/test — 发送 SMTP 测试邮件
- **修订 R14（2026-09-24，后端二 62f7283）**：权限改为 `ops.notification.write`。
- 状态：现有 `mail.go:220 testMailSettings`
- 权限：`billing.provider.write`｜reauth：否｜幂等：否
- 请求：`{ to: string }`
- 响应：200 `{ ok: true, to: string }`
- 错误：422 `fields.to`；422 validation_failed「SMTP 还没配置好…」/「SMTP 配置不完整」/「发送失败：<SMTP 原始报错>」
- 设计：SMTP 卡底部「测试收件地址」+「发送测试」。映射：用**已保存**配置直连发送，未保存修改时先提示保存

#### GET v1/mail/templates — 通知模板列表
- 状态：现有 `panel/internal/api/admin/mail_template.go:10 listMailTemplates`
- 权限：`ops.notification.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ templates: [{ code: string, channel: "email" | "inapp" | "telegram", locale: "zh-CN", category: string, status: string, version: int, subject: string, body: string, allowed_variables: string[], is_default: bool（无内置默认的模板恒 false）, updated_at: rfc3339, description: string（仅 4 个 code 有，其余 ""）, preview_subject: string, preview_body: string }] }`，按 code、channel 排序。现有 code：subscription.expiring(inapp/email/telegram)、quota.warning(inapp/email/telegram)、order.paid(inapp/telegram)、ticket.replied(inapp)、admin.broadcast(email)
- 错误：无特有
- 设计：后台-09「邮件模板」左栏列表（名称 + 默认/已自定义/未保存的修改）。映射：名称←`description` 或前端 code→中文名表；「已自定义」←`!is_default`；变量语法后端是 `{{var}}`，设计是 `{var}`，变量 chips 取自 `allowed_variables`、插入 `{{name}}`。待补·前端：列表顶部加渠道分段「邮件 / 站内信 / Telegram」（后端模板覆盖三个渠道，设计只画了邮件）。设计列出的「重置密码」「注册验证码」「礼品卡兑换成功」三个模板后端不存在，见本节「注册验证码」条与待决 D-A-5

#### POST v1/mail/templates — 保存模板
- 状态：现有 `mail_template.go:43 saveMailTemplate`
- 权限：`platform.settings.write`｜reauth：否｜幂等：否
- 请求：`{ code: string, channel: string, subject: string(1–200 字), body: string(1–20000 字) }`（只能改已有模板，不能新建）
- 响应：200 `{ template: TemplateRow（同列表行，不含 description/preview_*）, preview_subject: string, preview_body: string }`
- 错误：400 bad_request（缺 code/channel）；422 `fields.subject`；422 `fields.body`（长度，或「用到了这个模板不提供的变量：…」）；404 not_found（模板不存在）
- 设计：模板编辑区「保存」

#### POST v1/mail/templates/preview — 用示例数据渲染未保存的草稿
- 状态：待补·后端
- 权限：`ops.notification.read`｜reauth：否｜幂等：否（纯计算，不写库）
- 请求：`{ code: string, channel: string, subject: string, body: string }`
- 响应：200 `{ preview_subject: string, preview_body: string, unknown_variables: string[] }`（渲染复用 `notify.RenderPreview` 的示例值；unknown_variables 复用 `unknownVariables`，让编辑时就能提示，不必等保存报错）
- 错误：400 bad_request（缺 code/channel）；404 not_found（模板不存在，用于取 allowed_variables）
- 需迁移：否
- 设计：后台-09 模板右栏「以示例数据预览」。前端对编辑内容 debounce 300ms 调用；不在前端复制一份示例值表（与后端唯一来源冲突）

#### POST v1/mail/templates/reset — 恢复默认
- 状态：现有 `mail_template.go:67 resetMailTemplate`
- 权限：`platform.settings.write`｜reauth：否｜幂等：否
- 请求：`{ code: string, channel: string }`
- 响应：200 `{ template: TemplateRow }`
- 错误：404 not_found「这个模板没有内置默认内容」（`admin.broadcast|email` 与三个 telegram 模板都没有内置默认，见核对笔记）；以及保存接口的全部错误
- 设计：「恢复默认」+ 危险确认框。待补·前端：`is_default` 为 true 或该模板无内置默认时禁用按钮（后者前端靠调用失败判断，或由后端在列表加 `has_default: bool`——建议随 preview 一起补，需迁移：否）

#### POST v1/mail/templates/test — 用模板实发一封测试信
- **修订 R14（2026-09-24，后端二 62f7283）**：权限改为 `ops.notification.write`（注释说要 reauth、代码仍未挂，待后续处理）。
- 状态：现有 `mail_template.go:93 testMailTemplate`；**草稿发送 待补·后端**
- 权限：`billing.provider.write`｜reauth：否（router 注释写「要求近期重认证」但实际没挂，见核对笔记）｜幂等：否
- 请求（现有）：`{ code: string, channel: "email", to: string }`，发送的是**已保存**的模板；待补·后端追加 `subject?: string, body?: string`：提供时按草稿渲染发送（先做同样的长度与变量白名单校验），省略时行为不变。需迁移：否
- 响应：200 `{ sent: true, subject: string }`（主题前缀 `[测试] `）
- 错误：422 `fields.to`；422 validation_failed「只有邮件模板可以测试发送…」/「SMTP 还没配置好…」/「发送失败：…」；404 not_found；（待补后）422 `fields.subject|body`
- 设计：预览栏底部收件地址 +「实发测试信」。映射：非 email 渠道隐藏该区

#### （模板补充）auth.email_verify 注册验证码邮件
- 状态：待补·后端（不是新路由，是新模板 + 投递）
- 说明：`identity.StartRegistration` 写入 verification_codes 后**没有任何代码把验证码发出去**（只有开发模式在响应里回 `dev_code`），生产环境一旦开启 `email_verification`，注册必然卡死。补：新增模板 `auth.email_verify|email`（allowed_variables `site, code, minutes`），StartRegistration 在同一事务内经 notify 入队（邮箱已存在时不入队，保持 IAM-006 响应一致），`defaultTemplates` 与种子同步。设计「注册验证码」模板即它
- 需迁移：notification_templates 种子一行（数据迁移，无表结构变更）
- 设计：后台-09 邮件模板「注册验证码」；门户注册第二步

#### GET v1/plugin-hooks — Webhook 钩子列表与事件目录
- 状态：现有 `panel/internal/api/admin/appearance.go:119 listHooks`；**成功率 待补·后端**
- 权限：`platform.plugin.read`｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ hooks: Hook[] | null（一个都没有时是 null）, events: [{ Name: string, Desc: string }] }`——事件目录的键是**大写开头的 `Name`/`Desc`**（匿名结构体无 json tag）。`Hook = { id: uuid, code: string, name: string, description: string, enabled: bool, events: string[], endpoint_url: string, has_secret: bool, timeout_ms: int, max_attempts: int, queued_count: int, failed_count: int(近 7 天), last_sent_at?: "YYYY-MM-DD HH:MM" }`。事件目录：user.registered、order.created、order.paid、order.cancelled、subscription.provisioned、subscription.expiring、subscription.expired、traffic.exhausted、ticket.created、giftcard.redeemed
- 响应（待补·后端，追加）：Hook 加 `sent_count_7d: int`（前端算成功率 = sent/(sent+failed)）；顺手把 `hooks` 改为空数组、events 键改为小写 `name/desc`（破坏性但当前无前端消费者，第 3 阶段一并改；若不改，前端 zod 按现状写）。需迁移：否
- 错误：无特有
- 设计：后台-09「Webhook 钩子」卡片列表（状态点、URL、事件 · 成功率、投递记录、测试投递、删除）。映射：事件名以后端目录为准：设计 `user.created`→`user.registered`；设计 `ticket.replied`、`node.offline`、`node.online` 后端没有（见待决 D-A-6）；「全部事件」= 提交目录里全部 name。状态点颜色←`failed_count>0` 为黄。待补·前端：卡片加启用开关、编辑（name/description/events/timeout_ms/max_attempts/更换密钥）、`queued_count` 显示

#### POST v1/plugin-hooks — 新建或修改钩子（upsert）
- 状态：现有 `appearance.go:140 saveHook`
- 权限：`platform.plugin.write`｜reauth：是｜幂等：是 `plugin_hook_save`
- 请求：`{ code: string（小写，唯一键）, name: string, description?: string, enabled: bool, events: string[], endpoint_url: string(生产必须 https、不得解析到内网), secret?: string（空=不改；新建时空则自动生成）, timeout_ms?: int(0→5000), max_attempts?: int(0→5) }`。**按 code upsert，省略的字段会被写成零值/默认值**：编辑时必须回填全部字段；新建时前端必须生成一个不与现有 code 冲突的 code（例如 `hook-` + 6 位随机），否则会静默覆盖已有钩子
- 响应：200 `{ saved: true, secret?: string, secret_hint?: string }`（自动生成的密钥只返回这一次）
- 错误：422 `fields.code|name|endpoint_url|events`（未知事件、启用却没选事件、非 https、内网地址、域名解析失败）；409 conflict「该插件标识已存在」（理论上 upsert 不触发）
- 设计：钩子页顶部「端点 URL + 订阅事件 + 新建钩子」。映射：设计无 code/name 输入——code 前端生成，name 默认取 URL 主机名；设计 toast「签名密钥已复制」←把响应 `secret` 写入剪贴板并弹一次性展示框

#### DELETE v1/plugin-hooks/{code} — 删除钩子
- 状态：现有 `appearance.go:168 deleteHook`
- 权限：`platform.plugin.write`｜reauth：是｜幂等：否
- 请求：无
- 响应：200 `{ deleted: true }`
- 错误：404 not_found「插件不存在」
- 设计：卡片「删除」+ 危险确认框（映射：确认框走 reauth 流程）

#### GET v1/plugin-hooks/{code}/deliveries — 投递记录
- 状态：现有 `appearance.go:178 hookDeliveries`；**耗时字段 待补·后端**
- 权限：`platform.plugin.read`｜reauth：否｜幂等：否
- 请求：无（固定最近 50 条）
- 响应（现有）：200 `{ deliveries: [{ event: string, status: "queued"|"sent"|"failed", attempts: int, response_code: int(0=无响应), error_message: string, created_at: "YYYY-MM-DD HH:MM:SS", sent_at: string("" 表示未送达) }] | null }`；时间是数据库会话时区的无时区字符串（非 RFC3339）；code 不存在时返回 null 而不是 404
- 响应（待补·后端，追加）：每行 `duration_ms: int | null`（最后一次尝试的往返耗时）。需迁移：plugin_hook_deliveries 加列 `last_duration_ms int`
- 错误：无特有
- 设计：卡片展开「投递记录」（状态码、事件、耗时、时间）。映射：状态码←response_code（0 显示「—」并用 error_message 作 tooltip）；「user.created · 重试 2」这类行←后端一次投递一行、重试累加在 `attempts` 上，显示为「event · 第 n 次」

#### POST v1/plugin-hooks/{code}/test — 同步测试投递
- 状态：现有 `appearance.go:188 testHook`；**耗时字段 待补·后端**
- 权限：`platform.plugin.write`｜reauth：是｜幂等：否
- 请求：无 body（发送固定的 `panel.test` 事件，带签名）
- 响应：200 `{ sent: true, response_code: int }`；待补·后端追加 `duration_ms: int`（需迁移：否）
- 错误：422 validation_failed「发送失败：…」（包括钩子不存在——处理器把所有错误都包成 422，见核对笔记）/「对方返回 HTTP xxx，不是 2xx」
- 设计：卡片「测试投递」→ toast「200 OK · 88 ms」

### 后台-09 通知与安全 · 安全与运维（审计日志 / 访问日志 / 风控 / 降级开关）

#### GET v1/audit — 审计日志
- 状态：现有 `handlers.go:452 listAudit`（数据 `adminops/service.go:890 ListAudit`）；**搜索与展示字段 待补·后端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求（现有）：query `limit?: int(1–200，默认 50)`、`offset?: int`、`action?: string（前缀匹配）`、`actor_kind?: "user"|"admin"|"system"|"agent"|"plugin"|"anonymous"`、`outcome?: "success"|"failure"|"denied"|"partial"`；待补·后端追加 `q?: string`（对 action、操作者邮箱做 ILIKE，对 resource_id 做精确匹配）
- 响应（现有）：200 `{ events: [{ id, occurred_at: rfc3339, actor_kind, actor_email: string|null, action, resource_type: string|null, resource_id: string|null, api_domain: string|null, outcome, reason: string|null }], total: int }`
- 响应（待补·后端，追加）：每行 `source_ip: string | null`（解密 source_ip_enc，与 access-log 同权限同做法）、`resource_label: string | null`（按 resource_type join：user→email、order→order_no、node→name、plan→name、ticket→ticket_no，其它 null）、`auth_context: "session" | "reauth" | null`（写入时主体是否处于近期二次认证内；历史记录为 null）。需迁移：audit_events 加列 `auth_context text NULL CHECK (auth_context IN ('session','reauth'))`，由 `audit.Write` 从 ctx 的 Principal 取值
- 错误：无特有
- 设计：后台-09「审计日志」（搜索框「操作人、动作或对象」、导出、表格：时间、操作人、动作、对象、来源 IP、认证）。映射：操作人←`actor_email`（actor_kind=system 显示「系统」）；对象←`resource_label ?? resource_type + 短 id`，`reason` 作为对象列 tooltip；认证←auth_context（reauth=「二次认证」、session=「会话」、null=「—」）。待补·前端：搜索框旁加筛选「动作前缀 / 操作者类型 / 结果」，分页用 total

#### GET v1/audit/export — 审计日志导出
- 状态：待补·后端
- 权限：`security.audit.read` + `ops.export`｜reauth：是（导出带走含明文 IP 的全量记录）｜幂等：否（GET）
- 请求：query 与 GET v1/audit 相同的筛选（action、actor_kind、outcome、q）+ `from?: YYYY-MM-DD`、`to?: YYYY-MM-DD`（半开区间，与订单列表的日期解析一致）；单次最多 50000 行
- 响应：200 `text/csv; charset=utf-8`，`Content-Disposition: attachment; filename="audit-YYYYMMDD-HHMMSS.csv"`，列：occurred_at, actor_kind, actor_email, action, resource_type, resource_id, resource_label, api_domain, outcome, reason, source_ip, auth_context。导出本身写一条审计 `audit.export`（筛选条件与行数进 after_digest）
- 错误：422 `fields.from|to`（格式错）；422 validation_failed「超过 50000 行，请缩小时间范围」
- 需迁移：否（依赖上一条的 auth_context 列）
- 设计：审计页「导出」按钮。映射：前端用 fetch 带 Bearer 取 Blob 再触发下载（不能用 `<a href>`）；该路径不以 events/stream 结尾，受 25 秒超时约束，所以设上限

#### GET v1/access-log — 安全事件明细（非 HTTP 访问日志）
- 状态：现有 `panel/internal/api/admin/access_log.go:49 accessLogList`；**筛选 待补·后端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求（现有）：query `limit?: int(1–200，默认 50)`、`offset?: int`、`category?: "login"|"register"|"subscribe"|其它`、`ip?: string（精确匹配，走哈希）`、`user?: uuid | 邮箱片段`
- 响应：200 `{ items: [{ category: "login"|"register"|"reset_password"|"order"|"payment"|"ticket"|"admin"|"other"|"subscribe", action?: string, user_id?: uuid, user_email?: string, ip?: string, geo?: string, network_kind?: string, user_agent?: string, outcome?: string, occurred_at: rfc3339 }] }`（无 total；audit_events 与 subscription_fetch_log 两路归并）
- 待补·后端：①`category` 对 login/register/subscribe 以外的值（order、payment、ticket、admin、reset_password、other）目前**不过滤审计表**（`auditActionFilter` 返回 nil），改成按 `categoryFromAction` 的同一套前缀过滤，并在非 subscribe 类别时跳过订阅日志；②新增 `outcome?: "success" | "failure" | "denied" | "partial" | "error"`（error = 非 success；订阅日志 result≠'ok' 视为 error）。需迁移：否
- 错误：无特有
- 设计：后台-09「访问日志」（深色「实时尾随」终端：时间、方法、路径、状态码、耗时、IP；分段「全部 / 仅错误 / 管理端」）。映射（以后端为准）：这是**安全事件流**，不是 nginx 访问日志：方法列←category 徽标，路径列←action，状态列←outcome（非 success 标红），耗时列删除，IP 列←`ip · geo`，行 tooltip←user_email + user_agent；标题改「实时尾随 · 登录/注册/订阅拉取/管理动作」；「仅错误」←`outcome=error`，「管理端」←`category=admin`；「实时尾随」用 5 秒轮询 `offset=0` 实现（审计表不在 SSE 监听里）。待补·前端：加 IP、账号两个筛选框和「订阅拉取」「登录」「注册」分段

#### GET v1/ip-clusters — 共享 IP 聚类
- 状态：现有 `profile.go:287 ipClusters`；**展示与处置字段 待补·后端**
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：无（固定前 50 个，近 90 天，账号数 >1）
- 响应（现有）：200 `{ clusters: [{ ip: string（解不开时为 ""）, accounts: int, events: int, first: rfc3339, last: rfc3339, emails: string[] }] }`
- 响应（待补·后端，追加）：每个 cluster 加 `key: string`（source_ip_hash 的 hex，处置接口用它定位，不依赖明文 IP）、`geo: string`、`network_kind: string`（与 access-log 同一 GeoIP 解析）、`risk: "high" | "mid" | "low"`（high：账号数 ≥5 或 network_kind 为机房；mid：账号数 ≥3；否则 low）、`users: [{ id: uuid, email: string, status: string, active_plan: string | null }]`（替代只有邮箱的 emails，emails 保留兼容）、`review: { decision: "normal" | "disabled", decided_at: rfc3339, expires_at: rfc3339 | null } | null`；标记为正常且未过期的聚类默认不返回，`?include_reviewed=1` 时返回。需迁移：新表 ip_cluster_reviews（见迁移预估）
- 错误：无特有
- 设计：后台-09「风控」卡片（IP、归属 · 最近时间、风险徽标、账号+套餐列表、标记为正常、禁用 N 个账号）

#### POST v1/ip-clusters/{key}/review — 标记为正常
- 状态：待补·后端
- 权限：`security.risk.review`（权限字典已有、目前无路由使用）｜reauth：否｜幂等：否（重复标记只刷新有效期）
- 请求：`{ note?: string(≤500) }`
- 响应：200 `{ key: string, decision: "normal", expires_at: rfc3339（now+30 天） }`；写审计 `risk.ip_cluster.mark_normal`
- 错误：404 not_found（key 不对应任何近 90 天聚类）
- 需迁移：新表 ip_cluster_reviews
- 设计：卡片「标记为正常」→ 结果行「已标记为正常，30 天内不再提示」

#### POST v1/ip-clusters/{key}/disable-accounts — 批量禁用聚类内账号
- 状态：待补·后端
- 权限：`security.risk.review` + `iam.user.write`｜reauth：是｜幂等：是 `ip_cluster_disable`
- 请求：`{ user_ids: uuid[]（必须是该聚类当前成员的子集，前端默认全选）, reason: string(5–500 字) }`
- 响应：200 `{ disabled: int, skipped: [{ user_id: uuid, reason: "self" | "administrator" | "not_member" | "already_disabled" }] }`。逐个复用 `SetUserStatus(status="suspended")` 的语义（吊销会话与 refresh、最后一个管理员保护、不能改自己），单事务；写 review 记录 decision=disabled 与审计 `risk.ip_cluster.disable`
- 错误：404 not_found（key 无效）；422 `fields.reason|user_ids`；409 idempotency_key_reuse
- 需迁移：同上一条的新表
- 设计：卡片「禁用 N 个账号」+ reauth 危险确认框（「账号将被登出，订阅停止下发」）。映射：设计「禁用」→ 后端 `suspended`（可恢复，不用 banned）。待补·前端：确认框加「原因」必填输入

#### GET v1/switches — 降级开关列表
- 状态：现有 `handlers.go:468 listSwitches`
- 权限：`security.audit.read`｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ switches: [{ code: string, enabled: bool, essential: bool, reason: string | null }] }`（essential 在前）。**语义：enabled=true 表示功能正常可用，关闭=降级**；现有 code：auth.login、subscription.renewal、client.config_sync（essential，永远开着）、auth.registration、ops.bulk_export、ops.reports、node.autoscale
- 错误：无特有
- 设计：后台-09「降级开关」列表（名称、code、说明、状态「已开启/关闭」、开关）。映射（以后端编码为准）：设计 `portal.register`「暂停新用户注册」↔ 后端 `auth.registration`，**极性相反**：设计「已开启（红）」= 后端 `enabled=false`；设计的其余 5 个开关后端没有对应，见下一条；中文名、说明文案由前端按 code 维护字典。待补·前端：essential 行显示锁定、不可切换；ops.bulk_export/ops.reports/node.autoscale 目前没有任何代码读取（见待决 D-A-3），在定下之前以「未接入」灰显

#### POST v1/switches/{code} — 切换降级开关
- 状态：现有 `handlers.go:482 setSwitch`；**reauth、广播、新开关 待补·后端**
- 权限：`platform.settings.write`｜reauth：现有否 → 待补·后端改为是（设计明确要求每次切换二次认证）｜幂等：否
- 请求：`{ enabled: bool, reason: string }`（`enabled=false` 时 reason 必填——数据库 CHECK 约束）
- 响应：200 `{ ok: true, enabled: bool }`
- 错误：404 not_found（code 不存在）；409 conflict「开关 x 不允许该操作：…」（关闭 essential 开关，或关闭时没给 reason——两者都是数据库约束报错，**不是 422**）
- 待补·后端：①路由加 RequireRecentReauth；②成功后处理器向 `ChannelAdmin` 发布 topic `switches.changed`，payload `{ code, enabled }`（只发 admin 频道，不挂表触发器——触发器会把无 user_id 的变更同时广播到门户公共频道）；③新增 4 个开关并在对应位置生效（沿用「enabled=可用」极性）：`billing.checkout`（关=拒绝新建订单与发起支付，已发起支付的回调照常处理）、`marketing.giftcard.redeem`（关=拒绝兑换礼品卡）、`notify.email`（关=邮件投递扫描器跳过 email 渠道，队列保留，恢复后按序投递）、`admin.writes`（关=admin 网关除 `POST v1/switches/*`、`v1/auth/*`、`v1/me/password` 外的所有非 GET 请求回 503 service_unavailable「管理端只读模式」）；「订阅下发使用缓存」见待决 D-A-3。需迁移：feature_switches 按租户插入 4 行（数据迁移，无表结构变更）
- 设计：开关点击 → reauth 危险确认框 → toast；顶栏实时事件广播。映射：设计文案「开启『暂停…』」对应后端 `enabled=false`。待补·前端：确认框加「原因」输入（后端关闭时必填，开启时可选）

## 4. public 网关

### 本网关通用事实

> 金额一律为币种最小单位整数（CNY 分）；流量一律为字节（quota metric `traffic.bytes`）。时间为 RFC3339 UTC。
> 门户是 hash 路由单页：网关只下发 `/` 与 `/assets/*`（`platform/webapp/webapp.go:91 Mount`），`/orders` 这类单段路径会落到 JSON 404，两段路径会落到订阅通配 `/{prefix}/{token}` 的伪装 404。

> 本段全部路由都在 `panel/internal/api/public/router.go` 的「需登录」分组内：`RequireAuth` + 按账号限流 `acct`。所以下文权限一律写「登录用户」，通用的 401 和 429 不再重复。
> 金额一律用最小货币单位（分，int64），币种目前固定 CNY。时间是 RFC3339 字符串。
> 设计稿文件用简写：「门户-06」=用户门户-06-邀请返利，其余类推；「外壳」=用户门户.dc.html。

### 门户外壳与认证（用户门户.dc.html：登录、两步注册、快捷登录、顶部导航、支付弹窗）

#### POST v1/auth/login — 邮箱密码登录
- 状态：现有 `panel/internal/api/public/handlers.go:142 login`
- 权限：匿名｜reauth：否｜幂等：否（认证组限流 auth_route/auth_net）
- 请求：`{ email: string, password: string }`
- 响应：200 `{ access_token: string, refresh_token: string, token_type: "Bearer", expires_in: int(秒), user_id: uuid }`
- 错误：401 unauthorized「邮箱或密码不正确」（账号不存在、口令错、账号非 active 三者同一响应，IAM-006）
- 设计：门户外壳 登录 tab。refresh_token 无任何消费接口（见核对笔记），前端**不保存**；access TTL 默认 720h，任一接口 401 → 清令牌回登录页。

#### POST v1/auth/quick-login — 消费快捷登录令牌换正式凭证
- 状态：现有 `panel/internal/api/public/selfservice.go:107 quickLogin`（domain `identity/quicklogin.go ConsumeQuickLogin`）
- 权限：匿名｜reauth：否｜幂等：否（令牌一次性，认证组限流）
- 请求：`{ token: string }`
- 响应：200 `{ access_token: string, refresh_token: string, expires_in: int }`（**不含** token_type、user_id，登录后调 `GET v1/me`）
- 错误：401「快捷登录链接无效或已过期」（不存在/已用/超 60 秒/签发会话已登出或被踢/并发被抢用）；403「账号当前不可登录」
- 设计：门户外壳 快捷登录 tab。保留规则 1：**删除**设计里「输入邮箱获取一次性登录链接」输入框、「发送登录链接」按钮与「模拟：打开邮件中的链接」；该 tab 改为说明文字（「在已登录设备打开 账号安全 → 快捷登录 生成链接，60 秒内在本设备打开」）+ 一个「粘贴链接或令牌」输入框与「登录」按钮。链接由门户生成（签发接口 `POST v1/me/quick-login` 属账号安全分段，返回 `{ token, expires_at, expires_in: 60 }`），格式定为 `<门户根>/#/quick-login/<token>`：令牌放 hash 不进服务器与 nginx 日志；入口页加载时识别该 hash 自动调用本接口，成功后 `history.replaceState` 抹掉令牌。账号安全分段须用同一格式。

#### POST v1/auth/register/start — 注册第 1 步：邮箱 + 邀请码，发验证码
- 状态：现有 `panel/internal/api/public/handlers.go:74 registerStart`
- 权限：匿名｜reauth：否｜幂等：否（RateLimitStrict：Redis 不可用即拒绝；同邮箱 5 次/10 分钟、同邀请码 20 次/10 分钟）
- 请求：`{ email: string, invite_code?: string }`
- 响应：200 `{ registration_token: string, verification_required: bool, expires_at: RFC3339(10 分钟), message: string, dev_code?: string(仅开发模式) }`；邮箱已注册与否响应完全一致
- 错误：422 validation_failed `fields.email`「邮箱格式不正确」；403 forbidden「注册当前不可用或邀请码无效」（registration_mode=closed；invite_only 下邀请码缺失/无效；open 下填了无效邀请码也拒）
- 设计：门户外壳 注册步骤 1「发送验证码」。邀请码输入框是否必填按 `GET v1/site-config` 的 `registration_mode`：invite_only 必填，open 选填（标签去掉「必填」），closed 隐藏注册 tab。邀请链接 `/?invite=CODE`：入口页读 `location.search` 的 `invite`，转大写存 sessionStorage，自动切到注册 tab 并预填，随后 `replaceState` 清掉查询串。
- 映射：`verification_required=false` → 步骤 2 隐藏验证码输入框，按钮文案「完成注册」不变。

#### POST v1/auth/register/complete — 注册第 2 步：验证码 + 密码
- 状态：现有 `panel/internal/api/public/handlers.go:117 registerComplete`
- 权限：匿名｜reauth：否｜幂等：否（同 registration_token 6 次/10 分钟）
- 请求：`{ registration_token: string, code: string(6 位数字，verification_required=false 时传 ""), password: string }`；`invite_code` 为废弃兼容字段、服务端忽略，**前端不传**
- 响应：201 `{ user_id: uuid, email: string }`（**不签发令牌**）
- 错误：403「注册当前不可用或邀请码无效」（令牌无效/过期、验证码错、尝试 ≥5 次、邀请码已被用尽）；422 `fields.password`「密码至少需要 8 个字符」/「密码必须同时包含字母和数字」/「密码过长」
- 设计：门户外壳 注册步骤 2「完成注册」。映射：成功后前端用同一邮箱+密码自动调 `POST v1/auth/login` 再进入门户；步骤 2 收到 403 显示「验证码错误或已过期」；前端校验补「必须同时含字母和数字」。

#### POST v1/auth/logout — 退出当前会话
- 状态：现有 `panel/internal/api/public/handlers.go:171 logout`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无 body
- 响应：204
- 错误：无特有
- 设计：账户下拉菜单「退出登录」。成功或 401 都清本地令牌回登录页。

#### GET v1/me — 当前用户
- 状态：现有 `panel/internal/api/public/handlers.go:362 me`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ user_id: uuid, email: string, display_name: string|null, status: string, created_at: RFC3339, permissions: string[]|null }`
- 错误：无特有
- 设计：顶部头像首字母、下拉菜单用户名/邮箱。映射：`userName = display_name ?? email.split('@')[0]`；菜单里的套餐名取 `GET v1/me/subscriptions`，余额取 `GET v1/me/balance`，佣金取 `GET v1/me/commission`（邀请返利分段），未读数取消息分段的通知接口。

#### GET v1/site-config — 登录/注册页需要的站点开关
- 状态：现有 `panel/internal/api/public/handlers.go:942 siteConfig`
- 权限：匿名｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ registration_mode: "closed"|"invite_only"|"open", email_verification: bool }`
- 错误：无特有
- 设计：登录页。映射：页脚文案「Pandora · 注册已开放 · 需要邀请码」按 mode 生成（closed →「注册已关闭」；invite_only →「注册已开放 · 需要邀请码」；open →「注册已开放」）；closed 时注册 tab 不渲染。
- 待补·前端（→ 补进 登录页注册步骤 2）：`email_verification=false` 时预先隐藏验证码框（以 register/start 返回的 verification_required 为最终准）。

#### GET v1/appearance — 主题与插槽
- 状态：现有 `panel/internal/api/public/telegram.go:147 appearance`（domain `appearance/service.go:75 Public`）
- 权限：匿名｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ theme: null | { id, code, name, is_builtin: bool, is_active: bool, tokens: {[k]: string}(扁平 CSS 变量), branding: { site_name?, tagline?, ... }, custom_css: string }, slots: { [slot_key]: html(服务端已白名单净化) } }`；slot_key 取值：`portal.login.notice`、`portal.home.banner`、`portal.home.aside`、`portal.sidebar.extra`、`portal.subscribe.notice`、`portal.plans.notice`、`portal.footer`
- 错误：无特有
- 设计：登录页顶部「国庆活动…」横幅 → `portal.login.notice`；概览顶部 → `portal.home.banner`，概览底部 → `portal.home.aside`；我的订阅顶部 → `portal.subscribe.notice`；选购套餐顶部 → `portal.plans.notice`；页脚 → `portal.footer`；新设计无侧栏，`portal.sidebar.extra` 放到账户下拉菜单分隔线下方。插槽为空则不占位。theme 的 tokens/custom_css 见 D-E-4。

#### GET v1/events — 用户实时事件流（SSE）
- 状态：现有 `panel/internal/api/public/events.go:27 events`
- 权限：登录用户｜reauth：否｜幂等：否（路径末段 events，Timeout 中间件豁免）
- 请求：无；必须用 fetch 流读（Bearer 头），不能用 EventSource
- 响应：200 `text/event-stream`。首帧 `retry: 5000\n\n`；事件帧 `id: <连接内自增序号>\nevent: <topic>\ndata: <单行 JSON>\n\n`；无事件时每 25 秒一帧注释 `: ping\n\n`。不支持 Last-Event-ID 续传，重连后应全量重拉当前页数据。topic 与载荷（`platform/realtime/listener.go:43 topicFor`、`realtime.go:264 FormatSSE`）：
  - 数据库变更类，载荷 `{ table: string, op: "INSERT"|"UPDATE"|"DELETE", id?: string }`：`orders.changed`（orders，只推本人）、`subscriptions.changed`（subscriptions / subscription_credentials 只推本人；quota_balances 无 user_id，**推全租户**，见核对笔记）、`tickets.changed`（tickets，本人）、`plans.changed`（plans / plan_versions / prices，全租户）、`nodes.changed`（全租户）、`announcements.changed`（全租户）、`data.changed`（兜底）
  - 应用发布类：`ticket.updated` 载荷 `{ ticket_id: uuid }`（`public/handlers.go:792`、`admin/handlers.go:622`）
- 错误：无特有
- 设计：全局。映射：orders.changed → 重拉订单列表/待支付条/余额/订阅（充值、下单履约都会改 orders）；subscriptions.changed → 节流（≥5 秒合并一次）后重拉 `GET v1/me/subscriptions`；plans.changed → 选购页重拉；announcements.changed → 概览公告重拉；nodes.changed → 我的订阅节点列表节流重拉。余额的其他来源（礼品卡、佣金转余额、后台调账）没有推送，自己的操作完成后主动重拉 `GET v1/me/balance`。60 秒收不到任何字节视为断线重连。

#### GET v1/payment-methods — 可用的支付方式
- 状态：待补·后端
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ methods: [{ provider: string(payment_providers.code), method: string(渠道内方式，epay 为 alipay|wxpay|qqpay…), label: string, currencies: string[] }] }`。来源：`payment_providers WHERE enabled AND accepting_new`（人工单专用渠道 accepting_new=false 自然排除）；一个渠道可展开多行，方式列表取渠道 `config.methods`（string[]），缺省为该渠道 default_method；label = 渠道 display_name 或方式中文名（alipay 支付宝、wxpay 微信支付）。
- 错误：无特有
- 需迁移：否（methods 存于 payment_providers.config jsonb）
- 设计：结账页「支付方式」四宫格、钱包「充值」下拉。映射：设计的 wechat → method `wxpay`；**USDT 选项删除**（不做 USDT）；「余额」不是支付方式而是抵扣开关，见 `POST v1/orders`。

### 门户-01 概览

本页组合读取：`GET v1/me/subscriptions`（当前套餐、剩余/已用/总流量、到期、重置日、在线设备，见下一模块）、`GET v1/me/balance`（余额格）、`GET v1/me/commission`（可提佣金格，邀请返利分段）、`GET v1/me/announcements`（公告列表，消息分段）、`GET v1/orders?status=pending_payment`（顶部待支付条）、`GET v1/me/subscription-links`（复制订阅地址）。
映射：待支付条文案「15 分钟内未支付将自动取消」→ 用订单 `expires_at` 显示倒计时（后端 30 分钟）；「买流量包 →」跳选购页流量包 tab；「续费」「立即续费」→ 结账页续费模式（`POST v1/me/subscriptions/{id}/renew`）。多条订阅时概览展示第一条 status∈{active,trialing,grace,past_due} 的订阅（按 current_period_end 最晚）。

#### GET v1/me/subscriptions/{id}/usage — 本期按日用量（柱状图）
- 状态：待补·后端
- 权限：登录用户（订阅须归属本人，否则 404）｜reauth：否｜幂等：否
- 请求：query `days?: int(1–93，默认覆盖当前流量周期)`
- 响应：200 `{ timezone: string(用户 timezone，缺省租户 timezone), period_start: RFC3339, period_end: RFC3339|null, days: [{ date: "YYYY-MM-DD", bytes: int(计费后字节，已乘倍率) }], today_bytes: int, avg_daily_bytes: int }`
- 错误：404 订阅不存在或不属于本人
- 需迁移：新表 `subscription_usage_daily(tenant_id, subscription_id, day date, bytes bigint, PRIMARY KEY(tenant_id, subscription_id, day))` + RLS；在 `nodefabric/uniproxy.go` 流量上报事务里与 `quota_balances.consumed` 同事务 upsert；不挂 zz_notify 触发器
- 设计：概览「本期用量」柱状图、日均/今天、「按目前的速度…」预测文案（预测由前端用 avg_daily_bytes 与剩余流量计算）。

### 门户-02 我的订阅

#### GET v1/me/subscriptions — 我的订阅列表
- 状态：现有 `panel/internal/api/public/handlers.go:394 listSubscriptions`；字段扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ subscriptions: [{ id: uuid, plan_id: uuid, price_id: uuid|""(无关联价格), plan_name: string, plan_version: int, status: "pending"|"trialing"|"active"|"past_due"|"grace"|"paused"|"cancelled"|"expired", current_period_start: RFC3339|null, current_period_end: RFC3339|null, currency: string, amount: int(下单时快照价), quotas: [{ metric: string, limit: int|null(仅套餐基础额度，不含追加), consumed: int, remaining: int|null(= limit+追加+调整−已用；null=不限) }] }] }`，按 created_at 倒序，含已过期/已取消
- 响应（待补·后端追加字段）：订阅级 `device_limit: int|null`（COALESCE(subscriptions.device_limit, plan_versions.max_devices)）、`online_devices: int`（视图 subscription_online_devices.device_count，近 5 分钟不同 IP 数）、`quota_reset_strategy: "never"|"natural_month"|"billing_cycle"|"fixed_day"`、`next_reset_at: RFC3339|null`（traffic.bytes 当前行 period_end）、`renewable: bool`（status∈{active,trialing,grace,past_due} 且 plans.allow_renewal）、`renewal_price: null | { id, currency, unit_amount, billing_interval, interval_count, available: bool }`（订阅当前 price_id 对应价格，归档/组价不符/不在有效期则 available=false）、`pack_remaining_bytes: int`（流量包上线后）；quotas[] 追加 `period`、`period_start`、`period_end`、`granted_addon`、`adjusted`
- 错误：无特有
- 设计：概览主卡、我的订阅头部 metaLabel「N 天后到期 · 日期 · M 台设备 · 当前在线 K」、下拉菜单套餐徽标。映射：已用 = consumed；总量 = consumed + remaining（remaining 为 null 显示「不限」）；剩余 = remaining；重置日 = next_reset_at（strategy=never 时隐藏「N 天后重置」）；天数 = current_period_end − now。
- 待补·前端（→ 补进 我的订阅头部）：用户同时有多条可用订阅时（后端允许，新购另一套餐会开第二条），套餐名旁加订阅切换下拉；status=grace/past_due 时头部加警示徽标「宽限期」「待续费」。

#### GET v1/me/subscriptions/{id}/nodes — 该订阅可用节点摘要
- 状态：现有 `panel/internal/api/public/subscribe.go:204 meSubscriptionNodes`（domain `subscription/service.go:419 ListOwnedNodePreviews`）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：路径 id（订阅 uuid）
- 响应：200（Cache-Control: no-store）`{ count: int, nodes: [{ name: string, protocol: string(节点 type，如 vless/hysteria2/trojan/vmess/shadowsocks), traffic_rate: number }] }`
- 错误：404 订阅不属于本人、非 UUID、status 不在 {active,trialing,grace}、或无有效订阅凭据
- 设计：我的订阅「可用节点」。保留规则 3：**删除**国家代码徽标列（cc）与负载列（空闲/较忙/繁忙/维护中及圆点）；网格改为 名称 / 协议 / 倍率 三列。映射：protocol 前端映射展示名（vless→VLESS，hysteria2→Hysteria2…）。
- 待补·前端（→ 补进 可用节点每行）：traffic_rate ≠ 1 时显示「×1.5」倍率标签。

#### GET v1/me/subscription-links — 订阅地址与拉取统计
- 状态：现有 `panel/internal/api/public/subscribe.go:167 meSubscriptionLinks`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ links: [{ subscription_id: uuid, url: string(PublicBaseURL/前缀/token), expires_at: RFC3339|null, fetch_count: int, last_fetched_at: RFC3339|null, distinct_sources_24h: int }] }`；只含 active 且未过期的凭据；无法解密的旧凭据不返回
- 错误：无特有
- 设计：我的订阅「订阅地址」+「复制」、概览「复制订阅地址」、一键导入（深链由前端用 url 拼：clash://install-config?url=…、sing-box://import-remote-profile?url=… 等；需要固定格式时可附 `?target=` / `flag=` / `type=`）。按 subscription_id 与订阅配对；某订阅无链接时地址框显示「重新生成」按钮 → rotate。
- 待补·前端（→ 补进 我的订阅「订阅地址」说明行下方）：「已被拉取 N 次 · 最近 <相对时间> · 近 24 小时 K 个来源」；K > device_limit 时整行转 warn 色并提示「可能已泄露，建议更换订阅地址」。

#### POST v1/me/subscriptions/{id}/rotate — 更换订阅地址
- 状态：现有 `panel/internal/api/public/subscribe.go:237 rotateSubscriptionLink`
- 权限：登录用户｜reauth：否｜幂等：否（每次调用都会再换一次）
- 请求：路径 id；无 body
- 响应：200 `{ url: string }`（旧凭据立即 revoked）
- 错误：404 订阅不存在/不属于本人/非 UUID
- 设计：我的订阅「更换订阅地址」确认框。按钮在请求期间禁用防连点；成功后用返回 url 覆盖显示并重拉 subscription-links。

#### POST v1/me/subscriptions/{id}/renew — 续费（在原订阅上延长）
- 状态：现有 `panel/internal/api/public/handlers.go:1063 createRenewal`（domain `billing/renewal.go:51 CreateRenewal`）
- 权限：登录用户｜reauth：否｜幂等：是 `subscription_renewal_create`
- 请求：`{ price_id?: uuid(不传沿用订阅当前价格；传同套餐其他价格=换周期续费), use_balance?: int(≥0，超出应付自动截断到应付), coupon_code?: string }`
- 响应：201 `{ order_id: uuid, order_no: string, currency: string, total_amount: int, discount_amount: int, balance_applied: int, payable_amount: int, status: "pending_payment"|"fulfilled" }`；payable=0 时当场履约（status=fulfilled，不再调 pay）
- 错误：404 订阅不属于本人；409「这条订阅当前不能续费」（status 不在 active/trialing/grace/past_due）、「该套餐当前不允许续费」、「这条订阅没有关联价格，无法自动续费」、「所选价格已下架，请重新选择」（价格归档、组价不符、不在有效期）、「余额不足」；优惠码错误同 coupons/preview
- 设计：概览/我的订阅「续费」、选购页当前套餐卡「续费」→ 结账页（续费模式）。映射：结账页「剩余天数折算：续期叠加」为真实语义（从当前周期末往后延，凭据与订阅地址不变）；续费周期选项取 `renewal_price` + 该 plan_id 在 `GET v1/plans` 中的 prices。
- 待补·前端（→ 补进 结账页续费模式）：续费遇改价——价格不可变（改价=后台归档旧价+新增新价），`renewal_price.available=false` 或下单回 409「所选价格已下架」时，周期列表只列该套餐当前有效价格、默认选同 billing_interval，并在订单预览加一行灰字「原价格已调整，按当前价格计费」；该套餐已不在 plans 列表（下架/隐藏）时提示「该套餐已停售，请选购其他套餐」并跳选购页。

### 门户-03 选购套餐与结账（套餐列表、流量包、结账页）

#### GET v1/plans — 可购套餐目录
- 状态：现有 `panel/internal/api/public/handlers.go:188 listPlans`；字段扩展为待补·后端（无迁移）
- 权限：匿名（路由在免鉴权区，但会解析 Bearer：带令牌才看得到 authenticated/group 套餐与组专属价格，门户登录后必须带）｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ plans: [{ id: uuid, code: string, name: string, description: string|null, version: int|null, max_devices: int|null, quotas: [{ metric: string, limit: int|null, unit: string(traffic 为 "bytes"), period: "total"|"cycle"|"day"|"month" }], prices: [{ id: uuid, currency: "CNY"|"USD", unit_amount: int, billing_interval: "day"|"week"|"month"|"quarter"|"year"|"one_time", interval_count: int, trial_days: int }] }] }`，按 sort_order；prices 按金额升序
- 响应（待补·后端追加字段）：`quota_reset_strategy`、`quota_reset_day: int|null`（来自当前 plan_version）、`allow_renewal: bool`、`allow_upgrade: bool`
- 错误：无特有
- 设计：选购页「订阅套餐」卡片、月付/季付/年付切换、结账页周期选项。映射：周期 1m/3m/12m ↔ (month,1)/(month,3)|(quarter,1)/(year,1)|(month,12)，其他组合按「每 N 天/周/月」通用文案；「省 ¥X」「折合 ¥Y/月」「年付 · 省 N%」由前端用同套餐同币种价格计算；GB/月 = traffic.bytes 的 limit；「到期日自动重置」按 quota_reset_strategy 出文案（never→「不重置」、natural_month→「每月 1 日重置」、fixed_day→「每月 N 日重置」）；「N 台设备同时在线」= max_devices；只展示 CNY 价格（余额与 epay 只有 CNY）；「当前」徽标 = plan.id 等于当前订阅 plan_id；月付副文案「可随时取消自动续费」**改为**「按月付费」（后端无自动续费）。特性列表与「推荐」徽标见 D-E-3。

#### POST v1/coupons/preview — 优惠码试算
- 状态：现有 `panel/internal/api/public/handlers.go:817 previewCoupon`（domain `billing/coupon.go:248 PreviewForPrice`）；扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否（不落库）
- 请求（现有）：`{ plan_id: uuid, price_id: uuid, coupon_code: string }`；（待补·后端）增加互斥形态 `{ pack_id: uuid, coupon_code: string }`
- 响应（现有）：200 `{ subtotal: int, discount: int, payable: int, currency: string }`；（待补·后端追加）`coupon: { code: string, discount_type: "percent"|"fixed", discount_value: int(percent 为万分比，fixed 为分) } | null`
- 错误：422 `fields.plan_id`/`fields.price_id`「必填」（非 UUID）；404 价格不属于该套餐或非 active；422「优惠码不存在或已失效」「优惠码已过期」「订单金额未达到优惠码的使用门槛」「这个优惠码不适用于所选套餐」「这个优惠码不可用」；409「优惠码已达使用上限」「你已使用过这个优惠码」。coupon_code 为空时不报错、discount=0，前端空码不要调用。
- 设计：结账页优惠码「使用」。映射：成功行「已使用 CODE：20% 折扣 / 立减 ¥X」用追加的 coupon 字段；失败统一显示后端 message。

#### POST v1/orders — 新购下单
- 状态：现有 `panel/internal/api/public/handlers.go:497 createOrder`（domain `billing/checkout.go:119 CreateOrder`）
- 权限：登录用户｜reauth：否｜幂等：是 `order_create`
- 请求：`{ plan_id: uuid, price_id: uuid, use_balance?: int(余额抵扣金额，分；<0 视为 0，超过应付截断到应付), coupon_code?: string }`
- 响应：201 `{ order_id: uuid, order_no: string, currency: string, total_amount: int(优惠后), discount_amount: int, balance_applied: int, payable_amount: int, status: "pending_payment"|"fulfilled" }`；订单 expires_at = 创建 + 30 分钟；payable=0 时当场履约（status=fulfilled，余额已扣，不再调 pay）
- 错误：422 `fields.plan_id`/`fields.price_id`「必填」；404 套餐/价格不存在或对本人不可见；409「该套餐当前不接受新购」「该套餐尚未发布可用版本」「已达到该套餐的限购次数」「该套餐已售罄」「该价格已下架」「该价格当前不在有效期内」「套餐当前版本未完成发布」「余额不足」；优惠码错误同 coupons/preview
- 设计：结账页「提交订单并支付」。映射：设计把「余额」当成四选一支付方式，后端是**抵扣金额**——结账页改为「使用余额抵扣（可用 ¥X）」开关 + 外部支付方式单选；开关打开时 `use_balance = min(余额, 优惠后应付)`；抵扣后 payable=0 时隐藏支付方式、按钮文案「确认支付」；订单预览「余额抵扣」行显示 balance_applied。「剩余天数折算：差价已计入」只在变更套餐（change-plan）时出现，新购无此行。用户已有可用订阅且选了其他套餐时走 `POST v1/me/subscriptions/{id}/change-plan`，选了同套餐走 renew，没有订阅走本接口。

#### POST v1/orders/{id}/pay — 发起支付，取收银台跳转
- **修订 R8（2026-09-24）**：支付渠道已停用时回 503「该支付渠道已停用」（原为 500）。
- 状态：现有 `panel/internal/api/public/handlers.go:562 payOrder`（domain `billing/payments.go:157 CreatePaymentIntent`，epay 适配 `domain/payment/epay/epay.go:123`）
- 权限：登录用户｜reauth：否｜幂等：否（服务层行锁复用在途意图：同渠道重复点击回同一收银台，reused=true；换渠道作废旧意图重建）
- 请求：`{ provider: string(payment-methods 的 provider), method?: string(payment-methods 的 method), return_url?: string(必须以 PublicBaseURL + "/" 开头，否则回落 PublicBaseURL + "/#orders") }`
- 响应：201 `{ intent_id: uuid, http_method: "GET"|"POST", redirect_url: string, form_fields: {[k]: string}, amount: int(=payable), currency: string, reused: bool }`。epay 与 demo 适配器都返回 **GET**，redirect_url 已含签名参数
- 错误：422 `fields.provider`「必填」；404 订单不存在/不属于本人、「未知的支付渠道」；409「该订单当前状态不可支付」（非 draft/pending_payment）、「该订单无需外部支付」（payable≤0）；503「该支付渠道暂停收单…」；渠道未启用与渠道下单失败目前是 500
- 设计：门户外壳「支付弹窗」。映射：**无二维码**——弹窗等待态改为「正在前往收银台…」+ 金额 + 订单号 + 「订单 30 分钟内有效」（设计写 15 分钟），拿到响应后 `window.location.assign(redirect_url)`（顶层 GET 导航不受 CSP `form-action 'self'` 约束）；`http_method=="POST"` 目前无适配器产出，前端视为错误提示「该支付方式暂不可用」，**不**渲染自动提交表单（会被 form-action 'self' 拦截）。return_url 传 `new URL('./#/orders/' + order_id + '?paid=1', location.href)`。回跳后订单详情页轮询 `GET v1/orders/{id}`（3 秒一次，最多 2 分钟）或等 orders.changed，status∈{paid,fulfilled} 时弹「支付成功」态（文案按 kind：topup「余额 +¥X」、new/renewal「已开通，有效期至 …」、addon「流量包已到账」）；超时显示「支付结果确认中，可稍后在订单中查看」。「模拟：支付完成」按钮删除；「稍后支付」= 关弹窗跳订单页，不调接口。

#### GET v1/traffic-packs — 流量包目录
- 状态：待补·后端
- 权限：匿名（与 plans 同在公开目录）｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ packs: [{ id: uuid, name: string, traffic_bytes: int, currency: "CNY", unit_amount: int, recommended: bool, standalone_allowed: bool }] }`，按 sort_order
- 错误：无特有
- 需迁移：新表 `traffic_packs(id, tenant_id, name, traffic_bytes, currency, unit_amount, recommended, standalone_allowed, status, sort_order, created_at)` + RLS + 挂 zz_notify（topic 需在 topicFor 加 `traffic_packs → plans.changed`）；后台管理接口与界面设计稿缺（见 D-E-1）
- 设计：选购页「流量包」tab 卡片（容量、价格、约 ¥/GB、「最划算」= recommended）、结账页流量包容量单选。

#### POST v1/me/traffic-pack-orders — 购买流量包
- 状态：待补·后端
- 权限：登录用户｜reauth：否｜幂等：是 `traffic_pack_order_create`
- 请求：`{ pack_id: uuid, subscription_id?: uuid(不传=当前可用订阅中 current_period_end 最晚的一条), use_balance?: int, coupon_code?: string }`
- 响应：201 与 `POST v1/orders` 同形 `{ order_id, order_no, currency, total_amount, discount_amount, balance_applied, payable_amount, status: "pending_payment"|"fulfilled" }`；订单 kind=`addon`，30 分钟过期；履约写 traffic_pack_grants
- 错误：404 流量包不存在/下架、subscription_id 不属于本人；409「没有可用订阅」（standalone_allowed=false 时）、「余额不足」；优惠码错误同上
- 需迁移：新表 `traffic_pack_grants(id, tenant_id, user_id, subscription_id, order_id, granted_bytes, consumed_bytes, created_at)` + RLS；改约束：00036 预留图/订单形状检查按 kind 分支（`o.kind IN ('new','renewal')` 与 `o.kind='topup'` 两支）加 `addon` 分支；orders.kind CHECK 已含 addon 无需改；uniproxy 扣量改为「先扣周期配额、再扣 grants」
- 设计：结账页（流量包模式）「提交订单并支付」；订单列表「流量包 · 200 GB」。

#### GET v1/me/traffic-packs — 我的流量包余量
- 状态：待补·后端
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ remaining_bytes_total: int, packs: [{ id: uuid, order_id: uuid, subscription_id: uuid|null, granted_bytes: int, consumed_bytes: int, remaining_bytes: int, created_at: RFC3339 }] }`
- 错误：无特有
- 需迁移：否（读 traffic_pack_grants，随上一条）
- 设计：选购页流量包 tab 提示「您的专业版本期还剩 N GB。买了流量包后…」、概览主卡剩余流量（订阅剩余 + 流量包剩余，另起一行小字「含流量包 X GB」）。

#### POST v1/me/subscriptions/{id}/change-plan/preview — 变更套餐试算（剩余价值折算）
- 状态：待补·后端
- 权限：登录用户｜reauth：否｜幂等：否（不落库）
- 请求：`{ plan_id: uuid, price_id: uuid, coupon_code?: string }`
- 响应：200 `{ direction: "upgrade"|"downgrade", currency: string, subtotal: int(新价格), proration_credit: int(原订阅剩余价值), discount: int, total: int(= max(0, subtotal − proration_credit − discount)), current_period_end: RFC3339, new_period_start: RFC3339(立即), new_period_end: RFC3339 }`
- 错误：404 订阅/套餐/价格不属于本人或不可见；409「目标套餐不允许变更」（allow_upgrade=false）、「与当前套餐相同，请使用续费」、「当前订阅状态不能变更」、「暂不支持降级」（视 D-E-2 结论）
- 需迁移：否（试算）
- 设计：选购页副文案「换套餐时剩余天数自动折算」、结账页订单预览「剩余天数折算：差价已计入」行（显示 −¥proration_credit）。折算公式见 D-E-2。

#### POST v1/me/subscriptions/{id}/change-plan — 变更套餐下单
- 状态：待补·后端
- 权限：登录用户｜reauth：否（public 无 reauth 机制，与 POST v1/orders 一致）｜幂等：是 `subscription_change_plan_create`
- 请求：`{ plan_id: uuid, price_id: uuid, use_balance?: int, coupon_code?: string }`
- 响应：201 与 `POST v1/orders` 同形，另加 `proration_credit: int`；订单 kind=`upgrade`，subscription_id=原订阅；履约**原地**更新该订阅的 plan_id/plan_version_id/price_id/周期/配额，凭据不变（设计「订阅地址不变」）
- 错误：同 preview，另 409「余额不足」
- 需迁移：改表 `orders` 加列 `proration_credit_amount app.minor_amount NOT NULL DEFAULT 0`，并把 total 恒等式扩成 subtotal − discount − proration_credit；改约束：00036 按 kind 分支的检查加 `upgrade` 分支（orders.kind CHECK 已含 upgrade）
- 设计：结账页（选了非当前套餐且已有可用订阅）「提交订单并支付」；支付成功文案「已开通，订阅地址不变」。

### 门户-04 订单

#### GET v1/orders — 我的订单列表
- 状态：现有 `panel/internal/api/public/my_orders.go:13 listMyOrders`（domain `billing/my_orders.go:58`）；扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求（现有）：query `status?: "draft"|"pending_payment"|"processing"|"paid"|"fulfilled"|"cancelled"|"expired"|"partially_refunded"|"refunded"`、`limit?: 1–100(默认 20，非法值也回 20)`、`offset?: int`；（待补·后端）`status` 接受逗号分隔多值
- 响应（现有）：200 `{ orders: [{ id: uuid, order_no: string, kind: "new"|"renewal"|"topup"(待补后还有 "addon"|"upgrade"), status: 上述 9 值, currency, total_amount, discount_amount, balance_applied, payable_amount, paid_amount, refunded_amount: int, plan_name?: string(订单项快照；topup 为空), cancellable: bool, created_at, paid_at?, cancelled_at?, cancel_reason?: string, expires_at? }], total: int }`，按 created_at 倒序；（待补·后端追加）行字段 `interval?: string`、`interval_count?: int`（首个订单项 snapshot_interval/_count）、`item_name: string`（首个订单项 snapshot_product_name，流量包据此显示容量）；顶层 `counts: { open: int(draft+pending_payment+processing), paid: int(paid+fulfilled), closed: int(cancelled+expired), refunded: int(partially_refunded+refunded) }`
- 错误：400「不支持的订单状态」
- 设计：订单页顶部待支付卡片（`status=pending_payment,processing`）、筛选「全部/已支付/已取消」+ 计数、按月分组列表、「显示更早的订单」分页（offset 递增，每页 6）、概览待支付条。映射：设计 pending ← draft/pending_payment/processing（processing 显示「处理中」、不显示「去支付」）；设计 paid ← paid/fulfilled；设计 cancelled ← cancelled/expired；「全部」= 除 open 外所有状态；标题：topup →「余额充值」，addon →「流量包 · <item_name>」，new →「<plan_name> · <月付|季付|年付>」，renewal → 同上 +「续费」，upgrade →「<plan_name> · 变更套餐」；金额列显示 total_amount；月分组合计「已支付 ¥X」只对已加载行求和。
- 待补·前端（→ 补进 订单列表状态徽标与筛选）：partially_refunded「部分退款」、refunded「已退款」两种徽标（info 色），归入「全部」，筛选段加「已退款」一项（counts.refunded=0 时隐藏）。

#### GET v1/orders/{id} — 订单详情
- 状态：现有 `panel/internal/api/public/my_orders.go:29 myOrderDetail`（domain `billing/my_orders.go:156`）；扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：路径 id
- 响应（现有）：200 `{ order: { ...列表行全部字段, items: [{ name: string, quantity: int, unit_amount: int, line_amount: int }], payments: [{ status: string, amount: int, currency: string, created_at: RFC3339 }] } }`；（待补·后端追加）`coupon_code?: string`（orders.coupon_id → coupons.code）、`subscription_period_end?: RFC3339`（fulfilled 且有 subscription_id 时取该订阅 current_period_end）、payments[] 加 `method?: string`、`provider_name: string`（payment_providers.display_name）
- 错误：404 不存在/不属于本人/非 UUID
- 设计：订单行展开区「订单号 / 下单时间 / 结果」、支付回跳后的结果确认。映射：「结果」= 按状态与 kind 生成（fulfilled+topup「余额已到账」；fulfilled+new/renewal「<支付方式> · 有效期至 <subscription_period_end>」；expired「超时未支付，已自动取消」；cancelled 且 cancel_reason=user_cancelled「已由您取消」，其余 cancel_reason 原样显示）。
- 待补·前端（→ 补进 订单行展开区）：原价（items 合计）、优惠（discount_amount + coupon_code）、余额抵扣（balance_applied）、实付（paid_amount）、退款（refunded_amount>0 时）、支付记录（payments 每条：方式 · 金额 · 状态 · 时间）；待支付订单展开区显示过期时间 expires_at。

#### POST v1/orders/{id}/cancel — 取消待支付订单
- 状态：现有 `panel/internal/api/public/order_cancel.go:11 cancelOrder`（domain `billing/release.go:140 CancelOrder`）
- 权限：登录用户｜reauth：否｜幂等：否（对已是 cancelled 的订单重复取消返回成功，already_terminal=true）
- 请求：路径 id；无 body
- 响应：200 `{ order_id: uuid, status: "cancelled", state_version: int, cancelled_at?: RFC3339, cancel_reason?: string, already_terminal: bool }`；同事务释放库存/限购预留、优惠码预留、余额冻结
- 错误：400 非 UUID（注意与详情接口回 404 不一致）；404 不存在/不属于本人；409 已支付/已履约/已退款、已过期（expired）、存在已结算支付证据
- 设计：订单页待支付卡片「取消订单」确认框（「取消后优惠码和已冻结的余额会立即退回」与后端语义一致）。

### 门户-05 钱包

#### GET v1/me/balance — 余额与流水
- 状态：现有 `panel/internal/api/public/handlers.go:999 myBalance`（domain `billing/topup.go:403 BalanceOf`、`:424 ListBalanceHistory`）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ balance: int(CNY 分), currency: "CNY", history: [{ kind: string(账本交易类型), delta: int(正=入账，负=出账), memo: string, at: RFC3339 }] }`，history 最近 100 条（未按币种过滤）
- 错误：无特有
- 设计：钱包「账户余额」、顶部余额胶囊、下拉菜单钱包提示、概览余额格、结账页可抵扣余额。
- 待补·前端（→ 补进 钱包页左列余额卡下方新卡片「余额明细」）：列表 时间 / 类型 / 金额(±) / 备注；kind 映射：balance_topup「充值」、balance_hold「下单抵扣（冻结）」、balance_release「订单取消退回」、balance_adjusted「人工调整」、commission_to_balance「佣金转入」、gift_card_balance「礼品卡」、late_payment_applied / late_payment_suspense「挂账转入」（保留规则 6），其他 kind 显示 memo。

#### POST v1/me/topups — 创建充值单
- 状态：现有 `panel/internal/api/public/handlers.go:1022 createTopup`（domain `billing/topup.go:92 CreateTopup`）
- 权限：登录用户｜reauth：否｜幂等：是 `balance_topup_create`
- 请求：`{ amount: int(分，100–5000000), currency?: "CNY"|"USD"(默认 CNY) }`
- 响应：**200**（不是 201）`{ order_id: uuid, order_no: string, amount: int, currency: string }`；订单 kind=topup、30 分钟过期、不可用余额与优惠码
- 错误：422「充值金额太小」「单次充值金额超出上限」「充值币种只支持 CNY 或 USD」
- 设计：钱包「充值」按钮。映射：金额按钮与输入框元 → ×100 转分；选中支付方式（来自 payment-methods，删 USDT）后依次调本接口与 `POST v1/orders/{order_id}/pay`，走同一个支付弹窗；前端校验下限改为 ¥1、上限 ¥50000。

#### POST v1/gift-cards/preview — 礼品卡预览
- 状态：现有 `panel/internal/api/public/giftcard.go:14 previewGiftCard`（domain `giftcard/codes.go:246 PreviewCode`）；扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：`{ code: string(8–32 字符，服务端转大写去空白) }`
- 响应（现有）：200 `{ card: { id, name, description, type: "general"|"plan"|"mystery", status, rewards: { balance?: int, traffic_bytes?: int, expire_days?: int, reset_quota?: bool, plan_id?: uuid, price_id?: uuid, pool?: [{ label: string, weight: 0 }] }, conditions: {...}, limits: {...}, theme_color: string, code_total: int, code_used: int, created_at } }`（mystery 只回奖品名）
- 响应（待补·后端调整）：type=plan 时追加 `plan_name: string`、`interval: string`、`interval_count: int`；从用户响应中去掉 `code_total`、`code_used`（运营数据）
- 错误：422「卡密无效、已被使用或已过期」（不存在/已用/停用/过期同一响应）
- 设计：钱包「兑换礼品卡」查询结果卡。映射：face = balance →「¥X 余额」；traffic_bytes →「流量 +N GB」；expire_days →「延长 N 天」；plan →「<plan_name> <周期>」；mystery →「盲盒：可能抽到 a / b / c」；note 用 description，缺省按类型给固定文案。删除设计里的演示提示行「试试 GC-0923…」。

#### POST v1/gift-cards/redeem — 兑换礼品卡
- 状态：现有 `panel/internal/api/public/giftcard.go:28 redeemGiftCard`（domain `giftcard/redeem.go:48 Redeem`）
- 权限：登录用户｜reauth：否｜幂等：是 `gift_card_redeem`
- 请求：`{ code: string }`
- 响应：200 `{ template_name: string, type: string, prize_label?: string, balance?: int, traffic_bytes?: int, expire_days?: int, quota_reset?: bool, plan_granted?: string, summary: string[](给用户看的文案) }`
- 错误：422「卡密无效、已被使用或已过期」「这个礼品卡活动已经停用了」「这张卡只能新用户使用」「这张卡需要有过付费记录才能使用」「你当前的套餐不在这张卡的适用范围内」「这张卡只对通过邀请注册的用户开放」、次数/冷却限制、「你当前没有生效中的订阅，这类奖励需要先有一个套餐才能发放」「当前订阅没有流量配额，无法追加」
- 设计：钱包「立即兑换」。映射：toast「兑换成功：」+ summary.join('，')；成功后重拉余额、订阅、我的礼品卡。

#### GET v1/me/gift-cards — 我的兑换记录
- 状态：现有 `panel/internal/api/public/giftcard.go:44 myGiftRedemptions`（domain `giftcard/redeem.go:406 MyRedemptions`）；扩展为待补·后端（无迁移）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ redemptions: [{ template_name: string, type: string, prize_label?: string, balance?: int, traffic_bytes?: int, expire_days?: int, redeemed_at: RFC3339 }] }`，最近 100 条；（待补·后端追加）`code_hint: string`（gift_card_redemptions.code_id → gift_card_codes.code 前 12 位 + "…"）
- 错误：无特有
- 设计：钱包「我的礼品卡」列表三列 code / 获得 / 日期。映射：「获得」按 prize_label ?? balance/traffic/expire 生成，plan 类型用 template_name。

### 门户-06 邀请返利

#### GET v1/me/invite — 我的邀请码与被邀请人
- 状态：现有 `panel/internal/api/public/handlers.go:797 myInviteCode`
- 权限：登录用户｜reauth：否｜幂等：否（注意：没有活动邀请码时，这个 GET 会**懒生成**一个码，有写入副作用，`identity/invite.go:103`）
- 请求：无
- 响应：200 `{ invite: { code: string(8 位，字母表 A–Z2–9，去掉 0/O/1/I/L), invited: int(该码 used_count), max_uses: int|null, created_at }, invitees: [{ email: string(已打码，如 "ab***@x.com"), bound_at, risk_flag: "none"|"suspicious"|"confirmed_fraud" }] }`，invitees 最多 100 条，按 bound_at 倒序
- 错误：无特有
- 设计：门户-06 顶部邀请卡片（链接 + 复制链接 + 邀请码按钮）、统计格「邀请注册」。
  - 映射：设计的 `https://<门户域名>/r/ZW8K` 改为 `{location.origin}/?invite=<code>`，因为 `/r/CODE` 会撞 `/{prefix}/{token}` 订阅通配路由（router.go:108）。注册页要读 `?invite=` 预填 `invite_code`，注册流程归分段 E。设计的码是 4 位，后端是 8 位。
  - 映射：「邀请注册」取 `invite.invited` 或 `GET v1/me/commission` 的 `summary.invitees`。前者数的是码的 used_count，后者数的是 referrals 行数；统一用后者，因为它和佣金同源。
  - 待补·前端（→ 门户-06「佣金记录」卡片下方新增「邀请记录」卡片）：列出 invitees 的 email 和 bound_at，**不展示 risk_flag**，因为那是风控内部判定，不该让邀请人看到。

#### GET v1/me/commission — 佣金概况、明细、提现记录
- 状态：现有 `panel/internal/api/public/handlers.go:860 myCommission`；另有待补·后端（改形状，见下）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应（现有）：200 `{ summary: { currency, pending: int64(冻结中), available: int64(可提现), withdrawing: int64(提现在途), settled: int64(已打款), invitees: int, orders: int(产生佣金的订单数), rate_percent: int, min_withdraw: int64 }, entries: [{ amount, base, rate_percent, currency, status: "pending"|"available"|"reversed"(CHECK 里还允许 frozen/settled/rejected，但 Go 从不写入), frozen_until, created_at, order_no, from: string(打码邮箱) }], withdrawals: [{ id, amount, currency, status: "requested"|"reviewing"|"approved"|"rejected"|"processing"|"paid"|"failed"|"returned", reject_reason: string, requested_at, completed_at|null }] }`。entries 最多 100 条，withdrawals 最多 50 条
- 待补·后端（改形状，不需要迁移）：
  - `summary` 增加两个字段：`paid_invitees: int`，即 commission_entries 中 status≠reversed 的 distinct referee 数；`total_earned: int64`，即 status≠reversed 的 commission_amount 之和。
  - 顶层增加 `transfers: [{ ledger_txn_id, amount, currency, created_at }]`，取 ledger_transactions 中 kind='commission_to_balance'、属于本人的记录，最多 50 条。
  - `summary.available` 的口径要统一，见 D-F-1。
- 错误：无特有
- 设计：门户-06 的四格统计、「佣金记录」列表。
  - 映射：「邀请注册」→ `summary.invitees`；「付费好友」→ `summary.paid_invitees`（待补）；「累计佣金」→ `summary.total_earned`（待补）；「可用佣金」→ `summary.available`。
  - 映射：「佣金记录」由三类记录合并，按时间倒序：
    - entries：`好友 {from} · {order_no}`，金额 `+`。status 映射为：pending → 「冻结至 {frozen_until MM-DD}」，available → 「已结算」，reversed → 「已冲销」。
    - transfers：「转入余额」，金额 `−`，状态「完成」。
    - withdrawals：「申请提现」，金额 `−`。status 映射为：requested/reviewing → 「审核中」，approved/processing → 「打款中」，paid → 「已打款」，rejected → 「已驳回」（悬停显示 reject_reason），failed/returned → 「失败已退回」。
  - 横幅文案「好友首单，您得 20% 佣金」：数字取 `summary.rate_percent`。「首单」二字只在营销分段补出「仅首单返佣」配置后才成立；在那之前前端写「好友每笔订单」。

#### POST v1/me/withdrawals — 申请佣金提现
- **修订 R5（2026-09-24）**：幂等改为「是」，scope `commission_withdrawal_request`；可提现金额按 D-F-1 统一口径（账本余额 − 在途提现）。
- 状态：现有 `panel/internal/api/public/handlers.go:886 requestWithdrawal`
- 权限：登录用户｜reauth：否｜幂等：否（后端限制同一时间只能有一笔在途提现，重复提交会回 409；按「动钱写操作」惯例建议补幂等，但本段不强制）
- 请求：`{ amount: int64(分), payout_detail: string(收款方式，非空；信封加密落库) }`
- 响应：200 `{ id: uuid }`
- 错误：
  - 422 `fields.payout_detail`：payout_detail 为空。
  - 422 无 fields：「提现金额低于最低限额」（amount < min_withdraw，默认 10000 分）；「没有可提现的佣金」。
  - 409：「提现金额超过可提现余额」；「还有正在处理的提现申请」；「可提现佣金包含多个币种」。
- 设计：门户-06「使用佣金」卡片中的「申请提现」。
  - 映射：最低额取 `summary.min_withdraw`，不写死 ¥100。设计输入框占位是「支付宝账号 / USDT 地址」，改为「支付宝账号 / 银行卡号」，不做 USDT。成功后 invalidate `v1/me/commission`。

#### POST v1/me/commission/transfer — 佣金转入余额
- **修订 R7（2026-09-24）**：可用金额按 D-F-1 统一口径；409 文案改为「可提现佣金不足……提现处理中的金额也不能再转」。
- 状态：现有 `panel/internal/api/public/selfservice.go:26 transferCommission`
- 权限：登录用户｜reauth：否｜幂等：是 `commission_transfer_to_balance`
- 请求：`{ amount: int64(分，>0) }`
- 响应：200 `{ ledger_txn_id: uuid, amount: int64 }`
- 错误：
  - 422 `fields.amount`：amount ≤ 0。
  - 409：「可提现佣金不足」，判断依据是账本科目 user_commission_available 的余额。
- 设计：门户-06「全部转入余额」，外加确认框「转入后不可再提现」。
  - 映射：amount 取 `summary.available`。在 D-F-1 落地之前，这个值可能比账本余额大，此时后端回 409，前端提示「可用佣金已变化，请刷新」。

### 门户-07 工单

> 实时：客服回复或用户自己回复后，后端会在用户频道推 `ticket.updated {ticket_id}`（public handlers.go:792、admin handlers.go:623），前端据此重新拉取详情。SSE 入口 `GET v1/events` 不归本段。

#### GET v1/support/categories — 工单分类
- 状态：现有 `panel/internal/api/public/handlers.go:670 listTicketCategories`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ categories: [{ code, name }] }`，顺序固定：general 一般咨询、technical 连接与技术、subscription 订阅与套餐、billing 账单与支付、account 账号与安全、abuse 举报与投诉
- 错误：无
- 设计：门户-07「提交新工单 › 问题分类」胶囊按钮。
  - 映射：设计写死的 5 类（线路质量/客户端/账号/账单/其他）改为按接口返回渲染，显示 name，提交 code，默认选 general。

#### GET v1/support/tickets — 我的工单列表
- 状态：现有 `panel/internal/api/public/handlers.go:719 listTickets`；另有待补·后端（改形状）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无，不分页，最多 100 条，按 updated_at 倒序
- 响应（现有）：200 `{ tickets: [{ id, ticket_no, subject, category, priority, status, created_at, updated_at, resolved_at|null, message_count, last_reply_at }] }`
- 待补·后端（改形状，不需要迁移）：每项增加 `closed_reason: "user_closed"|"withdrawn"|"agent_closed"|null`，否则前端分不出「已撤回」和「已关闭」。同时让 `closeByUser` 写入 `closed_reason='user_closed'`（现在没写，见核对笔记）。
- 错误：无
- 设计：门户-07 左侧列表，每项显示 `#id · 状态标签 · 分类 · 标题`；撤回的工单半透明显示。
  - 映射：`#{ticket_no}`，形如 `TK20260923-XXXXXXXX`，不是设计里的纯数字。分类名用 categories 接口的 name。
  - 映射（状态）：
    - open / pending_agent / escalated → 「等待回复」
    - pending_user → 「客服已回复」
    - resolved → 「已解决」
    - closed 且 closed_reason=withdrawn → 「已撤回」
    - 其余 closed → 「已关闭」

#### GET v1/support/tickets/{id} — 工单详情（含消息）
- 状态：现有 `panel/internal/api/public/handlers.go:729 getTicket`；另有待补·后端（改形状）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：path `id: uuid`
- 响应（现有）：200 `{ id, ticket_no, subject, category, priority, status, created_at, updated_at, resolved_at, message_count, last_reply_at(零值，详情不填), messages: [{ id, author_kind: "user"|"agent"|"system", author_name: string|null(非 user 恒为 null), body, created_at }] }`。内部备注在 SQL 层已排除，`internal_note` 字段不会出现
- 待补·后端（改形状，不需要迁移）：增加 `closed_reason` 同上；增加 `related_order: { id, order_no } | null`，取 tickets.related_order_id 关联 orders。
- 错误：404 表示不存在或不属于本人。非 UUID 的 id 目前可能回 500（INFERENCE：SQL 里 uuid 列与文本参数比较；withdraw 有 uuid.Parse 保护，这里没有）
- 设计：门户-07 右侧会话区，按钮「撤回」和「问题已解决」，底部回复框。
  - 映射：author_kind=user 显示「我」。agent 在设计里显示「周敏 · 客服」，后端不给姓名，先显示「客服」，见 D-F-2。system 消息居中灰字显示。
  - 映射（按钮可见条件）：
    - 撤回：`status≠closed` 且 messages 中没有 author_kind=agent（与后端 withdraw.go 的判定一致）。
    - 问题已解决（即关闭）：`status≠closed`。
    - 回复框：`status≠closed`（后端允许在 resolved 状态下回复，会重开工单）。
  - 待补·前端（→ 门户-07 详情头部副标题）：related_order 非空时显示「关联订单 {order_no}」，点击跳订单页。

#### POST v1/support/tickets — 提交工单
- 状态：现有 `panel/internal/api/public/handlers.go:693 createTicket`
- 权限：登录用户｜reauth：否｜幂等：是 `support_ticket_create`；另有按账号的额外限流 `ticket_create`，10 次/小时
- 请求：`{ subject?: string(4–120 字；空则取正文首行), category?: string(categories 的 code，空为 general), body: string(10–5000 字), order_id?: uuid(必须是本人订单) }`
- 响应：201 `{ id, ticket_no, subject, category, priority(由分类推导：billing/account→high，abuse→urgent，其余 normal), status: "open", created_at, updated_at, resolved_at: null, message_count: 1, last_reply_at }`，不含 messages
- 错误：
  - 422 `fields.subject` / `fields.body` / `fields.category`。
  - 422 `fields.order_id`：「订单不存在」。
  - 409：「您有 5 个进行中的工单」，指未结（非 resolved/closed）工单达到 5 个。
  - 429：超过 10 次/小时。
- 设计：门户-07「提交新工单」表单（分类、一句话标题、详细描述）；门户-09「仍未解决，提交工单」也跳到这里。
  - 映射：设计只把标题设为必填，正文可空（原型会用标题兜底）。后端要求正文 ≥10 字，所以前端要把正文也设为必填并做长度校验。
  - 待补·前端（→ 门户-07 新工单表单，放在分类与标题之间）：可选下拉「关联订单」，选项来自 `GET v1/orders`（分段 E）；订单详情页可带 order_id 预填跳入。

#### POST v1/support/tickets/{id}/reply — 回复工单
- 状态：现有 `panel/internal/api/public/handlers.go:744 replyTicket`
- 权限：登录用户｜reauth：否｜幂等：是 `support_ticket_user_reply`
- 请求：`{ body: string(1–5000 字) }`
- 响应：200 `{ ok: true }`；成功后工单状态变为 pending_agent，resolved 工单会被重开
- 错误：422 `fields.body`；409「工单已关闭，如需继续请新建工单」；404
- 设计：门户-07 底部「补充信息…」+「发送」，按 Enter 也发送。

#### POST v1/support/tickets/{id}/close — 关闭工单（问题已解决）
- 状态：现有 `panel/internal/api/public/handlers.go:769 closeTicket`
- 权限：登录用户｜reauth：否｜幂等：是 `support_ticket_user_close`
- 请求：无 body
- 响应：200 `{ ok: true }`；后端会追加一条 system 消息「用户已关闭此工单」
- 错误：404，包括工单已是 closed 的情况（注意不是 409）
- 设计：门户-07「问题已解决」，toast「感谢反馈，工单已关闭」。

#### POST v1/support/tickets/{id}/withdraw — 撤回工单
- 状态：现有 `panel/internal/api/public/selfservice.go:72 withdrawTicket`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：`{ reason?: string(≤500 字) }`。**必须带 JSON 体**，至少传 `{}`；空 body 会被 DecodeJSON 以 400 拒绝
- 响应：200 `{ withdrawn: true }`；status 变为 closed，closed_reason=withdrawn，并追加 system 消息
- 错误：422 `fields.reason`；409「这个工单已经关闭了」；409「客服已经回复过这个工单，不能再撤回」；404（包括非 UUID 的 id）
- 设计：门户-07「撤回」+ 确认框「客服尚未回复，撤回后工单关闭」。

### 门户-08 消息（含外壳顶栏铃铛角标）

#### GET v1/me/notifications — 站内信收件箱
- 状态：现有 `panel/internal/api/public/notifications.go:23 listNotifications`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：query `limit?: int(1–100，默认 30)`、`unread?: "1"`（只看未读）。没有游标，也没有分类筛选
- 响应：200 `{ notifications: [{ id: uuid, code: "subscription.expiring"|"quota.warning"|"order.paid"|"ticket.replied"|..., subject, body(已用变量渲染), sent_at, read_at|null }], unread: int }`
- 错误：无
- 设计：门户-08「通知」页签；外壳铃铛角标（`unread`）。
  - 映射：title → subject，at → sent_at（前端转相对时间），read → `read_at != null`。
  - 映射（点击跳转）：设计里的 `go` 由 code 推导：order.paid → 订单页，ticket.replied → 工单页，subscription.expiring / quota.warning → 我的订阅。
  - 新通知没有 SSE 事件，角标靠前端在窗口聚焦时或定时（例如 60 秒）用 `limit=1` 刷新 `unread`。
  - 设计里没有消息分类筛选，所以这里没有缺口。

#### POST v1/me/notifications/read-all — 全部标为已读
- 状态：现有 `panel/internal/api/public/notifications.go:127 markAllNotificationsRead`
- 权限：登录用户｜reauth：否｜幂等：否（天然幂等）
- 请求：无 body
- 响应：200 `{ ok: true }`
- 错误：无
- 设计：门户-08「全部标为已读」按钮，只在「通知」页签下显示。

#### POST v1/me/notifications/{id}/read — 单条标为已读
- 状态：现有 `panel/internal/api/public/notifications.go:100 markNotificationRead`
- 权限：登录用户｜reauth：否｜幂等：否（天然幂等）
- 请求：path `id: uuid`，无 body
- 响应：200 `{ ok: true }`。不存在、不属于本人、已读，都同样回 200
- 错误：非 UUID 的 id 可能回 500（INFERENCE：`$2::uuid` 转换失败）
- 设计：门户-08 点击某条通知，先标已读再跳转。

#### GET v1/me/announcements — 我可见的公告
- 状态：现有 `panel/internal/api/public/handlers.go:1104 myAnnouncements`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ announcements: [{ id, title, body(纯文本), severity: "info"|"notice"|"warning"|"critical", pinned: bool, published_at }] }`。最多 20 条，置顶优先，按用户组和套餐定向过滤
- 错误：无
- 设计：门户-08「公告」页签（title/body/at，mock 里有 pinned）。
  - 映射：at → published_at；pinned=true 的条目加「置顶」标签。
  - 待补·前端（→ 门户-08 公告列表每行标题前）：按 severity 加色点或标签，info 不显示，notice 用 --info，warning 用 --warn，critical 用 --danger。建议 critical 也在门户-01 概览顶部显示横幅，那个页面归分段 E。

### 门户-09 帮助

#### GET v1/content/pages — 帮助文章列表
- 状态：现有 `panel/internal/api/public/content.go:47 listContentPages`；另有待补·后端（加 query 参数）
- 权限：登录用户｜reauth：否｜幂等：否（响应头 `Cache-Control: no-store`）
- 请求（现有）：query 字段如下
  - `kind?: "page"|"kb_article"|"tutorial"|"legal"`
  - `platform?: "web"|"windows"|"macos"|"linux"|"android"|"ios"`
  - `client_version?: string(v?1.2.3)`
  - `locale?: string(默认 zh-CN)`
- 响应：200 `{ pages: [{ slug, kind, category?, version, title, summary?, locale, target_platforms: string[], min_client_version?, max_client_version?, published_at? }] }`。**列表不含 body**；最多 200 条，按 published_at 倒序
- 待补·后端（加参数，不需要迁移）：
  - `q?: string(≤100 字)`：对 title、summary、body 做不区分大小写的包含匹配。设计的搜索会匹配正文，而列表拿不到正文，所以要后端做。
  - `platform=any`：不按平台过滤，全部返回。现在不传 platform 时，设置了 target_platforms 的教程会**被隐藏**（`cardinality=0 OR $4=ANY` 这个条件），而帮助中心需要看到所有平台的客户端教程。
- 错误：422 `fields.kind` / `fields.platform` / `fields.client_version` / `fields.locale`
- 设计：门户-09 左栏的搜索框和按分类分组的文章目录。
  - 映射：分组 = `category`，是后台随意填写的文本，前端按出现顺序分组，空分类归入「其他」。只展示 kind ∈ {kb_article, tutorial}，legal 和 page 不进帮助中心。

#### GET v1/content/pages/{slug} — 帮助文章正文
- 状态：现有 `panel/internal/api/public/content.go:63 getContentPage`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：path `slug`（`^[a-z0-9]+(-[a-z0-9]+)*$`），query 同列表
- 响应：200 `{ page: { …同列表项, body: string } }`。body 是纯文本/Markdown 源码，后端不渲染 HTML
- 错误：404 表示 slug 非法、不存在或当前用户不可见；422 同列表
- 设计：门户-09 右侧文章区，头部为「{分类} · 更新于 {日期}」，正文中 `## ` 开头的行渲染成小标题。
  - 映射：「更新于」→ published_at。正文由前端按段落和 `## ` 标题渲染为纯文本节点，不用 innerHTML。

#### POST v1/content/pages/{slug}/feedback — 文章「有帮助」反馈
- 状态：待补·后端
- 权限：登录用户｜reauth：否｜幂等：否（按用户、文章、版本 upsert，天然幂等）
- 请求：`{ helpful: bool, version: int(当前看到的 page.version) }`
- 响应：200 `{ ok: true }`
- 错误：404 表示 slug 对当前用户不可见（与 GET 详情的可见性判定相同）；422 `fields.version`，表示该版本不存在
- 需迁移：新表 `content_page_feedback(tenant_id, page_slug, page_version, user_id, helpful bool, created_at, updated_at, PRIMARY KEY(tenant_id, page_slug, page_version, user_id))`，并启用 RLS。后台统计（每篇的有帮助/没帮助数）归「后台-08 知识库」分段。
- 设计：门户-09 底部「这篇文章有帮助吗？」→「有帮助」（toast「感谢反馈」）。「仍未解决，提交工单」不调接口，直接跳到门户-07 新工单表单。

### 门户-10 账号安全

> 「个人信息」卡片用 `GET v1/me`（分段 E）：email、user_id、created_at。设计里的「用户组」和数字 ID「#10482」，`GET v1/me` 目前都没有，由分段 E 定。「退出登录」按钮用 `POST v1/auth/logout`（分段 E）。

#### POST v1/me/password — 修改密码
- 状态：现有 `panel/internal/api/public/handlers.go:959 changePassword`；另有待补·后端（改行为）
- 权限：登录用户｜reauth：否（本身要求填旧密码）｜幂等：否
- 请求：`{ old_password: string, new_password: string }`
- 响应：200 `{ ok: true }`
- 现状行为（FACT，`identity/password.go:104-126`）：吊销该用户**全部**会话（包括当前会话），以及全部 refresh_tokens。当前令牌下一次请求就会 401。handler 注释说「当前会话不在吊销范围内」，与代码不符。
- 待补·后端（改行为，不需要迁移）：public 侧按设计**保留当前会话**。给 `ChangePasswordInput` 加 `KeepSessionID`，public handler 传 `p.SessionID`，吊销 sessions 和 refresh_tokens 时排除该会话；admin 侧不传，继续踢掉全部会话（保留规则 4）。
- 错误：
  - 422：两个字段有任一为空，都会同时回 `fields.old_password` 和 `fields.new_password`。
  - 401「当前密码不正确」：**是 401 而不是 422**。前端的 api.ts 必须把这个接口的 401 当作表单错误处理，不能当会话过期跳去登录。
  - 400：「新密码不能与当前密码相同」。
  - 422 `fields.password`：注意键名是 `password`，不是 `new_password`。规则为 ≥8 字符、同时含字母和数字、≤256 字节。
- 设计：门户-10「修改密码」卡片。原型只有新密码框绑定了值，前端要把「当前密码」框也接上。成功后 toast「密码已更新，其他会话已下线」，并刷新会话列表。
  - 映射：设计只提示「至少 8 位」，要补上「需包含字母和数字」。

#### GET v1/me/sessions — 登录会话列表
- **修订 R15（2026-09-24，后端二 62f7283）**：只列出 `audience=public` 的会话，后台会话不可见（缺陷 6 已修）。
- 状态：现有 `panel/internal/api/public/selfservice.go:42 listMySessions`；另有待补·后端（改行为）
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ sessions: [{ id: uuid, current: bool, user_agent: string, country?: string(ISO2), created_at, last_seen_at?, expires_at? }] }`。最多 50 条，只列未吊销、未过期的会话
- 现状（FACT）：`ip_country` 没有任何代码写入，所以 country 恒为空；`last_seen_at` 只在建会话时写一次，此后从不更新，等于 created_at。查询不按 audience 过滤，用户在 admin/client 域的会话也会列出来。
- 待补·后端（改行为，不需要迁移）：
  - 刷新令牌时更新 `sessions.last_seen_at`，节流为最多每 5 分钟一次。
  - 查询加上 `audience='public'`。或者保留全部，但在响应里加 `audience` 字段，让前端标出「管理后台」。建议前者。
- 错误：无
- 设计：门户-10「登录会话」：设备名 + 「当前」标签；meta 为「城市 · IP · 活跃中/2 分钟前」；非当前会话有「下线」按钮。
  - 映射：设备名由前端解析 user_agent，得到「浏览器/客户端 · 系统」。活跃时间取 last_seen_at。current=true 的显示「当前」且不显示「下线」。城市和 IP 见 D-F-3，在此之前 meta 只显示「登录于 {created_at} · 最近活跃 {last_seen_at}」。

#### DELETE v1/me/sessions/{id} — 踢下线某个会话
- **修订 R15（2026-09-24，后端二 62f7283）**：只能吊销 `audience=public` 的会话；指向后台会话回 404 且不做任何修改。
- 状态：现有 `panel/internal/api/public/selfservice.go:53 revokeMySession`
- 权限：登录用户｜reauth：否｜幂等：否（天然幂等，第二次回 404）
- 请求：path `id: uuid`
- 响应：200 `{ revoked: true }`；该会话的 refresh_tokens 同时被吊销
- 错误：404 表示不存在、不属于本人或已吊销；422「这是你当前正在使用的会话…」，指 id 是当前会话
- 设计：门户-10 会话行的「下线」按钮，toast「已将 {dev} 下线」。

#### POST v1/me/quick-login — 生成快捷登录令牌
- 状态：现有 `panel/internal/api/public/selfservice.go:89 issueQuickLogin`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无 body
- 响应：200 `{ token: string(明文，只返回这一次), expires_at, expires_in: 60 }`。同一会话重新生成时，旧令牌立即作废；令牌绑定签发它的会话，该会话退出或被踢后令牌也失效
- 错误：401「当前登录状态无法签发快捷登录链接」，指会话无效
- 设计：门户-10「快捷登录」卡片。
  - 映射（保留规则 1）：文案「10 分钟内有效」改为「60 秒内有效，仅可使用一次」，并在卡片上做 60 秒倒计时，到时自动隐藏链接，按钮变回「生成快捷登录链接」。
  - 映射（链接格式）：设计的 `https://<门户域名>/q/<token>` 改为 `{origin}/#/quick-login/<token>`。有两个原因：`/q/<token>` 也会撞 `/{prefix}/{token}` 订阅通配路由；令牌放在 fragment 里，就不会进 nginx 访问日志和 Referer。消费端是 `POST v1/auth/quick-login {token}`，登录页归分段 E，两边的路由必须一致。
  - 外壳登录页的「快捷登录 › 发送登录链接（邮件）」不做，这是保留规则 1。

#### GET v1/me/telegram — Telegram 绑定状态
- 状态：现有 `panel/internal/api/public/telegram.go:19 telegramInfo`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ bound: bool, username?: string, bot_username?: string, enabled: bool(站点是否启用 Telegram) }`
- 错误：无
- 设计：门户-10 Telegram 卡片的状态「已绑定 @xxx / 未绑定」，以及「我已发送，检查绑定状态」按钮。
  - 映射：绑定成功没有实时事件，「检查绑定状态」就是重新请求这个接口。可以在展示绑定码期间每 3 秒自动轮询一次，最多轮询到 expires_at。
  - 待补·前端（→ 门户-10 Telegram 卡片）：`enabled=false` 时整张卡片显示「站点未启用 Telegram 通知」，不显示「获取绑定码」；通知偏好里 Telegram 那一列也禁用。

#### POST v1/me/telegram/bind-code — 获取绑定码
- 状态：现有 `panel/internal/api/public/telegram.go:30 telegramBindCode`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无 body
- 响应：200 `{ code: string(8 位，A–Z2–9 去掉易混字符), bot_username: string, expires_at }`，有效期 10 分钟；再次获取时旧码作废
- 错误：422「站点还没有启用 Telegram 通知」；409「你已经绑定过 Telegram 了」
- 设计：门户-10「获取绑定码」和「向 @bot 发送：/bind 123456 · 5 分钟内有效」。
  - 映射：指令改为 `/start {code}`（webhook 只认 `/start CODE` 或直接发码，telegram.go:99-102；`/bind` 会被当成码的一部分导致绑定失败）。有效期显示 10 分钟，用 expires_at 倒计时。另外提供深链按钮「在 Telegram 中打开」，指向 `https://t.me/{bot_username}?start={code}`，点开后 Telegram 会自动发送 `/start CODE`。bot 名取接口返回值，不写死。

#### DELETE v1/me/telegram — 解绑 Telegram
- 状态：现有 `panel/internal/api/public/telegram.go:41 telegramUnbind`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ unbound: true }`
- 错误：404「你还没有绑定 Telegram」（CodeNotFound）
- 设计：门户-10「解绑」+ 危险确认框。

#### GET v1/me/notification-preferences — 通知偏好
- 状态：现有 `panel/internal/api/public/notifications.go:182 getNotificationPreferences`
- 权限：登录用户｜reauth：否｜幂等：否
- 请求：无
- 响应：200 `{ preferences: [{ category: "transactional"|"service"|"marketing", channel: "email"|"telegram", enabled: bool, locked: bool }] }`，固定 6 项（3 类 × 2 渠道）；transactional 两项 locked=true 且恒为开启
- 错误：无
- 设计：门户-10「通知偏好」表格，行是 到期/流量/工单回复/订单与支付/新公告 五项，列是 邮件和 Telegram。
  - 映射（以后端为准）：表格改为 3 行。
    - 「交易通知（支付成功等，不可关闭）」对应 transactional，开关置灰。
    - 「服务提醒（到期、流量、工单回复）」对应 service。
    - 「营销与群发」对应 marketing。
  - 设计里每个事件单独一个开关，后端做不到：同一类里的到期、流量、工单回复只能一起开关。Telegram 列在未绑定时置灰，这点与设计一致。

#### PUT v1/me/notification-preferences — 修改一项通知偏好
- 状态：现有 `panel/internal/api/public/notifications.go:227 setNotificationPreference`；另有待补·后端（修 bug）
- 权限：登录用户｜reauth：否｜幂等：否（天然幂等）
- 请求：`{ category: "transactional"|"service"|"marketing", channel: "email"|"telegram", enabled: bool }`，一次改一项
- 响应：200 `{ ok: true }`
- 错误：422 `fields.preference`，指类别与渠道的组合不在目录中；422 无 fields「交易类通知无法关闭」
- 待补·后端（修 bug，不需要迁移）：SQL 写的是 `ON CONFLICT (tenant_id, user_id, category, channel)`，而表的主键是 `(user_id, category, channel)`（00008:443），全部迁移里也没有四列的唯一约束。PostgreSQL 找不到匹配的冲突仲裁索引会报 42P10，所以这个接口现在**每次调用都应当回 500**（INFERENCE，没有数据库测试覆盖）。修法：冲突目标改成 `(user_id, category, channel)`，并补一个 PG18 集成测试。
- 设计：门户-10 偏好开关，每次点击调用一次，采用乐观更新，失败时回滚。

## 5. 待决

> 以下 30 条都不属于 6 条保留规则，也不能按「设计优先」直接定，需要用户拍板。每条都写了设计怎么要、后端现状和可选方案，多数附有建议。
> 未决前：前端按各条目里写的「未决前」处理（通常是隐藏或降级展示），后端不动。
> 第 3 阶段开工前最好先定「阻塞」那一栏标了 ● 的：它们要么是财务/安全不变量缺口，要么决定整块功能做不做。**● 项已全部定案，见 5.A。**

### 5.A 已决（2026-09-23 用户拍板，优先于下文各条的方案与「未决前」）

8 条阻塞项全部定案。下文 5.1–5.6 保留原始分析备查，与本节冲突时以本节为准。

| 编号 | 结论 |
|---|---|
| D-B-1 | 方案 B：`POST v1/subscriptions/{id}/rotate` 响应去掉 `token`，改为 `{ user_email, old_revoked: true }`；用户到门户重新复制地址 |
| D-C-2 | 方案 a：`POST v1/plans/complete` 与 `PUT v1/plans/{id}/complete` 改为 `catalog.publish` + RequireRecentReauth，与单独发布接口同门槛 |
| D-C-4 | 方案 a：存量批次回填 `exported_at = 迁移时间`，存量码此后只显示掩码 |
| D-D-1 | 方案 a：路由规则下拉只放后端支持的类型（domain / domain_suffix / ip_cidr / port / network / source / source_port），设计里的 geosite/geoip 示例换成等价写法，去掉 selector 出站；pdnd 支持 geosite/geoip 另行立项 |
| D-D-4 / D-E-4 | **主题模块保留，只保留设计稿「默认 · 纸白」一个主题**：新迁移删除 00051 默认主题与 00055 stellar 两个内置主题，新建内置主题「默认 · 纸白」并激活，tokens 用设计稿变量名，分 light / dark 两组（切暗色不能被主题覆盖）；站点名称、标语、Logo 继续放在该主题的 branding。后台主题区只显示这一张卡片（使用中），不做「夜航」「国庆限定」，「保存新主题」入口隐藏（接口保留）。custom_css 本期停用。门户只认设计稿 token 白名单内的键 |
| D-E-1 | **流量包挂在用户身上，永不过期，用完为止，可叠加**（剩 30G 再买 100G 即 130G）；每周期先扣套餐额度，扣完再扣流量包，流量包不随周期重置；订阅到期或续费余量保留；必须有生效订阅才能消耗（无订阅时可以买、留着）。礼品卡赠送流量并入同一余额，修掉现有 addon 每周期复用的问题；已发放未用完的 addon 一次性转入流量包余额。设计里「没有订阅也能买并单独使用（3 台设备、全部常规线路）」一条删去 |
| D-E-2 | **升级、降级都允许**：剩余价值 = 原订单实付（现金 + 余额抵扣）× 剩余比例，**剩余比例取剩余时间比例与剩余流量比例中的较小者**，向下取整到分；新套餐价 > 剩余价值则补差价，< 则差额退入余额（账本记分录，余额不可提现）；新周期从当天按新套餐周期起算。赠送 / 礼品卡 / 0 元单实付为 0，降级不退 |
| D-F-1 | 方案 a：可用佣金统一以账本为准（账本余额 − 在途提现），提现申请与转余额在同一把锁下按同一口径校验；授权修改计费域 |

其余 22 条非阻塞项：第 3 阶段先按各条「未决前」处理，做到对应页面时由协调会话汇总再请用户定。

### 5.0 索引

| 编号 | 问题 | 性质 | 阻塞 | 关联 |
|---|---|---|---|---|
| D-A-1 | 顶栏实时事件要可读标题正文，SSE 只推表名 | 产品取舍 | | |
| D-A-2 | 用户流量排行显示完整邮箱，与 DASH-01 冻结契约冲突 | 隐私契约 | | |
| D-A-3 | 「订阅下发使用缓存」等开关后端做不到；3 个现有开关没接线 | 产品取舍 | | |
| D-A-4 | Telegram「管理员群组」的用途 | 产品取舍 | | |
| D-A-5 | 设计多出的邮件模板（重置密码、礼品卡兑换）做不做 | 产品取舍 | | D-B-2 |
| D-A-6 | 3 个 webhook 事件后端没有 | 产品取舍 | | |
| D-B-1 | 后台换订阅地址会把新令牌明文回给管理员，破了保留规则 2 | 安全缺陷 | ● | |
| D-B-2 | 重置用户密码：设计发邮件链接，后端是管理员直接设新密码 | 产品取舍 | | D-A-5 |
| D-B-3 | 用户组「可用节点池」/ 节点池「仅用户组」，会改变订阅下发的节点集合 | 交付语义 | | 后台-07 节点池 |
| D-B-4 | 设备识别窗口可选，后端固定 5 分钟并与节点 TTL 对齐 | 跨 pdnd | | |
| D-B-5 | 全局默认设备数会让已售「不限设备」套餐突然受限 | 交付语义 | | |
| D-B-6 | 工单升级「通知二线值班」，后端没有值班概念 | 产品取舍 | | D-A-4 |
| D-B-7 | 批量生成账号时直接开通套餐，会绕过账本与订单链路 | 财务 | | |
| D-B-8 | 设备明细要 IP 与客户端名，后端只存 IP 哈希 | 隐私 | | D-F-3 |
| D-C-1 | 已归档套餐「恢复上架」，数据库触发器禁止 | 不变量 | | |
| D-C-2 | 套餐向导接口只要 catalog.write、不要 reauth，却能建价格并发布 | 权限缺陷 | ● | |
| D-C-3 | 人工开单「从余额扣除」＝管理员代用户花余额 | 财务 | | |
| D-C-4 | 礼品卡改一次性导出后，存量批次算不算已导出 | 运营风险 | ● | |
| D-C-5 | 限速是常驻还是超额后才生效（数据面与校验两种理解） | 交付语义 | | |
| D-D-1 | 路由规则 geosite/geoip 与 selector 出站，节点端不支持 | 跨 pdnd | ● | |
| D-D-2 | 公告可见范围「即将到期」是动态人群 | 产品取舍 | | |
| D-D-3 | 已撤回公告能否重新发布 | 产品取舍 | | |
| D-D-4 | 内置主题 tokens 用旧门户变量名 | 与 D-E-4 同一问题 | ● | D-E-4 |
| D-E-1 | 流量包的语义（无订阅能否买、用完为止与现有 addon 每周期复用冲突） | 整块功能 | ● | |
| D-E-2 | 变更套餐的折算公式、是否允许降级 | 整块功能 | ● | |
| D-E-3 | 套餐卡特性列表与「推荐」徽标没有数据来源 | 产品取舍 | | 后台-04 |
| D-E-4 | 主题 tokens / custom_css 与新设计及 CSP 冲突 | 与 D-D-4 合并决定 | ● | D-D-4 |
| D-F-1 | 佣金「可用」两套口径，同一笔佣金可先转余额再提现 | 财务缺陷 | ● | |
| D-F-2 | 工单里显示客服姓名 | 隐私 | | |
| D-F-3 | 会话列表显示城市与 IP，后端只存哈希 | 隐私 | | D-B-8 |

合并说明：
- **D-D-4 与 D-E-4 是同一件事**（主题数据用旧变量名、custom_css 在 CSP 下无法注入），一次决定。它还卡着第 ③ 步：tokens.css 以设计规范为准，第 ③ 步先按「门户只认设计稿 token 白名单、跳过 is_builtin 主题、忽略 custom_css」实现，这等于 D-E-4 的方案 (a)，改方案不影响 tokens.css 本身。
- **D-B-3 与后台-07 节点池的「仅用户组」是同一概念的两面**：D 段把 `node_pools.allowed_user_group_ids` 写成了待补·后端，合并时已改为「取决于 D-B-3」，第 6 节把这处迁移记为条件项。
- **D-B-1、D-C-2、D-F-1 本质是既有缺陷**，不是设计冲突，列在这里是因为修复要动安全/计费逻辑，按边界需要授权。它们也列在第 7 节。

### 5.1 分段 A（后台外壳、仪表盘、通知与安全）

- **D-A-1 实时事件的可读内容**。设计：顶栏事件是带标题、正文（订单号、金额、支付方式、邮箱片段、节点名）的可读流，并覆盖新用户注册、提现申请、礼品卡兑换、节点负载告警。后端：SSE 刻意只推 `{table, op, id}`（realtime.go 包注释：推数据就得按订阅者逐个做权限过滤，一处算错即越权），且 users、withdrawals、礼品卡使用、节点指标不在监听表里；admin 频道只要求 `ops.notification.read`，推金额会让没有 `billing.order.read` 的人看到订单金额。可选：(a) 维持现状，前端按 topic+op 显示通用条目，需要细节时按权限逐条拉详情（请求放大、多数条目只能显示「订单变更」）；(b) 新增 `GET v1/activity?after=<cursor>`：从 audit_events 按动作白名单取可读活动（审计已有 actor、action、resource、after_digest），按每类动作对应的读权限过滤后返回渲染好的 `{ id, kind, title, body, at, link }`，SSE 只追加一个 `activity.changed` 信号；无新表（推荐，权限过滤走 REST 这一条已验证的路）；(c) 按订阅者权限在推送时渲染（违背现有设计原则，不推荐）。
- **D-A-2 用户流量排行显示完整邮箱**。设计显示完整邮箱；DASH-01 冻结契约第 326、364 行规定该端点只返回脱敏邮箱、不提供 full 模式，且 UI 不得用其它已加载对象补回。这不在保留 6 条里，但改它等于解冻一份隐私契约。可选：(a) 保持脱敏（设计改显示 `email_masked`，点击行进用户详情看全称）；(b) 解冻 DASH-01，给持 `iam.user.read_sensitive` 的管理员加 full 模式。
- **D-A-3 设计里后端做不到或没接线的降级开关**。①「订阅下发使用缓存」`subscription.cache_fallback`：后端没有订阅内容缓存，要做就得新建一层缓存（缓存内容、失效、密钥和订阅地址的保护都要重新设计），不是加个开关的事；可选：不做（前端不显示这一行）或单独立项。②现有 `ops.bulk_export`、`ops.reports`、`node.autoscale` 三个开关没有任何代码读取，切了也不生效；可选：接线（bulk_export→用户导出、礼品卡码导出、审计导出；reports→仪表盘读模型）、从种子里删掉，或前端灰显「未接入」。③`admin.writes` 只读模式的豁免清单是否就是上面写的那几条。
- **D-A-4 Telegram「管理员群组 Chat ID」的用途**。设计有这个字段，后端没有任何「发给管理员」的 Telegram 通知。可选：(a) 只存储，作为测试消息的默认目标（本契约按这个写）；(b) 同时把新工单、提现申请、降级开关切换等管理告警推到这个群（要新增告警事件和模板，范围明显变大）。
- **D-A-5 设计中后端没有的邮件模板**。「注册验证码」已按缺陷修复写成待补·后端；「重置密码」依赖门户的找回密码流程（后端目前没有，由门户分段决定做不做）；「礼品卡兑换成功」要新增一种用户通知（兑换时入队）。需要确认后两个做不做；不做的话，模板列表按后端现有模板显示。
- **D-A-6 Webhook 事件缺口**。协调结论是事件名以后端为准，但设计里的 `ticket.replied`、`node.offline`、`node.online` 在后端目录里没有同义事件，不属于改名。可选：(a) 不做，下拉只显示后端目录；(b) 补这 3 个事件（ticket.replied 在客服回复事务里 Emit；node.offline/online 要一个心跳超时检测器，目前只有读时计算的 stale，没有状态跃迁事件）。

### 5.2 分段 B（后台工单、用户）

- **D-B-1 换发订阅链接的响应会回传新令牌明文，与保留规则 2 冲突**
  - 冲突：保留规则 2 规定后台看不到用户的订阅地址，但 `POST v1/subscriptions/{id}/rotate` 的响应里有 `token` 明文（`handlers.go:277`）。后端的原设计意图是让管理员把新地址转交给用户
  - 设计稿：换发后不展示新地址，只是切到「订阅」tab，而那里显示的订阅地址已经按规则 2 删掉了
  - 方案 A：后端保持现状，前端不展示也不保存 `token`。代价是明文仍会出现在响应体、浏览器开发者工具和可能的日志里
  - 方案 B（建议）：后端去掉 `token` 字段，响应改为 `{ user_email, old_revoked: true }`，用户到门户里重新复制地址。这只是改响应，不需要迁移
- **D-B-2 重置密码：设计稿发邮件链接，后端由管理员直接设新密码**
  - 设计稿：「将向 xxx 发送一次性重置链接，现有会话全部失效」
  - 后端现状：管理员填写新密码，后端吊销该用户的全部会话。handler 注释（`handlers.go:287`）说明这样做的原因是找回密码功能没实现，而且部署机 SMTP 出站被封
  - 为什么不直接按设计改：要按设计实现，需要新表 `password_reset_tokens`、一个邮件模板、public 端的重置页面和接口（门户属于其他分段）；它还依赖 SMTP 能送达；而保留规则 1 已经因为类似的考虑放弃了邮件登录链接
  - 方案 A：保留现状，前端对话框改为输入「新密码 + 原因」
  - 方案 B：新增邮件重置流程，与方案 A 并存，SMTP 不可用时降级到方案 A
- **D-B-3 用户组的「可用节点池」**
  - 设计稿：用户组表格有「可用节点池」一列
  - 后端现状：节点池只绑定在套餐上（`POST v1/plans/{id}/pools`）。用户组没有池白名单，`user_groups.policy` 这个 jsonb 列存在，但没有任何代码读写它
  - 为什么需要产品取舍：按用户组限制池会改变订阅下发时的节点集合计算，影响已售套餐的交付
  - 方案 A：前端隐藏这一列
  - 方案 B：只读展示，由前端推导为「该组可见套餐绑定的池的并集」，只作提示，不参与交付
  - 方案 C：真正做成用户组级的池白名单，写进 policy.pool_ids，下发时和套餐的池取交集。这需要改 nodefabric 的下发逻辑
- **D-B-4 设备识别窗口（10 分钟 / 30 分钟 / 1 小时）**
  - 设计稿：设备识别窗口可选
  - 后端现状：窗口写死在视图 `subscription_online_devices` 里，是 5 分钟，与节点端 TTL 对齐。迁移注释写明，窗口取得更长会把已经离线的设备继续算进去，造成误杀（`migrations/00024_device_limit_modes.sql`）
  - 方案 A：前端去掉这个下拉，改为只读说明「5 分钟内活跃视为在线（与节点同步周期一致）」
  - 方案 B：做成可配置，但需要同时改节点端 TTL 和视图，而且跨越了 pdnd 的边界
- **D-B-5 全局「默认同时在线设备数」**
  - 设计稿：「套餐未设置上限时使用」全局默认值
  - 后端现状：`plan_versions.max_devices` 为 NULL 就表示**不限**（取值只能是 NULL 或大于 0），下发时的生效值计算为 `COALESCE(s.device_limit, pv.max_devices, 0)`（`nodefabric/uniproxy.go:547`）
  - 冲突点：如果按设计引入默认值，所有「不限设备」的已售套餐会突然被限制
  - 方案 A：前端去掉滑块
  - 方案 B：新增 system_settings 键 `device_limit.default`，只作用于新发布的套餐版本。这需要套餐向导区分「不限」和「用全局默认」两种选择，不需要迁移表结构
- **D-B-6 「升级到 L2 → 通知二线值班」**
  - 设计稿：升级后「通知二线值班」
  - 后端现状：没有 L2 或值班表的概念；人工升级只改状态，不发任何通知
  - 方案 A：只做状态和优先级变更，也就是本分段已经列出的待补项，不发通知
  - 方案 B：新增一个设置项，把升级事件推送到指定的 Telegram 群或邮箱，复用分段「通知与安全」里的通知渠道
- **D-B-7 批量生成账号时「开通套餐」**
  - 设计稿：生成账号时可以选择「标准版·月付 / 专业版·月付 / 不开通」
  - 后端现状：只建账号，不建订阅
  - 为什么需要产品取舍：不经订单直接开订阅，会绕过账本、收入确认和人工开单的幂等链路（分段 C）
  - 方案 A：去掉这一项，生成之后走人工开单
  - 方案 B：生成时为每个账号建一笔 0 元人工订单，走现有的 manual_order 履约链路，reason 继承生成原因。这需要写权限、reauth、幂等，并且要确认 0 元订单在收入报表里的口径
- **D-B-8 抽屉「设备」tab 的逐台设备明细（客户端名 + IP + 最近在线）**
  - 设计稿：逐台列出设备，每台显示客户端名、IP、最近在线时间
  - 后端现状：在线设备只有 `node_alive_ips.ip_hash`（迁移注释写明「只存 IP 的哈希」），没有明文 IP，也没有 UA。明文 IP 和 UA 只存在于订阅拉取日志中（加密存储，在画像接口里要 `security.audit.read` 才能看到）
  - 方案 A：只显示在线台数和上限，不列明细
  - 方案 B：tab 改名为「最近使用的客户端」，数据取画像接口的 `fetches`，按 family + ip 分组，只在持有 `security.audit.read` 时显示
  - 方案 C：节点上报加密的明文 IP。这需要迁移，并改 pdnd 协议

### 5.3 分段 C（后台套餐、订单与收款、营销）

- **D-C-1 套餐「恢复上架」**。冲突：设计后台-04 的已归档套餐有「恢复上架」。后端现状：套餐生命周期单调，是有意设计的不变量（迁移 00035 的 `app.guard_plan_catalog_transition` 只允许 draft→active/archived、active→archived，注释写明「永久归档的目录项不会被悄悄恢复」；`UpdatePlan` 也拒绝编辑已归档套餐）。方案：(a) 保持不可逆。设计的「归档套餐 / 恢复上架」改成可逆的「下架 / 重新上架」，走 PUT v1/plans/{id}（下架 = `allow_new_purchase:false` + `visibility:"hidden"`，重新上架时恢复原值），真正的「归档」作为危险操作单独放在更多菜单里，并注明不可恢复。不需迁移。(b) 新增 POST v1/plans/{id}/unarchive，并用新迁移放开 archived→active（要求 current_version 已发布）。推荐 a。
- **D-C-2 向导接口绕过发布权限**。现状：POST v1/plans/complete 和 PUT v1/plans/{id}/complete 只要 `catalog.write`，没有 reauth，却能建价格、绑池，并且**发布**（PUT 在额度或线路变化时会自动发布新版本）。同样的动作走单独接口时要 `catalog.publish` + reauth。设计的「保存并发布」就依赖这两个接口。方案：(a) 两条路由都改成 `catalog.publish` + RequireRecentReauth；(b) 路由保持 `catalog.write`，由 domain 判断：`publish=true` 或 body 里带了 prices / pool_ids / traffic_gb / max_devices / throttle_kbps 时要求 `catalog.publish` + reauth（需要把权限传进 domain）；(c) 维持现状。推荐 a，前提是先确认各角色的 catalog.write 与 catalog.publish 是怎么授予的。
- **D-C-3 人工开单「从余额扣除」**。设计的结算方式里有「从余额扣除」，也就是管理员代用户花他自己的余额。后端现状：人工单只有赠送（全额减免）一种。「待用户支付」「线下已收款」按设计补（契约已定形状，见 POST v1/orders/manual）。balance 一项牵涉资金授权：用户没有同意就被扣余额，而且管理员已经可以通过调账达到同样效果。方案：(a) 提供，要求 `billing.adjustment.write` + reauth，并写进审计；(b) 不提供，设计里去掉这一项，需要时先调账再赠送；(c) 提供，但只对 kind=renewal（续当前订阅）开放。推荐 b。
- **D-C-4 存量礼品卡批次的「已导出」状态**。改成一次性导出后，迁移前生成的批次在旧接口下已经能被反复导出明文。方案：(a) 回填时全部标记 `exported_at = 迁移时间`（从严：存量码之后只能看掩码）；(b) 全部标记为未导出（允许再导出一次）。另外要确认运营是否接受「导出文件丢失后永远拿不回明文」（设计明确要求这样，但这属于运营风险）。推荐 a。
- **D-C-5 限速字段语义**。设计把「限速 Mbps」当作常驻限速。后端现状：数据面（`nodefabric/uniproxy.go` 下发 `speed_limit = throttle_kbps`，`pdnd/core/ratelimit.go` 按 kbps 对用户常驻限速）确实按常驻限速执行；但后台校验要求 `throttle_kbps` 只能配 `overage_policy:"throttle"`，结果是向导（写死 suspend）一旦带限速就必定 422。两种理解都说得通。方案：(a) 以数据面为准：放开校验，throttle_kbps 与 overage_policy 独立，throttle 策略另外定义「超额后的限速值」（要新增字段，需迁移）；(b) 以 schema 为准：限速只在超额后生效，那么数据面要改成只在配额耗尽后下发 speed_limit，设计的文案也要改成「超额后限速」。推荐 a 的第一步：只放开校验、不加新字段，throttle 策略暂时复用同一个值。

### 5.4 分段 D（后台节点与服务器、内容与外观）

- **D-D-1 路由规则类型 geosite/geoip 与 selector 出站**：设计路由页的核心规则是 `geosite:category-ads-all`、`geosite:cn`、`geoip:cn, private`，出站里有「代理 · selector 自动选优」。后端 `qnodeMatch`（`nodefabric/uniproxy.go:425`）只接受 domain/domain_suffix/ip_cidr/port/network/source/source_port，其余键 422「暂不支持跨内核转换」；`node_outbounds.type` 的 CHECK 里也没有 selector。这不是 panel 单方面能补的：geosite/geoip 需要 pdnd NativeCore 带数据文件并实现匹配，按 CONSTRAINTS 的 fail-closed，面板不能下发节点端不认识的规则。方案：(a) 前端下拉只放后端支持的类型，设计的 geosite/geoip 示例换成等价域名后缀/IP 段，selector 去掉；(b) 立项让 pdnd 支持 geosite/geoip（跨模块，含数据文件分发与更新），面板随后放开校验；(c) 面板侧把 geosite/geoip 在保存时展开成域名/IP 列表（数据量大、更新难）。
- **D-D-2 公告可见范围「即将到期」**：设计的可见范围选项有「即将到期」，这是动态人群，不是套餐或用户组。后端公告只有 `target_plan_ids`、`target_user_group_ids` 两种静态定向。方案：(a) 去掉该选项；(b) 新增定向维度 `target_expiring_within_days?: int`，public 端按订阅 `current_period_end` 过滤（需改表 announcements 加列，并要定义「即将到期」的天数与订阅状态口径）。
- **D-D-3 已撤回公告能否重新发布**：设计的「发布」按钮对 withdrawn 公告同样可点（`publishAnn` 对任何非 live 状态都置为 live）。后端有意设为终态：「已撤回公告不可重新编辑，请新建公告」（`announce.go:154`）。两种理解都说得通（设计可能只是没考虑撤回态）。方案：(a) 按设计改后端，允许 withdrawn → draft/published，审计保留；(b) 前端对 withdrawn 隐藏「发布」，改成「复制为新公告」（预填标题正文走 POST v1/announcements）。建议 (b)。
- **D-D-4 主题 token 键名**：内置主题（00051、00055 两个 migration 种子）的 tokens 用的是旧门户的变量名（`brand`、`brand-2`、`brand-soft`、`brand-on-soft`、`r-lg`、`fg`、`line`…）；新设计的变量是 `--brand/--brand-hover/--brand-ink/--brand-soft/--brand-tint/--bg/--surface*/--text*/--border*`。门户按「键名 = CSS 变量名」无映射直接 setProperty，旧种子在新门户上大多不生效。方案：(a) 新门户维护一张旧键→新键的兼容映射；(b) 写一个 migration 把内置主题的 tokens 重写成新变量名（内置主题不可原地编辑，只能迁移改）；(c) 两者都做。影响 public 段门户渲染，需与 A/B 段对齐。

### 5.5 分段 E（门户外壳、订阅、结账、钱包）

- D-E-1 流量包语义与配置入口。冲突：设计要「买完立即生效、不限时间、用完为止；先用订阅流量再用流量包；**没有订阅也能买**，单独使用时 3 台设备、全部常规线路」。后端现状：无流量包商品与下单路径；唯一相近的 `quota_balances.granted_addon`（礼品卡追加流量在用）挂在周期配额行上，周期重置只清 consumed 不清 addon，因此一次性追加会**每个周期重新可用**，不符合「用完为止」；「无订阅也能买」需要凭空建一条带设备数与节点池的订阅，节点池与设备数的来源没有定义；后台设计稿（管理后台-04）没有流量包管理界面。可选方案：(a) 按本契约新建 traffic_packs/traffic_pack_grants，只允许挂在已有订阅上，standalone_allowed 恒为 false，设计「没有订阅也能买」一条删去；(b) 同 (a) 但支持独立购买，后台为流量包配置一个「承载套餐版本」（设备数、节点池取自该版本），履约时建一条 total 型订阅；(c) 本期不做流量包，选购页隐藏流量包 tab、概览去掉「买流量包 →」。另需同时决定：现有礼品卡流量是否迁到 grants 语义（改变已发放礼品卡的效果）。
- D-E-2 变更套餐折算规则。设计只说「剩余天数自动折算」「差价已计入」。后端现状：plans.allow_upgrade 与 orders.kind='upgrade' 只存在于 schema，没有任何代码路径；新购另一套餐会开第二条订阅。需要产品定：折算基数用原订单实付（paid_amount + balance_applied）还是当前价；是否扣除已用流量比例；降级是否允许、差额（credit > 新价）是否进余额（进余额即产生负债，需账本分录）；变更后周期从今天重算还是保留原到期日。建议：只做升级（新价 ≥ 剩余价值），credit = 原订单（实付+余额抵扣）× 剩余秒数 / 周期总秒数，向下取整；新周期从今天起按新价格周期；降级回 409，引导到期后换购。
- D-E-3 套餐卡的特性列表与「推荐」徽标来源。设计每张卡有 3 条特性与「推荐」标；后端 plans 只有 description，后台设计也没有这两个字段。可选：(a) plans 加 `highlights text[]`、`featured bool` 两列并在后台-04 表单补字段（需迁移 1 处改表）；(b) 前端把 description 按行拆成特性列表，「推荐」取 sort_order 第二位（设计为三卡居中推荐）；(c) 特性列表只显示后端已有事实（流量、设备数、周期重置），不显示「推荐」。
- D-E-4 主题 tokens / custom_css 与新设计及 CSP 的冲突。`GET v1/appearance` 下发的 tokens 是旧门户的变量名（brand、brand-2、brand-soft、brand-on-soft、r-lg），且内置 default 主题默认激活、主色为紫 #6d5efc（迁移 00051），与设计稿 #b9442b 及其浅/深两套 token 不一致——前端若照注释 `setProperty('--'+k, v)` 应用，新门户会变紫且深色模式被覆盖；custom_css 在 CSP `style-src 'self'`（无 unsafe-inline）下无法以 `<style>` 注入。可选：(a) 门户只认设计稿 token 白名单内的键并跳过 is_builtin 主题，custom_css 忽略；(b) 新迁移把内置主题 tokens 改成设计稿变量名（分 light/dark 两组），后端新增同源 `GET /theme.css` 下发净化后的 custom_css（style-src 'self' 允许）；(c) 停用 custom_css 功能。需与「内容与外观」分段一起定。

### 5.6 分段 F（门户邀请、工单、消息、帮助、账号安全）

- **D-F-1　「可用佣金」有两套口径，转余额与提现互不扣减**
  - 冲突：`summary.available` 和 `RequestWithdrawal` 用的是「commission_entries 中 available 之和 − 在途提现 − 已打款」（commission.go:450-466、533-600）；`TransferCommissionToBalance` 用的是账本科目 user_commission_available 的余额（commission_transfer.go:57）。两边互相看不到对方：
    - 转入余额之后，summary 里的可用佣金不会减少，还能再申请提现同一笔钱。申请会被受理，要到管理员打款时才会被账本余额检查拦下（commission.go:696，报 underfunded）。
    - 提现申请不会在账本上冻结金额（hold_txn_id 从未写入），申请之后仍然可以把同一笔钱转入余额。
  - 设计怎么要：一个「可用佣金」数字，同时驱动「全部转入余额」和「申请提现」。
  - 后端现状：同一笔佣金可以被两条路径各用一次，最后在打款环节失败。这属于财务不变量的缺口，不是设计引起的。
  - 可选方案：
    - (a) 统一以账本为准：available = 账本余额 − 在途提现；提现申请和转余额都按这个口径在同一把锁下校验。建议选这个，不需要迁移。
    - (b) 在 (a) 的基础上，提现申请时再过一笔 hold 分录（冻结科目），在账本层面彻底隔离。
    - (c) 维持现状，只在前端提示。不建议。
  - 需要用户授权修改计费域。
- **D-F-2　工单里客服的名字**
  - 冲突：设计显示「周敏 · 客服」；后端 `GetForUser` 对非 user 的消息把 author_name 置为 null，理由是「客服真实姓名不暴露给用户」（support/service.go:437-441）。
  - 可选方案：
    - (a) 维持后端规则，统一显示「客服」。
    - (b) 直接暴露客服的 display_name。
    - (c) 给后台用户加一个对外昵称 `support_alias`，只暴露昵称。需改表 users（或管理员资料表）加一列。
  - 这属于隐私与产品取舍，不在 6 条保留规则里，也不宜直接按设计开放。
- **D-F-3　会话列表的位置与 IP**
  - 冲突：设计显示「上海 · 112.10.x.x」。后端 sessions 表只存 ip_hash、ip_asn、ip_country，表注释明确「不采集精确位置」（CLI-001，00002_identity.sql:267）；而且 ip_country 目前没有任何代码写入。
  - 可选方案：
    - (a) 只显示国家：登录时用 GeoIP 填 ip_country。需要引入 GeoIP 数据源，不需要迁移。
    - (b) 存打码后的 IP 前缀（如 112.10.*.*）：改表 sessions 加列 `ip_masked text`。
    - (c) 存加密的完整 IP，展示时解密。与 CLI-001 冲突，不建议。
    - (d) 位置和 IP 都不显示，只显示设备和时间。
  - 这属于隐私不变量层面的取舍。

## 6. 迁移预估

> 现有最新迁移 00067。编号由协调会话统一分配，本阶段不创建迁移文件。「条件」项只有在对应待决选了特定方案时才需要。各分段的原文预估见第 8 节。

### 6.1 确定要做的（不依赖待决）

| # | 类型 | 内容 | 服务的接口 | 分段 |
|---|---|---|---|---|
| M1 | 新表 | `ip_cluster_reviews`（source_ip_hash、decision normal/disabled、note、decided_by/at、expires_at；租户 RLS；UNIQUE(tenant_id, source_ip_hash)） | `GET v1/ip-clusters`、`POST v1/ip-clusters/{key}/review`、`POST v1/ip-clusters/{key}/disable-accounts` | A |
| M2 | 新表 | `ticket_macros`（快捷回复） | `GET/POST/DELETE v1/ticket-macros` | B |
| M3 | 新表 + 改表 | `gift_card_batches`（含 exported_at / exported_by，回填存量批次，回填口径见 D-C-4）；`gift_card_codes.batch_id` 加外键 | `GET v1/gift-cards/batches`、`POST v1/gift-cards/batches/{id}/export` | C |
| M4 | 新表 | `subscription_usage_daily`（按订阅按日流量，uniproxy 上报事务内 upsert） | `GET v1/me/subscriptions/{id}/usage` | E |
| M5 | 新表 | `content_page_feedback`（主键 tenant_id, page_slug, page_version, user_id；helpful bool；RLS） | `POST v1/content/pages/{slug}/feedback` | F |
| M6 | 改表 | `audit_events` 加 `auth_context text NULL CHECK (IN ('session','reauth'))`；表是追加写 + 哈希链，是否纳入 entry_hash 由实现定 | `GET v1/audit`、`GET v1/audit/export` | A |
| M7 | 改表 | `plugin_hook_deliveries` 加 `last_duration_ms int` | `GET v1/plugin-hooks/{code}/deliveries` | A |
| M8 | 改表 | `nodes` 加 `country_code char(2) NULL`（CHECK `^[A-Z]{2}$`）、`server_token_issued_at timestamptz NULL`、`server_token_issued_by uuid NULL` | `GET/POST/PATCH v1/nodes`、`GET v1/nodes/{id}/identity`、`POST v1/nodes/{id}/server-token` | D |
| M9 | 数据 | 权限字典新增 `ops.dashboard.read` 并授予内置管理角色 | `GET v1/dashboard/tasks` | A |
| M10 | 数据 | `feature_switches` 每租户插入 `billing.checkout`、`marketing.giftcard.redeem`、`notify.email`、`admin.writes` | `POST v1/switches/{code}` | A |
| M11 | 数据 | `notification_templates` 种子 `auth.email_verify|email` | 注册验证码投递（第 7 节缺陷 1） | A |

合计：**新表 5 张**（M1–M5），**改表 4 处**（M3 的外键、M6、M7、M8），**数据迁移 3 项**（M9–M11）。

### 6.2 条件项（取决于待决）

| # | 条件 | 内容 |
|---|---|---|
| C1 | D-E-1 选 (a)/(b)（做流量包） | 新表 `traffic_packs`、`traffic_pack_grants`；00036 订单形状约束加 `kind='addon'` 分支 |
| C2 | D-E-2 定下折算规则（做变更套餐） | `orders` 加 `proration_credit_amount` 并扩金额恒等式；00036 约束加 `kind='upgrade'` 分支（与 C1 同一批约束，可合并为一次） |
| C3 | D-B-3 选 (c)（用户组级节点池） | `node_pools` 加 `allowed_user_group_ids uuid[] NOT NULL DEFAULT '{}'`，或写进 `user_groups.policy`；两种落点二选一 |
| C4 | D-C-1 选 (b)（允许恢复上架） | 替换函数 `app.guard_plan_catalog_transition`，放开 archived→active |
| C5 | D-C-5 选 (a) 且加超额限速值 | `plan_versions` 加 `overage_throttle_kbps` |
| C6 | D-D-2 选 (b)（公告「即将到期」定向） | `announcements` 加 `target_expiring_within_days` |
| C7 | D-D-4 / D-E-4 选 (b)（改写内置主题） | 数据迁移：内置主题 tokens 改为设计稿变量名（分 light/dark） |
| C8 | D-E-3 选 (a)（套餐卡特性与推荐） | `plans` 加 `highlights text[]`、`featured bool` |
| C9 | D-B-2 选 (b)（邮件重置密码） | 新表 `password_reset_tokens` |
| C10 | D-B-8 选 (c) | `node_alive_ips` 加 `ip_enc`（还要改 pdnd 上报协议） |
| C11 | D-F-2 选 (c) | `users` 加 `support_alias` |
| C12 | D-F-3 选 (b) | `sessions` 加 `ip_masked` |

条件项全选时最多再加新表 3 张（C1 两张、C9 一张）、改表或改约束约 10 处、数据迁移 1 项（C7）。

## 7. 核对中发现的既有缺陷

> 这些不是设计冲突，是现有代码的问题，第 3 阶段「补后端」会话按优先级处理。标「授权」的动到安全/计费逻辑，按边界需要用户点头；其余属于「补接口时顺手修」。出处见第 8 节对应分段。

### 7.1 安全与财务（授权）

1. **注册验证码从未发出**：`identity.StartRegistration`（`domain/identity/service.go`）只把验证码写进 `verification_codes`，全仓没有投递路径，只有开发模式在响应里带 `dev_code`。生产环境一开邮箱验证，新用户就注册不了。修法见后台-09「auth.email_verify」条（M11）。
2. **后台换订阅地址回传令牌明文**（D-B-1）：`api/admin/handlers.go:276` 返回 `token`，与保留规则 2 冲突。
3. **佣金可被转余额与提现各用一次**（D-F-1）：两条路径用两套「可用」口径，互不扣减，靠打款时账本检查兜底。
4. **套餐向导绕过发布权限**（D-C-2）：`POST v1/plans/complete`、`PUT v1/plans/{id}/complete` 只要 `catalog.write`、不要 reauth，却能建价格、绑池、发布。
5. **礼品卡明文码过度暴露**：`GET v1/gift-cards/codes/export` 只要读权限、无 reauth、不写审计、可无限次导出；码列表、使用记录、生码响应也回完整明文（`api/admin/giftcard.go`）。修法见后台-06 批次与一次性导出条目（M3、D-C-4）。
6. **门户令牌能列出并吊销同一用户的后台会话**：`identity/sessions.go` 的 `ListActiveSessions` / `RevokeSession` 不按 audience 过滤。修法见门户-10 会话条目。

### 7.2 必然出错的功能缺陷

7. **门户通知偏好保存必然 500**：`api/public/notifications.go:258` 的 `ON CONFLICT (tenant_id, user_id, category, channel)` 与表主键 `(user_id, category, channel)`（`migrations/00008_ops_marketing.sql:443`）不匹配，之后的迁移也没有加匹配的唯一索引。PostgreSQL 会拒绝这条语句。
8. **后台用户列表的用户组永远为空**：`adminops/service.go` ListUsers 的 SELECT 与 Scan 都没有 group_name。
9. **用户详情 recent_orders 的 plan_name 等 4 个字段恒为零值**（`adminops/service.go:389` 没选出）。
10. **`POST v1/orders/{id}/mark-paid` 响应是 PascalCase**：`billing.PaymentWebhookOutput`（`checkout.go:765`）没有 json tag。
11. **套餐向导设限速必然 422**（D-C-5）：向导写死 `overage_policy:"suspend"`，校验只允许 throttle 策略带速率。
12. **向导编辑会归档没回传的价格**：`PUT v1/plans/{id}/complete` 只回传 CNY 价时，会把 USD 价和用户组专属价全部归档，且整个操作不是原子的。
13. **挂账待处理合计把 CNY 与 USD 直接相加**（`billing/late_payment.go`）。
14. **访问日志分类筛选失效**：`category` 只对 login/register/subscribe 生效，传 admin 等值时审计表不过滤（`api/admin/access_log.go` auditActionFilter）。
15. **流量上报放大推送**：`quota_balances` 无 user_id 却在监听名单里，每次节点上报都给全租户在线用户推 `subscriptions.changed`。建议按 subscription 反查 user 或移出监听名单。
16. **礼品卡追加流量每个周期重新可用**：周期重置与续费不清 `quota_balances.granted_addon`（`billing/traffic_reset.go:172`、`renewal.go:550`），一次性追加变成永久追加（D-E-1 相关）。
17. **用户关闭工单不写 closed_reason**（`support/service.go` closeByUser），与迁移 00046 的意图相反；门户因此分不出「已撤回」和「已关闭」。
18. **单节点路由保存不通知节点**，且出站引用校验不认全局出站 tag（`api/admin/handlers.go` nodeSetRouting）。
19. **支付渠道未启用时发起支付回 500**（`payment/factory.go:95` 普通 error）。
20. **`POST v1/themes` 不做字段校验**，违反数据库 CHECK 时回 500。
21. **`GET v1/nodes` 硬上限 200 且 total 只是本页条数**，超过 200 个节点被静默截断。

### 7.3 缺少的保护（代码没挂、注释说要挂或同类都挂）

- 缺审计：`POST v1/nodes/{id}/server-token`（也不拒绝已退役/已销毁节点）、`POST v1/subscriptions/{id}/device-limit`（订阅不存在也回 200）、`POST v1/settings/device-limit`。
- 注释说要 reauth、代码没挂：`users/bulk/export`（且只挂读权限）、`users/bulk/generate`、`users/bulk/mail`、`users/{id}/traffic-reset`（设计也要 reauth，已写成待补·后端）、`users/{id}/status`、`settings/device-limit`、`orders/manual`、`orders/{id}/cancel`、`mail/templates/test`。契约以代码为准，只有设计也要求的才写成待补。
- 同类都有、这条没有：`POST v1/coupons/batch` 无幂等（一次最多 1000 张）；`POST v1/gift-cards` 改模板奖励会改变全部未兑换码的价值却无 reauth；`POST v1/commission/config` 无 reauth；门户 `POST v1/me/withdrawals` 无幂等；admin `POST v1/me/password` 无认证类限流。
- 权限码错位：通知类测试发送挂 `billing.provider.write`；分销路由用 `billing.*`，种子里的 `marketing.commission.*` / `marketing.withdrawal.approve` 无路由使用；`GET v1/coupons` 只挂写权限。
- 两条批量改节点状态的路由（`status:batch` 与 `batch/status`）同一处理器、不同幂等 scope，同一个 key 跨路径不去重。
- `reset-password`、`rotate` 两条先 reauth 后权限，第 ⑤ 步之后无权限的管理员会先被要求输密码再收到拒绝；建议统一为先权限后 reauth（只改 router.go）。

### 7.4 过时注释与文档（协调会话合并 router.go 时处理）

- `api/admin/router.go` 与 `api/public/router.go` 的 L3 头 [POS] 仍写「/ 手写控制台（门户）、/app/ React 候选」，与第 1 阶段后的 `webapp.Mount` 不符。
- `api/admin/handlers.go:63` 与 `identity/reauth.go` 注释「53 条写路由」，实际 40 条。
- `router.go` 删除节点的注释说「外键 CASCADE 连带删指标」，实际是转 destroyed 终态并改名，数据保留。
- `router.go` 节点批量操作注释说批量处理器「一直没接进路由」，605 行早已接上。
- `router.go:572`「节点（NODE / AGT）」分节注释错位在用户组与设备之间。
- `api/public/handlers.go` changePassword 注释说「当前会话不在吊销范围内」，实际全部吊销。
- `pools.go` 的 `assignNodePool` 没有路由，它表达的「暂不允许移动节点分组」冻结与 `PATCH v1/nodes/{id}` 可随意改 pool_id 矛盾，需确认冻结是否仍有效。

## 8. 核对笔记（各分段原文）

> 协调会话此前的缺口清单逐条结论（确认/更正及依据）、Go 注释与代码不符、以及其它 FACT。第 6、7 节是从这里汇总出来的。

### 8.1 分段 A

#### 迁移预估（原文）

- GET v1/plugin-hooks/{code}/deliveries → 改表 plugin_hook_deliveries 加列 `last_duration_ms int`（投递时写入；测试投递的耗时只在响应里返回，不落库）
- GET v1/audit（auth_context）、GET v1/audit/export → 改表 audit_events 加列 `auth_context text NULL CHECK (auth_context IN ('session','reauth'))`（表是追加写 + 哈希链：新列是否纳入 entry_hash 由实现决定，建议纳入并只对新记录生效）
- GET v1/ip-clusters、POST v1/ip-clusters/{key}/review、POST v1/ip-clusters/{key}/disable-accounts → 新表 `ip_cluster_reviews(id uuid pk, tenant_id, source_ip_hash bytea, decision text CHECK IN ('normal','disabled'), note text, decided_by uuid, decided_at timestamptz, expires_at timestamptz NULL, UNIQUE(tenant_id, source_ip_hash))`，启用租户 RLS
- GET v1/dashboard/tasks → 数据迁移：权限字典新增 `ops.dashboard.read` 并授予内置管理角色（无表结构变更）
- POST v1/switches/{code} → 数据迁移：feature_switches 新增 `billing.checkout`、`marketing.giftcard.redeem`、`notify.email`、`admin.writes` 四行（enabled=true，非 essential）
- auth.email_verify 模板 → 数据迁移：notification_templates 种子一行
- 合计：新表 1 张、改表 2 处；另有数据迁移 3 项（无结构变更）

#### 核对笔记

**分配的缺口逐条结论**
- 降级开关编码与设计不一致：**确认并补充**。后端 7 个 code（`00010_seed_rbac.sql:172`），设计 6 个，唯一有语义对应的是 `portal.register`↔`auth.registration`，且极性相反（后端 enabled=可用）。另发现：全仓只有 `domain/identity/registration_policy.go:29` 读 feature_switches，其余 6 个后端开关都没接线；关闭开关必须带 reason 是数据库 CHECK（`00009_security_audit.sql:418`），违反时回 409 而不是 422。
- webhook 事件名与设计不一致：**确认**。后端白名单 `domain/plugin/hooks.go` 的 `Events`（10 个）；`user.created`→`user.registered` 可映射，`ticket.replied`、`node.offline/online` 没有对应（D-A-6）。另：`GET v1/plugin-hooks` 返回的事件目录键是 `Name`/`Desc`（大写，匿名结构体无 json tag）。
- 访问日志是安全事件而非 HTTP 访问日志：**确认**。`access_log.go` 归并 audit_events 与 subscription_fetch_log，没有方法、路径、状态码、耗时。另发现缺陷：`category` 只对 login/register/subscribe 生效，传 `admin` 等值时审计表不过滤（`auditActionFilter` 返回 nil），结果是「全部审计事件（不含订阅拉取）」。
- 邮件模板预览：**更正**。后端并非完全没有预览：列表与保存都返回 `preview_subject/preview_body`（`mail_template.go:29,59`），但只能预览**已保存**内容，设计要的是编辑中实时预览 → 待补 `POST v1/mail/templates/preview`。另：变量语法后端 `{{var}}`、设计 `{var}`。
- 风控批量禁用与标记正常：**确认**，后端只有只读聚类。聚类视图 `audit_ip_clusters`（00026，近 90 天）没有稳定 id，定位用 source_ip_hash；「30 天不再提示」需要新表。权限字典里已有 `security.risk.read/review`，目前没有路由使用，直接用。
- 审计导出与搜索：**确认**。`GET v1/audit` 只有 action 前缀、actor_kind、outcome 三个筛选；审计行也没有来源 IP、对象可读名、认证方式。
- 管理员姓名与邮箱：**确认**。`GET v1/me` 只回 user_id/kind/permissions/reauthed；`users.display_name` 列已存在，补字段无需迁移。
- 系统状态组件：**确认**。`system/status` 只有 backup 与 database（大小、连接数）；设计 7 个组件里后端一个都没有延迟或积压数据。SSE 连接数：`Hub.Count()` 只统计本进程，数据库变更监听跑在 aegis-public（`cmd/aegis-public/main.go:124`），admin 进程只经 Valkey 收事件——要全局数字得跨进程汇总。
- 仪表盘只读数据缺口（逐项）：需要处理 5 张卡在后端无聚合接口（待补 tasks）；KPI「较昨日」缺 yesterday、「本周新增」缺 new_7_days、「x/y 节点在线」缺节点数；收入「较上一区间」缺 previous_total；「注册与活跃」的活跃数缺失（stats/timeseries 只有 registered/logins/orders/unique_ips）；流量排行节点缺协议名（前端从节点列表补）、用户只有脱敏邮箱（D-A-2）；邮件积压不分渠道（DASH-01 冻结，分渠道放 system/status）。
- 备份状态（后端有、设计缺）：**确认**，`system_status.go` 的 backup 段 → 补进仪表盘系统状态卡第 8 行 + 抽屉。
- 调账生效日（后端有、设计缺）：**确认**，`effective_on` 可选、不得晚于租户今天 → 补进后台-05 收入调整的表单与列表；另发现冲销必须带 5–500 字原因，设计确认框也缺。
- 邮件设置里的注册模式：**确认并定位**。存于 system_settings `auth.registration_mode`（closed/invite_only/open，默认 closed）与 `auth.email_verification`；唯一写入口是 `POST v1/settings/mail`（`mail.go:78-79` 可选指针字段），读取方 `domain/identity/registration_policy.go`，且 feature_switches `auth.registration` 是总闸（缺行或关闭一律按 closed）。→ 补进后台-09 通知渠道页签第三张卡「注册与验证」。注意 POST settings/mail 每次整体覆盖 SMTP 字段。
- RequireRecentReauth 当前回的错误：**确认**。`middleware/middleware.go:192-197`：403 forbidden「此操作需要重新验证身份」，前端无法与其它 403 区分；第 ⑤ 步改 `reauth_required`。
- reauth 返回新令牌后前端要替换令牌：**确认**。`identity/reauth.go` 签新访问令牌（同 session、rat=now、exp 重新计算），不发 refresh；旧令牌仍有效到过期，但 rat 旧，必须换。
- admin 令牌有没有 refresh 机制：**确认没有**。Login 在库里建了 refresh_tokens，但 admin 响应不下发 refresh_token，两个网关也都没有 refresh 路由（public 的登录响应倒是下发了 refresh_token，却同样没有刷新接口）。访问令牌 TTL = `AEGIS_ACCESS_TOKEN_TTL`（默认 30 天），会话过期 = `AEGIS_REFRESH_TOKEN_TTL`（默认 30 天，且配置校验要求 access ≤ refresh）；reauth 会顺延访问令牌的 exp，但中间件每次请求都校验会话 expires_at，所以最长活到会话到期。到期即 401，前端回登录页。
- admin 改密码后的行为（保留规则 4）：**确认**。`identity/password.go` 同事务吊销该用户全部 sessions（revoked_reason=password_changed）、全部 active refresh_tokens 及 refresh 家族；响应 `{ok:true, reauthenticate:true}`。设计文案「其他会话被登出」需改成「所有会话」。
- RequireRecentReauth 路由总数：**40 条**（`router.go` 中 `RequireRecentReauth(d.Log)` 共 40 处，每处对应一条路由，其中 1 处在 `registerCatalogPlanUpdate` 的 `PUT v1/plans/{id}`；第 146 行那处出现在注释里，不算）。`handlers.go:63`、`identity/reauth.go` 注释和 phase2-brief 里写的「53 条」**与代码不符**。本分段占 5 条：POST revenue/adjustments、POST revenue/adjustments/{id}/reverse、POST plugin-hooks、DELETE plugin-hooks/{code}、POST plugin-hooks/{code}/test；待补后再加 3 条（POST switches/{code}、GET audit/export、POST ip-clusters/{key}/disable-accounts）。

**发现的其他事实**
- **注册验证码从未发送（缺陷）**：`identity.StartRegistration`（`domain/identity/service.go:65` 注释「开启注册事务并发送验证码」）只写 verification_codes，全仓没有投递路径，只有开发模式在响应里回 `dev_code`。生产环境开启 email_verification 后没有人能完成注册。已写成待补·后端（auth.email_verify 模板）。
- router 注释与代码不符（均未挂 RequireRecentReauth）：`router.go:481-483` 说模板测试发送要求近期重认证；`:310` 说改用户状态要重认证；`:238-240` 说导出、生成账号、群发都要重认证；`:578` 说设备模式切换要重认证。后三处属于其它分段，这里只记录。
- 权限声明偏差：通知测试类接口（`POST settings/telegram/test`、`settings/mail/test`、`mail/templates/test`）挂的是 `billing.provider.write`；读取 `settings/telegram`、`settings/mail` 挂的是 `security.audit.read`；保存挂 `platform.settings.write`。同一张卡片要三种权限才能完整使用。建议补后端时统一为 `ops.notification.write`（测试）/`platform.settings.write`（读写），是否调整由协调会话定（不影响本契约形状）。
- `POST v1/me/password` 会校验明文口令，却没有认证类限流（reauth 有按账号与按 IP 两级），只受网关级 240 次/分钟约束。
- `POST v1/me/password` 的错误键不一致：空值时 `fields.old_password` 与 `fields.new_password` 同时出现；策略不合格时键是 `fields.password`。
- 口令错误在 `me/password` 与 `auth/reauth` 上都回 401 unauthorized，与「令牌失效」同码；前端的全局 401 登出拦截必须排除这两个接口。
- `GET v1/plugin-hooks` 无钩子时 `hooks: null`；`GET .../deliveries` 无记录或 code 不存在时 `deliveries: null`（Go nil 切片），时间字段是数据库会话时区的无时区字符串。
- `POST v1/plugin-hooks/{code}/test` 把领域层的 404「插件不存在」也包成 422「发送失败：插件不存在」（`appearance.go:189-194`）。
- `POST v1/plugin-hooks` 是按 code 的 upsert，没有新建与更新的区分；前端新建时若复用已有 code 会静默覆盖。
- `POST v1/mail/templates/reset` 对 `admin.broadcast|email` 和迁移 00050 种下的三个 telegram 模板都回 404（`notify/template_admin.go` 的 `defaultTemplates` 只收了 6 个 inapp/email 模板）。
- 时区口径不一：收入（overview.revenue、revenue/timeseries、调账生效日）按 `tenants.timezone` 切日；`overview.users.today`、`orders.paid_today` 与 `stats/timeseries` 用数据库会话时区的 `date_trunc('day', now())`。同一张仪表盘上「今日」可能指两个不同的日界。
- 仪表盘读模型三件（traffic nodes/users、backlog）受 `docs/潘多拉面板-DASH-01冻结契约-20260730.md` 约束，本契约没有给它们加字段，需要的扩展都放在 overview、system/status 和新接口里。
- 实时事件依赖 aegis-public 进程里的数据库变更监听；aegis-public 停掉时 admin 的 SSE 连接仍然正常保活，但收不到任何事件，前端看不出区别。

### 8.2 分段 B

#### 迁移预估（原文）

- GET、POST、DELETE v1/ticket-macros → 新表 `ticket_macros`
- 其余待补·后端都不需要迁移：
  - 工单：队列多状态筛选、`last_message_author_kind`、`user_active_plan`、升级时提升优先级
  - 用户列表：修复 `group_name`、增加 `current_subscription`、增加筛选、扩展 q
  - 用户详情：修复 recent_orders 字段，增加 subscriptions 字段、stats、referrer、telegram
  - 画像：`registered_ip`
  - 批量运营：新增筛选、`sample_rows`、正文变量替换
  - 手动重置流量：挂 reauth
- 待决项如果选了会带来迁移的方案：D-B-2 方案 B 需要新表 password_reset_tokens；D-B-8 方案 C 需要改表 node_alive_ips 加 ip_enc。以上不计入合计
- 合计：新表 1 张，改表 0 处

#### 核对笔记

**协调会话给出的缺口清单**
- 「工单状态枚举与设计不一致」：**确认**。后端有 6 个状态，其中只有 4 个能由客服设置；设计稿有 4 个状态，而且都能自由设置。映射表写在工单一节开头。补充两点：「升级到 L2」应调用 `POST v1/tickets/{id}/status {status:"escalated"}`，而不是 `POST v1/tickets/escalate`，后者是租户级的批量 SLA 扫描（`handlers.go:680`，`support/service.go:1102`）；设计稿里客服把状态手动设为「等待用户」或「待处理」，后端会拒绝，因为 open 和 pending_user 只能由系统驱动（`support/service.go:1002`）
- 「设备策略的状态/模式编码与设计不一致」：**确认，并补充**。这不只是编码不同，语义也不一样。设计稿的 reject/kick 是超限时的处理动作，后端的 loose/strict 是判定发生在哪里（节点本地还是面板汇总）；后端没有「踢下最早设备」这种行为。此外，后端还有一个设计稿没有的宽容值 grace（0–5）。设计稿的「默认设备数」和「识别窗口」后端都没有，而且它们牵涉不变量，所以列为 D-B-5 和 D-B-4
- 「保留规则 2：GET v1/users/{id} 等接口不回订阅地址」：**确认**。GetUser、ListUsers、Profile、导出都不含令牌或 URL（`adminops/service.go:318–447`，`profile.go:218`，`adminops/bulk_users.go` 的 ExportRow）。**另有发现**：`POST v1/subscriptions/{id}/rotate` 会回传新令牌明文，列为 D-B-1。设计稿「订阅」tab 里的订阅地址和复制按钮由前端删除
- 「工单快捷回复后端缺」：**确认**。support 域没有 macro 或 canned 相关的表和代码，已定下接口形状，需要新表 1 张
- 「用户列表 / 详情缺到期、流量、设备数等只读数据」：**确认，并更正**。列表只有 `active_plan` 套餐名，没有到期时间、流量、设备数。详情有到期时间（`subscriptions[].current_period_end`），但没有流量和设备数。**另有一个缺陷**：列表的 `group_name` 永远是空串（`adminops/service.go:274–307`，SELECT 和 Scan 都漏了这一列）
- 「订单 tab 用哪个接口」：**更正**。`GET v1/orders` 不支持 user_id（`handlers.go:342`，`adminops/service.go:556 ListOrdersInput` 没有这个字段），只能用 `q` 做 order_no 或邮箱的 LIKE 模糊匹配。本分段改用 `GET v1/users/{id}` 已经返回的 `recent_orders`（最多 20 条），但其中的 plan_name 等 4 个字段因为没被选出而恒为零值（`adminops/service.go:389`），需要待补。「查看全部」的精确跳转依赖分段 C 给 listOrders 增加 `user_id` 参数，请分段 C 登记这一项
- 「用户组编辑与删除设计缺」：**确认**，已写为待补·前端。删除有四种引用冲突的保护，前端要提前把按钮置灰
- 「工单内部备注与 SLA」：**确认后端有**：
  - 内部备注：`ticket_messages.internal_note`；reply 接口的 `internal_note` 参数；内部备注不计入首次响应，也不改状态；用户端靠 omitempty 完全看不到这个字段
  - SLA：`sla_first_response_due`、`sla_resolution_due`、`first_responded_at`、`escalated_at`、`sla_breached`，以及 `breached=1` 筛选；SLA 时长按优先级设定：urgent 1h/8h、high 4h/24h、normal 12h/72h、low 24h/168h
  - 以上都已写为待补·前端
- 「完整用户资料设计缺」：**确认**。getUser 返回但设计稿没有展示的字段有 display_name、email_verified、risk_level、roles、全部订阅（含 status、plan_version、amount、auto_renew），已写为待补·前端，补在「画像」和「订阅」两个 tab。风控画像（`GET v1/users/{id}/profile`）在设计稿里也没有对应位置，设计稿「画像」tab 其实是资料卡。补为新的「风控」tab，按 `security.audit.read` 权限控制显示
- 「router 注释说要写权限 + 重认证、但代码没挂 RequireRecentReauth」：**确认，逐条列出**。契约以代码为准：
  1. `router.go:238–253` 批量运营的注释说「导出…生成账号…群发…这三个都要写权限 + 近期重认证」。实际代码：导出只挂了 `iam.user.read`，是**读**权限，没有 reauth；生成是 `iam.user.write` 加幂等，没有 reauth；群发是 `ops.notification.write` 加幂等，没有 reauth
  2. `router.go:255–267` 流量重置的注释说「手动重置…要写权限 + 近期重认证」，代码没有 reauth。设计稿两处入口都要求 reauth，所以写为待补·后端「挂 reauth」
  3. `router.go:311–313` `users/{id}/status` 的注释说「要写权限 + 近期重认证」，代码没有 reauth。设计稿的启用/禁用确认框也不要求 reauth，所以维持现状
  4. `router.go:579–581` `settings/device-limit` 的注释说「要求近期重认证」，代码没有 reauth。设计稿的保存也不要求 reauth，所以维持现状

**其他发现（FACT，除非另有标注）**
- 设备相关的两个写接口 `setDeviceLimit` 和 `setDeviceMode`（`devices.go:89`、`devices.go:130`）只写了 slog 日志，**没有写审计**。这与 router.go 包文档里「全部写操作留审计（SEC-012）」的说法不符。`setDeviceLimit` 还不检查影响的行数，订阅不存在也会返回 200
- `reset-password` 和 `rotate` 的中间件顺序是先检查 reauth、再检查权限（`router.go:316`、`router.go:321`），其他路由都是先检查权限。等到第 ⑤ 步引入 `reauth_required` 之后，没有 `iam.user.write` 权限的管理员会先被要求输密码，输完之后才收到 403。建议统一改为先检查权限（只改 router.go）
- 管理员重置密码时，校验失败的字段名是 `fields.password`（`identity/service.go:589`），而请求体里的字段叫 `new_password`，前端做错误映射时要注意
- `GET v1/devices` 的行里只有 `subscription_id` 和 `email`，没有 `user_id`，所以前端从「接近上限」列表打开用户抽屉时只能按 email 搜索。建议顺手给行加上 `user_id`（需迁移：否），可以一起归入列表的待补
- `has_active_sub`（批量运营）只认 `s.status='active'`，而列表的 `active_plan` 认 `active` 和 `trialing`，两处口径不一致。本分段的待补统一采用「订阅态口径」一节的定义
- 手动重置流量只选 status 为 `active` 的订阅，处于试用期（trialing）的订阅不能手动重置（`billing/traffic_reset.go:195`）
- 以下接口在 path id 不是合法 uuid 时推测会返回 500，而不是 400 或 404（INFERENCE：handler 和 domain 都没有做 uuid 预校验，SQL 里直接比较 uuid 列）：getUser、saveUserGroup（编辑）、deleteUserGroup、assignUserGroup、setDeviceLimit、ticketAssign 的 `assigned_to`。作为对照，traffic-resets、rotate、manual reset 都有校验
- 工单详情和回复在工单不存在时返回的是 `CodeNotFound`「工单不存在」，没有用 NotFoundOrForbidden，与其他 admin 接口的风格不一致，但错误码同样是 404
- 在 `router.go:572`，「--- 节点（NODE / AGT）---」这个分节注释错放在了用户组和设备两段之间，下面紧跟的实际是设备路由，属于注释错位

### 8.3 分段 C

#### 迁移预估（原文）

- GET v1/gift-cards/batches、POST v1/gift-cards/batches/{id}/export → 新表 `gift_card_batches`（含 exported_at / exported_by，回填存量批次）；改表 `gift_card_codes` 给 batch_id 加外键（1 处）。
- POST v1/plans/{id}/unarchive → 只有 D-C-1 选 b 时才需要：替换 `app.guard_plan_catalog_transition` 函数（改函数 1 处，不改表）。
- D-C-5 选 a 且加「超额限速值」→ 改表 `plan_versions` 加列 `overage_throttle_kbps`（1 处，可选）。
- 其余扩展（列表加字段、`user_id` 筛选、pending_amounts、渠道统计、created_by_email、人工单 settlement、commission scope、卡码掩码）都不需要迁移。
- 合计：新表 1 张；改表 1 处（必做）+ 最多 2 处（取决于 D-C-1 / D-C-5 的选择）。

#### 核对笔记

缺口清单逐条结论：
- **订单状态枚举与设计不一致**：确认。后端 9 个状态（`panel/migrations/00004_billing_ledger.sql:30`），设计 3 个；映射已写进 GET v1/orders。另外 `status` 参数是 LIKE 模式、只能传一个值，按设计分组筛选需要改成多值（已列为扩展）。
- **支付是 epay 收银台跳转（无二维码），订单 30 分钟过期**：确认。`checkout.go:383` 写 `expires_at = now() + interval '30 minutes'`，`payments.go:269` 意图也是 30 分钟；适配器目录 `internal/domain/payment/` 下只有 `epay`、`demo`。对后台的影响只有状态映射（`expired`）和渠道卡片。
- **欠费单 = 挂账**：确认，并补充一点：两者**方向相反**。挂账是平台多收或迟收了用户的钱（贷记用户余额），设计的欠费单是用户欠平台的钱（扣余额、余额可为负）。文案改法见 GET v1/late-payments。另外发现 `pending_amount` 跨币种直接相加（`panel/internal/domain/billing/late_payment.go` ListLatePayments 里的 `sum(amount) FILTER (WHERE status='suspense')`）。
- **礼品卡码掩码展示 / 一次性导出 / 批次列表**：确认后端都没有。现状比预想的更开放：GET codes、GET usages、生码响应都返回完整明文；export 可以无限次导出，只要只读权限，没有 reauth，也不写审计（`giftcard.go:113`）。批次只有 `gift_card_codes.batch_id` 这一列，没有批次表。已定形状。
- **优惠券编辑**：更正。设计 06 **没有**优惠券编辑入口（只有新建、批量、启停、兑换记录；`管理后台-06-营销.dc.html` 的 coupons 部分里没有 edit 动作），不需要补。
- **对账（设计 05）**：更正。设计 05 的第四个 tab 是「收入调整」（`管理后台.dc.html:269` 的 `tabs: [...['adjust','收入调整']]`；`功能对照.md` 写作「收入调整登记与冲销（只追加）」），不是渠道对账。后端已有 GET/POST v1/revenue/adjustments 和 POST .../{id}/reverse（`revenue.go:27/43/62`），已写进本分段。真正的对账表 reconciliation_runs / reconciliation_discrepancies 已在 00067 删除；权限 `billing.reconciliation.read/write` 仍在种子里，但没有路由使用。
- **套餐「取消归档」**：确认后端没有，而且是库级不变量阻止的 → 写成待决 D-C-1，没有直接定为「待补·后端」。
- **仅首单返佣**：确认后端没有。`accrueCommission`（`billing/commission.go:69`）对每一笔已支付订单都计提。已定形状（`scope` 设置），不需迁移。顺带一提：coupons 表有 `applicable_order_kinds` 列，但结账时不校验（设计 mock 里的「首单」券因此也做不了）；设计的创建表单没有这个字段，所以不列为待补。
- **套餐高级设置（后端有、设计缺）**：已列表，写明每个字段补进向导的哪一步或详情页的哪个位置（见「后端有、设计缺：套餐高级设置」）。注意 POST complete 不收 visible_from/until、宽限 / 续费语义、max_concurrent、价格的 user_group_id 和有效期；PUT complete 还不收 quota_reset_strategy。这些只能走单独接口。
- **调账**：本分段出现两类。① 收入调整（报表口径，只追加，`billing.adjustment.write`），已写入；② 挂账转余额（真实动账）。用户余额调账 POST v1/users/{id}/balance 在分段 B。
- **listOrders 的 query 参数**：已逐条写清（q / status / from / to / limit / offset），并说明**没有 user 筛选**，`q` 按 email 子串匹配会误中其他用户，所以新增 `user_id` 参数（待补·后端）。

发现的其他事实：
- **mark-paid 响应是 PascalCase**：`billing.PaymentWebhookOutput`（`checkout.go:765`）没有 json tag，POST v1/orders/{id}/mark-paid 实际返回 `{"Processed":…,"LedgerTxnID":…}`。
- **向导不能设限速**：POST v1/plans/complete 带 `throttle_kbps` 时必定 422（`plan_wizard.go` 写死 `OveragePolicy:"suspend"`，而 `validateVersionSemantics` 禁止非 throttle 策略设置速率），之后自动回滚。PUT complete 的 rollPlanVersion 沿用当前版本的 overage_policy，同样的问题。
- **PUT v1/plans/{id}/complete 不是原子操作**，而且会顺带发布新版本；价格同步会归档清单里没有的全部在售价格，包括 USD 价格和用户组专属价（`plan_wizard_update.go` syncPlanPrices）。
- **向导权限缺口**：见 D-C-2。
- **路由注释与代码不符**：`router.go` 人工单那段注释写着「写权限 + 近期重认证 + 幂等键」，POST v1/orders/manual 实际没挂 RequireRecentReauth；`billing/release.go` AdminCancelOrder 的注释说 reauth 由路由负责，POST v1/orders/{id}/cancel 也没挂。
- **本该有而没有的保护**：POST v1/coupons/batch（一次最多 1000 张）没有幂等；POST v1/gift-cards（改模板奖励会直接改变所有未兑换码的价值）没有 reauth；POST v1/commission/config（改返佣比例）没有 reauth。
- **权限码错位**：分销相关路由用的是 `billing.order.read` / `billing.provider.write`，而种子里有 `marketing.commission.read`、`marketing.commission.review`、`marketing.withdrawal.approve`，没有任何路由使用它们。GET v1/coupons 只挂了写权限 `marketing.coupon.write`，没有只读权限可用。
- **错误码不一致**：cancelOrder 的理由长度校验回 400 而不是 422；applyLatePayment 用的是 `json.NewDecoder`（不拒绝多余字段，上限 4 KiB），不是 httpx.DecodeJSON；setCouponStatus / couponRedemptions 收到非 uuid 的 id 时回 500 而不是 404；listOrders 的 offset 为负时回 500。
- **状态口径不一致**：ListPlans 的 `node_count` 按 `nodes.serving_status='active'` 计数，GET v1/plans/{id}/pools 的 `active_nodes` 按 `nodes.status='active'` 计数，同一个页面上两个数字可能对不上。
- **生码响应与返回值**：createPlanComplete 返回的 `plan` 是建壳那一刻的快照（没有版本和价格）；createPlanVersion 返回的 VersionRow 除标识字段外都是零值。
- **礼品卡余额币种写死 CNY**（`giftcard/redeem.go:233`）。

### 8.4 分段 D

#### 迁移预估（原文）

- GET/POST/PATCH v1/nodes 的 `country_code` → 改表 nodes 加列 `country_code char(2) NULL`（CHECK `^[A-Z]{2}$`）。
- GET v1/nodes/{id}/identity 与 POST v1/nodes/{id}/server-token 的签发时间/签发人 → 改表 nodes 加列 `server_token_issued_at timestamptz NULL`, `server_token_issued_by uuid NULL REFERENCES users(id) ON DELETE SET NULL`。
- GET/POST v1/node-pools 的用户组限制 → 改表 node_pools 加列 `allowed_user_group_ids uuid[] NOT NULL DEFAULT '{}'`（空=不限；订阅下发过滤由 public/订阅侧实现）。
- 以下待补·后端不需要迁移：GET/PUT v1/nodes/routing（复用 node_id 为 NULL 的行）、POST v1/nodes/{id}/retire、GET v1/nodes 的其余新字段、公告 target_user_group_ids（列已存在）、content created_by（列已存在）、PUT v1/nodes/{id}/routing 的校验修正。
- 合计：新表 0 张、改表 3 处（nodes 2 处、node_pools 1 处）。待决 D-D-2 选 (b) 再加 1 处，D-D-4 选 (b) 再加 1 个数据 migration。

#### 核对笔记

**分配的缺口逐条结论**
- 保留规则 5（move 守卫）：**确认**。`nodefabric/node_admin.go:576 MoveAdminNode` 拒绝 serving_status active/draining，并拒绝带任何 agent 资产（控制节点、身份、运行实例、指标、任务、provisioning、引导令牌、配置应用、用量源/批次、流量上报、有效 server token）的节点。router.go:666 的注释也写明「只有从没用过的草稿节点能移」。设计允许迁移任意节点 → 前端禁用规则与提示已写在 move 条目。补充：前端能判断的只有 serving_status、last_heartbeat_at、identity_serial，其余资产只能靠 409 的 fields 回显。
- 全局路由组 +「发布到全部节点」：**确认缺，并细化**。schema 已支持全局（`00017_node_routing.sql` 两表 node_id 可空），`LoadRouting`/`loadEffectiveRoutingTx` 已合并全局**出站**，但全局**规则**从不读取，且没有任何接口能写 node_id 为 NULL 的行。`nodes/config/publish scope=global` 发布的是 legacy 叠加层 payload，保留键禁止 outbounds/routes，**不能**用来发路由。已定 GET/PUT v1/nodes/routing。另发现单节点 PUT 的出站引用校验不认全局出站 tag（handlers.go:1263 只放了 direct/block + 本次私有出站），已列为待补·后端。
- 节点 CPU 等监控只读数据：**部分更正**。`GET v1/nodes/{id}/metrics` 已提供 CPU/内存/磁盘/负载/网速/连接数曲线与 latest（最长 1440 分钟），抽屉「监控」够用；缺的只是**列表**的负载列与 24h 流量（列表只有 30 天 traffic_bytes、health_score）。已定为 GET v1/nodes 字段扩展，无迁移。
- 服务商统计：**更正——设计里没有**。后台-07 服务器 tab 只有 region/IP/agent/CPU/内存/磁盘/下属节点，没有按服务商/provider 的统计；整个管理后台原型里 `providers` 只出现在「订单与收款 › 支付渠道」tab。库里有 `cloud_providers` 表，但它是 RESERVED-TABLES.md 登记的孤儿表（`node.provider.write` 权限也没有路由使用）。本分段无此项接口。
- 节点池增删改：**确认**后端有、设计缺 → 三个待补·前端入口已写；另外设计要的「节点名标签、绑定套餐名、仅用户组」后端列表没有 → 字段扩展（用户组限制需迁移）。
- 单节点路由编辑：**确认**后端 GET/PUT 有，设计只有只读展示 → 抽屉新增「路由」tab。
- 服务器详情与编辑：**确认**，GET /servers/{id}、GET /servers/{id}/nodes、PATCH、POST /servers 表单都是待补·前端。
- 节点交付提示：**确认**，后端提供四处：GET v1/nodes 的 `delivered_to_users`/`delivery_note`；create/copy 的 `warnings`；server-token 的 `hint`；bootstrap-token 的 `install_command`（令牌从终端读入，不嵌在命令里）。均已写进对应条目。
- 节点字段 vs 设计表单：**确认**，后端可编辑字段全集 = name、pool_id、node_type、server_host、server_port、kernel、traffic_rate、display_name、protocol_config（创建时另有 server_id、sort_order）；设计只画了协议字段 → 抽屉补「基本信息」区。设计的 uuid/password 是每节点凭据，后端没有（用户凭据跟订阅走）。
- 服务商 accepting_new：**更正——不属于节点/服务器**。它是 `payment_providers.accepting_new`（`00004_billing_ledger.sql:154`），PAY-009「停止新收单但保留回调」，接口是 `GET v1/payment-providers` / `POST v1/payment-providers/{code}/toggle`（handlers.go:418/432），属于订单与收款分段，本分段不写。
- 公告定时发布：**确认**后端支持（`publish_at` 在未来 → scheduled，`domain/notify/announce.go:91` 到点自动转 published），设计缺 → 待补·前端。
- 公告级别：**确认** severity 四档，设计缺 → 待补·前端。另发现公告按用户组定向在库和 public 端都已实现，只是 admin 接口没暴露 → 待补·后端（无迁移）。
- 知识库字段：**确认**，content page 全字段见 POST v1/content-pages；设计只用了 title/category/body/version → 「发布设置」折叠区待补·前端；版本历史的作者需后端加 created_by_name。
- 主题品牌：**确认** theme = code、name、is_builtin、is_active、tokens、branding（site_name、tagline、Logo）、custom_css；设计只有名称+两色 → 主题编辑器待补·前端。

**发现的其他事实**
- Go 注释与代码不符：router.go:646–648 注释说删除节点「会连带删掉这个节点全部的指标与流量上报（外键是 CASCADE）」，实际 `DeleteNode` 是转 destroyed 终态并改名，数据全部保留（`nodefabric/node_admin.go:853` 的注释才是对的）。
- `POST v1/nodes/{id}/server-token` 不写审计（`IssueServerToken` 与处理器都没有 audit.Write），也不拒绝 retired/destroyed 节点——与包头「全部写操作留审计（SEC-012）」不符。
- `POST v1/nodes/status:batch` 与 `POST v1/nodes/batch/status` 是同一处理器，但幂等 scope 不同（`node_status_batch` / `node_batch_status`），router.go:659 注释说批量处理器「一直没接进路由」，但 605 行早已接上，注释过时。
- 死代码：`pools.go:246 assignNodePool` 没有路由；它表达的「配置发布身份升级完成前暂不允许移动节点分组」冻结，而 `PATCH v1/nodes/{id}` 的 pool_id 可以随意改池（PatchAdminNode 只递增 config_source_generation）。两处规则矛盾，需后端确认冻结是否仍有效；contract 暂按 PATCH 现状写。
- 未校验 UUID、非法 id 会变 500 的路由：`GET v1/nodes/{id}/metrics`（还会把不存在的节点回成 200 空数据）、`DELETE v1/nodes/{id}`、`POST v1/nodes/{id}/status`、`POST v1/nodes/{id}/revoke-identity`、`POST v1/nodes/{id}/server-token`、`GET/PUT v1/nodes/{id}/routing`、`POST/DELETE v1/node-pools/{id}`、`POST v1/servers/{id}/bootstrap-token`（node_name 省略时）。同一资源的 404 有的回 `NotFoundOrForbidden`、有的回 `CodeNotFound` 的中文消息，前端统一按 404 处理即可。
- `GET v1/nodes` 硬上限 200、`total` 只是本页条数；超过 200 个节点会被静默截断。
- `POST v1/node-pools` 回 200 而非 201；更新时 status 非法回 422 但不带 fields；pools 的写事务 Scope 没带 ActorID（审计 actor 由 auditPool 另取）。
- `POST v1/themes` 不校验 code 格式和 name 长度，靠 DB CHECK（`^[a-z][a-z0-9_-]{1,38}$`、1–60 字），违反时变成 500；`GET v1/themes` 在无主题时回 `null`。
- 创建节点的默认安装令牌 TTL 是 20 分钟（上限 30）；设计写「30 分钟内有效」，前端必须显式传 `ttl_minutes: 30`。
- 服务器状态机里 ready 不能直达 maintenance（只能 ready→draining→maintenance），设计「标记维护」已映射到 draining。
- `PUT v1/nodes/{id}/routing` 保存后不调用 `notifyNodeChanged`（PATCH 会），在线节点要等下次拉取才生效；已并入该条的待补·后端。
- 管理员创建的节点生命周期 status 恒为 draft（只有 serving_status 在变），所以 DELETE 对它们直接可用；只有经过 enrollment 进入 active 的节点才会被「请先退役」卡住——这是新增 POST v1/nodes/{id}/retire 的原因。

### 8.5 分段 E

#### 迁移预估（原文）

- `GET v1/me/subscriptions/{id}/usage` → 新表 subscription_usage_daily（按订阅按日计费流量），uniproxy 上报事务内 upsert。
- `GET v1/traffic-packs` → 新表 traffic_packs（流量包目录，挂变更通知）。
- `POST v1/me/traffic-pack-orders` / `GET v1/me/traffic-packs` → 新表 traffic_pack_grants；改约束：00036 订单形状/预留图检查加 kind='addon' 分支。
- `POST v1/me/subscriptions/{id}/change-plan` → 改表 orders 加列 proration_credit_amount 并扩金额恒等式；改约束：00036 检查加 kind='upgrade' 分支（与上一条同一批约束，合并为一次改动）。
- `GET v1/payment-methods`、各现有接口的字段扩展（subscriptions、plans、coupons/preview、orders、orders/{id}、gift-cards/preview、me/gift-cards） → 否。
- （D-E-3 若选 a）plans 加 highlights、featured 两列 → 另计 1 处改表。
- 合计：新表 3 张、改表 2 处（orders 加列 1 处；00036 按 kind 分支的约束 1 处）。

#### 核对笔记

- 支付是 epay 收银台跳转、无二维码：**确认**。`payments.go:157` 返回 `http_method/redirect_url/form_fields`，epay（`epay/epay.go:160`）与 demo（`demo/demo.go:62`）都是 `http.MethodGet`，redirect_url 已带签名查询串；前端 `location.assign` 顶层导航，不受 `webapp.go:44` 的 `form-action 'self'` 影响。POST 分支（`payment/provider.go:32` 注释）目前没有适配器产出，若将来出现需同时放宽 CSP，前端遇到按错误处理。
- `use_balance`：**更正**——不是布尔开关，是 int64 抵扣金额（最小货币单位），`checkout.go` 与 `renewal.go` 中 <0 置 0、>total 截断为 total，余额不足 409「余额不足」；payable=0 当场履约 status=fulfilled。
- 订单 30 分钟过期：**确认**。orders/renewal/topup 三处 INSERT 都是 `now() + interval '30 minutes'`（`checkout.go:384`、`renewal.go:223`、`topup.go:125`），支付意图过期 ≤ 订单过期（`payments.go:269`）；到期由预留过期任务转 status=expired。设计写 15 分钟，按 30 分钟与 expires_at 显示。
- 订单状态枚举与设计不一致：**确认**。后端 9 值（`migrations/00004:31`、`my_orders.go isKnownOrderStatus`）：draft、pending_payment、processing、paid、fulfilled、cancelled、expired、partially_refunded、refunded；设计 3 值 pending/paid/cancelled，映射见 `GET v1/orders`。kind 实际只产生 new/renewal/topup（`checkout.go:1836 orderKindFor` 恒为 new，人工单也是 new），CHECK 另允许 upgrade/downgrade/addon/manual 但无代码路径。新购与续费成功终态是 fulfilled（`checkout.go:1707`、`renewal.go:637`），充值单也会被推进到 fulfilled（`checkout.go:1423`）。
- 邀请链接 `/?invite=CODE`：**确认**。`webapp.go:91 Mount` 只注册 `/` 与 `/assets/*`，`/r/CODE` 命中 `router.go` 的 `/{prefix}/{token}` 回伪装 404。注册带码：只在 `register/start` 的 `invite_code`；`register/complete` 的 `invite_code` 是废弃字段、被忽略（`handlers.go:112`）。补充：open 模式下填了无效邀请码也会 403（`registration_policy.go` enforceRegistrationStartPolicy）；同一邀请码 10 分钟内最多 20 次 register/start（`router.go` reg_invite），热门邀请链接可能 429。
- 保留规则 1 快捷登录：**确认**。签发 `POST v1/me/quick-login`（`selfservice.go:89`）返回 `{ token, expires_at, expires_in: 60 }`（`identity/quicklogin.go:33 QuickLoginTTL`），绑定签发会话、同会话只留最新一条；消费请求形状 `{ token }`，成功建全新会话（auth_methods=quick_login）。后端没有任何邮件登录链接接口，设计里的邮件入口删除。
- 保留规则 3：**确认**。`subscribe.go:224` 只输出 name/protocol/traffic_rate，`subscription.NodePreview` 注释明确不含地址端口；设计的国家与负载两列删除。
- 流量包：**确认后端没有**（更正补充：schema 残留 `orders.kind 'addon'` CHECK 与 `quota_balances.granted_addon` 列，后者只被礼品卡 `billing/giftgrant.go:85 GrantTraffic` 使用，且周期重置/续费不清 addon——`traffic_reset.go:172`、`renewal.go:550`——一次性追加会每周期复用）。见 D-E-1。
- 升级折算：**确认没有**。`plans.allow_upgrade`（`adminops/catalog.go:34`）只是可编辑标志，无任何读取它的下单路径。见 D-E-2。
- 流量重置日与在线设备：**更正**——数据后端都有，只是门户接口不返回：重置日来自 `quota_balances.period_end` 与 `plan_versions.quota_reset_strategy/quota_reset_day`（`00003:147`），在线设备来自视图 `subscription_online_devices`（`00024:24`，近 5 分钟不同 IP）与 `COALESCE(subscriptions.device_limit, plan_versions.max_devices)`（后台 `admin/devices.go` 同口径）。已写成 `GET v1/me/subscriptions` 的字段扩展，无迁移。
- 概览其他只读数据逐项：按日用量柱状图——**无数据源**（`usage_aggregates`、`usage_events` 已被 `00067_drop_orphan_tables.sql:45-47` 删除，`node_traffic_reports` 只有节点级合计与原始报文），新表见迁移预估；日均/今天/用完预测由 usage 接口派生；余额格 `GET v1/me/balance`；可提佣金 `GET v1/me/commission`（邀请返利分段）；公告 `GET v1/me/announcements`（消息分段）；待支付条 `GET v1/orders?status=pending_payment`；未读数属消息分段。
- 余额流水：**确认** `GET v1/me/balance` 带 `history`（≤100 条，`topup.go:424`，kind/delta/memo/at），设计缺 → 钱包页新增「余额明细」。
- 订单明细：**确认**，全部字段见 `GET v1/orders/{id}`；设计只用了订单号/时间/结果，其余字段补进展开区；支付方式、优惠码、有效期至需后端追加（数据都在：payments.method、payment_providers.display_name、coupons.code、orders.subscription_id）。
- 订阅拉取统计：**确认**在 `GET v1/me/subscription-links`（fetch_count、last_fetched_at、distinct_sources_24h），`GET v1/me/subscriptions` 没有。
- 续费遇改价：**确认并补充**。价格行不可变（`00035_catalog_authoring.sql:613 trg_prices_immutable`，后台只能归档 `adminops/catalog.go:884`），所以「改价」= 旧价归档；renew 不传 price_id 时用订阅当前 price_id，归档/组价不符/不在有效期统一 409「所选价格已下架，请重新选择」（`renewal.go:33`），不会静默按新价扣款；传新 price_id 按新价新周期；allow_renewal=false 409。
- 站点配置：**确认**只有 `registration_mode`、`email_verification` 两个字段（`handlers.go:949`），分别补进登录页注册 tab 显隐/邀请码必填/页脚文案，与注册步骤 2 验证码框显隐。
- refresh_token：**确认无刷新接口**。public 与 admin router 都没有 refresh 路由；全仓库 `refresh_tokens` 只有写入（`identity/service.go:509`、`quicklogin.go`）与吊销（logout/sessions/password/admin_reset_password/adminops），没有消费方。access token 默认 TTL 720h（`platform/config/config.go:77`，env `AEGIS_ACCESS_TOKEN_TTL`；`deploy/.env.example:28` 同为 720h），refresh 同 720h，二者上限 365 天且 access ≤ refresh。
- 其他事实：
  - 实时推送放大：`quota_balances` 表没有 user_id 列（`00006_metering.sql:229`），却在 `00021_notify_fix_tables.sql` 的监听名单里，`listener.go:122-129` 对无 user_id 的变更推 `ChannelPublic`——每次节点上报流量更新 consumed，都会给**全租户所有在线用户**推一条 `subscriptions.changed`（只含表名、操作、行 id，不含用量）。前端已按节流处理；后端应改为按 subscription 反查 user 或移出监听名单（不在本分段范围，建议协调会话单独排期）。
  - `00020` 监听名单里的 support_tickets、wallet_*、plan_prices 表不存在，已被 `00021` 纠正；余额所在的 ledger 表不在监听名单，余额变化无推送。
  - `POST v1/me/topups` 回 200，而 orders/renew 回 201（`topup.go` PrepareJSON(http.StatusOK)）。
  - 取消订单对非 UUID 回 400（`release.go:150`），详情对非 UUID 回 404（`my_orders.go:168`），口径不一。
  - 推断：`POST v1/orders` 只校验 plan_id/price_id 非空、不校验 UUID 形状，`POST v1/orders/{id}/pay` 不校验 id 形状，非法值会在 SQL 的 uuid 转换处失败并回 500。
  - 支付渠道未启用时 `payment/factory.go:95` 返回普通 error，接口回 500 而不是 4xx/503。
  - 支付回跳默认地址是 `PublicBaseURL + "/#orders"`（`payments.go:349`），前端 hash 路由需认 `#orders`，或始终显式传 return_url。
  - 礼品卡预览把 code_total、code_used、conditions 等运营数据返回给普通用户（`giftcard/codes.go:246` 返回整份 Template），契约里建议收窄。
  - 注册完成验证码错误回 403，文案是「注册当前不可用或邀请码无效」，前端需按步骤改写提示。
  - quick-login 响应缺 token_type、user_id，与 login 不一致。
  - `GET v1/me/balance` 的 history 没按币种过滤，balance 却固定 CNY（`handlers.go:1005`、`topup.go:430`）。
  - `listSubscriptions` 的 quotas.limit 只有套餐基础额度（limit_value），不含 granted_addon/adjusted，总量要用 consumed + remaining。
  - CSP `img-src 'self' data:`：插槽 HTML 与 branding 里的外链图片不会显示，只能用 data URI 或同源资源。
  - `panel/internal/api/public/handlers.go` 1116 行，超过单文件 800 行约束（仅记录）。

### 8.6 分段 F

#### 迁移预估（原文）

- POST v1/content/pages/{slug}/feedback → 新表 `content_page_feedback`（主键 tenant_id, page_slug, page_version, user_id；字段 helpful bool、created_at、updated_at；启用 RLS）。
- GET v1/me/commission 加字段、加 transfers → 否（查询现有表和账本）。
- GET v1/support/tickets(/{id}) 加 closed_reason、related_order → 否（列已存在）。
- GET v1/content/pages 加 q、platform=any → 否。
- POST v1/me/password 保留当前会话 → 否。
- GET v1/me/sessions 更新 last_seen_at、按 audience 过滤 → 否。
- PUT v1/me/notification-preferences 修正冲突目标 → 否（改 SQL 即可）。
- 视待决结果而定：D-F-2 选 (c) → 改表 users 加 `support_alias`；D-F-3 选 (b) → 改表 sessions 加 `ip_masked`；D-F-1 任一方案都不需要迁移。
- 合计：新表 1 张，改表 0 处（若待决选 D-F-2(c) 和 D-F-3(b)，最多 +2 处）。

#### 核对笔记

**分配缺口逐条结论**

- Telegram 绑定是 `/start CODE`、8 位、10 分钟：**确认**。依据：`notify/telegram.go:35`（BindCodeTTL=10m）、`:452-464`（8 位码）、`api/public/telegram.go:99-102`（接受 `/start X` 或裸码）。补充一点：设计里的 `/bind X` 会被当成码的一部分导致绑定失败；另外建议提供 `t.me/{bot}?start={code}` 深链。
- 邀请链接用 `/?invite=CODE`：**确认**，但路由是前端约定。后端只在注册接口收 `invite_code`，没有任何地方生成链接。`/r/CODE` 确实会命中 `r.Get("/{prefix}/{token}")`（router.go:108）。补充：设计的快捷登录链接 `/q/<token>` 同样会撞这条通配路由，已改为 fragment 形式。
- 工单状态枚举与设计不一致：**确认，并补充**。后端有 6 个状态（open/pending_user/pending_agent/escalated/resolved/closed）再加 closed_reason，没有 withdrawn 状态（迁移 00046 刻意用列表示）。**但现在的用户接口不返回 closed_reason**，前端分不出「已撤回」，所以列为待补·后端改形状。
- 通知偏好类目是 transactional/service/marketing × email/telegram：**确认**（notifications.go:162-169，00008:438）。映射方案见上。另外**发现 PUT 的 ON CONFLICT 目标与主键不匹配**，大概率每次都 500。
- 保留规则 1，快捷登录：**确认**。只能在已登录会话上签发，60 秒，一次性，绑定签发会话（identity/quicklogin.go:32,40-97）。外壳里的邮件登录链接不做。
- 设计有、后端缺：
  - 「有帮助」反馈：**确认缺失**，已定形状，需要 1 张新表。
  - 消息分类筛选：设计**没有**，只有「通知 / 公告」两个页签，不构成缺口。
  - 工单附件：设计**没有**；后端的 ticket_attachments 已在 00067 删除，不构成缺口。
  - 另外逐项查到的缺口：帮助的正文搜索（列表不返回 body，待补 `q`）；「付费好友」「累计佣金」两个统计；佣金记录里的「转入余额」条目（transfers）；会话的「活跃时间」（last_seen_at 从不更新）；会话的位置和 IP（待决 D-F-3）；客服姓名（待决 D-F-2）。
- 后端有、设计缺：
  - 提现：设计**已经有**「申请提现」，不是缺口。但设计没有展示提现记录的状态和驳回原因，已写进「佣金记录」的映射。
  - 被邀请人列表：**`GET v1/me/invite` 带 `invitees`**（handlers.go:813），`GET v1/me/commission` 不带。补进门户-06 新增的「邀请记录」卡片，不展示 risk_flag。
  - 工单关联订单：**`POST v1/support/tickets` 支持 `order_id`**，会校验订单属于本人（support/service.go:300-313）。但用户侧的列表和详情都不返回关联订单，所以写成待补·前端（表单下拉）加待补·后端（详情返回 related_order）。
  - 公告级别：announcement 带 `severity`（info/notice/warning/critical）和 `pinned`（notify/announce.go:18-25）。补进门户-08 公告列表的标签，critical 建议在概览页加横幅（分段 E）。
  - Telegram 的 `enabled` 开关：设计没有「站点未启用」这个状态，补进门户-10。
- 改密码后的会话行为：**更正**。协调会话此前没有定论。
  - FACT：public 改密会吊销该用户**全部**会话（**包括当前会话**）和全部 refresh token（identity/password.go:104-126），与 admin 走的是同一个函数（admin/handlers.go:180）。也就是说，现在 public 和 admin 的行为是一样的。
  - 设计要「其他会话已下线」、保留当前会话。保留规则 4 只覆盖管理员，所以 public 按设计改：给 ChangePassword 加 KeepSessionID，只由 public 传入。admin 不变。
  - `api/public/handlers.go:954-958` 的注释写着「当前会话不在吊销范围内」，与代码不符。

**其他事实**

- 改密时旧密码错误回的是 **401**（identity/password.go:64,76）。前端统一的「401 就跳登录」逻辑必须对 `v1/me/password` 做例外。新密码校验失败时，fields 的键名是 `password`，不是 `new_password`。
- `closeByUser`（support/service.go:586-589）没有写入 `closed_reason='user_closed'`。迁移 00046 只回填了历史数据，所以新产生的用户关闭工单 closed_reason 为 NULL，与 00046 注释「留成 NULL 会多出一类原因不明」的意图相反。已并入工单待补·后端。
- `getTicket`、`replyTicket`、`closeTicket`、`markNotificationRead` 没有预先校验 UUID，非法 id 可能回 500 而不是 404（INFERENCE）。`withdrawTicket` 和 `revokeMySession` 有校验。
- `markNotificationRead` 对不存在或他人的 id 也回 200 `{ok:true}`。这样不泄露信息，属于预期行为。
- `POST v1/support/tickets/{id}/withdraw` 必须带 JSON 体（`{}`），空 body 回 400。
- `GET v1/me/invite` 是一个会写库的 GET：懒生成邀请码。
- `ListActiveSessions` 不按 audience 过滤，public 令牌可以列出并吊销同一用户的 admin 域会话（sessions.go:51-58、81-98）。已写进会话接口的待补·后端。
- `POST v1/me/withdrawals` 没有挂幂等中间件，重复提交靠「同时只允许一笔在途」挡住（第二次回 409）。它和转余额（有幂等）不对称，建议补幂等，但不阻塞。
- `router.go` 的 L3 头部 [POS] 还写着「/ 手写门户、/app/ React 候选」，与第 1 阶段之后的 `webapp.Mount` 现状不符。这是 SEVERE-001 L3 过时，由协调会话在合并 router.go 时一并修正。
- 新站内信没有 SSE 事件，只有工单有 `ticket.updated`。铃铛角标需要前端轮询。
- `commission_entries.status` 的 CHECK 允许 frozen/settled/rejected，但 Go 只会写入 pending、available，冲销时由 SQL（00040）写入 reversed。「冻结」在语义上是 pending 且 frozen_until 在未来。

## 9. 修订记录

> 契约发布后按实现回写的修改。条目里以「修订 Rn」开头的行优先于该条目原文。

| 编号 | 日期 | 来源 | 内容 |
|---|---|---|---|
| R1 | 2026-09-24 | 后端一 0651cb2 | 套餐向导两条路由改 `catalog.publish` + reauth；编辑单事务、价格同步只动提交的币种 |
| R2 | 2026-09-24 | 后端一 0651cb2 | mark-paid 响应改 snake_case |
| R3 | 2026-09-24 | 后端一 0651cb2 | 挂账列表新增按币种的 `pending_amounts` |
| R4 | 2026-09-24 | 后端一 0651cb2 | 礼品卡模板保存、分销参数修改加 reauth |
| R5 | 2026-09-24 | 后端一 0651cb2 | 批量生成优惠券、门户申请提现加幂等 |
| R6 | 2026-09-24 | 后端一 0651cb2 | 优惠券与分销路由改用营销域权限码（迁移 00068） |
| R7 | 2026-09-24 | 后端一 0651cb2 | 佣金可用额统一口径，转余额 409 文案 |
| R8 | 2026-09-24 | 后端一 0651cb2 | 支付渠道停用时发起支付回 503 |
| R9 | 2026-09-24 | 后端二 62f7283 | reauth 路由增至 45 条（批量导出/生成/群发、改用户状态、全局设备模式）；reset-password / rotate 先权限后 reauth；批量导出权限改 `iam.user.write` |
| R10 | 2026-09-24 | 后端二 62f7283 | 两条批量改节点状态路由共用幂等 scope `node_status_batch` |
| R11 | 2026-09-24 | 后端二 62f7283 | 换发订阅链接不再回传令牌（D-B-1） |
| R12 | 2026-09-24 | 后端二 62f7283 | 设备上限两条写接口写审计，单订阅不存在回 404 |
| R13 | 2026-09-24 | 后端二 62f7283 | server-token：非法 id 404、退役/销毁 409、写审计 |
| R14 | 2026-09-24 | 后端二 62f7283 | 三个测试发送接口权限改 `ops.notification.write` |
| R15 | 2026-09-24 | 后端二 62f7283 | 门户会话列表与吊销只作用于 public 会话（缺陷 6） |
| R16 | 2026-09-24 | 后端二 62f7283 | 注册验证码经 notify 按地址投递（迁移 00074 模板种子），注册事务提交后立即派发（缺陷 1） |
