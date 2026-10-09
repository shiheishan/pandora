---
name: node-accept
description: pandora 节点端（pdnd）大流量验收与 Linux 复测：在同机房 VPC 内网的 4c8g 节点加压测机上跑 10 万连接、1–2Gbps 稳态、单协议档和故障演练（面板宕机、断流、删人、重连风暴），按用户定的标准与取消线出成绩单，含跑前公网出流量上报与远端自撤销的故障注入。用户或总协调说「节点压测」「节点验收」「VPC 复测」「节点大流量」「10 万连接」「每 Gbps CPU」「重连风暴」，或改了 pdnd 转发路径、QUIC、SS 要在 Linux 上复测吞吐、延迟、重传、内存时使用。逐协议能不能连用 node-e2e；面板整机压测（面板复测、10k 基线）用 prod-retest；开机登记与流量费估算公式用 test-machine；测试中途被打断用 resume-work。
---

# 节点端大流量验收

目标：同一套机器、同一种负载、同一种口径，逐项回答「达标没有」，并且不再超公网流量。10-08 那一轮（主线 3e51335）的完整报告、原始数据和脚本在 `ops-local/nodeaccept/`，做法以它为准；本 skill 只写每轮都要照做的部分和踩过的坑。更早两轮是 `ops-local/nodescale/`（10 万连接容量）、`ops-local/nodeperf/`（单项性能），只作对照。

## 红线

- 仓库公开（根 CLAUDE.md）：IP、密钥、节点 token 只放 `~/.ssh/config`、`~/ai/servers/`、`ops-local/<轮次>/`。
- 不改仓库代码；测出的 pdnd 问题写进报告的问题表，由总协调派任务。
- 压测工具不在仓库：在 `ops-local/nodeaccept/harness-src/`（scalepanel、scaleload、sbrun、echosrv、udpblast、overlay、sampler.py）。要不要搬进仓库由用户另定，本 skill 不搬。
- 远端后台进程一律 `setsid -f`；pkill 只按 PID 或 `-x` 精确进程名（根 CLAUDE.md「环境与工具坑」）。
- 删机只删用户点名或明说授权的（test-machine「回收」）。

## 1. 标准与取消线（用户 10-07 定，`.claude/TASKS.md`「节点端大流量验收」）

| 项 | 达标 | 取消线（中途抽查触到就停，返工后重测） |
|---|---|---|
| 稳态 | 4c8g 独享节点，10 万 TCP 长连接 + 1Gbps 跑 3 小时；另一轮 2Gbps（上轮 30 分钟） | — |
| 崩溃、泄漏、建连失败、换页 | 都是 0 | 任一出现 |
| 每连接内存 | ≤35KB | >50KB |
| 代理附加延迟 p50（本机口径，见第 5 节） | ≤0.1ms | >0.5ms |
| TCP 重传（整机） | ≤0.1% | >1% |
| 每 Gbps CPU | vless/trojan ≤0.5 核；hy2/TUIC ≤1 核 | — |
| hy2/TUIC 单连接 500Mbps | 丢包 ≤0.1% | — |
| 故障演练 | 面板宕机 / 断流、节点重启后 10 万重连风暴、批量删人：数据面不受影响，恢复后补报不丢 | — |

用户原话：「中途抽查，表现过于低下就取消测试返工」，不跑满 3 小时浪费机器。取消线之外还要人工看 panic、重启、RSS 与 goroutine 是否单调上涨。

## 2. 机器与 VPC

| 角色 | 规格 | 跑什么 |
|---|---|---|
| 节点 | 4c8g 独享（上轮 AMD EPYC-Rome） | pdnd（生产 unit）、本机回显、采样器 |
| 压测机 ×3 | 2c4g | scaleload（长连接）、sbrun + iperf3 客户端、采样器 |
| 目标机 | 2c4g | scalepanel（模拟面板）、echosrv（回显 + REALITY dest 替身）、iperf3 服务端 |

- **开机时勾同机房 VPC，测试流量只走内网地址**（上一轮没开内网，超了约 1.3TB 流量池；费用口径与估算公式见 test-machine「费用」）。
- 开机、登记照 test-machine（register.sh 可以带内网地址）。`ops-local/<轮次>/ips.env` 里**只写内网地址**，脚本、配置、scaleload 的 `-servers` / `-targets`、iperf3 目标、pdnd 的 `panel.url` 全用它。
- **pdnd 默认拒绝私网目标**（`pdnd/outbound/private_guard.go`：直连出站对用户目标拒回环、10/8、172.16/12、192.168/16、100.64/10 等，UDP 逐包静默丢）。回显和 iperf3 目标在内网时，节点配置要加：
  ```json
  "runtime": { "allow_private_destinations": true }
  ```
  （`pdnd/runtime_tuning.go`，`main.go` 的 `runtime` 段；启动日志会打「已放开私网目标」，没看到这句就是没生效）。这是验收专用；其余 runtime 项保持缺省，否则测的不是生产行为。
- **REALITY dest 和探测回落目标不受这个开关管**：它们由 `kernel/reality_listener.go`、`kernel/probe_fallback.go` 用自己的 `net.Dialer` 直拨，不经 outbound；`ParseRealityServerConfig` / `parseProbeFallback` 目前也不拒私网地址，所以 dest 替身放内网现在能用。TASKS 的节点遗留里排着「这两处要拒私网 dest」，修好之后 dest 替身放内网会被拒（除非修法也挂在这个开关上）。每轮开跑前读一遍这两个函数；被拒就把 dest 替身放到目标机的公网地址上（只有握手流量，10 万次重连风暴也只有几百 MB）。
- pdnd 连模拟面板走 UniProxy 兼容通道，`panel.url` 用 `http://<目标机内网IP>:18080`、`signed_required: false`。签名通道只认 https 或回环 http（`pdnd/panel/signed.go` 的 `validateSignedServer`），别切过去。
- ufw：按内网来源或内网网段放行，不要只放端口段（坑见 test-machine「坑」）。
- 采样器 `sampler.py` 取 `/proc/net/dev` 里**第一块非 lo 网卡**算 rx/tx。开了 VPC 后这通常是公网卡，内网流量算不进来：开跑前改成按内网卡（`ip -o addr` 找内网地址所在的网卡）。

### 跑前估算公网出流量，报给用户

公式见 test-machine「费用」（全走公网约 900GB / (Gbps·小时)，全走 VPC 只剩几 GB）。
- 报用户的格式：「计划 X Gbps × H 小时；走内网，预计公网出流量 N GB；若某段只能走公网（例如 dest 替身），那段预计 M GB」。
- 开跑前、稳态开始、收尾各跑一次 `bash .claude/skills/node-accept/scripts/netdev.sh <别名>...`，按公网卡的 tx 差值核对估算。

## 3. 构建与部署

- **pdnd**（在 `pdnd/` 下，与 CI 同 Go 版本，flag 同 `release/build.sh`）：
  ```bash
  GOTOOLCHAIN=go$(awk '/^go /{print $2}' go.mod) CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=readonly -trimpath \
    -overlay ../ops-local/nodeaccept/harness-src/overlay/overlay.json \
    -ldflags "-s -w -X main.buildVersion=accept-<短 sha>-pprof" -o <输出> .
  ```
  overlay 注入 `zz_scale_pprof.go`：只在 `PDND_PPROF=127.0.0.1:6060` 时开 pprof 与 `/scale/stats`（goroutine、堆），仓库里不落文件。Go 版本取 `pdnd/go.mod` 的 `go` 行，与 CI、发布一致，升版本不用改本 skill。
- **压测工具**（在 `ops-local/nodeaccept/harness-src/` 下）：
  ```bash
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags with_utls,with_quic -o ../bin/ ./scaleload ./sbrun ./udpblast ./echosrv ./scalepanel
  ```
- `harness-src/go.mod` 的 `replace` 和 `overlay.json` 写的都是主目录的绝对路径。在 worktree 里测分支时，两处都要改成那个 worktree 的 `pdnd`，否则测的是主目录的代码。
- 部署：仓库原样的 `release/pandora-native.service`，只加一个设 `PDND_PPROF` 的 drop-in（`scripts/setup_node.sh` 的做法）。内核参数用 `tune.sh` 临时 `sysctl -w`，改前值存 `sysctl.before`。
- 每轮在 `ops-local/` 下新开 `nodeaccept-<日期或分支>/`，从上一轮复制 `scripts/` 与 `cfg/`，把脚本里的机器名、`ips.env` 换成这一轮的。脚本用法见上一轮 REPORT「用到的命令与工具」。

## 4. 阶段（每段起止写进 `stages.log`，一行一个 UTC 时间戳）

1. **部署**：起目标端、模拟面板、节点、各机采样器；核启动日志（入站就绪、私网放开、名单同步）。
2. **单协议档**：节点上只跑这一种负载，vless、trojan、hy2、TUIC 各 1Gbps，每档 ≥10 分钟（上轮只跑了 4 分钟，记为偏差）。hy2/TUIC 另测单连接 500Mbps（`blast.sh`，udpblast）。
3. **预检**：爬坡到 5 万、10 万连接，空载基线（pdnd 核数、RSS、重传）；本机探针 pl 先起好，跑一次 `spot.sh` 确认没触取消线。
4. **3 小时稳态 + 抽查**：iperf3 按 200 秒一段循环（`soakloop.sh`；控制连接整段无数据，一次跑长了会被断）。每 30–40 分钟 `spot.sh <标签> <窗口起>` 一次，触到取消线就停（第 1 节）。
5. **演练**（满载下）：面板宕机、断流半开（第 6 节）、批量删人（`drill_delete.sh`）。
6. **重启与重连风暴**：客户端集体重连；`systemctl restart pandora-native`，看停机耗时、就绪后多久回到 10 万、失败数。
7. **2Gbps**：重连后直接上，30 分钟。
8. **收尾**（第 7 节）。

## 5. 指标口径

- **附加延迟**，两种都报，**取消线按本机口径判**：
  - 压测机口径 = pv − pd − pn（pv：压测机经节点到目标回显；pd：压测机直连节点回显；pn：节点直连目标回显）。会混进压测机自己的 CPU 和调度噪声，上轮 p50 在 1.2–3.3ms 之间跳。
  - 本机口径 = pl − pn（pl：节点本机的 sing-box 经节点地址进 pdnd 再到目标回显）。
  - p99 只列原值不相减：直连探针自己就有 8–17ms 的 p99。
- **重传**：整机 `tcp_retrans / tcp_outsegs`（`/proc/net/snmp` 的差值），按窗口算。
- **每连接内存**：(pdnd RSS − 空载基线约 76MB) ÷ 连接数，连接数 = pdnd fd ÷ 2；另报堆在用 + 栈的拆法。
- **每 Gbps CPU**：单协议档直接用「pdnd 核数 ÷ Gbps」；稳态用「(满载核数 − 10 万空载核数) ÷ Gbps」。
- **窗口统计**：`win.py <samp.jsonl> <起> <止>` 出单机窗口平均；`spot.py` 汇总各机，每个窗口一行写进 `results/windows.jsonl`。演练窗口、删人后窗口单列，不并进稳态。

## 6. 故障注入必须在远端自己撤销

10-08 本机断网，DROP 的撤销命令没送到，半开演练从 4 分钟拖成 31 分钟。限时的注入一律用：

```bash
bash .claude/skills/node-accept/scripts/selfrevert.sh <节点别名> 240 \
  'iptables -I OUTPUT 1 -d <目标机内网IP> -p tcp --dport 18080 -j DROP' \
  'iptables -D OUTPUT -d <目标机内网IP> -p tcp --dport 18080 -j DROP' ops-local/<轮次>/stages.log
```

它先在远端 `setsid` 布置好「N 秒后撤销」，再注入。面板宕机同理：注入 `pkill -x scalepanel`，撤销 `bash /root/nodeaccept/start_panel.sh`。到点后回读远端日志（「已撤销 rc=0」）和 `iptables -S OUTPUT`，确认规则数为 0 再继续。

## 7. 收尾

- 停全部测试进程（按 PID 或 `-x`），回读 iptables 无残留。
- sysctl 按 `sysctl.before` 改回（只用过 `-w` 的话重启也会恢复）；撤掉本轮加的 ufw 规则。
- 各机 `/root/README.md` 追加：装了什么、留下了什么（二进制、unit、系统用户）、服务是否 stop。
- `pull.sh` 拉回原始数据；`netdev.sh` 出各机网卡累计收发，存 `results/netdev_end.txt`。
- 告诉用户测完了，机器可以删；删机范围由用户点名或授权，撤登记照 test-machine。

## 8. 报告模板（`ops-local/<轮次>/REPORT.md`，照上一轮的结构）

1. **结论**：整体一句；达标项；不达标项（实测 vs 目标）；有没有触到取消线。
2. **环境与做法**：机器表（规格、角色、是否走 VPC）、pdnd 构建（sha、Go 版本、overlay）、节点与用户、连接构成、吞吐打法、延迟口径、内核参数、ufw、runtime 段。
3. **标准逐条**：标准 | 实测 | 目标 | 结论。
4. **单协议档**、**稳态分窗口**（窗口 | CPU | RSS | 每连接 | goroutine | 重传 | 两种口径延迟 | 建连失败 | swap）、**抽查表**（时间 | 抽查 | 结果）。
5. **故障演练**、**重启与重连风暴**、**2Gbps**。
6. **发现的 pdnd 问题**：# | 严重度 | 问题 | 证据（profile 与原始数据路径）；另列「上一轮的问题本轮确认已修」。
7. **机器出流量**：机器 | 网卡 | rx | tx，分公网与内网；与跑前估算对比。
8. **中断与偏差**：时段 | 事件 | 影响（作废或单列哪个窗口），见 resume-work。
9. **机器状态**：停了什么、留下什么、能不能删。

## 坑

- iperf3 控制连接整段测试没有数据，上轮在第 240 秒被断（来源没查明，不排除 pdnd），所以按 200 秒分段。
- 单连接 hy2/TUIC 打不到 500Mbps 时先分清是客户端还是 pdnd 到顶：看客户端核数和 pdnd profile。经 sing-box 路由的 sbrun 当发端会先成为瓶颈，结果作废。
- 重连风暴时压测机和 dest 替身也会打满，「多久重连完」受客户端限制，报告里写明。
- 压测机自己 CPU 40% 以上、steal 高时，压测机口径的延迟不可信，只看本机口径。
