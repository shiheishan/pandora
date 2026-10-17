# 节点端性能闸门（pdnd）

同一台测试机上、同一份虚构数据、同样的压力与场景顺序，先量基线提交的 pdnd、再量候选提交，按相对变化判定一项改动是变快了、持平还是变慢了。

- **单机模式只判相对变化**：所有进程在命名空间 `nodegate` 里走回环，绝对数字只能和同一台机器、同一套 tools 上的数字比。用来给每一项性能优化出「改前改后」数字，也用来防止热路径改动悄悄变慢。
- **多机模式出绝对数字**：三台内网机分工（节点 / 压测与控制 / 压测），流量走内网地址，发版前可跑全套拿里程碑级吞吐与握手率。
- **基线当天现跑**：名单、会话与面板模拟状态随运行变化，几天前的结果不能拿来比。
- **先量噪声**：同一提交跑两遍（A/A），`compare.py --aa` 用两遍之差放宽阈值。
- **tools 换了不能比**：`meta.json` 里的 `tools_id` 是 `tools/` 目录的 git 树哈希前 12 位；与旧运行不同则只能重跑基线再比。

## 文件

| 文件 | 在哪跑 | 做什么 |
|---|---|---|
| `gate.sh` | 本机（macOS） | 入口：交叉编译 pdnd 与工具、上传脚本与二进制、启动一次运行、等完、取回、对比 |
| `orchestrate.py` | 单机在测试机后台；多机在本机 | 一次运行的全部场景编排，收集产物，调 `report.py` |
| `remote.sh` | 每台测试机（root） | 检查、准备与清理、起停 pdnd、后台进程、采样、iptables 守卫、可选 rmem |
| `collect.py` | 节点机 | cgroup 与 `/proc` 快照、每秒采样到 `samples.jsonl` |
| `report.py` | 编排所在机器 | 汇总成 `result.json` |
| `compare.py` | 本机 | 读两次 `result.json`，判定规则与面板闸门一致 |
| `test_compare.py`、`test_report.py`、`test_collect.py` | 本机 / CI | 单元测试，不需要网络 |
| `tools/` | — | 独立 Go module：`scalepanel`、`loadgen`、`target` |

测试机 ssh 别名：环境变量，或主仓库 `ops-local/nodegate/gate.env`（只写键名，仓库里不放值）。
结果取回到 `ops-local/nodegate/<label>/`（被 git 忽略）。

## 两种拓扑

### 单机（A/B 闸门）

- 所有协议端口、scalepanel、loadgen、target 都在网络命名空间 `nodegate` 里；pdnd 用 `systemd-run` 临时单元 `nodegate-pdnd`，限 **0–1 号核、2G 内存**，并挂 `NetworkNamespacePath=/run/netns/nodegate`。
- scalepanel、loadgen、target 在 ssh 会话所在的 **user.slice**（其余核）。
- pdnd 入站只能听全部地址（节点代码固定），放在命名空间里才**不对公网开放**，也**不改防火墙**。
- 跑闸门时会停面板闸门占用的 `pandora-serve` / `pandora-worker`（记在机上状态里），**结束 cleanup 会恢复**。
- 编排：`gate.sh run` 在测试机上 `setsid` 后台跑 `orchestrate.py`；本机可 `wait` / `fetch`。

### 多机（绝对数字）

| 角色 | 跑什么 |
|---|---|
| **node** | pdnd、名单与配置的接收、节点采样 |
| **load1** | scalepanel（gRPC 听 load1 内网地址，控制口只听回环）、target（echo / sink / REALITY 诱饵）、loadgen（多数场景） |
| **load2** | loadgen（空闲长连接、握手、部分延迟等，与 load1 分担连接数） |

- target 与 scalepanel 的监听地址是 **load1 的内网地址**（端口：19000–19002、19443、9443 等，见契约 port_base）。**前提：该内网地址不在公网网卡上**，这些端口才不会对公网开放；节点机上的 **iptables 守卫只限制 node 上 pdnd 的协议端口**（`fw-guard`，只放行 `<内网网段>`，缺省 **4 小时**到点自撤，`orchestrate` 传 `--ttl 14400`）。
- 节点配置里 **`runtime.allow_private_destinations` 为 true**，压测目标走内网。
- **编排在本机**前台跑 `orchestrate.py`（ssh 断了会中止；在 Claude Code 里用 Bash 后台运行）。
- 跨机时刻（如 `pdnd_start` 在节点机、`panel` 状态在 load1）要求各机 **NTP 同步**。

## 测试机前提

- **root**、**python3**、**curl**、**systemd**（≥ 242，支持 `NetworkNamespacePath`）。
- **单机**：**iproute2**（`ip netns`）。
- **多机 node**：**iptables**（`fw-guard`）。
- **不需要 Go**：pdnd 与工具在本机交叉编译后 scp 上去。
- 机上根目录 `R=/root/nodegate`：`bin/`、`tools/`、`runs/`、`state/`。

`ops-local/nodegate/gate.env` 键（示例占位，不写真实值）：

| 键 | 用途 |
|---|---|
| `NODEGATE_HOST` | 单机 ssh 别名 |
| `NODEGATE_NODE` | 多机：节点机别名（设了即多机模式） |
| `NODEGATE_LOAD1` | 多机：load1 别名 |
| `NODEGATE_LOAD2` | 多机：load2 别名 |
| `NODEGATE_NODE_IP` | 多机：节点内网 IP |
| `NODEGATE_LOAD1_IP` | 多机：load1 内网 IP |
| `NODEGATE_CIDR` | 多机：iptables 放行来源 `<内网网段>` |

环境变量同名键优先于 `gate.env`。可选：`NODEGATE_ENV`、`NODEGATE_OUT`、`NODEGATE_PARAMS`、`NODEGATE_ALLOW_DIRTY`。

## 每次运行

```bash
pdnd/tests/perf/gate/gate.sh check
pdnd/tests/perf/gate/gate.sh run b0-a <基线提交>
pdnd/tests/perf/gate/gate.sh run b0-b <基线提交>
pdnd/tests/perf/gate/gate.sh run o1 <候选提交>
pdnd/tests/perf/gate/gate.sh compare b0-a o1 --aa b0-b --target handshake.
```

一次运行约 **30 分钟**。单机在测试机后台跑，本机 `gate.sh run` 会等完并自动取回；多机在本机前台跑并 `tee` 到 `ops-local/nodegate/<label>.log`。

`NODEGATE_PARAMS` 示例：`users=5000;hs_max_rate=400`（段用 `;` 分隔，键名小写加下划线）。

## 场景顺序与指标

固定顺序（契约 §8）：

| # | 场景 | 测什么 | 主要指标（`compare` 键前缀） |
|---|---|---|---|
| 0 | 准备 | check、profile、起 target/scalepanel、pdnd、采样 | — |
| 1 | 接入 `connect` | enroll、probe、全量配置与名单 g0 | `roster.*` |
| 2 | 名单增量 `delta` | swap 一代、长连接保持 | `roster.delta_*` |
| 3 | 空闲 `idle` | 万级长连接占用 | `idle.*` |
| 4 | 握手 `hs-<P>` | 每协议 connrate | `handshake.<P>.*` |
| 5 | 吞吐 `tput-<P>` / `tput1-<P>` | 多流定速 + 单流满速 | `tput.<P>.*` |
| 6 | 延迟 `lat` | direct 基线 + 每协议附加 | `lat.*` |
| 7 | UDP `udp-<P>-<R>` | hy2、tuic 多档速率 | `udp.<P>.<R>.*` |
| 8 | 故障 `failover` | 面板 down、节点重启、up 后恢复 | `failover.*` |
| 9 | 收尾 | cleanup、`result.json` | — |

协议全集（固定端口序号，port_base 缺省 20000）：`vless_vision`、`vless_grpc`、`trojan_tcp`、`trojan_ws`、`ss`、`vmess_ws`、`hy2`、`tuic`、`anytls`。

### 场景参数（`--param` / `NODEGATE_PARAMS`）

| 键 | single 缺省 | multi 缺省 | 说明 |
|---|---|---|---|
| `users` | 5000 | 5000 | 名单人数 |
| `delta_size` | 500 | 500 | 每代轮换人数 |
| `protocols` | all | all | 协议子集 |
| `delta_hold_conns` | 200 | 1000 | 增量场景长连接 |
| `idle_conns` | 10000 | 30000 | 空闲长连接总数（multi 两台各一半） |
| `idle_secs` | 60 | 120 | 空闲保持秒数 |
| `hs_concurrency` | 32 | 64 | 握手每台并发 |
| `hs_secs` | 60 | 60 | 每协议握手时长 |
| `hs_max_rate` | 0 | 400 | 每台每秒新建上限（多机防 TIME_WAIT） |
| `tput_mbps` | 300 | 2000 | 定速总 Mbps（multi 两台各一半） |
| `tput_streams` | 4 | 8 | 定速流数（每台） |
| `tput_secs` | 30 | 30 | 定速时长 |
| `tput1_secs` | 20 | 20 | 单流满速时长 |
| `lat_count` | 2000 | 2000 | 每协议延迟采样次数 |
| `udp_rates` | 50 100 300 | 100 300 1000 | UDP 档位（Mbps，空格分隔） |
| `udp_secs` | 20 | 20 | 每档 UDP 时长 |
| `fo_conns` | 200 | 1000 | 故障场景长连接 |
| `fo_down_secs` | 60 | 60 | 面板 down 时长 |
| `udp_rmem_8m` | 0 | 0 | 见下文 |
| `settle_secs` | 10 | 10 | 场景间静置 |

## 判定

`compare.py` 与面板闸门共用同一套判定函数（`panel/tests/perf/gate/compare.py`）：延迟 p50 +5%（n≥100）、p99 +10%（n≥1000）、内存 +10%、每次操作 CPU +5%；`--aa` 取缺省阈值与两倍噪声中较大者；计数类在 0 附近有绝对容差；样本不足为「不能判」。

- **退回**：有退化。
- **未达预期**：给了 `--target` 但目标前缀下无改善。
- **可合**：无退化，改善项作为改前改后数字。

## 配置文档字段从哪来

`scalepanel` 下发的 `config_json` 形状照面板 `panel/internal/module/node/service/config_docs.go` 的 `buildNodeConfigDoc`，协议字段经 `panel/internal/module/node/protocol/kernel_config.go` 翻译后摊平在顶层；节点端读的键见 `.claude/rules/pdnd-node-panel.md` 与 `pdnd/kernel/*.go`。

**形状对不上时节点拒收**，第一步接入就会失败。面板侧 trojan 的普通 TLS 校验器目前只允许 REALITY；本闸门测的是 **内核已支持的 trojan_tcp / trojan_ws（TLS 自签）** 等能力，与后台表单校验范围不必相同。

## 客户端实现

全部 **不经 sing-box**：

| 协议组 | 实现 |
|---|---|
| vless_vision、vless_grpc、trojan_tcp、trojan_ws、ss、vmess_ws | **xray-core** 客户端 |
| hy2、tuic | **sing-quic** |
| anytls | **sing-anytls** |

复用口径（与契约 §5.5 一致）：`vless_grpc` 在同一 xray 实例内复用 HTTP/2；QUIC 类与 anytls 在同一客户端内复用连接；`hold` 按真实客户端复用；`connrate` 对 QUIC/anytls 每次新建客户端（真握手），对 `vless_grpc` 量的是 gRPC 流建立成本。

## 吞吐为什么用 loadgen 自带的 tput 而不用 iperf3

iperf3 **不支持 socks**；经代理要再套 proxychains 一类工具，多一层转发与依赖。自带的 **tput** 直接经协议客户端连 target 的 sink，定速、满速、结果 JSON 都在 **loadgen** 里，A/B 两边同一套。

## pdnd 没有 pprof

闸门用 **线程数**与 **fd 数** 近似 goroutine 压力；网络计数读 pdnd 所在命名空间的 **`/proc/<pid>/net/snmp`**（`collect.py`）。

## 多机模式的 TIME_WAIT

流量不走回环时，Linux 上 **`tcp_tw_reuse` 缺省往往不生效**；握手场景用 **`hs_max_rate`** 限速，避免压测机本地端口耗尽。

## `udp_rmem_8m=1`

节点机临时把 **`net.core.rmem_max` / `wmem_max`** 提到 8MB（记下原值到 `$R/state/rmem.orig`）。**cleanup** 与下次 **`check`** 会写回原值；另挂 **到点自撤**（与 `fw-guard` 类似，缺省约 **4 小时**）。异常中断后，下次 `check` 也会尝试恢复。

## 结果目录与文件

`<label>/` 下（节选）：

| 文件 | 内容 |
|---|---|
| `meta.json` | label、sha、`tools_id`、mode、params、协议列表 |
| `result.json` | `report.py` 汇总的 `metrics` / `n` / `notes` |
| `events.json` | enroll、pdnd 启动、delta、故障时刻 |
| `windows.jsonl` | 各计量窗口 t0/t1 |
| `snap/*.json` | 窗口前后节点快照 |
| `lg/*.json` | loadgen 单行 JSON |
| `panel/*.json` | scalepanel `/status` 快照 |
| `samples.jsonl` | **每秒一行**节点快照（契约 §7）；`report.py` 目前不用，供人工看曲线 |
| `logs/pdnd.log` 等 | 服务日志（取回时 **不含** 机上 `pdnd/` 身份与缓存目录） |
| `done` / `failed` | 成功或失败标记 |
| `notes.txt` | 异常备注，并入 `result.json` notes |

读结果时注意 **`tools_id`** 与 **`sha`** 必须和对比方一致；多机还有 `mode=multi`。

## 红线

- 测试端口不对公网开放（单机靠命名空间；多机靠内网监听 + 节点 iptables）。
- 只放虚构数据（`*.example.test`、契约 §2 派生 UUID），不上传真实密钥。
- 仓库、README、`gate.sh` 输出、对话里**不写机器别名、IP、域名**；结果目录不进仓库。
- **不改系统设置**，除显式 `udp_rmem_8m=1`（结束或 check 恢复）。
- **测试机不重装、不改套餐**；遵守 `~/ai/servers/<别名>/AGENTS.md`。
- 测量与编译不重叠：编译在本机，`run` 才开始测；同一时间只跑一个运行（机上 `$R/lock`）。
