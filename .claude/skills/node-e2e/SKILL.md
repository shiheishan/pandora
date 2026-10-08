---
name: node-e2e
description: pandora 真节点逐协议验证：在两台 1c1g 测试机上（一台跑 pdnd/NativeCore 并接入复测面板，一台跑 sing-box / mihomo / xray / juicity / mieru 真客户端），按面板下发的订阅（clash / sing-box / uri 三种格式）逐个协议测 TCP 与 UDP，另含 Hysteria2/TUIC 多 UDP 会话出口端口稳定、删人断线、整份 sing-box 订阅原样（TUN）加载三项专项。改了 pdnd kernel 的协议实现或传输层（vless/vmess/trojan/ss/hy2/tuic/anytls/juicity/naive/mieru/shadowtls/xhttp/REALITY/Vision）、订阅渲染、协议 schema、节点配置下发路径，或发版前要确认"用户拿到的订阅在真机真网络上能用"时使用。比 subscription-e2e 多的是：真网络、真证书校验、多个独立客户端实现。
---

# 真节点逐协议验证（node-e2e）

目标：用户从面板拿到的订阅，在真机、真网络、真客户端上能用。subscription-e2e 在本机回环上用 sing-box 一个实现验过一遍；这里补它测不到的三件事：

1. 真网络与真证书校验：节点证书由测试 CA 签、客户端系统信任该 CA，不靠 insecure 兜底。
2. 多个独立客户端实现互相对照：sing-box、mihomo、xray、juicity 官方、mieru 官方。pdnd 自己的互操作测试大多对着 sing-box 系库跑，Xray 才是 VMess / Vision / XHTTP 的参考实现，这次三个缺陷全是在 Xray 或"真 TLS1.3 内层流量"下才露的。
3. pdnd 走面板真实下发路径（签名配置 + 用户名单轮询/SSE）起入站，而不是测试里直接 new 适配器。

产出：`协议 × 客户端 × TCP/UDP` 矩阵、UDP 多会话结论、删人断线结论、整份订阅加载结论，写在 `ops-local/<轮次>/node-e2e/`（含真实 IP 与令牌，不进仓库）。2026-10-08 那一轮的结果在 `ops-local/vultr-test2/node-e2e/summary.md`。

## 红线（继承机器工位 AGENTS.md）

- 两台都是一次性测试机：不删、不重装、不改套餐；不改 `authorized_keys`、`sshd_config`，不生成 SSH 密钥，不读私钥；不读 `ops-local/**/secrets/`。
- 协议端口只用 20000–20099（tcp+udp）。客户端机本来只放行 22，要放回显/持续流服务就放行同一段（`ufw allow 20000:20099/udp|tcp`），收尾写进 `/root/README.md`。
- 面板机不升级、不重启网关、不动 PG 以外的配置；只在后台建服务器/节点池/套餐/节点/测试用户。直接改 PG 数据只限测试用户的数据，用完还原并写进面板机 README。
- 仓库公开：IP、域名、后台前缀、令牌、订阅链接只放 `ops-local/` 和机器上；报告里用别名。后台口令与前缀只从 `ops-local/<轮次>/admin-cred.txt`、`admin-path.txt` 读，不进命令行、不打印。
- ssh 经 1Password SSH agent，agent 偶发 `signing failed ... communication with agent failed`（退出码 255）。脚本一律走 `scripts/sshx` / `scpx`（ControlMaster 复用连接 + 失败重试）。子 agent 的沙箱连不上，要关沙箱。

## 机器分工

| 机器 | 角色 | 装什么 |
|---|---|---|
| node3（服务端） | pdnd（NativeCore）服务，30 个节点同一进程 | 待测 pdnd 二进制、测试 CA 与服务端证书、sysctl（面板安装脚本的 pandora_tune_sysctl 写入） |
| node4（客户端机） | 真客户端 + 回显/持续流目标 | sing-box（glibc 版，含 naive）、mihomo、xray、juicity-client、mieru；UDP 回显与 TCP 持续流服务 |
| panel2（面板） | 只做后台操作与订阅下发 | 已装好的复测面板，不动 |

两台 1c1g 够用：30 个节点同进程空闲时 pdnd RSS 约 32 MB。

## 脚本位置

全在 `ops-local/<轮次>/node-e2e/scripts/`（下文 `$S`）；`$R` 指 `ops-local/<轮次>/node-e2e/raw/`。`adm.py` 以 `scripts/../..`（即轮次目录）为基准读 `env.sh`（需要 `PANEL_DOMAIN=`）、`admin-cred.txt`、`admin-path.txt`，所以 `node-e2e/` 要放在轮次目录下。哪些脚本升到仓库由总协调定：与现场无关、值得固化的是 `nodes.py`、`client-matrix.py`、`probe.py`、`udpfix.py`、`holders.py`、`kick-db*.py`、`aggregate.py`、`analyze-udpfix.py`；其余带现场路径的留在 ops-local。

## 步骤

### 0. 构建 pdnd、装客户端、造证书

1. 本机构建待测 pdnd（与发布脚本同参数，可交叉编译）：
   `cd pdnd && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath -ldflags "-s -w -X main.buildVersion=<标签>" -o $S/../bin/pandora-native-linux-amd64 .`
2. node4 装客户端：`$S/sshx <node4别名> 'bash -s' < $S/install-clients.sh`。官方发布物，脚本里写死了 GitHub 公布的 SHA-256，升版本要同步改。sing-box 必须用 `-glibc` 包（含 `with_naive_outbound` 与 libcronet），默认包与 musl 包没有 naive。
3. 测试 CA 与服务端证书：`$S/sshx <node3别名> 'bash -s' -- <域名> <IP> < $S/node3-certs.sh`，把 `ca.crt` 拷到 node4 的 `/usr/local/share/ca-certificates/` 并 `update-ca-certificates`。域名用 `<ip 用横线>.sslip.io`（无需自己的域名），证书 SAN 同时写域名和 IP。

### 1. 面板侧建节点与用户，节点接入

1. `python3 $S/panel-setup.py <域名> <IP>`：建 server、节点池、套餐（草稿）和 `nodes.py` 里列的全部节点（`protocol_config` 用后台表单的形状），写 `$R/manifest.json`。加协议或改配置只改 `nodes.py`；第二批追加节点用 `nodes.extra()` + `panel-add-nodes.py`。
2. `python3 $S/issue-tokens.py`：给每个节点签一次性接入令牌（120 分钟），写 `$R/tokens.json`（0600）。
3. 节点接入（node3）：把 `node3-enroll.sh` 与 `manifest.json` 传到 node3，`$S/sshx <node3> 'bash /root/node-e2e/enroll.sh <面板https地址> /root/node-e2e/manifest.json' < $R/tokens.json`。
   - **接入证据必须由面板分发的官方 pdnd 来做**：面板 `release-artifact.env` 钉了官方包的 SHA-256 与版本，`enrollment commit` 拒绝别的二进制。脚本用官方包做 begin / commit / verify-identity，服务二进制再换成待测构建，这也是"升级二进制不重新接入"的真实路径。
   - 多节点同进程：`config.json` 的 `nodes[]` 每项一个 node_id / token / identity_path，身份文件一节点一份。
   - 换服务二进制用 `node3-swap.sh`（`official | new | 绝对路径`），它会重启并等入站就绪。
4. `python3 $S/panel-activate.py`：激活全部节点，**再**发布套餐（顺序同 loadtest seed；节点没激活时 pdnd 拉名单 401）。
5. `JOB=... python3 $S/user-setup.py <个数> <前缀>`：后台批量生成用户 -> 赠送开单 -> 门户登录取订阅链接，追加写 `$R/users.json`（0600）。脚本中途失败时用 `JOB=<任务id>` 复用已生成的任务，免得多造用户。
   - **订阅每凭据每小时限 60 次**（超了回 429 `too many requests`，且 `fetch-subs.sh` 只在 200 时才覆盖本地快照）。所以至少建三类用户：拉订阅专用（`user3`）、断线对照（`user4`）、断线被处理（`user5`），别拿同一个用户反复拉。
6. 订阅链接拷到 node4 的 `/root/node-e2e/sub-<用户>.url`（0600），`bash fetch-subs.sh user3` 按 clash.meta / sing-box / v2rayN 三种 UA 各拉一份到 `subs/`。节点配置变了（换 REALITY dest、改节点）要重拉。

### 2. 逐节点矩阵

`$S/sshx <node4> 'bash /root/node-e2e/matrix-all.sh <标签>'`（约 8 分钟），产出 `results-<标签>/matrix-*.jsonl` 与 `logs/`（客户端日志原样）；拉回后 `python3 $S/aggregate.py results-<标签> > table.md`。

- sing-box：按 tag 从订阅 JSON 取出站（含 shadowtls 的 detour 配对），自建 mixed 入站；`sing-box check` 不过就记"起不来"。订阅标 `network:tcp` 的（ss 系、shadowtls、naive）UDP 记 n/a。
- mihomo：整份 Clash 订阅原样加载（只改 mixed-port 与控制端口），控制 API 切"节点选择"逐个测；另记整份 `-t` 校验。
- xray：由订阅 URI 转配置（vless / vmess / trojan / ss）；xhttp、mkcp 只有 xray 与 mihomo 能测。
- juicity 官方客户端由 URI 转配置；mieru 官方客户端由 Clash 条目转配置（`mieru-official.py`）。
- 每个用例：curl 经本地 socks 拉 `https://www.cloudflare.com/cdn-cgi/trace` 两次（`ip=` 必须是 node3 的 IP，证明真经代理出站；记首个请求的 time_starttransfer），再经 SOCKS5 UDP ASSOCIATE 向 8.8.8.8 发 3 次 DNS 查询（记 rtt）。

### 3. UDP 多会话修复验证（Hysteria2 / TUIC，对应 c8b3871）

问题：一个客户端开多个 UDP 会话，其中一个会话关闭（服务端空闲超时）后，另一个会话的上游端口不得变。

- 用 `hy2-udp15` / `tuic-udp15` 两个节点：`udp_timeout: "15s"`，**写成字符串**（数字会撞 json.Number，见"已知问题"），让服务端空闲回收足够频繁。
- node4 起 `udp-echo.py 20050 <log>`（记每个包的来源 ip:端口）；`udpfix.py` 用 sing-box 真客户端开 B（长活，每秒 1 包）和每 5 秒一个新的 A（奇数个由客户端关控制连接，偶数个发两包后闲置到服务端 15 秒超时），跑 150 秒；node3 同时每 2 秒记 `ss -uanp`（pdnd 的上游 UDP socket）。
- 一条命令：`bash $S/udpfix-run.sh new <标签> 150 hy2-udp15,tuic-udp15`；对照"修复前"：`bash $S/udpfix-run.sh official <标签> 150 ...`（官方包比待测版本旧时才有意义；也可传旧构建的绝对路径）。
- 判定（`analyze-udpfix.py` 自动出）：回显端看到 B 的来源端口集合应当只有 1 个；修复前会在某个 A 过期后多出新端口，同时上游 socket 数涨到 10 左右（修复后 3-4）。
- 采样器的 pid 写文件再 kill，不要在远端用 `pkill -f '含自己命令行的模式'`（会杀掉自己的 ssh 会话，`sshx` 又重试，表现就是卡住）。

### 4. 删人断线

后台没有"删除用户"接口，所以分三种验证，口径同 kernel 的 `TestDelUsersKicksLiveConnections`（被删用户的长连接 1 秒内断、其他用户不受影响、被删用户重连被拒）：

- `python3 $S/kick-run.py ban 75`：封禁用户。结果应当是节点名单不变、连接不断（2026-10-08 实测就是这样，见"已知问题"）。
- `python3 $S/kick-run.py rotate 75`：换发订阅链接。panel2 若是 bca0245 之前的版本，节点密码不变，连接不断；之后的版本应当断。
- `python3 $S/kick-db.py 90`：把被处理用户订阅的 `current_period_end` 直接在 PG 里改到过去（与"到期"等价，名单按 `current_period_end > now()` 过滤），测完还原。再 `python3 $S/kick-db-analyze.py` 出"PG 提交 -> 各节点应用移除 -> 连接断开"表。需要后台把订阅状态翻回来时用"加时长"（扫描器会把状态翻成 expired）。
- 三者共用 `holders.py`：user4（对照）与 user5（被处理）各起一组长连接，每个节点一条长 TCP（读 node4 上 `tcp-stream.py 20051` 的持续流）加一条长 UDP（每 100 ms 一包到 `udp-echo.py 20050`）。管理员动作在 **node4 上**用本机拿到的短期令牌（经 stdin，不落盘）执行，所有时间戳取 node4 时钟；三台机器 chrony 偏差在 0.1 ms 量级。
- 回显/持续流目标必须是公网地址：pdnd 默认拒绝回环与内网目标，用 node4 自己的公网 IP。

### 5. 整份订阅原样加载（sing-box TUN，在网络命名空间里）

sing-box 订阅带 TUN 入站、DoH 分流、远程规则集（经"节点选择"下载）、cache_file。真客户端第一次启动规则集经代理下载，选到的节点不通整份就起不来；subscription-e2e 测不到这条。

- `bash netns-up.sh`（veth + NAT 的隔离命名空间 `ne2e`，宿主路由与 ssh 不受影响；临时改 ip_forward、iptables FORWARD、nft 表，`netns-down.sh` 全部还原）。
- `bash asis-run.sh <订阅json> 60`：在命名空间里原样（一个字节不改）起 sing-box，记启动是否成功与出口探测；失败时看 `asis.log` 的 FATAL。
- 二分用 `asis-variant.py`（删指定出站）和 `asis-variant-v4.py`（去掉 TUN 的 IPv6 地址），`asis-diag.sh` 起 info 级日志的变体并 curl 几个站点。
- mihomo 的整份订阅在矩阵里已经原样加载过。

### 6. 收尾

- 把被处理用户的订阅恢复（后台"加时长 1 天"），测试节点与用户按面板机 `/root/README.md` 的约定留着；删机由用户在控制台做。
- 三台机器的 `/root/README.md` 各补一段：pdnd 是否继续跑、服务名 `pandora-native`、配置与日志位置、证书目录、临时 ufw 规则、测试 CA 是否在系统信任库、遗留的样例节点。停掉回显/持续流服务，消费掉的一次性令牌文件删掉。
- 还原试验改动：REALITY dest、node3 的 debug 日志级别与 `pandora-native.service.d/` drop-in、`ss.pid` 之类。

## 坑

- **node 接入证据钉官方包摘要**：见步骤 1.3，不要试图让面板接受 dev 构建（那要改面板 `.env`、重启网关）。
- **后台表单能存、节点不一定能起**：数字字段在签名下发路径上是 json.Number，hy2 / tuic 的校验不认；测试配置里时间写字符串（`"15s"`）。每个协议都要真起一遍，面板上看 `effective_state` / `delivery_degraded`，门户 `/nodes` 的数量也会少。
- **REALITY dest 别用 www.microsoft.com / www.cloudflare.com / dl.google.com**；apple、mozilla、lovelive 可用。`reality-dest-scan.sh` 逐个换 dest 看客户端通不通（每次要重拉订阅，注意限流）。
- **Xray 26 删了 allowInsecure**：订阅 URI 里的 `allowInsecure=1` 对 Xray 内核客户端无效，带它的节点（trojan-tls-ws）起不来；harness 里 xray 不传 allowInsecure，靠测试 CA 系统信任。
- **一个 SOCKS5 UDP ASSOCIATE 就是一个出站会话**：sing-box 的 hy2 / tuic 出站每个 ASSOCIATE 新开一个会话 ID，多会话测试靠这个。
- 等入站就绪以 `journalctl -u pandora-native` 里"入站已就绪"条数为准，别数监听端口（hy2/tuic 是 UDP，REALITY 入站要等 dest 探测）。
- `mieru start` 会拉起常驻进程，子进程别用 pipe 接输出（会等不到 EOF 卡死），写文件。
- 远端 `pkill -f` 的模式不能出现在自己的命令行里（`sshx ... 'pkill -f foo'` 的 bash -c 命令行本身含 foo，会误杀自己的 ssh 会话）。
- 订阅链接只在 200 时覆盖快照；429 时保留上一份好的。

## 已知问题（2026-10-08 主线 c8b3871 实测，详见 summary.md；修了就划掉并更新）

| # | 现象 | 根因 | 复现 |
|---|---|---|---|
| 1 | VLESS+REALITY+Vision：内层 TLS1.3 流量 bad record mac（sing-box、mihomo、xray 都是；明文 HTTP、TLS1.2、UDP 正常） | `kernel/vision.go` 发 command=2（直通）后仍经 REALITY 外层加密读写，客户端已切裸转发。Xray 做法见 `proxy.CopyRawConnIfExist` / `XtlsRead`；REALITY 连接有 `NetConn()` | 矩阵里 vless-reality-vision 行；`curl --socks5-hostname` 拉任意 https |
| 2 | VMess 在 Xray 内核客户端上不通 | 响应头首字节写死 0（应回显请求头 V 字节 `header[33]`）；不支持 GlobalPadding(0x08)，Xray 对 aes-128-gcm / chacha / auto 默认置位 | 矩阵里 vmess-* × xray；xray 日志 `unexpected response header. Expecting N but actually 0` |
| 3 | XHTTP `mode=auto`（节点默认）下 Xray / mihomo 连不上 | `kernel/xhttp_server.go`：`allowStreamOne := Mode == XHTTPStreamOne`，auto 下 Xray 选的 stream-one 被判 400 | 矩阵里 vless-reality-xhttp；节点改 stream-one 即通 |
| 4 | hy2 `bandwidth.up/down`、tuic `heartbeat` 等数字字段让节点起不来 | `pdnd/node/signed_config.go` 用 `UseNumber`，`kernel/hysteria2.go` / `tuic.go` 的 `nonNegativeInt` / `parseHysteriaDuration` 不认 json.Number；后台 schema 把它们声明成 number 且能保存 | 建 hy2 带 bandwidth 的节点，看 pdnd 日志与面板 effective_state |
| 5 | 封禁用户后仍能代理；admin 换发链接（72c04d9 上）不换节点密码 | 节点用户名单只看订阅状态 / 周期 / 流量 / 设备，不看 `users.status`；rotate 换节点密码是 bca0245 之后的事 | `kick-run.py ban` |
| 6 | 订阅行变更（到期、状态）传到节点要等 15 秒轮询，无 SSE 推送 | 只有节点/池/用户组类后台写会 `NotifyUsersChanged` | `kick-db.py` 的"应用移除"列 0.27–14.8 秒 |
| 7 | 整份 sing-box 订阅原样加载：规则集下载经"节点选择"，选到坏节点则 FATAL 退出；TUN 带 IPv6 地址，IPv4-only 节点上 AAAA 站点失败；`download_detour` 在 sing-box 1.14 已弃用 | 订阅模板（`render_singbox_template.go`） | `asis-run.sh`、变体脚本 |
| 8 | REALITY dest 对 microsoft / cloudflare / dl.google.com 不可用 | pdnd REALITY fork 的握手缓冲（单条记录 > 8192）等 | `reality-dest-scan.sh` |
| 9 | SS2022 后台不让建 | `protocol_schema.go` 的 shadowsocks `methods` 枚举只有 3 个 | `POST /v1/nodes` 带 `cipher: 2022-blake3-aes-128-gcm` -> 422 |
