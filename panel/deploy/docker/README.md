# Docker / Compose / 1Panel 部署

Pandora 面板提供两种部署方式：裸机 `install.sh`（systemd 托管）与 Docker Compose（容器化编排，支持 1Panel 导入）。

---

## 1. 什么时候用哪种

| 维度 | 裸机部署（`get.sh` / `install.sh`） | Docker Compose 部署（`compose.yaml`） |
|---|---|---|
| **适用环境** | 独占一台 Debian / Ubuntu 机器，追求最低系统损耗与纯粹配置 | 已有 Docker / 1Panel 环境、与其他站点共用宿主机、或希望使用外部反向代理 |
| **组件运行形态** | PostgreSQL 18、Redis 8、nginx、面板均为宿主系统原生服务 | 全部组件运行在 Docker 容器中，编排声明依赖与生命周期 |
| **服务加固** | systemd 深度加固、内存配额（MemoryMax）、OOM 自动拉起 | 容器只读根文件系统（read_only）、cap_drop ALL、no-new-privileges |
| **证书管理** | 内置自动申请与 systemd timer 周期续期 | 方式一手工放置或宿主 certbot 挂载；方式二交由外部反代处理 |
| **异地备份** | 原生支持 WebDAV 异地加密上传与可信检查点校验 | 仅支持本地加密归档（需自行同步宿主卷目录），PG 处于隔离内部网络不出网 |
| **系统巡检** | 内置 `pandora-health.timer`，多维度健康状态检查 | 依赖 Docker 健康检查探针与外部监控接入 |
| **二进制与迁移** | 完全一致的单二进制、相同的 `PANDORA_*` 配置键与数据库迁移脚本 | 完全一致的单二进制、相同的 `PANDORA_*` 配置键与数据库迁移脚本 |

> **注意**：两套装法共用相同的数据库结构与程序，但底层服务托管与恢复流程不同，**请勿在运行中的生产环境跨装法直接互相就地迁移**（立项时选定一种即可）。

---

## 2. 组成

### 服务清单（8 个服务）

| 服务名 | 使用镜像 | 网络 | 作用说明 |
|---|---|---|---|
| `postgres` | `postgres:18-bookworm` | `backend`（internal） | 数据库。经 SCRAM-SHA-256 认证，严格调参，不对外暴露宿主端口 |
| `redis` | `redis:8-alpine` | `backend`（internal） | 缓存与 asynq 队列基座。开启 AOF、禁用高危命令，不对外暴露宿主端口 |
| `migrate` | `pandora-ops` | `service:postgres` | 一次性任务：初始化角色权限、升级前自动转储、执行 Goose 迁移 |
| `netns` | `pandora-nginx`（只跑 `sleep`） | `edge` + `backend` | 只持有网络命名空间与外部端口映射（同 Kubernetes Pod 的 pause 容器），serve 与 nginx 都挂在它上面 |
| `serve` | `pandora` | `service:netns` | 面板 Web 核心与节点 gRPC。挂载备份卷只读 |
| `worker` | `pandora` | `edge` + `backend` | 异步任务调度与定时后台处理（清理、统计等） |
| `nginx` | `pandora-nginx` | `service:netns` | 边缘 HTTP/HTTPS 反向代理与限流。与 `serve` 在同一个网络命名空间 |
| `ops` | `pandora-ops` | `service:postgres` | 运维工具镜像（`profiles: ["ops"]`）。用于备份、校验、手工连库等 |

### 端口映射说明

| 端口 | 协议 / 绑定 | 作用 |
|---|---|---|
| `80` | TCP（公网 / 宿主） | HTTP 入口。默认强制 308 重定向至 HTTPS（ACME 挑战路径除外） |
| `443` | TCP（公网 / 宿主） | HTTPS 入口。面板门户与管理后台反代 |
| `127.0.0.1:9080` | TCP（宿主回环） | 本地明文 HTTP 入口。供宿主自检，或在方式二中供外部反代接入 |
| `9443` | TCP（公网 / 宿主） | 节点通信 gRPC 入口。由面板内部自行终结双向 mTLS，不经由 nginx |

### 镜像体系

发布镜像由本仓库发布工作流（`release.yml`）构建并出来源证明（见第 11 节），只打版本号标签、不打 `latest`：
- `ghcr.io/shiheishan/pandora`：Distroless 极简运行时，非 root 运行（65532:65532），仅含面板二进制、节点发布包与 GeoIP 数据。
- `ghcr.io/shiheishan/pandora-nginx`：基于官方 Nginx 构建，启动时动态渲染安全规则，日志直接接入 Docker 收集。
- `ghcr.io/shiheishan/pandora-ops`：基于 PostgreSQL 18 镜像构建，预装 age、openssl 与运维脚本，提供迁移与加密备份能力。

### 安全网络隔离模型

- **回环约束**：面板的核心 HTTP 监听（门户 `127.0.0.1:9000` 与后台 `127.0.0.1:9001`）被程序底层强制仅绑定回环地址；`serve` 与 `nginx` 都通过 `network_mode: "service:netns"` 加入同一个网络命名空间，反代直接走容器内回环，任何外部流量均无法绕过 nginx 访问面板后端。
- **数据不出网**：`postgres` 与 `redis` 仅接入 `internal: true` 的 `backend` 专用网络，不出公网、不向宿主机映射任何端口，杜绝因防火墙配置失误造成的数据库泄露。

---

## 3. 要求

- **容器引擎**：Docker Engine 24.0+，Docker Compose v2.20+。
- **架构支持**：Linux x86_64（amd64）或 aarch64（arm64）。
- **防火墙放行**：
  - **方式一（内置 TLS）**：放行 TCP `80`、`443`、`9443`。
  - **方式二（已有反代）**：仅放行 TCP `9443`（`80` / `443` 由外部反代独占，内部接入走本地回环）。

---

## 4. 首装（方式一：nginx 自己终止 TLS）

适用于独立主机，希望一键拉起并由 Pandora 内置 Nginx 终结 TLS 证书的场景。

### 步骤 1：下载编排配置与生成环境变量

```bash
mkdir -p /opt/pandora-docker && cd /opt/pandora-docker
V=v1.2.3   # 请替换为 GitHub Release 页面的最新版本号

curl -fsSLo compose.yaml https://raw.githubusercontent.com/shiheishan/pandora/$V/panel/deploy/docker/compose.yaml

# 自动生成包含全部高强度密码、JWT 密钥、AES 密钥与 age 私钥的 .env 文件
(umask 077 && docker run --rm -e PANDORA_PUBLIC_BASE_URL=https://panel.example.test ghcr.io/shiheishan/pandora-ops:$V gen-env > .env)
```

> **重要安全提示**：
> 1. 请确认 `.env` 权限为 600：`chmod 600 .env`。
> 2. `.env` 内包含主加密密钥 `PANDORA_SECRETS_MASTER_KEY` 与备份私钥 `PANDORA_BACKUP_AGE_KEY`，一旦丢失将无法解密数据库与备份归档！**务必立即复制一份妥善备份到宿主机之外的安全位置**。

### 步骤 2：准备证书（可选）

创建证书目录并将该域名的有效证书放置于此：
```bash
mkdir -p tls
# 放置 fullchain.pem 与 privkey.pem 至 tls/
```
*注：若暂未准备证书，系统将在初次启动时自动生成自签名证书兜底（浏览器会提示不安全），后续可随时换上真实证书。*

### 步骤 3：启动服务与检查

```bash
docker compose up -d
```

等待启动完成后查看容器状态：
```bash
docker compose ps
```
确保 `migrate` 状态为 `Exited (0)`，`nginx`、`postgres`、`redis` 为 `Up` 且带 `(healthy)` 标识，`serve`、`worker` 为 `Up`（`pandora` 镜像没有 shell，面板的就绪由 `nginx` 的健康检查一并探 `/readyz`）。

### 步骤 4：创建初始管理员账号

管理员密码必须通过标准输入传入，避免明文记录在 Shell 历史中：

```bash
read -rsp '管理员口令：' pw && echo
printf '%s\n' "$pw" | docker compose exec -T serve /opt/pandora/bin/pandora admin create --email admin@example.test --password-stdin
unset pw
```

### 步骤 5：访问后台

获取管理后台前缀：
```bash
grep '^PANDORA_ADMIN_PATH=' .env
```
管理后台完整访问地址为：`<PANDORA_PUBLIC_BASE_URL>/<PANDORA_ADMIN_PATH>/`（例如 `https://panel.example.test/ops_abcdef0123456789/`，请注意末尾斜杠）。

---

## 5. 证书配置

### 情况 A：自己已有证书

将证书文件重命名为 `fullchain.pem` 与 `privkey.pem` 放入 `PANDORA_TLS_DIR`（缺省为 `./tls`）：
- 首次放置后重启 Nginx：`docker compose restart nginx`
- 证书后续换新后，热重载生效：`docker compose exec nginx nginx -s reload`

### 订阅域名（可选）

后台「节点 → 订阅 → 订阅域名」里填了额外的订阅域名时，在 `.env` 的 `PANDORA_SUB_DOMAINS` 里逐个登记（逗号分隔，两边要一致），证书按域名放进 `PANDORA_SUB_TLS_DIR`（缺省 `./tls-sub`）下的 `<域名>/fullchain.pem` 与 `privkey.pem`，然后 `docker compose up -d nginx`。这些域名上只放行订阅拉取与 `/v1/sub-probe`，其余路径 404；缺证书的域名只开 80（ACME 校验 + 跳 https）。订阅域名的证书不自动签发，自备或用宿主机 Certbot 签好放进去。

### 情况 B：使用宿主机 Certbot（HTTP-01 验证）

内置 Nginx 容器已将 80 端口上的 `/.well-known/acme-challenge/` 映射到了容器内的 `/var/www/pandora-acme`（对应宿主目录 `PANDORA_ACME_DIR`，缺省为 `./acme`）。

使用宿主机 Certbot 签发示例：
```bash
certbot certonly --webroot -w /opt/pandora-docker/acme -d panel.example.test \
  --deploy-hook "cp -L /etc/letsencrypt/live/panel.example.test/fullchain.pem /opt/pandora-docker/tls/fullchain.pem && \
                 cp -L /etc/letsencrypt/live/panel.example.test/privkey.pem /opt/pandora-docker/tls/privkey.pem && \
                 docker compose -f /opt/pandora-docker/compose.yaml exec -T nginx nginx -s reload"
```
*说明：必须使用 `cp -L` 解开 Certbot 生成的软链接，将真实证书实体文件复制进 `./tls/` 挂载点。*

---

## 6. 方式二：前面已有反代（1Panel / OpenResty / Caddy / Nginx）

如果宿主机上已运行 1Panel、宝塔、OpenResty、Nginx 或 Caddy 占用 80/443，则由前置反代终止 TLS，Pandora 在本地回环以明文承接。

### 修改 `.env` 配置

在 `.env` 中修改以下配置项：
```ini
# 将端口绑定限制在宿主机回环随机端口，避免直接对公网暴露
PANDORA_HTTP_PUBLISH=127.0.0.1:
PANDORA_HTTPS_PUBLISH=127.0.0.1:

# 信任来自容器网关的代理请求
PANDORA_REALIP_FROM=gateway
```

随后前置反向代理统一转发至宿主机内部端口：`http://127.0.0.1:9080`。

> **安全核心红线：前置反代必须强制覆写 `X-Real-IP` 请求头**
> **面板仅严格信任由配置头提供的客户端 IP 地址。如果前置反代透传了客户端伪造的 `X-Real-IP` 请求头，恶意攻击者便可任意伪装来源 IP，从而绕过系统的按 IP 限流、防爆破和安全风控策略！**
> 此外，前置代理必须：
> 1. 禁用 SSE 长连接缓冲（关闭对 `/v1/events` 与 `/<后台前缀>/v1/events` 的 proxy buffering）。
> 2. 将代理读取超时（read timeout）放宽至至少 1 小时。
> 3. 设置 `client_max_body_size` 不小于 2m。

> **警告**：`PANDORA_REALIP_FROM=gateway` **仅可在 80/443 未对公网发布时使用**。若将 80/443 映射到公网的同时设置了 `gateway`，公网通过 `docker-proxy` 进来的客户端在 Nginx 看来都将呈现为网关 IP，不仅会导致来源 IP 混乱，更会让伪造来源头得逞。

### 前置反代配置示例

#### OpenResty / Nginx 站点配置
```nginx
location / {
    proxy_pass http://127.0.0.1:9080;
    proxy_http_version 1.1;

    # 关键：强制覆写真实 IP，禁止透传
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header Connection "";

    # 长连接与流式推送必要参数
    proxy_buffering off;
    proxy_read_timeout 1h;
    client_max_body_size 2m;
}
```

#### Caddy 配置
```caddy
panel.example.test {
    reverse_proxy 127.0.0.1:9080 {
        header_up X-Real-IP {remote_host}
        flush_interval -1
    }
}
```

---

## 7. 1Panel 部署指南

Pandora 原生支持直接导入 1Panel 编排面板，**无需**使用 1Panel 应用商店中的 PostgreSQL 或 Redis。

1. **导入编排**：
   - 在 1Panel 左侧导航进入「容器」→「编排」→「创建编排」。
   - 方式选「编辑」，编排名称填写 `pandora`。
   - 将本仓库 `compose.yaml` 内容全文粘贴到内容编辑框中。
   - 将通过 `gen-env` 生成并调整好的 `.env` 内容全文复制粘贴到下方「环境变量」区域。
2. **反向代理网站建站**：
   - 进入「网站」→「创建网站」→「反向代理」。
   - 主域名填写面板域名，代理地址填写 `http://127.0.0.1:9080`。
   - 在「HTTPS」页签申请或绑定 SSL 证书。
   - 进入该网站的反向代理「配置文件」，核对并确保包含覆写 `X-Real-IP`、`proxy_buffering off;` 以及 `proxy_read_timeout 1h;`。
3. **防火墙放行**：
   - 1Panel「安全」→「防火墙」中放行 TCP `9443` 端口（供外部节点端 mTLS 接入）。
4. **定时备份任务**：
   - 进入「计划任务」→「创建计划任务」，类型选择「Shell 脚本」。
   - 执行周期设为每日凌晨。
   - 脚本内容填入：
     ```bash
     cd /opt/1panel/docker/compose/pandora && docker compose run --rm -T ops backup
     ```
     *（注：编排路径以 1Panel 实际部署目录为准）*

> **声明**：本仓库尚未向 1Panel 官方应用商店提交打包应用，请统一通过上述官方 Compose 编排模式导入维护。

---

## 8. 升级与回退

### 升级面板

1. 编辑 `.env` 文件，将 `PANDORA_VERSION` 修改为新的版本号（如 `v1.2.4`）。
2. 若新版本有结构微调，建议重新下载同版本下的 `compose.yaml`。
3. 执行拉取与更新启动：
   ```bash
   docker compose pull && docker compose up -d
   ```
4. **升级生命周期机制**：
   - `migrate` 容器会最优先运行。
   - 若检测到数据库已有表结构且存在待执行的新增迁移，`migrate` **会在动手修改前自动执行全量逻辑转储**，保存至备份卷中的 `/var/backups/pandora/pre-upgrade-<时间戳>.dump`。
   - 一旦迁移过程报错，`migrate` 容器会异常退出，后续的 `serve` 与 `worker` **绝不会启动**，旧版本容器在此前已停止，数据库不会继续产生脏数据。此时请排查 `docker compose logs migrate`。

### 回退策略

> **特别注意：请勿通过直接把 `PANDORA_VERSION` 改回旧版本来尝试回退！**
> 数据库迁移机制不会自动逆向降级，旧版本的程序直接运行在执行过新迁移的数据库上会发生未知故障。
> 若要回退，必须恢复升级前自动生成的转储备份，具体操作步骤请严格查阅 [panel/deploy/RUNBOOK.md](../RUNBOOK.md) 第 14 章。

---

## 9. 日常运维命令

所有涉及数据库与文件维护的运维工具均通过轻量级 `ops` 服务按需调用：

| 运维操作 | 执行命令 |
|---|---|
| **查看容器状态** | `docker compose ps` |
| **跟踪运行日志** | `docker compose logs -f --tail 100 serve` |
| **重启核心服务** | `docker compose restart serve`（注：nginx 将随之重载） |
| **列出管理员列表** | `docker compose exec serve /opt/pandora/bin/pandora admin list` |
| **以超管连入数据库** | `docker compose run --rm ops psql` |
| **执行即时加密备份** | `docker compose run --rm -T ops backup` |
| **校验指定备份完整性** | `docker compose run --rm -T ops verify /var/backups/pandora/<文件名>.dump.age` |
| **列出所有备份文件** | `docker compose run --rm --entrypoint ls ops -l /var/backups/pandora` |
| **将备份拷贝至宿主** | `docker run --rm -v pandora_backups:/b:ro -v "$PWD":/out busybox cp /b/<文件名> /out/` |

*注：`docker compose cp` 仅适用于正在运行的容器；从命名的数据卷抽取文件推荐使用上述临时 busybox 挂载方式（数据卷名称通常为 `<项目名>_backups`）。*
数据灾难恢复步骤请查阅 [RUNBOOK.md](../RUNBOOK.md)。

---

## 10. 和裸机部署的区别

1. **无内置 WebDAV 异地同步**：由于容器隔离设计，数据库内部网络禁止访问外部互联网，异地备份请在宿主机通过 rclone / crontab 将数据卷中的备份文件同步至异地存储。
2. **无内置 systemd 巡检定时器**：不配置 `pandora-health.timer`，服务就绪状态直接通过容器编排的 `healthcheck` 机制呈现。
3. **禁止原地覆盖恢复生产库**：为杜绝误操作与文件锁死，容器环境下禁止直接向正在运行的正式库覆盖恢复数据，必须先恢复到临时库再重命名切换。
4. **无证书自动续期**：内置 Nginx 模式需通过宿主挂载更新，反代模式由外部代理统一续期。
5. **公网 IPv6 客户端识别**：若 80/443 对公网发布，经由宿主机 `docker-proxy` 转入的客户端（典型是 IPv6 客户端）在 Nginx 端将显示为网关 IP，按 IP 限流机制会将其归并计算。

---

## 11. 核对镜像来源证明

为防止镜像供应链攻击，Release 发布的各架构镜像均由 GitHub Actions 官方工作流生成 OCI 来源证明（Attestation）。可以使用 GitHub CLI 对拉取的镜像进行验签：

```bash
V=v1.2.3

# 校验核心镜像
gh attestation verify oci://ghcr.io/shiheishan/pandora:$V \
  --repo shiheishan/pandora \
  --signer-workflow shiheishan/pandora/.github/workflows/release.yml \
  --source-ref refs/tags/$V

# 同样可校验 nginx 与 ops 镜像
gh attestation verify oci://ghcr.io/shiheishan/pandora-nginx:$V --repo shiheishan/pandora --signer-workflow shiheishan/pandora/.github/workflows/release.yml --source-ref refs/tags/$V
gh attestation verify oci://ghcr.io/shiheishan/pandora-ops:$V --repo shiheishan/pandora --signer-workflow shiheishan/pandora/.github/workflows/release.yml --source-ref refs/tags/$V
```

镜像内部打包的二进制程序与 GitHub Release 附件中的二进制哈希完全一致。
