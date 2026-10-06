# panel/tools/loadtest/
> L2 | 父级: /panel/CLAUDE.md（tools/ 一行）

面板压测工具链：给 Vultr 上的资源占用实测提供「测得出、测得准」的造数、负载与采集，本身不做任何调优
  - main 包只经 `go run ./tools/loadtest`（或 go build 后拷到压测机）使用，不进发布包、不被任何包 import
  - 四个子命令 seed / nodes / users / burst 之间只经 ltkit 的造数清单（Manifest）交换数据，计量统一走 ltkit.Recorder，三类场景的 QPS、分位数与错误码同一口径
  - 签名规范串、配置验签、线格式一律直接引用 domain/nodefabric 与 platform/crypto（同一 module），不再抄一份；pdnd 是另一个 module，引它要在 panel 的 go.mod 加 replace，故不引
  - 模拟用户的来源 IP 全在 198.18.0.0/15（RFC 2544），轮流分到 512 个 /24，经 X-Real-IP 带给面板；邮箱只用 @loadtest.invalid，节点与目录名以 loadtest- 开头
  - CI：panel-smoke.yml 在冒烟栈上跑一次 200 用户、5 节点、60 秒的试跑（零 5xx、零签名失败），证明工具对着真网关是对的
  - 用法与 Vultr 压测流程见 README.md

成员清单
main.go: 子命令分发 seed|nodes|users|burst
README.md: Runbook（按总协调定案定稿）：开机 → install.sh 生产模式装面板 → 观测开关（pprof、pg_stat_statements、nginx 真实 IP 顶替，压测机直连源站）→ 每档重装数据基座、造数、真 pdnd 重新接入 → 空载 / 5k / 10k / 15k 各两次 30 分钟稳态加 burst → 15k 档 24 小时（订阅余量版 30m / 贴近真实版 6h）→ 四条及格线与 15k 必报的 nr_throttled 增量 → 撞上限才补放开上限的对照轮 → 删机；地址全是占位符
ltkit/: 共享底座
  - manifest.go 造数清单：seed 写、其余读，含节点私钥与共用口令（虚构，0600 落盘）；另有订阅前缀、池与套餐版本、分阶段造数耗时，用户带订阅 id 与 node_uid，节点带名字与服务器
  - stats.go 计量：对数分桶直方图（2% 精度、常数内存，24 小时也不涨）出 QPS、p50/p95/p99、错误码（无响应归 transport:*）、自定义标签、按窗口时间线；Stop 冻结分母免得收尾排空摊薄 QPS；写 <场景>.json 与 .txt 一页摘要
seed/: 造数：退役旧批次 → 池与套餐草稿 → 服务器 → 节点与接入令牌 → 两段式接入 → 一步上线 → 发布套餐 → 用户与订阅 → 核对，写 manifest 与分阶段耗时
  - 节点与目录全走真实网关（admin.go 按后台每 IP 240/分节流、429 退避、reauth_required 自动重认证；enroll.go 本地生成 Ed25519 与运行令牌；nodes.go 照冒烟 seed.ts 的顺序）
  - users.go 用 unnest 多行 INSERT 按批一事务，经运行角色与租户上下文让 RLS 与触发器真起作用，镜像 adminops.GenerateUsers 与 billing 的开通（pending 经状态机转 active、开通事件、配额、哈希凭据），口令只哈希一次
  - retire.go 只圈 loadtest- 名字与 @loadtest.invalid 用户：节点经后台批量接口退役，订阅经状态机转 expired（追加写表连着它们，删不掉）
  - verify.go 以节点身份核对签名 effective-config、UniProxy config，以及用户列表恰为本批用户；naming.go（命名与 /24 轮转的地址分配）、options.go（flag → LOADTEST_* → SMOKE_* → AEGIS_*）为纯函数
  - *_test.go 覆盖纯函数、批量 SQL 构造、对照 api/node 验签口径的假网关、后台客户端重试
nodesim/: nodes 子命令：M 个模拟 pdnd 对着 node 网关跑，请求序列与节拍逐段对齐 pdnd/node/node.go 与 pdnd/panel/*，内核换成虚构负载
  - nodesim.go 入口与编排：取清单前 N 个节点、-stagger 窗口内纳秒级随机起跑、每 -progress 一行进度、到时或 SIGINT/SIGTERM 收尾；-strict 判 5xx、签名或令牌 401、配置验签失败、迟迟没拿到配置的节点
  - node.go 单节点循环：先 syncOnce 再报状态再挂流；pull/push 按下发 base_config 重置 ticker，status 30 秒写死；签名配置每轮重放 switched、稳定 5 秒后报 health_passed；sync.config 当信号回头拉、sync.users 设版本换 304、增量基准不符改拉全量
  - signed.go 签名通道：每次拉配置前先问 config-signing-key；规范串调 nodefabric.CanonicalPayloadV2、验签调 VerifyEffectiveReleaseSignature / VerifyConfigSignature
  - uniproxy.go UniProxy 与 SSE：ETag 只在解析成功后记；流只记建连延迟，断开 1→30 秒指数退避加抖动且不归位（同 pdnd）
  - workload.go 虚构负载：在线用户按 uid 哈希落到唯一一个模拟节点，上报的在线 IP 按 node_uid 取该用户在清单里的固定地址，主机指标落在面板校验范围内
  - fleet.go 计量接线：请求进 Recorder，起跑 / 起来 / 流事件 / 验签失败等整机计数进 meta
  - *_test.go：fakegw_test.go 用面板原语搭的假网关（验签同 requireNodeSignature，配置由真 BuildNodeConfig + SignEffectiveRelease 产出，心跳严格解码）；nodesim_test.go 覆盖签名全验过、外来公钥拒收、无身份退兼容通道、ETag/304、流事件、节拍重置、起跑错开、-strict
userload/: 用户侧流量（users）与全量重拉触发（burst）
  - userload.go users 入口：四类速率逐类可配（订阅、门户读、后台读、重新登录；订阅也可用 -sub-interval 按「每人多久一次」给，三档自动折算），活跃池等间距挑选，订阅前缀与订阅 id 取自清单，写 users.json/.txt
  - warmup.go 计时前的预热：活跃池定速登录一次复用令牌（Argon2 不进正式窗口），计量写 users-warmup.json
  - traffic.go 流量内容：订阅客户端 UA 表（逐条对应 subscription.DetectFormat 的 clash / sing-box / URI 分支，按响应类型复核）、门户与后台读接口表（注明前端调用处）
  - sched.go 开环定速调度（不因响应慢降速，测得出排队）、在途上限（满则丢拍计数）、每分钟进度行
  - preflight.go 按目标速率预估面板各限流维度（每 IP、每 /24、每账号、订阅凭据每小时、登录、后台每 IP）并告警
  - client.go HTTP 出口：模拟来源 IP 落到 X-Real-IP（可加 CF-Connecting-IP）、端点名规范化（不出现令牌、邮箱、id、前缀）、429 按来源打标、登录与 reauth
  - burst.go burst 子命令：经后台来回切换一个用户的设备数覆盖（改用户集合版本）与用户组（触发租户级 node.users.changed），写 burst.json 与 burst-http.json/.txt
  - *_test.go：httptest 假网关上的 UA 分支、固定来源 IP、令牌复用、开环速率、端点名、429 来源、-strict、清单字段直用、按 seed 地址规划的四档限流预估、burst 请求形状与 reauth 重放
scripts/: 压测期在面板主机上以 root 跑的采集脚本，scp 过去即用，不进发布包（放这里不放 deploy：deploy 是随产品分发的安装链，这些是压测期诊断工具）
  - 兼容 install.sh 的 Docker 数据基座（/opt/aegispanel，容器 aegis-postgres/aegis-valkey）与 install-native.sh 的直装布局；只读解析 .env、不 source，口令只经 PGPASSWORD/REDISCLI_AUTH 传给子进程
  - lt-common.sh 被 source 的公共段：找 .env、判定数据基座、超级用户 psql、带口令 valkey-cli、重启 PG
  - pgstat.sh pg_stat_statements 开启（ALTER SYSTEM + 重启 + CREATE EXTENSION）、清零、导出 top N 三份 CSV（总耗时、平均耗时、调用次数）、撤销
  - sample-procs.sh 三网关、postgres、valkey、nginx 与整机的 CPU 与 RSS/PSS 定时采样成 CSV
  - sample-cgroup.sh 三网关 systemd 单元的 cgroup v2 cpu.stat（nr_throttled）与 memory.current/max/events 定时采样成 CSV：判「撞 CPUQuota / MemoryMax」与 15k 档必报的节流增量
  - snapshot-mem.sh PostgreSQL 内存参数、共享内存、连接与库计数，Valkey INFO memory/stats/clients 快照，压测前后各一次做差
  - grab-pprof.sh 从三网关的回环 pprof 端口并行抓 CPU profile，再取 heap/allocs/goroutine
  - nginx-loadtest-realip.conf / nginx-realip.sh 压测期间顶替 cloudflare-realip.conf（须已由 deploy/render-nginx.sh 生成），只对压测机采信 X-Real-IP；备份、nginx -t 失败回滚、disable 还原；随包 nginx-aegis.conf 不变

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
