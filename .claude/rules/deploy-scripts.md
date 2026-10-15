---
paths:
  - "panel/deploy/**"
---

# 部署脚本与模板

- 部署参数不硬编码：脚本与模板不出现真实部署的域名、前缀与密钥。安装时生成并写入 `/etc/pandora/config.yaml`（公共配置）、`/etc/pandora/secrets.env`（服务机密，`root:pandora` 0640）与 `/etc/pandora/deploy.env`（部署变量，`root:root` 0600）。
  - nginx 模板 `nginx-pandora.conf` 只含占位符 `__PANDORA_DOMAIN__`、`__PANDORA_ADMIN_PATH__`；`config.example.yaml` 与 `secrets.env.example` 只放示例值。守卫：`render-nginx_test.sh`。
  - 维护者个人服务器的一次性脚本放仓库根被忽略的 `ops-local/`，不进 `panel/deploy/`。
- 生产两种装法，共用同一个二进制、同一套 `PANDORA_*` 配置键与迁移：
  - 裸机 `install.sh`（系统包 PostgreSQL 18 + Redis，安装至 `/opt/pandora`，备份在 `/var/backups/pandora`）。单二进制 `/opt/pandora/bin/pandora`，两个常驻服务 `pandora-serve`（Web :9000/:9001 与节点 gRPC :9443）与 `pandora-worker`。守卫：`install_mock_test.sh`。
  - Docker Compose（含 1Panel 导入）：`panel/deploy/docker/`，见文末「Docker」一节。
- 失败码在命令那一行取：`cmd || rc=$?`（或 `out=$(cmd) || rc=$?`）。写成 `if cmd; then …; fi; rc=$?` 时 `$?` 是 if 语句自己的退出码 0，会把失败当成功（10-13 安装器真 bug：`pandora_migrate`、`pandora_bootstrap_db`，见 `panel/deploy/install-lib.sh`）。
- 破坏性脚本必须先验证全部前置输入，再做首次破坏动作（先例 `restore-postgres.sh`：三道确认环境变量 `PANDORA_RESTORE_CONFIRM`、`PANDORA_RESTORE_PRODUCTION_CONFIRM`、`PANDORA_RESTORE_EXISTING_CONFIRM`；校验归档来路与角色，在删除正式库前中止）。
- 读配置文件绝不 source / eval（机密与防代码注入）：`render-nginx.sh`、`public-base-url.sh`、`bootstrap.sh`、`restore-postgres.sh` 均使用 awk 逐键解析 KEY=VALUE。
- 对外基准地址 `public_base_url` 规范必须三处同步：`public-base-url.sh` 的 `pandora_valid_public_base_url`、`render-nginx.sh` 的 `public_ipv4` / 域名校验、Go 侧 `panel/internal/pkg/config/validate.go`（production 要求 `https://`）。脚本侧要求 `https://<DNS 域名或公网 IPv4>`。守卫：`public-base-url_mock_test.sh`。
- Cloudflare 真实 IP 信任表路径 `/etc/pandora/cloudflare-realip.conf` 在 `nginx-pandora.conf`、`render-nginx.sh`、`update-cloudflare-realip.sh` 保持一致；渲染器仅在文件缺失时创建默认不信任配置，绝不覆盖已有文件。守卫：`cloudflare-realip_mock_test.sh`。
- 日志：`/var/log/pandora/{serve,worker}.log` 由进程内 lumberjack 按 config 的 `log.*` 滚动；`logrotate-pandora` 只轮转 `health.log`，绝不能再匹配服务日志（两套轮转动同一个文件会丢日志）。守卫：`logrotate-pandora_static_test.sh`。
- 发布包归档一致性：`build-release.sh` 维护发布清单，脚本与数据文件分别列入 `release_scripts`、`release_data`、`release_units`，拷贝与归档共用同一组列表。安装器专用的辅助库（`public-base-url.sh`、`install-lib.sh`、`platform.sh`）只进发布包，不拷贝至主机安装目录（主机仅装 `HOST_SCRIPTS` 与 `HOST_DATA`）。
- Go 构建工具链锁死：`build-release.sh` 与 `pdnd/release/build.sh` 读取各自 `go.mod` 的 `go` 版本指令，设置 `GOTOOLCHAIN=go<x.y.z>` 并校验产物。实际版本记在包内 `deploy/BUILD-INFO`。守卫：`build-release_toolchain_mock_test.sh`。
- 健康巡检：`healthcheck.sh` 配合 systemd 单元 `pandora-health.service` 和 `pandora-health.timer`（使用 `OnActiveSec` 避免开机即触发），安装器自动启用。巡检查两个服务、门户与后台的 `/readyz`（serve 自己再 ping PG 与 Redis），以及 worker 与调度器在 Redis 里的 asynq 心跳。守卫：`healthcheck-install_static_test.sh`、`healthcheck_mock_test.sh`。
- 证书续期服务：`edge-tls.sh` 管理 HTTPS 证书软链接至 `/etc/pandora/tls/live/`，续期由 `pandora-tls-renew.timer` 触发，结果记录于 `/var/lib/pandora/tls/status`。守卫：`edge-tls_mock_test.sh`、`tls-renew_static_test.sh`。
- 管理员初始化：`pandora_admin_bootstrap`（`install-lib.sh`）在 `pandora admin has-admin` 退出码为 3 时创建首个管理员；交互模式通过无回显现场提示，无人值守使用环境变量，密码只经 `--password-stdin` 管道传入，绝不进命令行参数或日志。
- 数据库角色分权与迁移：
  - 角色划分：`pandora_owner`（建库与迁移属主）、`pandora_app`（业务运行时角色）。
  - 执行顺序：`bootstrap.sh roles` → `pandora migrate up`（通过 `pandora_cli` 注入 `PANDORA_DATABASE_MIGRATION_URL`，仅运行在清理环境变量后的子 shell 中）→ `bootstrap.sh grants`（运行 `configure-app-role.sql` 收敛权限）。升级前必须先全量转储（`pandora_pre_upgrade_dump`）。守卫：`db-roles_pg18_test.sh`。
- 加密备份与防篡改验证：
  - 备份三件套（`backup-postgres.sh`、`verify-backup.sh`、`restore-postgres.sh`）采用 age 私钥（`/etc/pandora/backup-age.key`）加密，备份包含由 HMAC 签名的本地封条（`.seal` 文件，`pandora backup seal-local`）。
  - 运维连库函数 `pandora_pg` 在备份三件套中内联独立实现，以 postgres 超级用户直连回环 `127.0.0.1:POSTGRES_PORT`，口令通过 `PGPASSWORD` 传入，不依赖 `runuser`（以兼容 systemd 的 `SystemCallFilter=~@privileged` 限制）。守卫：`backup-scripts_mock_test.sh`、`backup-provenance_mock_test.sh`、`webdav-backup-hardening_mock_test.sh`。
- PostgreSQL 与 Redis 服务加固与调参：
  - PostgreSQL 配置：`postgresql-pandora.conf` 安装至 `/etc/postgresql/18/main/conf.d/pandora.conf`，检查 `include_dir = 'conf.d'` 生效后再应用。
  - systemd Drop-in 配置：写入 `pandora.conf` 限制内存与禁止换出（`MemorySwapMax=0`）；PostgreSQL 设置 `OOMPolicy=continue` 与崩溃拉起脚本 `ExecStopPost=+/opt/pandora/deploy/pg-revive.sh %i`（仅在被杀且无停止任务时拉起）。守卫：`pg-revive_mock_test.sh`。加固支持通过 `deploy.env` 中的 `PANDORA_SYSTEMD_HARDENING=0` 关闭。
  - Redis 依赖与配置：必须使用 Redis 6.0 及以上版本（`redis-server`），配置文件追加 `# >>> pandora` 块（`appendonly yes`、`maxmemory-policy noeviction`、`save ""` 等），并依据内核 IPv6 支持与 Redis 版本适配 `bind` 语法。守卫：`redis-hardening_versions_docker_test.sh`。
- 边缘 nginx 规约：
  - 仅反向代理 HTTP 流量（门户 9000、管理后台 9001）。节点端通过 gRPC 端口 9443 直连面板（mTLS 认证），不穿过 nginx，nginx 中不得包含节点路由。
  - 站点 80 端口非面板域名请求直接返回 444；面板域名 80 端口除 `/.well-known/acme-challenge/` 外全量返回 308 重定向至 HTTPS。
  - 本机回环入口 `127.0.0.1:9080` 供安装及运维脚本进行本地健康校验。
  - 限速区分设：`pandora_auth`、`pandora_api`、`pandora_adm_auth`、`pandora_adm`、`pandora_refresh`、`pandora_conn`。守卫：`render-nginx_test.sh`。
  - 订阅域名：`deploy.env` 可选键 `PANDORA_SUB_DOMAINS`（逗号分隔，最多 50 个，须是 DNS 域名、不能等于面板域名、不能重复），由 `render-nginx.sh` 内的 heredoc 给每个域名追加一个 80（ACME + 308）与一个 443（只放行 `= /v1/sub-probe`（门户探测，GET/HEAD）与订阅拉取正则 `^/[0-9a-f]{12}/[A-Za-z0-9_-]{16,}(\.[a-z]{2,4})?$`，其余 404；不含后台前缀；探测页、票据、上报只走门户主域名）。证书自备在 `/etc/pandora/tls/sub/<域名>/{fullchain,privkey}.pem`，缺证书只渲染 80 并在 stderr 告警；`PANDORA_SUB_TLS_DIR` 只给测试换目录。不写或留空时输出与原来逐字节相同。Docker：compose 把 `.env` 的 `PANDORA_SUB_DOMAINS` 传给 nginx 容器，`nginx-entrypoint.sh` 写进渲染用的 deploy.env，证书目录 `PANDORA_SUB_TLS_DIR`（缺省 `./tls-sub`）只读挂到 `/etc/pandora/tls/sub`（容器里的这个变量不传，渲染脚本用缺省路径）。渲染只在 nginx 容器启动时做：改了 `PANDORA_SUB_DOMAINS` 要 `docker compose up -d --force-recreate nginx`，只补证书时 `restart nginx` 也行；只 `up -d` 在 `.env` 没变时不会重建。订阅拉取路径形状改了要同步这里的正则与 subscription 的前缀规则。守卫：`render-nginx_test.sh`。
- CI 门禁约定：`.github/workflows/panel-deploy.yml` 逐一显式列出所有部署脚本测试。新增桩测试（`*_mock_test.sh`）或静态检查（`*_static_test.sh`）必须补充到 CI 清单中。

## Docker（`panel/deploy/docker/`）
- 一个 `Dockerfile`（构建上下文是仓库根，`.dockerignore` 在根）五个阶段：`release-build` 跑 `build-release.sh`（钉 `$BUILDPLATFORM`，多架构只跑一次）→ `release`（只放两架构发布树）→ 三个最终镜像 `pandora`（distroless 非 root，serve / worker）、`pandora-nginx`（nginx + `render-nginx.sh`）、`pandora-ops`（`postgres:18-bookworm` + 发布包 `deploy/`，跑 migrate、备份三件套、psql、gen-env）。镜像里的程序与部署脚本一律取自发布树，不另编一份、不另写一份。
  - 发版（`release.yml` 的 `images` job）用 `--build-context release=<已出来源证明的 tarball 解开的目录>` 覆盖 `release` 阶段：镜像里就是 Release 附件里那份字节；镜像各自 `actions/attest`。只打版本号标签，不打 latest。
  - `pandora-nginx` 阶段不写 RUN（arm64 不用模拟器）；目录、默认站点、日志软链都在 `nginx-entrypoint.sh` 里做。
- 安全模型与裸机相同，不为容器放宽：
  - serve 的两个 HTTP 监听照旧绑回环；serve 与 nginx 都用 `network_mode: "service:netns"` 加入同一个网络命名空间，nginx 经 127.0.0.1 反代。不改 `validate.go`。
  - 命名空间由 `netns` 服务持有（同 Kubernetes Pod 的 pause 容器：借 pandora-nginx 镜像只跑 `sleep infinity`，端口也发布在它上面）。不要改回 nginx 挂 serve：serve 重建换容器 id 后，挂在旧 id 上的 nginx 重启也接不回来（10-14 CI 量到）；之前的看门狗只能处理就地拉起，已删。
  - nginx 只用 `render-nginx.sh` 渲染 `nginx-pandora.conf`；渲染后的容器专用改动只有两处（`listen 127.0.0.1:9080` → `listen 9080`；容器无 IPv6 时删 `listen [::]:`），每处先核数量、不对就停。模板改了这两行要同步改入口脚本。
  - 真实 IP 信任表由 `PANDORA_REALIP_FROM`（`gateway` 或 IP/CIDR，逗号分隔）每次启动重写；空 = 不信任任何代理。`gateway` 只在 80/443 不对公网发布时可用（前面另有反代、转发到宿主回环的 9080；前面的反代必须覆写 X-Real-IP）。
  - PG 与 Redis 只在 `internal: true` 的 backend 网络，不映射宿主端口；Redis 口令经标准输入的配置给（`redis-server -`），不进进程参数。
  - 秘密只在 0600 的 `.env`（compose 插值，按服务挑着给），不进镜像、不进命令行参数；`ops.sh` 把需要的落成容器内 root 0600 文件（`/etc/pandora/{deploy.env,secrets.env,backup-age.key}`），裸机脚本原样复用。
- migrate 与 ops 在 PostgreSQL 的网络命名空间里（`network_mode: "service:postgres"`），部署脚本照旧经 `127.0.0.1:5432` 连库。migrate 每次 `up` 都跑：`bootstrap.sh roles` → 有待执行迁移且库已有结构时升级前 `pg_dump` → `pandora migrate up`（以 nobody 跑）→ `bootstrap.sh grants`；serve / worker `depends_on: service_completed_successfully`。
- 容器版不支持：WebDAV 异地上传（PG 网络不出网）；正式库直接覆盖恢复（`restore-postgres.sh` 要 systemctl）——先恢复到新库名，再停服务改名、重跑 migrate（RUNBOOK 的 compose 一章）。
- 守卫：`.github/workflows/panel-docker.yml` 从源码构建三个镜像，真起 compose 走一遍（迁移、/readyz、门户与后台经 nginx、建管理员并登录、真实来源 IP 两种口径、重建 serve 与就地杀掉 serve 后 nginx 都能恢复、备份校验恢复）。改 `panel/deploy/**`、迁移、`.dockerignore` 时触发。
