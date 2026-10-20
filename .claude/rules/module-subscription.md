---
paths:
  - "panel/internal/module/subscription/**"
---

# 订阅模块（状态、交付、拉取、续费）

## 状态口径（全仓库只认这里）
- 库里 `status` 只有 `active` / `expired`（`service/types.go`）；产品上的「能不能续费、叠加、换套餐」用 `StateAt` → `StateLive` / `StateRevivable` / `StateEnded`，关窗只看 `renewal_closed_at`（`service/state.go`）
- `State.AcceptsChange()` 是可续费、可叠加、可变更的唯一判定（`Renew`、`Stack`、`Extend`、`ChangePlan`、order 侧下单前都应对齐）
- 「当前订阅」：`SummariesByUsers` / `ByUser` 里 `currentIndex`（生效优先 → 到期晚 → 创建晚）（`service/read.go`、`service/state.go`）；后台别另写一套
- `status` 转换表只在 `CanTransition`（`active↔expired`）；关窗只写 `renewal_closed_at`、不经 `transition`（`service/state.go` 注释）

## 谁能连、能拉哪些线（与 node 分工）
- 节点名单：`Service.ServingSubscriptions` + `repo.ServingSubscriptions`（生效且未耗尽流量：本期窗口里没有 `remaining <= 0` 的限额行，remaining 含送的量；一次性套餐流量用完后订阅仍 active 但不进名单；账号、套餐绑池、池用户组不在这里判）（`service/serving.go`）；结果带 `PlanID`，node 按套餐取线路（catalog `PlanPools`）
- 订阅里的线路列表：`NodeSource.DeliverableNodes` 由 node 实现（`node/service/node_source.go`），`NodeQuery` 带 `PlanID`（线路池按套餐绑定），subscription 经 `UseNodeSource` 注入；没装配拉取/预览回 `ErrDeliveryUnavailable`（503）（`service/nodes.go`、`service/pull.go`）
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
- 凭据每小时拉取上限是 settings 的 `subscription.fetch_limit_per_hour`（缺省 60、1–10000），拉取经 `SubscriptionDelivery` 每次现读，改了立即生效；后台在订阅附加设置卡改（`GET/POST v1/settings/subscription` 的 `fetch_limit_per_hour`，部分更新）。
- 每个用户手里还算一份的订阅数 `HeldCountsByUser`（`service/user_caps.go`、`repo/user_caps.go`，一条 SQL：`renewal_closed_at IS NULL`，即 `StateLive` 加 `StateRevivable`；能续回的也算一份是用户 10-20 的产品决定）给 order 查每人订阅数上限用。别和按套餐计数的 `LiveSubscriptionCounts`（`service/report_reads.go`，catalog 用）混了。
- 成功/过期提示才占凭据每小时限流；未认证失败按来源 IP HMAC 采样落库（`service/pull.go`、`service/fetchlog.go`）
- 给 identity 溯源总览的读（`service/trace_reads.go`，实现 identity 的 `TraceFetches` 端口，10-20）：`TraceFetchIPs` 按来源 IP、`TraceFetchUsers` 按用户聚合拉取日志，各一条 SQL，走 `subscription_fetch_log_time_idx (fetched_at)`，都先在原表窄聚合（按 IP 的是 (ip_hash, ua_hash, user_id)，按用户的先按 (credential_id, ip_hash, ua_hash, ua_family) 不连表分组、再连凭据并到用户），后面的 CTE 从窄聚合派生，不物化整窗原始行；`TraceFetchUsers` 只补查（Limit = 0）时先取这些用户的凭据、走 `subscription_fetch_log_cred_idx`，共用 IP 再按这些 ip_hash 过滤同窗口别人的拉取，这一步的 CTE `other` 必须 `MATERIALIZED`（只引用一次会被内联进 EXISTS，拉取表没有 ip_hash 索引，每个 IP 全表扫，30 天实测 24 分钟）；按明文补查的 IP 在这里规范写法后用 `InfoHMACSubAudit` 算哈希（与审计盐不同，两路只按明文合并）；排序列只从 repo 白名单取。30 天窗口约等于整张表（保留 31 天）的扫描。10-20 在性能测试机（PG 按生产 drop-in 限 512MB、work_mem 4MB）实测 400 万拉取行、30 天窗口中位数：按 IP 10.3 秒、按用户 6–6.4 秒、只补查 3.4 秒；7 天都在 1.2 秒内、24 小时 0.25 秒内（`ops-local/netcup-perf/trace-explain-20261020/`）。按 Profile-Update-Interval 12 小时，1 万用户约 150 万行；`pandora_app` 语句超时 15 秒，到这个量级前改按日汇总表，不加 ip_hash 索引（拉取是最热的写路径）
- `Profile-Update-Interval`：正常 12 小时，过期 1 小时；续费页 `renewalPageURL`（`service/pull.go`）
- 查询参数：`target`/`flag`/`type` 选格式（`subscribeTarget`）；`types` 按协议过滤、`filter` 按名字关键字过滤（`render.ParseTypes`/`ParseKeywords`，口径照 Xboard）（`handler/subscribe.go`）
- 后台模板、订阅附加设置（信息节点、协议前缀，settings 的 `subscription.info_nodes` 等）与订阅域名分组（本模块的表）在拉取侧走进程内快照，30 秒过期（`service/pull_extras.go`）；附加设置由 settings 在提交后经 `SubscriptionExtrasListener` 回调 `SubscriptionExtrasChanged`（`pullExtras.invalidate`：代号加一并清空，失效前开始的重读读完也不写回），本进程保存或恢复模板、保存订阅域名后同样调 `invalidate()`；快照里记着读库前的代号，读时与当前代号比对（`fresh`），所以即使在「比完代号、写回之前」失效、旧内容被写了回去，也会作废重读。30 秒只是兜底。重读失败时有旧快照就沿用、打 Warn、5 秒后再试，没有旧快照才让拉取失败
- 信息节点的「距下次重置」用 `PullByTokenHash` 里同一行配额顺带取的 `traffic_period_end`（`PullRow.TrafficPeriodEnd`），不另查库；`Subscription-Userinfo` 的总量 = 这一行的上限 + 送的量（`bonus_value`，不限量的行按 0）
- 订阅域名分组（迁移 00048，三张表 `subscription_domain_groups`、`subscription_domains`、`subscription_domain_group_user_groups`，属本模块；用户 10-14 定，取代 settings 的 `subscription.link_base_urls`）：
  - 选组：用户组分到的组 → 缺省组 → 都没有用 `public_base_url`（`service/domain_set.go` 的 `groupFor`、`domainGroupFor`）；只有缺省组时不查用户组
  - `MyLinks`、换链接组内按有效权重随机（`pickWeighted`，crypto/rand），有效权重 = 基础权重（1–100）× 分数；Surfboard 托管配置取组里基础权重最高的、同权重取前面的（`stableBaseURL`、`subscribeURLFor`）；过期提示的续费页仍用 `public_base_url`
  - 分数在 Redis，每个域名一个 hash `subdom:score:<base_url>`（`service/domain_scores.go`），按访客地区分三级 field：全国 `*`、省 `<省>`、省+运营商 `<省>|<运营商>`；value `"<值四位小数>|<unix 毫秒>"`，每次写重设 key 的 TTL 7 天（不用 HEXPIRE）。**全国存绝对分**：6 小时半衰期向 1 衰减、夹在 [0.1, 3]（下限大于 0：任何域名都不会被压成 0；上限防独占）。**省存相对全国的偏移、省|运营商存相对省的偏移**（10-20 审查阻塞 1 返工）：同样 6 小时半衰期、向 0 衰减。**写偏移时按上一级当前的读分夹**（10-20 复审阻塞 1）：在同一段 Lua、同一个 hash 里先把上一级衰减到 now 算出读分 p（上一级过期按 0 的口径不变），旧偏移先夹到 [0.1 − p, 3 − p] 去掉余量，再加这次的加减量、再夹一次；同一次写里先写上一级、下一级按更新后的上一级夹（`Apply` 的 field 按字典序，`*` 在前、省是省|运营商的前缀；探测脚本按从粗到细的顺序写）。Redis 不可用按基础权重，不让生成链接失败。旧的单 hash `subdom:score` 不迁移不兼容读（10-20），靠 TTL 消失
  - 分桶（10-20，某省某运营商失败只降那一桶）：地区来自 ip2region（`internal/pkg/geoip`，经可选端口 `UseGeo` 注入，不进 MissingPorts；没放数据文件时只用全国一级）。只认 `Country == "中国"`、省非空（≤32 字符、不含 `|` `*`）、不是机房/本机/内网的 IP；运营商规整成电信、联通、移动（铁通并入）、教育网、广电、其他 6 类（`service/domain_region.go`）。一次信号的加减量加在它写到的每一级上（地区不可用只写 `*`）：全国加在绝对分上，省、省|运营商加在偏移上。读：`Read` 收访客地区，对组内每个域名一次 pipeline HMGET 三个 field，读分逐级夹：读_省 = clamp(全国分 + 省偏移, 0.1, 3)，读_省|运营商 = clamp(读_省 + 省|运营商偏移, 0.1, 3)（`combineLevels`，与 Lua 的 `level_read` 同口径），再交 `pickWeighted`；全国分没有记录当 1、**不受新鲜度约束**（与分桶前一致，24 小时没写也照常按衰减后的值算）；细分级 24 小时没写（`domainScoreFresh`）或解析不了，偏移当 0——读和写都这样（`apply_delta` 遇到过期偏移按 0 起步，过期的负偏移不会被一次新信号「复活」）。为什么存偏移不存绝对分：存绝对分时稀疏的细分桶从中性分 1 起步、按域名各自回落，一次本地成功能把某域名在当地从全国的 3 拉到约 1.05，有本地数据的输给没数据的，单个凭据还能绕开探测门槛改写当地的分；存偏移后同组域名在任何地区都按全国分同一口径比，细分级只在上面加减。由此钉住的性质（单测见 `domain_scores_test.go`）：任何一次成功信号都不会让该域名在任何地区读到的分数降低；单个凭据一小时内能让某地区读到的分数比上一级多出的不超过一次拉取信号 0.05（拉取只加不减，稳态每级不超过 0.05 / (1 − 0.5^(1/6)) ≈ 0.46）。为什么偏移要按上一级的读分夹（复审阻塞 1）：偏移各自夹 ±2.9、读分最后才夹时，细分级会攒下看不见的余量——拉取多的省把省偏移顶到 +2.9 后，某个运营商封了这个域名，省|运营商偏移压到 −2.9 读分也还是 3；全省被封要 4–5 小时才开始降。按上一级夹之后，同一个省里别的运营商的拉取只能把省一级顶到 3、不留余量，被封那个运营商的失败直接落在读分上（单测 `domain_scores_scenario_test.go`：全国 3、广东联通每小时 50 个凭据拉取、广东电信 5 个账号报失败，广东电信每轮都降、第 2 小时 ≤ 2，广东联通一直是 3；全省被封第 1 小时内就降）。全国分向 1、偏移向 0 用同一个半衰期，「读分 − 1」在两次写之间按同一系数缩，写入时在界内之后就一直在界内；余量只会在别的地区把全国分拉高时短暂出现，读分逐级夹所以看不见，那一级下一次写入就被夹掉。探测的 net 计数记的是请求的变动量，被夹掉的部分不计入分数，封顶只会更紧。生成链接（`linkBase`）按请求 IP 查一次归属地；Telegram /sub 的 IP 是 Telegram 服务器，自然只用全国分。后台 `AdminDomainConfig` 只读 `*`
  - 已知限制（10-20 审查建议 1）：地区按 nginx 写的 X-Real-IP 判。部署只给两种真实 IP 还原：Cloudflare（`update-cloudflare-realip.sh` 写官方回源网段、`real_ip_header CF-Connecting-IP`），以及容器版前面另有反代、由 `PANDORA_REALIP_FROM` 信任且覆写 X-Real-IP 的（`deploy-scripts.md`）。订阅域名放在别的 CDN 或国内中转后面、没按上面配时，记到的是边缘节点的地址：国内 CDN 节点在 ip2region 里的运营商常写成电信、联通而不是机房，会被当成住宅、记进边缘节点所在的省与运营商，分桶失真（只影响细分级，全国一级照常）。代码不处理，部署时按 RUNBOOK 配真实 IP
  - 拉取信号：经**令牌主人所在组**的已登记域名（`hostBaseURL` + `groupHas`，Host 可伪造，别组的不算）到达的成功拉取 +0.05，按拉取者 IP 的地区记三级；同一凭据对同一域名每小时只记一次（进程内去重表，上限 5 万条），归属地在去重通过之后、锁外才查（不在每次拉取上查）；攒批按（域名, 省, 运营商）分桶，冲刷时同一域名各地区的信号在全国一级相加、同省各运营商的在省一级相加，全国加在绝对分上，省与省|运营商加在偏移上（`expandBuckets`）；进程内攒 10 秒一批写，不给拉取加往返。冲刷在下一次拉取或下一次读分数（生成链接、后台查看）时顺带触发，没有定时任务，低流量时会滞后到那时；丢一批只影响增速
  - 探测信号（用户 10-14 重申「成功加权、失败降权、保留随机性」，失败信号是产品要求，只靠拉取给不出失败）：门户「我的套餐」页登录后 `POST /v1/sub-probe/ticket`（按账号每小时 6 次）领票据，隐藏 iframe 打开 `GET /v1/sub-probe/frame?t=`，探测页对本组每个域名 `GET <域名>/v1/sub-probe`（204，5 秒超时），再凭票据 `POST /v1/sub-probe/report`（`service/domain_probe.go`、`handler/probe_frame.go`）。成功 +0.1、失败 −0.3
    - 为何用独立探测页：门户入口页对匿名访客一样，CSP 列订阅域名就公开了只给某些组的域名、能从门户反查订阅域名（审查 10-14 应改 1 方案三）。入口页 CSP 不含任何订阅域名，只多一个 `frame-src 'self'`（`app/portal_csp.go`），入口页也不读库；探测页的 CSP 按请求生成，connect-src 只有 `'self'` 与本人所在组的域名，脚本固定、按 sha256 放行，`frame-ancestors 'self'` 并去掉全局的 `X-Frame-Options`。不许用 `connect-src https:` 之类放宽
    - 票据：HMAC（主密钥 HKDF 用途串 `crypto.InfoHMACSubProbe`），5 分钟，绑账号 id 与所在组指纹（组 id + 组内域名按顺序，`groupFingerprint`）；探测页与上报都复核签名、过期、账号仍有**不是注册试用发的**生效订阅（`repo.HasLiveNonTrialSubscription`：口径同 `StateLive`，再排除 `subscription_trials.subscription_id` 里的订阅；试用订阅续费后 id 不变，仍不算）、当前组指纹一致，任一不符统一 401「探测票据无效或已过期」。没有这样的订阅或组里没域名时票据接口回空串，门户不开 iframe。票据与探测页读配置走 30 秒快照
    - 为何排除试用（审查 10-14 应改 1）：注册试用人人一注册就有、不花钱，若试用也能领票据，开几个号就凑够「3 个不同账号」，防操纵形同虚设；要求非试用订阅，凑人数的成本就是几份付费订阅。拉取信号不受此限（它要真实拉取，且封顶见下）
    - 票据一次性键 `subdom:probe:ticket:<nonce>` 的 TTL 固定为票据有效期 + 1 分钟（`probeTicketKeyTTL`，覆盖验签容许的 60 秒时钟偏差），不按本进程时钟算剩余时间：多进程时钟有偏差时也不会在键过期后、票据还有效时留出重放窗口
    - `/v1/sub-probe`（GET/HEAD）只在请求 Origin 恰好等于门户源（`PortalOrigin`：public_base_url 规整成浏览器的源形状，小写、去掉 https 的 `:443` 与 http 的 `:80`，不是 http(s) 或解析不了为空）时回 ACAO，`Vary: Origin`；CORS 中间件挂在限流前，429 也带 ACAO。探测页把 429 与 5xx 当「不计」，被 CSP 拦下（`securitypolicyviolation` 按 origin 精确比）也不计
    - 探测页自检门户源（审查 10-14 建议 2）：探测页数据块带 `portal_origin`；门户源为空时服务端不列任何域名，脚本发现 `location.origin` 与 `portal_origin` 不一致时不探也不报。原因：public_base_url 配错或经别的主机名打开门户时，订阅域名的 CORS 只认门户源，探出来全是「失败」，报上去会冤枉所有域名
    - 防操纵（应改 3，`service/domain_probe_scores.go`，一次上报一段 Lua）：票据一次性（`subdom:probe:ticket:<nonce>`）；账号×域名每 30 分钟最多记一次，成败都算（`subdom:probe:seen:`）；成功与失败对称：同一小时桶里全国一级 ≥3 个、省与省|运营商两级 ≥5 个（`probeMinReportersRegional`，10-20 审查应改 1：小桶里能抵消的真实拉取少，细分级门槛更高）不同账号报同一域名成功才加分、报失败才扣分，凑够的那一个起生效（`subdom:probe:ok:` / `subdom:probe:fail:` 两个上报人集合，固定小时桶）——成功也要多人，是为了防单个账号长期把某域名顶高、把用户引向被监控的域名（主 Agent 10-14 定）；每个域名每小时每一级探测净变动封顶 ±0.5（`subdom:probe:net:`）；只收票据所绑组里的域名。地区取上报请求的 IP（上报者的地区，`ReportProbeResults` 从 `reqmeta` 取），一次上报对三级同时生效：seen 仍按账号×域名，ok/fail/net 按级别分开计（键 `subdom:probe:ok:<base>|<field>|<小时桶>` 等，`probeLevelName`），封顶三级都是 ±0.5/h，门槛全国 3、细分级 5（10-20 定：不因分桶放宽防操纵）。细分级加减在偏移上，所以细分级的封顶就是「相对上一级每小时最多偏 0.5」；三级都凑够人数时，某省某运营商的访客读到的分数一小时最多动 ±1.5（全国、省、省|运营商各 0.5）。拉取信号不受这些限制，口径不变（它是真实流量，已按令牌主人所在组、同一凭据每小时一次计）
    - 残余风险的量级（审查 10-14 建议 1，接受的前提）：同组 3 个持非试用生效订阅的账号串通，每人每 30 分钟报一次，每小时最多 4 次生效——报失败可把一个域名一直压在下限 0.1（−0.5/h 封顶，远大于向 1 回归的约 0.1/h），报成功（+0.4/h）可把它顶在上限 3；拉取信号的平衡点约为 1 + 0.43k（k 为每小时经该域名拉取的不同凭据数，试用凭据也算），约 5 个凭据就能顶到 3。影响只限这一组的域名、只改相对权重：分数夹在 [0.1, 3]，被压的域名仍按 0.1 倍权重出现，被顶的域名在两个等权域名的组里最多分到 3/4，不会独占也不会被删；真实用户的成功/失败上报与拉取会抵消一部分。要更强需要按付费额或账龄加权，属产品取舍，现在不做。分桶后（10-20 审查应改 1 按真实量级重写）：细分级的抵消力量远小于全国——同地区真实拉取每个凭据每小时只给那里的偏移 +0.05，小省小运营商每小时可能只有几个凭据，压不过 −0.5/h 的探测封顶，所以细分级门槛提到 5 个账号。残余风险：同组 5 个持非试用生效订阅的账号串通，用目标省、目标运营商的住宅 IP 持续报失败，几个小时能把某域名在那个省|运营商（及那个省）压到下限 0.1；这 5 个账号同时也满足全国一级的门槛，全国一级的影响与改前 3 个账号的情形相同（−0.5/h 封顶、被全国拉取抵消），不额外波及别的省与别的运营商。报成功同理可把某域名在当地顶到 3。拉取一侧（10-20 复审建议 1）：拉取不受探测门槛约束，而且对当地访客三级都加，单个凭据稳态下每级约 +0.46（0.05 / (1 − 0.5^(1/6))），省|运营商访客累计比全国高约 0.92；在一个安静的省|运营商里约 2 个凭据就能把某域名在当地一直顶在 3（全国一级约 5 个）。这和「拉取是真实流量、不设门槛」的既定口径一致，属于接受的风险：只能加分、不能压分，影响只在那个省、那个运营商，且要真的经该域名拉到订阅
  - 地址口径与 nginx 一致（`NormalizeBaseURL`）：只收 https + DNS 域名（至少两段、末段字母开头、≤253），不收 IP、localhost 与端口（`:443` 自动去掉）；库里 `subscription_domains.base_url` 的 CHECK 与它的输出同形（`https://` + 同一条域名正则、≤255，不带端口），改一边要同步另一边与 `tests/invariants.sql` 的 XB-SUBDOMAIN 段
  - 读整份配置（拉取快照、后台查看）时三条 SELECT 在同一个只读可重复读事务里：事务边界在 service（`service/domain_set.go` 的 `domainConfigRows`，`tx.Do` 里第一句 `repo.SetSnapshotReadOnly`），repo 的 `DomainConfig` 只读、不自己开事务；已在事务里（后台保存）直接用当前事务
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
- 两类套餐（catalog `Plan.Kind`）：流量落在哪一行一律按套餐类型判断（`trafficPeriod`：订阅 → cycle，一次性 → total），开通、换套餐、叠加、续费重建配额前先 `withKind` 把版本里跟着套餐走的那条流量定义（cycle 或 total）的 period 改对，不信版本里存的 period；day / month 是独立滚动窗口，原样保留（都改成 cycle 会折成多条同周期定义、建配额撞唯一键）（`service/internal.go`）
- 期末 `PeriodEndFor`：订阅按周期；一次性有天数 = 起点 + N 天，永久 = 远期哨兵（`IsPermanent` 判断，50 年以上）（`service/period.go`）
- 续费 `Renew`：只许订阅套餐（一次性回 `ErrRenewOneTime`）；提前续接 `RenewalBase`，版本与配额不动，新周期开始时由配额滚动切到套餐当前版本；过期恢复当场切到当前版本并 `rebuildQuotas`（清已用与送的量）（`service/renew.go`、`service/plan_change.go`）
- 叠加 `Stack`：只许一次性套餐、同一个套餐（版本须属于它）；total 行上限加上新版本的流量（饱和加，新版本不限就置不限），已用与送的量不动、不写重置日志；期末 `StackEnd`（原来永久就不变、新价永久则永久、否则 max(原到期, now) + N 天）；版本切到下单时的当前版本；已过期没停用的恢复成 active；事件 `renewed`（payload `mode: stack`）（`service/stack.go`）
- 换套餐 `ChangePlan` 可以跨类型，周期从现在起按新价重开，`rebuildQuotas` 按新版本重建（`service/plan_change.go`）
- 延期 `Extend`：救回折算 cycle 上限、走过到期日重对齐 day/month（`service/extend.go`）
- 开通 `Provision`：按套餐类型建 cycle 或 total 流量行（`service/provision.go`）；注册试用 `GrantSignupTrial` 与它共用 `provisionAt`（指定起止），不建订单、不计佣，每人终身一次（`subscription_trials` 主键，只追加），套餐不可用时记日志跳过（`service/trial.go`）
- 注销账号 `CloseUserSubscriptions`：identity 在注销事务里调，生效中的改 expired（到期截到现在）、关续费窗口、吊销全部凭据，提交后通知名单（`service/close.go`）
- 后台直接改：`AdminSetPeriodEnd` 只改生效中的到期时刻（晚于现在、10 年内，不折算流量），`AdminSetQuotaLimit` 按「指标 + 周期」改一行当期上限（0–10 TiB 或不限，下个周期滚动按套餐当前版本重设）；都要原因，各写一条订阅事件（`period_end_set` / `quota_limit_set`，状态不变）与审计，并推实时变更（`service/admin_edit.go`）
- 后台改订阅（改到期、改上限、加时长、加流量、重置流量、改设备数、换链接）一律经 `lockForAdmin` / `rejectDeletedOwner`：先不加锁读出属主，经 identity 的 `IsDeleted`（给用户行加共享锁）拒已注销账号 409，再锁订阅行（`service/internal.go`）。给 order/giftcard 的写方法（Provision、Renew、Extend 等）不在这里判断：注销时待付订单已在同一事务里取消，order、giftcard 在各自事务开头判断。

## 送流量
- `GrantBonus`（`service/bonus.go`）：礼品卡、后台「加流量」（`AdminGrantTraffic`，同一事务、审计 `traffic_granted`）、盲盒奖品共用，加到流量配额行的 `bonus_value`（饱和加）：订阅套餐加在本周期 cycle 行（滚动时清零，当期有效），一次性加在 total 行（一直有效）；只许 `StateLive`（否则 `ErrNotActive`），没有限额流量行回 `ErrUnlimited`；不写事件与审计（调用方写），提交后通知名单
- `remaining`（生成列）= `limit_value + bonus_value − consumed`：扣量、名单、拉取总量、流量提醒都按它；换套餐与过期恢复重建配额时清零

## 流量重置
- 付费重置次数 `ResetsUsed`（`service/resets_used.go`）：数 `traffic_reset_logs` 里 reason = order 的不同订单号，订阅套餐从本周期 cycle 行起点、一次性从 total 行起点起数；order 建重置单时与 `Plan.ResetLimit` 比，摘要里也带 `ResetsUsed`、`ResetLimit`、`ResetPrice`
- 后台手动、礼品卡、用户购买（xb-order #32）共用 `ResetTraffic`（`service/reset.go`）：只清生效中订阅全部流量配额行的已用量，每行一条 `traffic_reset_logs`，提交后通知名单；原因 `manual`（ActorID 必填）、`gift_card`、`order`（`ResetOrder`，UserID 必填校验归属，备注写订单号，由 order 履约流量重置单时调）。`TrafficReset.At` 非零时按它判生效（order 结算传结算时刻），零值用自己的时钟

## 配额滚动
- `RollQuotas`：第一遍后 `QuotaRollsRemain`，有则隔 `rollQuotaRetryDelay` 再滚一遍（`service/quota_roll.go`）
- 滚动清零 `consumed` 与 `bonus_value`；订阅套餐的 cycle 行滚进新周期时把订阅的 `plan_version_id` 切到套餐当前版本（`SwitchPlanVersions`，只改仍是旧版本的行），上限按新版本写；版本里没有这一行的定义就不动上限（一次性套餐遗留的 cycle 行照滚、不切版本）
- 周期起止只在 `period.go` 算，repo 只筛到期行

## 锁序
- 给 order/giftcard 的写方法在 `s.tx.Do` 内自己 `lockOwned`（FOR NO KEY UPDATE）；调用方先锁订单再调 subscription（`service/service.go` 包注释）
- 用户行与订阅行同在一个事务里时，一律先用户行、后订阅行：identity 先 `LockUser`（FOR NO KEY UPDATE）再调 `CloseUserSubscriptions`（注销）、`GrantSignupTrial`（注册）；subscription 自己从不显式锁 users 行（后台写入口经 `IsDeleted` 加的共享锁也排在锁订阅行之前），同一用户的多份订阅按 id 升序 `lockOwned`（`close.go`）。
