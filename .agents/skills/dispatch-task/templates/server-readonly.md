## 服务器只读红线

（派给 Composer 的服务器只读活，把本节原样抄进 brief，填好尖括号。Composer 读不到全局规则 `~/.claude/CLAUDE.md`，红线只能靠这一节。`cursor-launch.sh --dir` 会检查 brief 里有这个标题。会改动服务器的活不交 Composer，见根 CLAUDE.md「大任务拆子 agent」。）

**只许登录**：<别名 1>、<别名 2>…。**禁止登录**：清单外的任何机器，特别是 <别的业务的生产机、别的路正在压测的机器的别名>。

违反下面任何一条就立刻停下，写进报告：

1. 动手前先读 `~/ai/servers/README.md`，再读每台的 `~/ai/servers/<别名>/AGENTS.md`，然后读机器上的 `/root/README.md`。三处写的规矩与本 brief 冲突时，以更严的为准。
2. **只读**：不改任何文件，不启停服务，不 kill 进程，不改 sysctl / iptables / ufw，不装软件，不 apt，不重启。允许的命令只有查看类：`uptime`、`free -m`、`df -h`、`ps`、`ss -s`、`systemctl list-units --failed`、`systemctl is-active <单元>`、`systemctl list-timers`、`crontab -l`、`ls`、`du -sh`、`cat` / `head` / `tail`（只读文本文件）、`sysctl <键>`、`iptables -S`、`ufw status`。<本次另外允许的只读命令，没有就删掉这句>
3. ssh 只用 `ssh -n -o BatchMode=yes <别名> '<命令>'`：不用密码，不加 `-o StrictHostKeyChecking=no`；连不上就记下来，同一台重试不超过 3 次。
4. **不读、不输出任何秘密**：不 cat `.env`、`*secret*`、`*.key`、`*cred*`、`/etc/pandora*/` 下的凭证文件、`~/.ssh/`；不读本机 `ops-local/**/secrets/`。看到口令、令牌、私钥类内容，不抄进任何文件。
5. **IP 和域名不写进任何文件**：报告里只用别名；命令输出里的公网或内网 IP、域名，写进文件前换成 `<ip>`、`<domain>`。
6. 本机只许写 `<本次目录（主目录 ops-local/ 下）>`；不改 git 仓库里的任何文件，不提交、不推送。
7. 不从机器上下载文件，只列出来（路径、大小、修改时间）。
8. 报告末尾如实写：跑过的命令清单；连不上或被拒的命令；有没有违反或接近违反红线的地方（例如差点读到秘密文件）。
