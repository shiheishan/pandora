---
paths:
  - "panel/internal/module/subscription/**"
---

# 订阅模块（状态、交付、拉取、续费）

## 状态口径（全仓库只认这里）
- 库里 `status` 只有 `active` / `expired`（`service/types.go`）；产品上的「能不能续费、换套餐、挂流量包」用 `StateAt` → `StateLive` / `StateRevivable` / `StateEnded`，关窗只看 `renewal_closed_at`（`service/state.go`）
- `State.AcceptsChange()` 是可续费、可变更、可作流量包转入目标的唯一判定（`Renew`、`Extend`、`ChangePlan`、`TransferPacks`、order 侧下单前都应对齐）
- 「当前订阅」：`SummariesByUsers` / `ByUser` 里 `currentIndex`（生效优先 → 到期晚 → 创建晚）（`service/read.go`、`service/state.go`）；后台别另写一套
- `status` 转换表只在 `CanTransition`（`active↔expired`）；关窗只写 `renewal_closed_at`、不经 `transition`（`service/state.go` 注释）

## 谁能连、能拉哪些线（与 node 分工）
- 节点名单：`Service.ServingSubscriptions` + `repo.ServingSubscriptions`（生效且未耗尽流量；账号、套餐绑池、池用户组不在这里判）（`service/serving.go`）
- 订阅里的线路列表：`NodeSource.DeliverableNodes` 由 node 实现（`node/service/node_source.go`），subscription 经 `UseNodeSource` 注入；没装配拉取/预览回 `ErrDeliveryUnavailable`（503）（`service/nodes.go`、`service/pull.go`）
- `DeliverableNodes` 的结果按（套餐版本, 用户组）在 node 进程内缓存 5 秒、被所有命中者共享：拉取与预览只读不改返回的切片和 `Config`（`node/service/delivery_cache.go`）
- 拉取路径先判主人 `identity` 账号 `active`、再调 `DeliverableNodes`，用户组取自同一次 `Owners.Get`（`identitysvc.User.UserGroupID`），不另读；门户预览 `MyNodes` 还要求 `StateLive` + 有效凭据，只给名称、协议、倍率（节点标签只在后台用，门户不显示，用户 2026-10-14 定）（`service/pull.go`、`service/nodes_preview.go`）
- 池用户组限定：node 侧只有 `poolAdmits`（`node/service/delivery.go`）；默认组（`user_group_id` 为空）进不了限定了组的池
- 没划进池的节点不服务任何人（fail closed）；心跳超时、运行异常不在 SQL 里硬删，线路侧用 `preferFresh` 分层（`node/service/delivery.go`）
- 影响名单的事实变了：写路径里调 `notifyRoster` → 提交后 `RosterNotifier.SubscriptionsChanged`（node 注册）（`service/service.go`、`service/rotate.go`）；换备注名 `SetMyLabel` **不**通知名单

## 订阅拉取（`GET /:prefix/:token`）
- 认证不缓存；前缀进程内 `atomic.Pointer` 缓存，不对就不查库（`service/pull.go`）
- 除「令牌有效、可救回」外一律 `ErrPullDenied` → handler 伪装 404；过期回 200 + `render.RenderExpired`（`service/pull.go`、`handler/subscribe.go`）
- 凭据可用：`active`，或已吊销但 `grace_until` 仍晚于 now；换链接 `rotateLocked` 吊销时 `grace` 传 nil、立即失效（`service/pull.go`、`service/rotate.go`）
- 令牌按 `token_hash` 查库后再 `hmac.Equal`；短于 16 字符不查库（`service/pull.go`）
- 成功/过期提示才占凭据每小时限流；未认证失败按来源 IP HMAC 采样落库（`service/pull.go`、`service/fetchlog.go`）
- `Profile-Update-Interval`：正常 12 小时，过期 1 小时；续费页 `renewalPageURL`（`service/pull.go`）
- 查询参数：`target`/`flag`/`type` 选格式（`subscribeTarget`）；`types` 按协议过滤、`filter` 按名字关键字过滤（`render.ParseTypes`/`ParseKeywords`，口径照 Xboard）（`handler/subscribe.go`）
- 后台模板、订阅附加设置（信息节点、协议前缀，settings 的 `subscription.info_nodes` 等）与订阅域名分组（本模块的表）在拉取侧走进程内快照，30 秒过期（`service/pull_extras.go`）；附加设置改动最多 30 秒后生效，本进程保存或恢复模板、保存订阅域名后快照立即作废。重读失败时有旧快照就沿用、打 Warn、5 秒后再试，没有旧快照才让拉取失败
- 信息节点的「距下次重置」用 `PullByTokenHash` 里同一行配额顺带取的 `traffic_period_end`（`PullRow.TrafficPeriodEnd`），不另查库
- 订阅域名分组（迁移 00048，三张表 `subscription_domain_groups`、`subscription_domains`、`subscription_domain_group_user_groups`，属本模块；用户 10-14 定，取代 settings 的 `subscription.link_base_urls`）：
  - 选组：用户组分到的组 → 缺省组 → 都没有用 `public_base_url`（`service/domain_set.go` 的 `groupFor`、`domainGroupFor`）；只有缺省组时不查用户组
  - `MyLinks`、换链接组内按有效权重随机（`pickWeighted`，crypto/rand），有效权重 = 基础权重（1–100）× 分数；Surfboard 托管配置取组里基础权重最高的、同权重取前面的（`stableBaseURL`、`subscribeURLFor`）；过期提示的续费页仍用 `public_base_url`
  - 分数在 Redis `subdom:score`（`service/domain_scores.go`）：6 小时半衰期向 1 衰减、夹在 [0.1, 3]（下限大于 0：任何域名都不会被压成 0；上限防独占）；Redis 不可用按基础权重，不让生成链接失败
  - 拉取信号：经**令牌主人所在组**的已登记域名（`hostBaseURL` + `groupHas`，Host 可伪造，别组的不算）到达的成功拉取 +0.05；同一凭据对同一域名每小时只记一次（进程内去重表，上限 5 万条）；进程内攒 10 秒一批写，不给拉取加往返。冲刷在下一次拉取或下一次读分数（生成链接、后台查看）时顺带触发，没有定时任务，低流量时会滞后到那时；丢一批只影响增速
  - 探测信号（用户 10-14 重申「成功加权、失败降权、保留随机性」，失败信号是产品要求，只靠拉取给不出失败）：门户「我的套餐」页登录后 `POST /v1/sub-probe/ticket`（按账号每小时 6 次）领票据，隐藏 iframe 打开 `GET /v1/sub-probe/frame?t=`，探测页对本组每个域名 `GET <域名>/v1/sub-probe`（204，5 秒超时），再凭票据 `POST /v1/sub-probe/report`（`service/domain_probe.go`、`handler/probe_frame.go`）。成功 +0.1、失败 −0.3
    - 为何用独立探测页：门户入口页对匿名访客一样，CSP 列订阅域名就公开了只给某些组的域名、能从门户反查订阅域名（审查 10-14 应改 1 方案三）。入口页 CSP 不含任何订阅域名，只多一个 `frame-src 'self'`（`app/portal_csp.go`），入口页也不读库；探测页的 CSP 按请求生成，connect-src 只有 `'self'` 与本人所在组的域名，脚本固定、按 sha256 放行，`frame-ancestors 'self'` 并去掉全局的 `X-Frame-Options`。不许用 `connect-src https:` 之类放宽
    - 票据：HMAC（主密钥 HKDF 用途串 `crypto.InfoHMACSubProbe`），5 分钟，绑账号 id 与所在组指纹（组 id + 组内域名按顺序，`groupFingerprint`）；探测页与上报都复核签名、过期、账号仍有生效订阅（`repo.HasLiveSubscription`，口径同 `StateLive`）、当前组指纹一致，任一不符统一 401「探测票据无效或已过期」。没有生效订阅或组里没域名时票据接口回空串，门户不开 iframe。票据与探测页读配置走 30 秒快照
    - `/v1/sub-probe`（GET/HEAD）只在请求 Origin 恰好等于门户源（`PortalOrigin`，public_base_url 的 scheme://host）时回 ACAO，`Vary: Origin`；CORS 中间件挂在限流前，429 也带 ACAO。探测页把 429 与 5xx 当「不计」，被 CSP 拦下（`securitypolicyviolation` 按 origin 精确比）也不计
    - 防操纵（应改 3，`service/domain_probe_scores.go`，一次上报一段 Lua）：票据一次性（`subdom:probe:ticket:<nonce>`）；账号×域名每 30 分钟最多记一次，成败都算（`subdom:probe:seen:`）；成功与失败对称：同一小时桶里 ≥3 个不同账号报同一域名成功才加分、报失败才扣分，本小时第 3 个起生效（`subdom:probe:ok:` / `subdom:probe:fail:` 两个上报人集合，固定小时桶）——成功也要多人，是为了防单个账号长期把某域名顶高、把用户引向被监控的域名（主 Agent 10-14 定）；每个域名每小时探测净变动封顶 ±0.5（`subdom:probe:net:`）；只收票据所绑组里的域名。拉取信号不受这些限制，口径不变（它是真实流量，已按令牌主人所在组、同一凭据每小时一次计）
  - 地址口径与 nginx 一致（`NormalizeBaseURL`）：只收 https + DNS 域名（至少两段、末段字母开头、≤253），不收 IP、localhost 与端口（`:443` 自动去掉）
  - 拉取快照读整份配置时三条 SELECT 在同一个只读可重复读事务里（`repo.DomainConfig`），已在事务里（后台保存）直接用当前事务
  - 后台 `GET/PUT /subscription-domains`：整份替换，PUT 要近期重认证、写审计 `subscription.domains_changed`（域名不是秘密，前后整份记），先咨询锁 `LockDomainConfig` 再读旧值、没变化不写不审计（`service/domains.go`、`handler/domains.go`）；校验文案与假后端 `frontend/dev/mock/admin/subscription-domains.ts` 逐字一致
  - nginx 订阅域名上只放行拉取路径与 `= /v1/sub-probe`，其余 404（`deploy/render-nginx.sh`）；探测页、票据、上报只走门户主域名
- 后台模板接口：`/subscription-templates[/:format[/preview]]`；保存与恢复缺省要近期重认证、写审计（只记 sha256 与字节数，不记全文），事务里先按格式加咨询锁（`LockTemplateFormat`）再读旧值（`handler/templates.go`、`service/templates.go`）；预览只渲染示例节点，在后台只读模式的豁免表里（`settings/handler/middleware.go` 与假后端 `frontend/dev/mock-api.ts` 同步）

## 链接、换链、备注
- 门户链接列表：`MyLinks` 列续费窗口没关的订阅（过期可救回仍列，`Expired` 只读）（`service/links.go`）
- 门户换链：仅 `StateLive`；过期回 `ErrRotateWhileExpired`（`service/rotate.go`）
- `rotateLocked`：吊销凭据、签新凭据、`RotateProxyUUID`、事件 `link_rotated`、审计、`notifyRoster`（`service/rotate.go`）
- 门户限频：`handler/delivery.go` 的 `rotateRules`（间隔 10 分钟、每份 24h 5 次、账号 24h 20 次）；后台 `AdminRotate` 不限频、要原因进审计
- 备注名：`NormalizeLabel`（`service/label.go`），配置名 `render.ProfileName`（`render/profile.go`）；路由 `sub_rename` 每分钟 10 次（`handler/handler.go`）

## 到期与续费窗口
- `RenewalWindow` = 30 天（`service/types.go`）；`ExpireDue`：到期改 `expired`（不动凭据）→ 满窗写 `renewal_closed_at` 并吊销凭据（`service/expire.go`）
- 续费 `Renew`：提前续接 `RenewalBase`；过期恢复 `restartQuotas`（`service/renew.go`、`service/period.go`）
- 延期 `Extend`：救回折算 cycle 上限、走过到期日重对齐 day/month（`service/extend.go`）
- 开通 `Provision`：唯一生效订阅时 `attachUnassignedPacks` 挂未分配流量包（`service/provision.go`）；注册试用 `GrantSignupTrial` 与它共用 `provisionAt`（指定起止），不建订单、不计佣，每人终身一次（`subscription_trials` 主键，只追加），套餐不可用时记日志跳过（`service/trial.go`）
- 注销账号 `CloseUserSubscriptions`：identity 在注销事务里调，生效中的改 expired（到期截到现在）、关续费窗口、吊销全部凭据，提交后通知名单（`service/close.go`）
- 后台直接改：`AdminSetPeriodEnd` 只改生效中的到期时刻（晚于现在、10 年内，不折算流量），`AdminSetQuotaLimit` 按「指标 + 周期」改一行当期上限（0–10 TiB 或不限，下个周期滚动按版本重设）；都要原因，各写一条订阅事件（`period_end_set` / `quota_limit_set`，状态不变）与审计，并推实时变更（`service/admin_edit.go`）
- 后台改订阅（改到期、改上限、加时长、加流量、重置流量、改设备数、换链接）一律经 `lockForAdmin` / `rejectDeletedOwner`：先不加锁读出属主，经 identity 的 `IsDeleted`（给用户行加共享锁）拒已注销账号 409，再锁订阅行（`service/internal.go`）。给 order/giftcard 的写方法（Provision、Renew、Extend 等）不在这里判断：注销时待付订单已在同一事务里取消，order、giftcard 在各自事务开头判断。

## 流量包
- addon 履约必须带生效订阅：`CheckPackTarget`；订单来源 `GrantTrafficPack` 不强制生效中（晚到回调照发）（`service/packs.go`）
- `TransferPacks`：来源须 `StateEnded`，目标须未停用；不能从仍在用的那份转出（`service/packs.go`）
- `(Source, SourceID)` 防重：`ErrPackDuplicate`

## 流量重置
- 后台手动、礼品卡、用户购买（xb-order #32）共用 `ResetTraffic`（`service/reset.go`）：只清生效中订阅全部流量配额行的已用量，每行一条 `traffic_reset_logs`，提交后通知名单；原因 `manual`（ActorID 必填）、`gift_card`、`order`（`ResetOrder`，UserID 必填校验归属，备注写订单号，由 order 履约流量重置单时调）。`TrafficReset.At` 非零时按它判生效（order 结算传结算时刻），零值用自己的时钟

## 配额滚动
- `RollQuotas`：第一遍后 `QuotaRollsRemain`，有则隔 `rollQuotaRetryDelay` 再滚一遍（`service/quota_roll.go`）
- 周期起止只在 `period.go` 算，repo 只筛到期行

## 锁序
- 给 order/giftcard 的写方法在 `s.tx.Do` 内自己 `lockOwned`（FOR NO KEY UPDATE）；调用方先锁订单再调 subscription（`service/service.go` 包注释）
- 用户行与订阅行同在一个事务里时，一律先用户行、后订阅行：identity 先 `LockUser`（FOR NO KEY UPDATE）再调 `CloseUserSubscriptions`（注销）、`GrantSignupTrial`（注册）；subscription 自己从不显式锁 users 行（后台写入口经 `IsDeleted` 加的共享锁也排在锁订阅行之前），同一用户的多份订阅按 id 升序 `lockOwned`（`close.go`）。
