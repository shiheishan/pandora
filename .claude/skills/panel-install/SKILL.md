---
name: panel-install
description: pandora 在一次性测试机上按生产方式（panel/deploy/install.sh + build-release.sh 的发布包）首装或原地升级面板，做到后台能登录、三网关 healthz 200、https 可访问为止。用户或总协调说「在测试机装面板」「升级测试机上的面板」「按生产方式部署验证」「验证 install.sh」「验证升级链」时使用。压测与 seed 不在这里，见 prod-retest。
---

# 在测试机上按生产方式装 / 升级面板

终点：**后台能登录、三网关 healthz 200、https 可访问**。再往后（压测工具、realip、seed 数据、压测）是 prod-retest 的事。

## 前提与红线

- 机器已按 test-machine skill 开通登记（别名、ssh、`/root/README.md`）。下文 `$H` 指这台机器的 ssh 别名。
- 红线（仓库公开、现场值与口令的放置、测试机不删不重装）见根 CLAUDE.md「红线」与 test-machine skill。
- 本地发现安装链的问题，记下来交总协调派任务，不在测试机上临时改仓库脚本。
- 远端起长命令（构建约 10 分钟）的写法与 1Password 签名失败的处理见根 CLAUDE.md「环境与工具坑」。

## 首装

1. **工具链**（要在机器上构建时才装）：见 test-machine「开通」的 `install-toolchain.sh`。
2. **源码**：本机 `git archive --prefix=pandora-<sha>/ <sha> > <临时目录>/p.tgz`，scp 后解到 `$H:/root/src/`。
3. **构建**：`cd /root/src/pandora-<sha>/panel && PANDORA_VERSION=vt-<sha> bash deploy/build-release.sh /root/release`。2c4g 约 10 分钟。
   - 产物：`/root/release/pandora-panel_<版本>_linux_{amd64,arm64}.tar.gz`，旁边有 `.sha256` 与 `.manifest.sha256`。
   - 约束见「坑」：只能在 Linux 上跑、要在 `panel/` 下、显式给版本号。
4. **前置包**（Debian 主仓）：`apt-get update && apt-get install -y docker.io docker-compose age nginx certbot`。ufw 不用手工放行，install.sh 在 ufw active 时自己放行 80/443。
5. **校验并解包**：
   - 在 `/root/release/` 里 `sha256sum -c *_linux_amd64.tar.gz.sha256`；
   - 解到 `/opt/pandora-release/`，进包目录执行 `sha256sum -c SHA256SUMS`；
   - **保留这个目录**：之后原地升级、重装数据基座都要再跑它的 install.sh。
6. **安装**：通用步骤（安装命令、`PANDORA_CERTBOT` 的含义、nginx 渲染）照 `panel/tools/loadtest/README.md` 第 2 节第 2、3 步。测试机特有的：进包内 `deploy/`，`PANDORA_ASSUME_YES=1 PANDORA_CERTBOT=1 PANDORA_PUBLIC_BASE_URL=https://<域名> ./install.sh > /root/install-1.log 2>&1`，再 `chmod 600` 这个日志（结尾会打印后台前缀）。
   - 域名必须是 DNS 域名，不能是裸 IP 或 localhost（production 模式的硬要求）；没有域名就用 `<IP 用横线>.sslip.io`。
7. **核对安装结果**：
   - 日志里「迁移版本 0 → N」，N 等于包内 `migrations/` 的最大号；
   - 三网关 `127.0.0.1:9000/9001/9003` 的 `/healthz` 都是 200（`aegis-public / admin / node` 三个服务 active）；
   - 日志里「nginx 配置已渲染并生效」。若提示没有证书或被跳过，按 loadtest README 第 2 节第 3 步补一次 `render-nginx`。
8. **https 可访问**：`https://<域名>/healthz` 返回 200；`http://` 跳转 308；`https://<域名>/<后台前缀>/` 返回 200。前缀用机器上的 `/opt/aegispanel/deploy/admin-url.sh` 取。
9. **建管理员**：install.sh 首装在交互终端里会现场建第一个管理员，本 skill 的无人值守装法（`PANDORA_ASSUME_YES=1`、日志重定向）下它跳过，按下面手工建。
   - 在机器上生成随机口令写进 `/root/lt-admin-cred.txt`（0600，第 1 行邮箱、第 2 行口令），后台前缀（用 `admin-url.sh` 取）写进 `/root/lt-admin-path.txt`（0600）。
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
   给每行打时间戳，是为了量停服时间。检测到 `/opt/aegispanel/deploy/.env` 就自动走升级：保留 `.env` 与数据，不改 `AEGIS_ENV`。证书已在，不需要再加 `PANDORA_CERTBOT=1`。
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

- **证书与 Debian 默认站点**：Debian 的 nginx 包自带默认站点，也 listen 80 default_server，会和 `aegis.conf` 抢，`nginx -t` 报 duplicate default server。install.sh 在拿到证书后、渲染前，只停用发行版原样的那个链接（原文件留着，可 `ln -s ../sites-available/default` 链回）。没有域名用 sslip.io；撞上 Let's Encrypt 限额就停下报告。
- **网关经 unix socket 连 PG 与 Valkey**（`deploy/run/`）：复测时确认真走了 socket：`SELECT client_addr IS NULL AS unix, count(*) FROM pg_stat_activity WHERE usename='aegis_app' GROUP BY 1`。socket 不可用时 install.sh 保持回环端口；`.env` 已是 socket 而 socket 起不来，网关起不来，install.sh 会停下。要回退到更早的发布包，先按 `.env` 末尾的注释把 `AEGIS_DATABASE_URL`、`AEGIS_REDIS_URL` 改回回环形式（口令不变）。
- **`build-release.sh` 的两个约束**：
  - 在 Linux 上、在 `panel/` 下跑；
  - 要显式给版本号：`git archive` 解出来的源码没有 `.git`，缺省版本是 `dev`，追不到提交。
- **install 日志里有后台前缀**：保存后立刻 `chmod 600`，不要 `cat` 到对话里。
- 首装对外地址不合规（IP、带端口、带路径）会在动手前就停，不会装到一半。
