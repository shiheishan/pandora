---
name: test-machine
description: pandora 一次性 Vultr 测试机（压测面板机、压测机、真节点、开发对照机）的开通登记、环境准备与回收。用户发来新机器 IP、要开或删测试机、要在测试机上装 Docker / Go / Node 或在 Linux 上构建发布包时使用。
---

# 一次性测试机

目标：用户给出 IP 后几分钟内可用；登记只在固定的几处、删机后能干净撤掉；任何 IP、域名、口令都不进仓库。改服务器前先读 `~/ai/servers/README.md` 与该机目录的 `AGENTS.md`（全局约定）。

## 规格（向用户要机器时照这个说）

Vultr 新加坡，Shared CPU，Debian 13 x64（与生产同版），开机时用 Vultr SSH Keys 注入 `~/.ssh/id_ed25519.pub`，关自动备份。

| 角色 | 套餐 | 别名 |
|---|---|---|
| 面板机（install.sh 生产方式） | vc2-2c-4gb | `vultr-sgp-pt-panel<N>` |
| 压测机（loadtest nodes/users） | vc2-2c-4gb（1c 在 15k 档不够） | `vultr-sgp-pt-loadgen<N>` |
| 真节点（pdnd） | vc2-1c-1gb | `vultr-sgp-pt-node<N>` |
| 开发对照机（5k 库 EXPLAIN、构建） | vc2-2c-4gb | `vultr-sgp-pt-bench` |

## 开通

1. 首次连接：`ssh -o StrictHostKeyChecking=accept-new -o BatchMode=yes root@<IP> true`。
2. 登记三处，缺一不可（一次性机不建 1Password 条目，用户定的）：
   - `~/.ssh/config` 末尾追加 Host 块（别名、HostName、`User root`）；
   - `~/ai/servers/<别名>/`：`AGENTS.md`（用 `templates/AGENTS.md` 填身份卡、用途、红线）、`CLAUDE.md`（内容 `@AGENTS.md`）、`backups/`、`tools/`；`~/ai/servers/README.md` 总表加一行；
   - 机器上 `/root/README.md`：用途、关键容器与端口、口令文件位置（只写位置不写值）、结果目录。
3. 准备：`apt-get install -y chrony`；要 Docker 用 Debian 主仓的 `docker.io` 与 `docker-compose`；要构建就 `ssh <别名> 'bash -s' < scripts/install-toolchain.sh`，装与 CI 同小版本的 Go（go.mod 的 1.26 系列最新版）和 Node 22，官方包都核 sha256。

## 回收

删机只由用户在 Vultr 控制台操作，agent 不删、不重装、不改套餐。用户说删了之后：从 `~/ai/servers/README.md` 总表和 `~/.ssh/config` 去掉，`ssh-keygen -R <IP>`；机器目录保留，`AGENTS.md` 顶部注明「已于某日删除，只作记录」。结果先拉回 `ops-local/` 再让用户删。

## 坑

- 刚开机头一次 ssh 常报 `Connection timed out during banner exchange`，隔 15 秒重试即可，不是密钥问题。
- 镜像自带约 7.7G 的 `/swapfile`（磁盘上来就用掉 11G）。压测必须记录 swap 换页（runbook 的 sample-procs 已带），否则看不出「内存没撑爆但在换页」。
- 镜像开着 ufw 时，安装脚本不放行端口：面板机要 80/443，节点机要协议端口（约定 20000–20099 的 tcp 与 udp）。
- Debian 自带的 nginx 默认站点会和 aegis.conf 抢 80 端口的 default_server；安装链目前不申请证书。在安装链修好之前：趁默认站点还占着 80，先用 certbot webroot 申请证书，再删默认站点、执行 install.sh。没有域名就用 sslip.io；撞上 Let's Encrypt 限额就停下报告。
- `build-release.sh` 只能在 Linux 上跑（GNU tar、sha256sum），要在 `panel/` 目录下执行；它的 `git describe` 会取到 `archive/` 开头的标签，导致版本号被拒，要显式传版本号。
- 压测机经 nginx 压面板时，所有请求的来源 IP 相同，会撞上按 IP 限流（每分钟 240 次）和 IP 聚类：要让面板信任压测机并由压测机带 X-Real-IP（runbook 第 4 节，loadtest 已支持）。
- 1Password SSH agent 锁着时 ssh 会签名失败；子 agent 的沙箱连不到 agent，需要关掉沙箱或由主会话来执行。
- 同一个 IP 重装系统后主机密钥会变，要先 `ssh-keygen -R <IP>` 再连。
- 真实 IP 只能出现在 `~/.ssh/config`、`~/ai/servers/`、`ops-local/` 里；报告和仓库里一律写别名或 `<PANEL_IP>` 这类占位符。
