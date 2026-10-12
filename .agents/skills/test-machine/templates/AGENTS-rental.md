# <别名> · 服务器工位

> 进这个目录 = 要管这台服务器。**先读完本文件再动手。** 机器上有什么以服务器上的 `/root/README.md` 为准。

## 身份卡

| 项 | 值 |
|---|---|
| 名字 | `<别名>` |
| IP | <IP> |
| 内网 IP（VPC） | <内网IP> |
| 商家 / 机房 | <商家机房>；<套餐>。**租用的别人的机器** |
| 系统 | <系统与内核>，时区 UTC，北京 = 本机 + 8h |
| 用途 | <一句话：pandora 的哪项测试、装什么、和哪台配合> |
| 日常运维 | Claude Code 的 pandora 总协调会话（主目录 `~/ai/projects/pandora`，清单 `.claude/TASKS.md`） |
| 代码 | pandora 仓库（本地 clone `~/ai/projects/pandora`）；压测 runbook 是 `panel/tools/loadtest/README.md` |

**租用的测试机**，<日期> 登记，用到 **<到期日> 到期**。不进 git 仓库，机器上的 `/root/README.md` 只是说明，不版本化。

- 授权范围：<用户在对话里授权的范围，一句话>
- 计费与流量限制：<计费方式；外网流量上限与超限后果；哪些流量不计>
- 到期处理：以用户在对话里说的为准（要不要清理、何时撤登记），说了就补在这里。

用途分工：<这台机器适合做什么、不适合做什么>

## 登录

```bash
ssh <别名>
```

- 公钥由用户推入（`ssh-copy-id`，root 密码只有用户和机主有，agent 不用密码登录）；私钥在 1Password，由 1Password SSH agent 签名。1Password 锁着就登不上，请用户解锁。
- 不生成新密钥，沿用全局那把；**不要**改 `authorized_keys`、`sshd_config`，不读私钥。再改 SSH 配置要先问用户。

## 第一步永远是

```bash
ssh <别名> cat /root/README.md
```

## 红线

1. **不是 Vultr 机器：不要对它跑任何 vultr-*.sh**（开、删、挂 VPC、查账单都不适用）。不重装、不改系统级设置（sysctl、内核参数、SSH），除非用户点头；归还前按「装了什么」清单清理，除非用户说不用。
2. **不放生产数据、不放任何密钥。** 机主有 root 与控制台，能看到一切：只用 seed 造的虚构数据，云厂商密钥、1P 内容、私有 gitleaks 规则一律不上这台。
3. 结果拉回 `~/ai/projects/pandora/ops-local/<目录>/`，不进仓库。
4. 口令、后台路径前缀只留在机器上和 ops-local，不写进对话、报告。
5. 装的东西在 `/root/README.md`「装了什么」逐条登记（排障与归还时清理用）。到期或用户说不用了，跑 test-machine 的 `unregister.sh <别名>` 撤登记（它不碰机器），本目录保留作记录。
6. 测试端口只监听回环；不对公网提供代理服务。
