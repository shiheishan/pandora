---
name: test-machine
description: pandora 一次性 Vultr 测试机（压测面板机、压测机、真节点、开发对照机）与租用机的规格、要不要 VPC、费用与公网出流量估算、用 Vultr API 开机、挂 VPC、查账单与超额、删机，以及开通登记与回收（租用机只登记，不走 Vultr 脚本）。用户发来新机器 IP、要开或删测试机、要给机器挂内网、要查流量超额或估算压测流量费、要在测试机上装 Docker / Go / Node（不装面板时）使用。在机器上装面板见 panel-install，面板压测见 prod-retest，节点大流量验收见 node-accept。
---

# 一次性测试机

目标：用户给出 IP 后几分钟内可用；登记只在固定的几处、删机后能干净撤掉；任何 IP、域名、口令都不进仓库（根 CLAUDE.md 红线）；不再超公网流量。

## 规格（向用户要机器时照这个说）

Vultr 新加坡，Debian 13 x64（与生产同版），开机时用 Vultr SSH Keys 注入 `~/.ssh/id_ed25519.pub`，关自动备份。要 VPC 的，几台都勾**同一个**同机房 VPC。

| 角色 | 套餐 | 要不要 VPC | 别名 |
|---|---|---|---|
| 面板机（install.sh 生产方式，5k 档） | vc2-2c-4gb | 和压测机、真节点同测时勾 | `vultr-sgp-pt-panel<N>` |
| 面板机（1 万用户 / 1000 节点档） | 4c8g 独享（用户 10-09 定：这一档的延迟按 4c8g 面板机考） | 同上 | `vultr-sgp-pt-panel<N>` |
| 压测机（loadtest、节点验收客户端） | vc2-2c-4gb（1c 在 15k 档不够） | 大流量压测必勾 | `vultr-sgp-pt-loadgen<N>` |
| 真节点（pdnd，逐协议验证） | vc2-1c-1gb | 不用（客户端要走真公网） | `vultr-sgp-pt-node<N>` |
| 大流量验收节点 | 4c8g 独享 | 必勾 | `vultr-sgp-pt-node<N>` |
| 开发对照机（5k 库 EXPLAIN、构建） | vc2-2c-4gb | 不用 | `vultr-sgp-pt-bench` |

「大流量」指持续百 Mbps 以上、跑一小时以上的测试：节点验收、hy2/TUIC 复测、SS2022 吞吐等。

## 费用

- **机器时价很低**：2c4g 约 $0.03/h，4c8g 独享约 $0.11/h。省机器钱不成为删机或压缩测试的理由：中途取消返工时默认留机，省掉重新部署。
- **要盯的是公网出流量**：
  - 账户流量池 = 免费 2TB + 按各机开机时长折算的额度；超出约 $0.01/GB。Vultr 只计公网**出**方向，入方向和 VPC 内网流量不计。
  - 10-08 节点验收没开内网，5 台公网出流量合计约 4.11TB，流量池只有约 2.64TB，超了约 1.3TB。
- **估算**：1Gbps 跑 1 小时，每个方向约 450GB。节点过 X Gbps（一半上行、一半下行）跑 H 小时：节点出 ≈ 450·X·H GB，目标机与压测机合计再出 ≈ 450·X·H GB，**全走公网时合计约 900·X·H GB**（10-08 约 4.6 Gbps·小时，实测 4.11TB，与公式吻合）。全走 VPC 时公网只剩 ssh、拉数据、apt、推二进制，几 GB 量级。
- **跑前报用户**：计划多少 Gbps、多少小时、走不走内网、预计公网出流量多少 GB（节点验收的报法见 node-accept）。
- **跑中、跑后核对**：各机 `/proc/net/dev` 的 tx，默认路由那块网卡才是公网（`bash .claude/skills/node-accept/scripts/netdev.sh <别名>...`）。开跑前、收尾各记一次，差值写进报告。

## 用 API 开机、挂 VPC、删机（用户 10-08 授权 agent 开机）

脚本都在 `scripts/`，现场值（1Password 引用、地域、os、SSH key、VPC 的 id、可选的额度上限 `VULTR_CREDIT_CAP`）在主目录 `ops-local/vultr/env`（0600，不进仓库）。API 密钥优先读 1P Environment 挂载的 `ops-local/1p/pandora-ops.env`，读不到才退回 `op read`（`vultr.sh` 头注释）。Bash 调用设 `dangerouslyDisableSandbox: true`（要连 1Password app 与 ssh agent）。

| 要做的事 | 命令 | 什么时候能跑 |
|---|---|---|
| 开机并登记 | `vultr-create.sh [--no-vpc] <别名> <套餐> "<用途>"` | 先在对话里报套餐、台数、时长、公网出流量估算；大流量机器一律挂 VPC（缺省） |
| 已有机器挂 VPC | `vultr-attach-vpc.sh <别名>...` | 用户同意改这几台后；不重启，约 20 秒 enp8s0 有地址，登记一并补上 |
| 看账单与超额 | `vultr-billing.sh` | 随时（只读）；开机前、删机前各看一次，机器开着的日子**每天看一次**。设了 `VULTR_CREDIT_CAP` 时多一行「按在跑机器时价，约多少小时后到上限」 |
| 删机 | `vultr-delete.sh <别名>...` 先列出，`--yes` 才删，删完自动撤登记 | **只删用户在对话里点名或明说授权的那几台**（用户 10-09 定）；先列清单与账单；只删 `vultr-sgp-pt-*` |
| 底层调用 | `vultr.sh <METHOD> <路径> [JSON 文件]` | 上面没覆盖的接口 |

- 4c8g 独享是 `voc-c-4c-8gb-75s-amd`，2c4g 是 `vc2-2c-4gb`，1c1g 是 `vc2-1c-1gb`；套餐与 os 列表的接口免密钥：`curl -s 'https://api.vultr.com/v2/plans?type=all&per_page=500'`。
- 密钥只经 builtin `printf` 走 stdin 给 `curl -H @-`：不进命令行参数、不打印、不落盘。创建返回体里的 `default_password` 不保存。
- 老机器的 Vultr 标签不是别名（如 mianban2），脚本按 `~/.ssh/config` 的公网地址对 `main_ip` 找实例。
- **额度上限与提醒**：用户给的额度数值放 `ops-local/vultr/env` 的 `VULTR_CREDIT_CAP`（美元，不进仓库）。机器开着时每天跑一次 `vultr-billing.sh`，待结接近上限（10-09 的做法：上限的约 5/6）就提醒用户，并报推算的触线时间；推算不计流量额度抵扣，实际略晚。开到月底会超预算的，测完请用户点名删。
- 删不删机看超额：`vultr-billing.sh` 的「流量超额」没归零时，开着的机器按开机时长攒额度，比交超额便宜（2c4g 约 4.5GB/h 花 $0.030，1c1g 约 1.5GB/h 花 $0.0074，超额 $0.01/GB）；归零后再开就是纯开销。不要为了攒额度开到月底。

## 租用机（不是 Vultr）

用户租的别人的机器（如 `netcup-de-pt-perf`）：不跑任何 `vultr-*.sh`，不删、不重装、不清理，一切规矩以该机 `~/ai/servers/<别名>/AGENTS.md` 为准（先读它）。新登记一台租用机用 `register.sh --rental <到期日> --site "<商家 / 机房>"`，工位文件换 `templates/AGENTS-rental.md`，里面授权范围、计费与流量限制等占位要按用户说的补全；撤登记仍是 `unregister.sh`。

## 开通

```bash
bash .claude/skills/test-machine/scripts/register.sh [--vpc <内网IP>] [--rental <到期日> --site "<商家 / 机房>"] <别名> <IP> <套餐> "<用途一句话>"
```

- 必须 bash 跑；ssh 经 1Password agent，签名失败见根 CLAUDE.md「环境与工具坑」。出错时读 `register.sh` 手工补救。
- 用途只写一句话，**不带日期**：总表里脚本自己补「（一次性，日期）」（结尾误带的「（一次性…）」脚本会先去掉）。
- 勾了 VPC 就带 `--vpc`：内网地址进 `~/ai/servers/<别名>/AGENTS.md`、总表 IP 列和 `ops-local/vpc-hosts.tsv`，`~/.ssh/config` 仍是公网地址。脚本会核对机器上有没有这个地址，没有就去控制台看 VPC 设置。
- 同名目录已存在就换序号：删过的旧机目录保留作记录（如 node1/node2 已删，新开的叫 node3/node4）。
- 一次性机不建 1Password 条目（用户定的）。

准备：要 Docker 用 Debian 主仓的 `docker.io` 与 `docker-compose`；要构建就 `ssh <别名> 'bash -s' < .claude/skills/test-machine/scripts/install-toolchain.sh`，装与 CI 同小版本的 Go（go.mod 的 1.26 系列最新版）和 Node 22，官方包都核 sha256。装面板、构建发布包见 panel-install。

## 回收

删机只删用户点名或明说授权的（10-09 定），用 `vultr-delete.sh --yes`，或由用户在 Vultr 控制台删；agent 不重装、不改套餐。用户在控制台删了之后跑 `unregister.sh <别名>`：去掉 `~/.ssh/config` 的 Host 块、总表一行、`ops-local/vpc-hosts.tsv` 一行，`ssh-keygen -R`，`AGENTS.md` 顶部注明已删（目录保留作记录），重生成私有 gitleaks 规则。结果先拉回 `ops-local/` 再删。

### 删前只读巡检

列删机清单之前先做一次只读巡检，可交 Cursor 的 Grok。brief 以 `ops-local/vultr-test2/idle-check-1010/brief.md` 为模板：只读、不下载文件、报告里不写 IP 与域名。

判断机器上的结果有没有拉回时，先看对应 skill 的同步方向，再决定要不要拉。例：bench-eval 的 evalset 是本机 `ops-local/bench/remote/` 推上去的同步副本，结果已由 `run.sh` 拉回，不用再拉；容器 `bench-pg` 里的 5k 实测库和模板库才要处理，见 bench-eval「删对照机之前」。

## 坑

- 刚开机头一次 ssh 常报 `Connection timed out during banner exchange`，隔 15 秒重试即可，不是密钥问题（register.sh 已内置重试 3 次）。
- ssh 起后台脚本、限时故障注入：照根 CLAUDE.md「环境与工具坑」的远端那一条。
- 镜像开着 ufw。面板机的 80/443 由 install.sh 放行（见 panel-install）；**节点机的协议端口**（约定 20000–20099 的 tcp 与 udp）要手工放行。10-08 节点验收时端口段规则计数一直为 0、SYN 被 DROP（原因没查），按来源地址放行才通；VPC 下按内网来源或内网网段放行。放行后先实测连通（node-accept、node-e2e 都按这条办）。
- 测试流量走内网时，pdnd 默认拒绝私网目标，要改节点配置，见 node-accept 第 2 节。
