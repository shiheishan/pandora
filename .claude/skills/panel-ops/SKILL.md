---
name: panel-ops
description: pandora 已按生产方式装好的面板上的运维操作：管理员账号（aegis-adminctl 建号、改密、授权回收、has-admin）、支付渠道（aegis-payctl）、看后台地址（admin-url.sh）、迁移版本与无效索引（migrate.sh version/status/check-indexes）、面板 HTTPS 证书补救（edge-tls.sh）、加密备份/校验/恢复与 WebDAV 异地备份（backup-postgres.sh、verify-backup.sh、restore-postgres.sh、aegis-backup-webdav）、rollback-to、清单个限流键、服务器解绑、节点接入半途 status/abort（pandora-native enrollment），按只读/可逆/破坏性分级，含 docker 与直装两种布局的差异。用户或总协调说「建管理员」「重置管理员密码」「后台地址是多少」「配易支付」「手动备份/恢复/回滚」「开每日备份」「换上正规证书」「解除绑定」「清某个用户的冷却」这类已经知道要做什么的话时使用。症状说不清原因的（进不来、证书续期失败、打不开）先走 incident-runbook；首装与升级用 panel-install；只查库用 db-query。
---

# 已装面板上的运维操作

目标：知道要做哪件事时，一次拿对命令、环境和成功判据；口令不进命令行和记录；破坏性操作先备份、先问用户。

| 情况 | 去哪 |
|---|---|
| 首装、原地升级、install.sh 本身出错 | panel-install |
| 只读查库、查 Valkey、限流键还剩多久 | db-query（本 skill 只管写） |
| 有症状不知道原因（5xx、节点离线、证书续不上、回调没到账） | incident-runbook；查清后要写操作再回本 skill |
| 机器的开通、登记、删机 | test-machine |
| 迁移失败、rollback-to、从备份恢复的完整步骤 | 机器上的 `deploy/MIGRATION-RUNBOOK.md`（源码 `panel/deploy/MIGRATION-RUNBOOK.md`），本 skill 只给入口与前提 |

登录哪台机器、能做什么，先按根 CLAUDE.md 读 `~/ai/servers/<别名>/AGENTS.md`。下文 `$H` 是 ssh 别名，命令都在机器上以 root 跑。

## 先认布局

| | docker 布局（`install.sh`，存量） | 直装布局（`install-native.sh`，缺省） |
|---|---|---|
| 根目录 `$D` | `/opt/aegispanel` | `/opt/pandora` |
| PostgreSQL | 容器 `aegis-postgres`，以 `POSTGRES_USER` | 系统 PG18（`postgresql@18-main`），以 `postgres` 经 `127.0.0.1:POSTGRES_PORT`（`.env` 的 `POSTGRES_SUPER_PASSWORD`） |
| `deploy/` 下的运维脚本 | 齐全 | 同一套（备份、校验、恢复、`psql.sh`、`bootstrap.sh` 按 `.env` 的 `PANDORA_DB_LAYOUT` 认布局）；2026-10 之前装的直装机器升级一次才有 |
| 升级前备份 / 加密备份目录 | `/var/backups/aegispanel/` | `/var/backups/pandora/` |
| 加密备份单元 | `aegis-backup.service/.timer` 已装，**没启用** | 同左（单元改成直装路径、不依赖 docker） |
| 迁移连接串 | `.env` 有 `AEGIS_MIGRATION_DATABASE_URL`（老 `.env` 没有时才要 `PANDORA_LOCAL_MIGRATION_APPROVED=yes`） | 有 |

判断：`test -f /opt/aegispanel/deploy/.env && echo docker || echo native`（两个都在、且有 `/opt/pandora/deploy/from-docker.state`，是从 docker 迁过来的直装，以直装为准）。两种布局都是 `$D/bin/` 下的二进制（含 goose）、`$D/deploy/.env`（root 0600）。docker 布局迁直装：`install-native.sh --from-docker`（停服几分钟，先问用户），见 `panel/deploy/RUNBOOK.md` 第 13 章。

## 环境怎么给

三类，弄错就是「配置不完整」或「环境文件不可信」：

- **A. 经 `config.Load` 的二进制**（aegis-adminctl、aegis-payctl）：只读进程环境，要在**子 shell** 里导出 `.env`，免得交互 shell 留下全部机密：
  ```bash
  ssh -n $H 'cd /opt/aegispanel && (set -a; . deploy/.env; set +a; ./bin/aegis-adminctl list)'
  ```
  `config.Load` 在解析子命令之前就跑并连库，所以连 `-h` 也要完整配置和连得上的库。
- **B. 自己读 `.env` 的脚本**：直接跑，别先 source。
  - `migrate.sh`、`bootstrap.sh`、`psql.sh` 自己 source；
  - `backup-postgres.sh`、`verify-backup.sh`、`restore-postgres.sh` 要求 `.env` 属 root、单链接、0600/0400、父目录都属 root 不可组写，否则拒绝；
  - `admin-url.sh`、`edge-tls.sh` 逐键读，不 source。
- **C. aegis-backup-webdav**：不走 `config.Load`，只读 `AEGIS_BACKUP_WEBDAV_CONFIG`（缺省 `/etc/aegispanel/backup-webdav.json`，样例 `deploy/backup-webdav.example.json`），必须 root。

口令：只经 `--password-stdin` 从 0600 文件喂，或现场 `read -rs`；不写进命令行参数、环境变量、日志。机器上的现成写法见 install.sh 收尾提示的 `create_cmd`。口令要至少 8 个字符、同时含字母和数字（`crypto.ValidatePassword`）。

## 命令表

路径按 docker 布局写，直装把 `/opt/aegispanel` 换成 `/opt/pandora`。环境列的 A/B/C 见上节。

### 只读：随时可跑

| 要做什么 | 命令（在 `$D` 下） | 环境 | 成功判据 / 退出码 |
|---|---|---|---|
| 看后台完整地址 | `deploy/admin-url.sh` | 无 | 0 打印 URL；1 缺键或读不了。输出就是入口，不贴进对话和报告，要留存写进 0600 文件 |
| 列管理员、看角色码 | `bin/aegis-adminctl list`、`roles` | A | 0；`list` 只列持有未过期角色的账号 |
| 有没有能登录的管理员 | `bin/aegis-adminctl has-admin` | A | 0 有；**3 没有**；1 是连不上库或配置错，不等于没有 |
| 看支付渠道 | `bin/aegis-payctl list --tenant 00000000-0000-7000-8000-000000000001` | A | 0；`CREDS` 只说密文列非空，不解密，证明不了主密钥对 |
| 迁移版本 / 每个迁移的状态 | `GOOSE_BIN=$D/bin/goose deploy/migrate.sh version`（或 `status`） | B | 0；1 缺 `.env`、迁移目录或 goose；78 命令被拒 |
| 有没有半成品索引 | `GOOSE_BIN=$D/bin/goose deploy/migrate.sh check-indexes` | B | 0 没有；1 有（打印逐个 `DROP INDEX CONCURRENTLY`）；2 查不了 |
| 面板 HTTPS 证书 | `deploy/edge-tls.sh status` | 无 | 证书来源 `lego`（IP）/`certbot`（域名）/`selfsigned`/`custom`（运维自备）/`none`（还没走 HTTPS 边缘）、到期、上次续期结论 |
| 备份能不能解开 | `deploy/verify-backup.sh /var/backups/pandora/aegis-postgres-<时间>.dump.age` | B | 0 且输出 `backup verified:`。先核来路（本地封条 `.seal` 或 WebDAV 签名清单），见「备份与恢复」 |

### 可逆：会写，但能撤回或无害

| 要做什么 | 命令 | 环境 | 成功判据 / 注意 |
|---|---|---|---|
| 建管理员 | `sed -n 2p <0600 凭据文件> \| bin/aegis-adminctl create --email <邮箱> --password-stdin [--role platform_admin]` | A | 0，打印 `管理员已就绪`。邮箱已存在时是「重新激活 + 改密 + 授角色 + 吊销全部会话」，不是报错 |
| 管理员忘了密码 | 同上换成 `reset-password --email <邮箱> --password-stdin` | A | 0，`全部既有会话已失效`。只对有效管理员；普通用户走门户找回 |
| 授权 / 临时提权 / 回收 | `bin/aegis-adminctl grant --email <邮箱> --role <码> [--hours N]`；`revoke …` | A | 0。回收最后一个有效管理员会被守卫拒绝（`iamguard`） |
| 配易支付 | 首选后台「订单与收款 › 支付渠道」。命令行 `bin/aegis-payctl upsert-epay --tenant <租户> --base-url https://… --merchant … --key … [--methods alipay,wxpay] [--enable]` | A | 0，`已新建` / `已更新`。`--key` 会进进程表，见「坑」 |
| 立刻做一份加密备份 | `deploy/backup-postgres.sh` | B | 0 且最后一行 `backup complete: <路径>`。顺手按 `AEGIS_BACKUP_RETENTION_DAYS` 删旧备份 |
| 开每日加密备份 | `systemctl enable --now aegis-backup.timer` | 无 | `systemctl list-timers aegis-backup.timer` 有下次时间（每天 03:17 UTC 起随机 45 分钟） |
| 换上正规证书 / 补跑续期 | `deploy/edge-tls.sh issue`；`renew` | 无 | issue：0 正规证书，3 仍自签，1 出错且 nginx 保持原样；renew：0 / 1（结论写 `/var/lib/aegispanel/tls/status`）。撞 Let's Encrypt 限额就停 |
| 重新渲染 nginx | `deploy/edge-tls.sh apply`（整套用 `setup`） | 无 | 0；`nginx -t` 不过不换 |
| 站点在 Cloudflare 后 | `deploy/update-cloudflare-realip.sh && nginx -t && systemctl reload nginx` | 无 | 0。不在 Cloudflare 后别跑：会让客户端自填来源 IP |
| 重新收窄运行角色 | `deploy/bootstrap.sh` | B | `aegis_app runtime login configured`。恢复库后必跑 |
| 开 WebDAV 异地备份 | ① `bin/aegis-backup-webdav init-signing-key /etc/aegispanel/backup-manifest-ed25519.seed`（打印公钥；已存在就拒绝，不覆盖）② 写 0600 的 `/etc/aegispanel/backup-webdav.json` 与口令文件 ③ `.env` 设 `AEGIS_BACKUP_REMOTE_HOOK=$D/bin/aegis-backup-webdav` | C | 下次备份输出 `webdav backup upload complete`。失败只打一个原因词（`probe_failed`、`target_invalid`…），退出 1 |
| 节点装到一半看接入 | 节点机上 `pandora-native enrollment status --identity /etc/pandora-native/identity.json` | 节点机 root | 打印 `enrollment state`；`committed` 时顺手把待定身份转正（写文件） |

### 破坏性：先备份、先问用户

每条都按「破坏性操作的顺序」做。

| 要做什么 | 入口 | 前提与判据 |
|---|---|---|
| 从加密备份恢复 | `restore-postgres.sh --archive <绝对路径> --target-db <库>`，三道确认变量见 MIGRATION-RUNBOOK 第 3 节 | 先在临时库完整恢复一遍才动目标库；覆盖正式库前三个网关与备份单元必须全停。0 且 `restore complete:`。先恢复到别名库（只要 `AEGIS_RESTORE_CONFIRM`）核对，再决定是否覆盖正式库 |
| 回到旧迁移版本 | `migrate.sh rollback-to <版本>`，见 MIGRATION-RUNBOOK 第 2 节 | 用**新**发布的 migrate.sh；要 `PANDORA_ROLLBACK_WRITERS_STOPPED=yes`、`PANDORA_ROLLBACK_BACKUP=<刚做的备份>`、确认短语 `rollback <当前> to <目标>`。拒绝一律退出 78，一个 Down 都不执行 |
| 迁移失败后手工重跑 `up` | MIGRATION-RUNBOOK 第 1 节 | 先 `check-indexes` 为 0；不带预检凭据时它当场做完整预检 |
| 清掉某个用户的冷却 | `ssh $H "bash -s -- cli DEL '<完整键名>'" < .claude/skills/db-query/scripts/valkey.sh` | 只删一个、键名从 `valkey.sh rl <维度>` 原样复制（含冒号和 `\|`，所以远端要再套一层单引号）。valkey.sh 头注释写着只读，那是 db-query 的口径；这里是用户同意后的单键例外。`clear-ratelimit.sh` 只给开发基座 |
| 解除服务器绑定 | 后台接口 `POST /<后台前缀>/v1/servers/{id}/unbind`（`node.identity.revoke` + 近期重认证；前端还没有这个页面） | 同一事务吊销身份、中止进行中的接入、作废未用令牌；机器要重新签令牌、重新接入 |
| 删托管证书、删 DNS 凭据 | 后台「证书」页，见 `docs/node-certificates.md` 第 3.5、3.7 节 | 删证书连同全部版本与私钥密文；有证书在用的凭据删不掉；都要近期重认证 |
| 节点接入作废 | 节点机上 `pandora-native enrollment abort --identity /etc/pandora-native/identity.json --reason "<原因>"` | 只用于 begin 之后、commit 之前卡住的接入 |
| 迁到新主机 | `panel/deploy/migrate-to-new-host.sh` | 不在发布包里，且已过时（见「坑」）。用之前先问用户 |

## 破坏性操作的顺序

1. **问**：说清命令、影响面（哪些数据会回到哪一刻、谁会被踢下线）、能不能撤回，等用户明确同意。一次同意只管这一次。
2. **备份**：`backup-postgres.sh` 后跑 `verify-backup.sh` 验过（两种布局同一套）；还没升级到带这些脚本的老直装机器，照安装器的做法 `runuser -u postgres -- pg_dump -Fc -d aegis -p <端口>` 到 `/var/backups/pandora/`（0600），再 `pg_restore --list` 验能读。记下路径。
3. **停写入者**（恢复、回滚要）：`systemctl stop nginx aegis-public aegis-admin aegis-node`。
4. **执行**：输出 `| tee` 进 0600 日志，不贴原文（含邮箱、库名、路径以外的值时脱敏）。
5. **核对**：`migrate.sh version`、三网关 `127.0.0.1:9000/9001/9003/healthz`、后台能登录；恢复后先跑 `bootstrap.sh` 再起服务。

## 备份与恢复：几处容易误会

- **装好后没有每日备份。** 单元装了但没启用，要 `enable --now aegis-backup.timer`（两种布局都是；老直装机器升级一次才有这个单元）。
- **解密私钥和备份在同一台机器**（`install.sh` 首装生成到 `$D/secrets/backup-age.key`，`.env` 的 `AEGIS_BACKUP_AGE_IDENTITY` 指向它）。机器整体丢失时备份解不开，异地备份要连同这把私钥另存（经用户、走 1Password）。
- **备份先核来路，再恢复。** `verify-backup.sh`（`restore-postgres.sh` 自己会调）只认两种：
  - **本机写的备份**：`backup-postgres.sh` 每份都写封条 `aegis-postgres-<时间>.dump.age.seal`，是用这台安装的 age 私钥派生的签名。留存期内哪一份都能核、能恢复。封不上（私钥读不了）时这次备份算失败，刚写的归档会被撤掉；后台概览只把带封条的算作最新备份。
  - **从 WebDAV 取回的备份**：签名清单 `aegis-postgres-<时间>.manifest.json` 加 WebDAV 之外的可信检查点（`backup-webdav.json` 的 `manifest_checkpoint_file`），只认最新一份。恢复前把清单取回放到备份旁边，`.env` 设好 `AEGIS_BACKUP_MANIFEST_PUBLIC_KEY`、`AEGIS_BACKUP_TRUSTED_CHECKPOINT`。
  - 两样都没有报 `refusing an archive of unknown origin`；封条对不上报 `local backup seal verification failed`。确认来历可信的，按 MIGRATION-RUNBOOK 第 3 节「加密备份手工恢复到新库」走手工路线，然后 `bootstrap.sh`。
- **备份私钥文件要原样另存，重装后放回原处。** 封条认的是私钥本身（只取 `AGE-SECRET-KEY-1…` 那一行，注释、换行、大小写不影响），换了私钥，旧备份既解不开、也核不过封条。**本机只放公钥（私钥移走）的部署不支持本地备份**：封不上，每次备份都失败。
- 升级前备份 `pre-upgrade-*.dump` 是**未加密**的 `pg_dump -Fc`，只用手工路线恢复。
- 恢复失败会 **FAIL-CLOSED**：三个网关与备份单元被 `systemctl mask --runtime`，目标库 `CONNECTION LIMIT 0`。修好原因后要 `systemctl unmask --runtime` 这几个单元，并 `ALTER DATABASE <库> CONNECTION LIMIT -1`，否则服务起不来、库连不上。这一步也先问用户。

## 证书、服务器绑定与节点接入

- **面板自己的 HTTPS 证书**只归 `edge-tls.sh`，流程与续期细节见 panel-install「坑」。改对外地址（域名与 IP 互换）走重跑 install.sh，不在这里。
- **节点用的托管证书、DNS 凭据、ACME 设置**只在后台「证书」页操作，没有命令行。凭据只写不读，编辑时密钥框留空表示不改。回滚 00147 会删掉已录入的 DNS 凭据。详见 `docs/node-certificates.md` 第 3 节。
- **服务器级绑定只落地了面板侧（P1）**：
  - 后台接口有签令牌 `binding-token`、看状态 `binding`、解绑 `unbind`，前端还没有页面；nginx 还没把 `/v1/servers/` 转给 aegis-node，经公网入口是 404。
  - 机器侧的 `pandora-native bind` 还没实现。**不要在已装 pandora-native 的机器上跑后台生成的「绑定命令」**：`bind` 不是已知子命令，会按普通启动读 `/etc/pandora-native/config.json`，在前台再起一个内核。
- **按节点接入（现行）** 由节点装机脚本自动做 begin → commit。手工只在装到一半时用 `enrollment status/abort`，身份文件是 `/etc/pandora-native/identity.json`，不是二进制的缺省 `/var/lib/pandora-native/identity.json`。

## 坑

- **`aegis-payctl --key` 把商户密钥放进命令行**（进程表、shell 历史），这个命令没有 stdin 入口。真实密钥一律在后台录入；命令行只用于测试商户，或更新时不带 `--key`（留空沿用库里那份）。
- **`has-admin` 只有 3 表示「没有」。** 安装器也只在 3 时才问。
- **`clear-ratelimit.sh`** 只给开发基座：只认 docker 容器、删光全部 `rl:*`（所有用户的冷却一起没了）、口令经 `-a` 进命令行。测试机和生产上不用它。
- **`migrate.sh` 不给 `GOOSE_BIN`** 会找 `/root/go/bin/goose`，找不到退出 1。发布包自带的在 `$D/bin/goose`。`down`、`redo` 永远退出 78，回退只能 `rollback-to`。
- **`psql.sh` 两种布局都有**（老直装机器升级一次才有），以超级用户连，多语句经标准输入送，细节见 db-query。
- **`migrate-to-new-host.sh` 已过时**，用前先问用户：
  - 不在发布包里，只在源码树；
  - 不跑 `edge-tls.sh`，不装备份和续期单元，新机器没有证书续期；
  - 第 9 步的「主密钥迁移正确」检查一定失败：它 `. .env` 时没有 `set -a`，aegis-payctl 拿不到配置；而且 `payctl list` 本来就不解密凭据；
  - 收尾提示的回调地址 `http://<IP>:9080/...` 是只绑回环的运维口，不能填给支付商。
- **`aegis-backup-webdav` 的失败只有一个原因词**，不带细节：先看 JSON 配置（未知字段会被拒）、文件属主与权限（都要 root、私有），再看网络。
- 运维输出里有邮箱、订阅 id、订单号、后台前缀：不贴进仓库、报告和对话，要留存放 `ops-local/<目录>/`（0600）。
