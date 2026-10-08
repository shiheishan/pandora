---
name: test-machine
description: pandora 一次性 Vultr 测试机（压测面板机、压测机、真节点、开发对照机）的规格、开通登记与回收。用户发来新机器 IP、要开或删测试机、要在测试机上装 Docker / Go / Node（不装面板时）使用。在机器上装面板见 panel-install，压测见 prod-retest。
---

# 一次性测试机

目标：用户给出 IP 后几分钟内可用；登记只在固定的几处、删机后能干净撤掉；任何 IP、域名、口令都不进仓库（根 CLAUDE.md 红线）。

## 规格（向用户要机器时照这个说）

Vultr 新加坡，Shared CPU，Debian 13 x64（与生产同版），开机时用 Vultr SSH Keys 注入 `~/.ssh/id_ed25519.pub`，关自动备份。

| 角色 | 套餐 | 别名 |
|---|---|---|
| 面板机（install.sh 生产方式） | vc2-2c-4gb | `vultr-sgp-pt-panel<N>` |
| 压测机（loadtest nodes/users） | vc2-2c-4gb（1c 在 15k 档不够） | `vultr-sgp-pt-loadgen<N>` |
| 真节点（pdnd） | vc2-1c-1gb | `vultr-sgp-pt-node<N>` |
| 开发对照机（5k 库 EXPLAIN、构建） | vc2-2c-4gb | `vultr-sgp-pt-bench` |

## 开通

一条命令做完首次连接、三处登记、chrony 和更新私有 gitleaks 规则（提交前拦真实 IP）：`bash .claude/skills/test-machine/scripts/register.sh <别名> <IP> <套餐> "<用途一句话>"`（必须 bash 跑；ssh 经 1Password agent，见根 CLAUDE.md「环境与工具坑」）。同名目录已存在就换序号——删过的旧机目录保留作记录（如 node1/node2 已删，新开的叫 node3/node4）。它登记三处（`~/.ssh/config`、`~/ai/servers/<别名>/` 用 `templates/AGENTS.md` 生成、机器上 `/root/README.md`；一次性机不建 1Password 条目，用户定的），出错时读 `register.sh` 手工补救。

准备：`apt-get install -y chrony`；要 Docker 用 Debian 主仓的 `docker.io` 与 `docker-compose`；要构建就 `ssh <别名> 'bash -s' < .claude/skills/test-machine/scripts/install-toolchain.sh`，装与 CI 同小版本的 Go（go.mod 的 1.26 系列最新版）和 Node 22，官方包都核 sha256。装面板、构建发布包的步骤见 panel-install。

## 回收

删机只由用户在 Vultr 控制台操作，agent 不删、不重装、不改套餐。用户说删了之后：从 `~/ai/servers/README.md` 总表和 `~/.ssh/config` 去掉，`ssh-keygen -R <IP>`；机器目录保留，`AGENTS.md` 顶部注明「已于某日删除，只作记录」。结果先拉回 `ops-local/` 再让用户删。

## 坑

- 刚开机头一次 ssh 常报 `Connection timed out during banner exchange`，隔 15 秒重试即可，不是密钥问题（register.sh 已内置重试 3 次）。
- **ssh 起后台脚本会挂住会话**：写法与原因见根 CLAUDE.md「环境与工具坑」（`ssh -n host 'cd /root/lt; setsid -f ./x > log 2>&1 < /dev/null'`，两台机器分两条命令发；构建、install.sh 这类长命令同理）。
- 镜像开着 ufw。面板机的 80/443 由 install.sh 放行（见 panel-install）；**节点机的协议端口**（约定 20000–20099 的 tcp 与 udp）要手工放行。
- 同一个 IP 重装系统后主机密钥会变，要先 `ssh-keygen -R <IP>` 再连（register.sh 首次连接前已先清）。
