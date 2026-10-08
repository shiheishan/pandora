---
name: panel-install
description: pandora 在一次性测试机上按生产方式（panel/deploy/install.sh + build-release.sh 的发布包）首装或原地升级面板，做到后台能登录、三网关 healthz 200、https 可访问为止。用户或总协调说「在测试机装面板」「升级测试机上的面板」「按生产方式部署验证」「验证 install.sh」「验证升级链」时使用。压测与 seed 不在这里，见 prod-retest。
---

# 在测试机上按生产方式装 / 升级面板

终点：**后台能登录、三网关 healthz 200、https 可访问**。再往后（压测工具、realip、seed 数据、压测）是 prod-retest 的事。

## 前提与红线

- 机器已按 test-machine skill 开通登记（别名、ssh、`/root/README.md`）。下文 `$H` 指这台机器的 ssh 别名。
- 仓库公开红线见根 CLAUDE.md；现场值放 `~/.ssh/config`、`~/ai/servers/`、`ops-local/<目录>/`，口令只经 stdin 或 0600 文件传。不删、不重装测试机，不读 `ops-local/**/secrets/`。
- 本地发现安装链的问题，记下来交总协调派任务，不在测试机上临时改仓库脚本。
- 远端起长命令（构建约 10 分钟）的写法与 1Password 签名失败的处理见根 CLAUDE.md「环境与工具坑」。

## 首装

1. **工具链**（要在机器上构建时才装）：`ssh $H 'bash -s' < .claude/skills/test-machine/scripts/install-toolchain.sh`。装与 CI 同小版本的 Go（go.mod 的 1.26 系列最新版）和 Node 22。
2. **源码**：本机 `git archive --prefix=pandora-<sha>/ <sha> > <临时目录>/p.tgz`，scp 后解到 `$H:/root/src/`。
3. **构建**：`cd /root/src/pandora-<sha>/panel && PANDORA_VERSION=vt-<sha> bash deploy/build-release.sh /root/release`。2c4g 约 10 分钟。
   - 产物：`/root/release/pandora-panel_<版本>_linux_{amd64,arm64}.tar.gz`，旁边有 `.sha256` 与 `.manifest.sha256`。
   - 约束见「坑」：只能在 Linux 上跑、要在 `panel/` 下、显式给版本号。
4. **前置包**（Debian 主仓）：`apt-get update && apt-get install -y docker.io docker-compose age nginx certbot`。ufw 不用手工放行，edge-tls.sh 在 ufw active 时自己放行 80/443。
   - 只用 IP 时证书靠 lego 4.22+（Debian 13 主仓的 certbot 4.0 不支持 IP 证书，主仓 lego 4.9 不支持 ACME profiles）：不用手装，edge-tls.sh 在 Debian 上自动启用官方 `<版本>-backports` 源并 `apt-get install -t <版本>-backports lego`。
5. **校验并解包**：
   - 在 `/root/release/` 里 `sha256sum -c *_linux_amd64.tar.gz.sha256`；
   - 解到 `/opt/pandora-release/`，进包目录执行 `sha256sum -c SHA256SUMS`；
   - **保留这个目录**：之后原地升级、重装数据基座都要再跑它的 install.sh。
6. **安装**：进包内 `deploy/`，`PANDORA_ASSUME_YES=1 ./install.sh > /root/install-1.log 2>&1`，再 `chmod 600` 这个日志（结尾会打印后台前缀）。无人值守的全部变量见 install.sh 头注释。
   - 面板默认走 HTTPS：不给 `PANDORA_PUBLIC_BASE_URL` 时用本机公网 IPv4（`https://<IP>`），自动申请 Let's Encrypt IP 证书（shortlived，约 6 天，`aegis-tls-renew.timer` 每天两次续）；要验域名就加 `PANDORA_PUBLIC_BASE_URL=https://<域名>`（certbot 域名证书）。不再需要 sslip.io。
   - 对外地址只收 `https://DNS 域名` 或 `https://公网 IPv4`；localhost、私网 IP、IPv6 字面量、带端口或路径在动手前就停。
   - 同一台机器反复重装时，为免撞 Let's Encrypt 限额（同一 IP 每 7 天 50 张）可加 `PANDORA_ACME_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory` 走 staging（证书不受信任，第 8 步改用 `curl -k`）；最终验收用正式环境。
   - 证书申请失败（80 被安全组挡住等）不算安装失败：自签兜底，日志里有原因与 `edge-tls.sh issue` 补救命令。
7. **核对安装结果**：
   - 日志里「迁移版本 0 → N」，N 等于包内 `migrations/` 的最大号；
   - 三网关 `127.0.0.1:9000/9001/9003` 的 `/healthz` 都是 200（`aegis-public / admin / node` 三个服务 active）；
   - 日志里「nginx 配置已渲染并生效」「已换上 Let's Encrypt 证书」「续期 timer 已启用」；`/opt/aegispanel/deploy/edge-tls.sh status` 显示证书来源 `lego`（IP）或 `certbot`（域名）。若是 `selfsigned`，按日志里的原因修好后 `edge-tls.sh issue`。
8. **https 可访问**（`<地址>` 是 IP 或域名）：不带 `-k` 的 `curl https://<地址>/healthz` 返回 200（证书受信任）；`http://<地址>/x` 跳转 308 到 https；`http://<地址>/.well-known/acme-challenge/x` 是 404 而不是跳转；`https://<地址>/<后台前缀>/` 返回 200。前缀在 `/opt/aegispanel/deploy/.env` 的 `AEGIS_ADMIN_PATH`。`systemctl list-timers aegis-tls-renew.timer` 有下次运行时间。
9. **建管理员**：install.sh 不替人生成管理员。
   - 在机器上生成随机口令写进 `/root/lt-admin-cred.txt`（0600，第 1 行邮箱、第 2 行口令），`AEGIS_ADMIN_PATH` 写进 `/root/lt-admin-path.txt`（0600）。
   - 建管理员的命令见 loadtest README 第 2 节第 4 步，口令经 stdin（`printf %s "$pw" | … --password-stdin`），邮箱用 `ltadmin@example.com`：它是 prod-retest 的 run-load 缺省值，要压测就别换。
   - 两个文件 scp 回 `ops-local/<目录>/`，保持 0600。
   - 用这个账号登录一次后台（浏览器或后续 seed 的自检），登得进才算「后台能登录」。
10. 在机器 `/root/README.md` 补「现状」：版本、目录（`/opt/aegispanel`、`/opt/pandora-release`、`/root/release`）、日志与口令文件位置（只写位置不写值）。

## 原地升级

场景：同一台机器上换更新的发布包，验证升级链。

1. 照首装 2、3、5 做出新版本：**新的版本号、新的解包目录**，不覆盖旧目录。
2. 进新包的 `deploy/` 再跑一次 install.sh，不用加 `PANDORA_PUBLIC_BASE_URL`：
   ```bash
   set -o pipefail
   PANDORA_ASSUME_YES=1 ./install.sh 2>&1 | while IFS= read -r l; do printf '%s %s\n' "$(date -u +%T)" "$l"; done > /root/install-2.log
   ```
   给每行打时间戳，是为了量停服时间。检测到 `/opt/aegispanel/deploy/.env` 就自动走升级：保留 `.env` 与数据，不改 `AEGIS_ENV`。
   - HTTPS：已有 `aegis.conf` 的面板照常重渲染；以前 certbot 申请的 `/etc/letsencrypt/live/<域名>` 由 edge-tls.sh 直接接管（不再申请），nginx 改读 `/etc/aegispanel/tls/live/`，并启用续期 timer。
   - 从没走过 nginx 边缘的旧面板（没有 `aegis.conf`）升级时不替人接管 80/443，收尾提示切换命令：`PANDORA_ACME=1 ./install.sh`。要从域名换成 IP（或反过来），改 `.env` 的 `AEGIS_PUBLIC_BASE_URL` 后重跑 install.sh。
3. 它自己做的事（顺序即日志顺序；顺序与失败处置以 `panel/deploy/install-lib.sh` 的 `pandora_run_migrations` 为准）：
   - **升级前备份**：`pg_dump -Fc` 到 `/var/backups/aegispanel/pre-upgrade-<时间>.dump`（0600），并验过能读。备份失败就中止，此时服务没停、迁移没跑；
   - 起数据基座，确认 unix socket 可用后换连接串；
   - **停服之前**在一次性克隆库上演练迁移（服务照常在跑，库越大越慢），日志「预检通过（N 秒），凭据已写好；停服后只核对凭据，不再演练」；预检失败则服务没停、数据库没动；
   - **停服**（日志「停止服务后迁移」）：只核对预检凭据，再迁移；迁移失败会把服务拉回来；
   - 收窄数据库角色、装二进制与 systemd 单元、启动、健康检查、重渲染 nginx。
4. **停服时间**：日志里「停止服务后迁移」到「启动服务」之间的时间戳之差（预检不在这段里）。成绩里同时写迁移条数和库大小，否则这个数没法比。
5. **迁移号核对**：日志「迁移版本 M → N」，M 等于升级前（`SELECT max(version_id) FROM goose_db_version`，用 `/opt/aegispanel/deploy/psql.sh`），N 等于新包 `migrations/` 的最大号。
6. 照首装 7、8 重新核对 healthz 与 https；再登录一次后台。
7. 升级前后各留一份 `systemctl is-active aegis-public aegis-admin aegis-node` 与版本号（后台登录页或侧栏显示的发布版本）写进现场记录。

## 坑

- **证书与 Debian 默认站点**：Debian 的 nginx 包自带默认站点，也 listen 80 default_server，会和 `aegis.conf` 抢，`nginx -t` 报 duplicate default server。edge-tls.sh 渲染前只停用发行版原样的那个链接（原文件留着，可 `ln -s ../sites-available/default` 链回）。撞上 Let's Encrypt 限额就停下报告。
- **证书的流程**（edge-tls.sh setup）：没有证书先自签让 nginx 起来 → 渲染 nginx（80 只放 `/.well-known/acme-challenge/` 与 308 跳 https）→ certbot / lego 经 webroot `/var/www/aegis-acme` 申请 → 换 `/etc/aegispanel/tls/live` 链接 → reload。续期结论在 `/var/lib/aegispanel/tls/status`，`journalctl -u aegis-tls-renew` 看输出。节点用 https 接入面板要正规证书：自签兜底期间节点接入会校验失败。
- e65faec 之前的发布包没有自动配 nginx 这套（不申请证书、不放行 ufw、不渲染 nginx），要手工：趁默认站点占着 80 先 `certbot certonly --webroot -w /var/www/html -d <域名> --non-interactive --agree-tos --register-unsafely-without-email`，再删默认站点，再跑 install.sh 和 `render-nginx.sh`。
- **e65faec 起网关经 unix socket 连 PG 与 Valkey**（`deploy/run/`）：复测时确认真走了 socket：`SELECT client_addr IS NULL AS unix, count(*) FROM pg_stat_activity WHERE usename='aegis_app' GROUP BY 1`。socket 不可用时 install.sh 保持回环端口；`.env` 已是 socket 而 socket 起不来，网关起不来，install.sh 会停下。要回退到更早的发布包，先按 `.env` 末尾的注释把 `AEGIS_DATABASE_URL`、`AEGIS_REDIS_URL` 改回回环形式（口令不变）。
- **`build-release.sh` 的三个约束**：
  - 只能在 Linux 上跑（要 GNU tar、`sha256sum`），本机 macOS 不行；
  - 要在 `panel/` 目录下执行：它用绝对路径 `go build $ROOT/cmd/...`，cwd 不在模块里会失败；
  - 要显式给版本号：`git archive` 解出来的源码没有 `.git`，缺省版本是 `dev`，追不到提交。`archive/` 开头的标签把版本号弄成非法的问题，脚本现在用 `git describe --match 'v*'` 避开了，但显式版本号仍是对的做法。
- **install 日志里有后台前缀**：保存后立刻 `chmod 600`，不要 `cat` 到对话里。
- 首装对外地址不合规（私网 IP、IPv6 字面量、带端口、带路径）会在动手前就停，不会装到一半。
