# <别名> · 服务器工位

> 进这个目录 = 要管这台服务器。**先读完本文件再动手。** 机器上有什么以服务器上的 `/root/README.md` 为准。

## 身份卡

| 项 | 值 |
|---|---|
| 名字 | `<别名>` |
| IP | <IP> |
| 商家 / 机房 | Vultr，新加坡；<套餐>（共享型），自动备份关 |
| 系统 | Debian 13 x64，时区 UTC，北京 = 本机 + 8h |
| 用途 | <一句话：pandora 的哪项测试、装什么、和哪台配合> |
| 日常运维 | Claude Code 的 pandora 总协调会话（主目录 `~/ai/projects/pandora`，清单 `.claude/TASKS.md`） |
| 代码 | pandora 仓库（本地 clone `~/ai/projects/pandora`）；压测 runbook 是 `panel/tools/loadtest/README.md` |

**一次性测试机**，<日期> 开机，用完即删。不进 git 仓库，机器上的 `/root/README.md` 只是说明，不版本化。

## 登录

```bash
ssh <别名>
```

- 只认公钥：开机时由 Vultr 注入 `~/.ssh/id_ed25519.pub`，私钥在 1Password，由 1Password SSH agent 签名。
- 不在 1Password 建条目；root 密码需要时到 Vultr 控制台该机概览页看，不进对话、不落文件。
- **不要**改 `authorized_keys`、`sshd_config`，不生成新密钥，不读私钥。

## 第一步永远是

```bash
ssh <别名> cat /root/README.md
```

## 红线

1. **一次性测试机，用完即删。** 删机只能由用户在 Vultr 控制台操作，agent 不删、不重装、不改套餐。
2. **不放生产数据。** 只用 seed 造的虚构数据。
3. 结果拉回 `~/ai/projects/pandora/ops-local/<目录>/`，不进仓库。
4. 口令、后台路径前缀只留在机器上和 ops-local，不写进对话、报告。
5. 删机后从 `~/ai/servers/README.md` 总表和 `~/.ssh/config` 移除本机，本目录保留作记录。
