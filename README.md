# Pandora Panel（潘多拉面板）

Xboard 类代理订阅面板 + 自研节点端（Pandora NativeCore）。目标是提供 XBoard 级运营能力，并把协议、传输、路由与流量统计逐步收回单一自研内核。别名：AegisPanel / pandora-native。

本文件是项目事实的唯一入口：架构、功能、构建、部署、验证状态、进度与路线图都在这里。AI 协作规则不在这里：根目录 [CLAUDE.md](CLAUDE.md) 是给 Claude Code 的全局约定，仓库公开、不写部署专属值的红线在它的「红线：仓库公开」一节；按目录生效的模块约定在 [.claude/rules/](.claude/rules/)，验证流程与 CI 说明在 [.claude/skills/verify/](.claude/skills/verify/SKILL.md)。

## 架构

技术栈：**Go 1.26 + PostgreSQL 18 + Valkey 8（Redis 兼容）**。Go 让面板与节点端都是无运行时依赖、可交叉编译的单文件，常驻内存小；PostgreSQL 18 内置 `uuidv7()`，租户隔离靠 FORCE RLS 由数据库强制，复式记账靠 DEFERRABLE 约束触发器在提交时配平；Valkey 协议兼容 Redis，许可证仍是 BSD。

```text
用户浏览器 / 专属客户端
          |
      反向代理 + TLS
          |
  +-------+---------+----------------+
  |                 |                |
aegis-public    aegis-admin     aegis-node
用户门户         管理后台        节点接入/UniProxy 兼容
  |                 |                |
  +----------- PostgreSQL 18 -------+
                  |
              Valkey/Redis
                  |
        pandora-native / pdnd
   NativeCore：协议、传输、路由、流量统计
```

面板按域拆成三个独立的网关进程：

| 进程 | 职责 |
|---|---|
| `aegis-public` | 用户门户与订阅分发 |
| `aegis-admin` | 管理后台 |
| `aegis-node` | 节点接入（UniProxy 兼容） |

数据层用行级安全（RLS）做租户隔离；审计、账本、流量重置等表是追加写，由数据库触发器强制。应用角色 `aegis_app` 是非超级用户且不绕过 RLS。管理端、门户与节点接入都提供 SSE 事件流，跨进程分发走 Redis/Valkey（验证状态见下文门禁）。

## 仓库结构

| 路径 | 内容 |
|---|---|
| `panel/` | 面板：用户门户、管理后台、订阅分发、计费、节点编排、审计 |
| `panel/cmd/` | 各域网关与运维工具的可执行入口 |
| `panel/internal/` | `api/`（public、admin、node 路由与处理器）、`domain/`（业务域）、`middleware/`（认证、限流、幂等、租户注入）、`platform/`（配置、数据库、加密、日志、审计、令牌） |
| `panel/tests/` | 数据层不变量 SQL 与端到端脚本 |
| `panel/frontend/` | 面板前端，2026-09-23 起按设计稿从零重写、2026-09-26 完成（React + TypeScript + Vite，管理后台与用户门户双入口），构建后经 `make frontend-embed` 嵌入 `panel/web/` |
| `panel/web/` | 面板前端的 `go:embed` 嵌入点：两个网关在根 `/` 下发入口、`/assets/*` 下发产物；仓库只存占位入口，由 `make frontend-embed` 覆盖 |
| `panel/migrations/` | SQL 迁移，按序号递增，当前到 00097，共 94 个 `.sql`（00073、00091、00092 空号）；00067 删除 21 张无依赖孤儿表，未在任何生产库执行（CI 的一次性库会跑全部迁移）；`RESERVED-TABLES.md` 登记其余 21 张 Go 从不引用的表及锁定原因 |
| `panel/deploy/` | 安装、迁移、备份、WebDAV、Nginx、systemd、PG18 与 UI 验收脚本 |
| `panel/docs/` | `redesign/api-contract.md` 前后端接口契约；DASH / CLIENT-AUTH 历史冻结稿与客户端登录的代码、冻结迁移都已移出主线，在 tag `archive/client-auth` |
| `pdnd/` | Pandora node（pdnd / pandora-native）：NativeCore 协议入站、认证、路由、用户与流量 |
| `pdnd/kernel/`、`pdnd/internal/` | NativeCore 自研数据面 |
| `pdnd/core/` | 内核适配层：xray-core / sing-box 兼容与外部进程 |
| `pdnd/release/` | Linux amd64/arm64 构建、能力矩阵一致性检查、运行时验收 |
| `docs/` | 配置签名密钥轮换、发布物绑定 |
| `.githooks/`、`.gitleaks.toml` | 提交前密钥扫描：clone 后执行 `git config core.hooksPath .githooks` 启用，需先 `brew install gitleaks`；未装 gitleaks 时拒绝提交 |
| `.github/workflows/` | `pandora-native.yml`（pdnd 门禁、panel-frontend、nodefabric 契约、双架构发布构建）、`panel-pg18.yml`（panel-unit 全量单测 + PG18 集成门禁）、`panel-smoke.yml`（新前端对真实网关的联调冒烟）、`panel-deploy.yml`（deploy 脚本的桩测试，迁移脚本拿真实迁移目录校验；含两个安装脚本共用的首装对外地址闸门） |
| `CLAUDE.md`、`.claude/rules/`、`.claude/skills/` | 给 Claude Code 的说明：根 CLAUDE.md 是全局约定与红线，rules 是按路径自动加载的模块约定，skills 是验证流程 |

本地快照不含 `.env`、密钥、私钥和编译产物（二进制、`node_modules`、`dist`）。

### 源码与运维的边界

这个仓库是产品：任何人下载后，填上自己的域名就能部署。所以仓库里只有三样东西——

- **代码**；
- **模板**：只含占位符，例如 nginx 模板里的域名写成 `__AEGIS_DOMAIN__`，安装时从 `.env` 填入；
- **自动化测试**：检查代码对不对，和被测代码放在一起，只用虚构数据（测试夹具里的密钥都是新生成的假值），不进入发布物。

每一套部署自己的值都在安装时产生，不进仓库：域名由部署者写进 `.env`，主密钥、JWT 密钥、数据库口令和后台路径前缀由 `install.sh` 首装生成。

维护者操作自己服务器的东西——一次性运维脚本、安装日志、真实服务器地址——放在被 git 忽略的 `ops-local/`（按需创建，只存在于维护者本机）；脚本要用的密钥从 1Password 读取，不写进任何文件。提交前的 gitleaks 钩子（见上表 `.githooks/`）会拦下密钥、服务器 IP、后台前缀这类内容，误写进源码也提交不上去。

## panel：面板

### 已实现功能

用户门户（Portal）：

- 注册、登录、密码策略；登录和改密页有密码显示按钮。
- Access / Refresh Token 默认有效期 30 天。
- 套餐浏览、订阅购买、订单与支付结果；订阅节点预览与分发。
- 工单创建已简化为直接提交；支持查看、回复与撤回。
- 邀请返利；CNY 佣金转余额使用精确最小货币单位和幂等键。
- 通知偏好 GET/PUT；事务类通知锁定，失败回滚。

管理后台（Admin）：

- 仪表盘：收入、用户、订单、订阅、节点、流量、工单统计；CNY/USD 收入调整；7/30/90 天趋势；节点 / 用户流量排行。
- 用户、套餐、订单、优惠券、礼品卡、支付渠道管理。
- 节点、服务器、路由（全局 / 路由组 / 单节点三个范围）、权限组、节点排序（Node Fabric）。
- 公告、知识库、主题、插件管理。
- 工单、审计日志、风控与降级开关。管理后台路径是安装时生成的高熵串。

礼品卡、知识库、主题、插件、套餐、订单操作均已有真实代码；早期文档里“仅占位”的说法已过时。

前端：2026-09-23 起，旧的两套前端（`/` 下的手写单页与 `/app/` 下的 React 候选）已整体删除，管理后台与用户门户按新设计稿在 `panel/frontend` 从零重写，同时让设计稿与后端双向对齐——设计有而后端没有的能力补后端，后端有而设计没有的能力补进前端。

重写已完成：发布包构建时把前端嵌入网关，两个网关的 `/` 分别下发管理后台与用户门户；仓库里 `panel/web/` 只存占位入口，未嵌入真实产物时下发的是占位页。

运维：WebDAV 自动备份、签名清单、保留策略、systemd timer/service 与恢复脚本已存在，见 [panel/deploy/BACKUP.md](panel/deploy/BACKUP.md)。真实远端恢复演练状态见“验证状态与门禁”。

### 数据与迁移

迁移在 `panel/migrations/`，按序号递增。安装与升级都用包内固定版本的 goose 执行，升级前自动全量备份。

### 设计不变量

安全与财务不变量下沉到数据库层，不依赖应用层自觉。`panel/tests/invariants.sql` 是这些规则的可执行验收（`make invariants`）。

| 不变量 | 落点 | 需求编号 |
|---|---|---|
| 借贷必须配平 | `DEFERRABLE` 约束触发器，事务提交时校验 | PAY-005 |
| 账本 / 审计不可改删 | 追加写触发器 + 权限回收 | DATA-003、SEC-012 |
| 跨租户不可读写 | `FORCE ROW LEVEL SECURITY` + 非 superuser 应用角色 | DATA-002、ARC-006 |
| 订阅 / 节点状态机 | 合法转换表 + `BEFORE UPDATE` 守卫触发器 | SUB-004、NODE-010 |
| 重复回调只生效一次 | `(provider_id, provider_event_id)` 唯一约束 | PAY-003 |
| 申请人不能审批自己 | 跨表校验触发器 | SEC-013 |
| 各域令牌不可交叉 | 每域独立 HMAC 密钥，域名参与签名 | EXT-001 |

`aegis_app` 是应用专用的数据库角色（`NOSUPERUSER NOBYPASSRLS`）。这一点是必需的：Docker 镜像的 `POSTGRES_USER` 是 superuser，隐含 `BYPASSRLS`，若应用直接用它连库，RLS 就形同虚设。

### 本地开发

```bash
cd panel
make up                # 起 PostgreSQL 18 + Valkey 8（docker compose，只绑 127.0.0.1）
make migrate           # 迁移，先在临时库演练再动主库
make check-migrations  # 只在临时库演练迁移，不动主库
make invariants        # 数据层不变量测试
make build             # 编译全部网关到 bin/
make test              # go test -race
make frontend-check    # 面板前端：npm ci + lint + typecheck + vitest + 双入口构建
make frontend-embed    # 构建面板前端并同步进 web/{admin,portal}，由网关在根 / 下发；release-linux 会先跑它
make e2e               # 端到端链路：注册→下单→支付→账本→订阅→配置
make verify            # vet + check-migrations + invariants，提交前跑
```

`make help` 列出全部目标。`panel/tests/` 下另有 admin、uniproxy、epay、support 的端到端脚本（加上主链路 `e2e.sh` 共五个）。

### 端口

全部只绑 `127.0.0.1`，公网访问由反向代理接入。地址可用 `AEGIS_*_ADDR` 环境变量覆盖，见 `panel/deploy/.env.example`。

| 端口 | 用途 |
|---|---|
| 9000 | Public API |
| 9001 | Admin API |
| 9003 | Node API |
| 5433 / 6380 | PostgreSQL / Valkey（docker compose 映射） |

## pdnd：NativeCore 节点端

一个二进制承载全部协议。NativeCore、Panel Schema 与 serving allowlist 静态对齐 13 个协议：

`anytls, http, hysteria2, juicity, mieru, naive, shadowsocks, shadowtls, socks, trojan, tuic, vless, vmess`

对齐检查不需要 Go：

```bash
python pdnd/release/check_native_panel_parity.py   # 期望输出 NATIVE_PANEL_PARITY_OK
```

### 分层

当前兼容层与 NativeCore 并存，自动选择只会把已验证能力交给 NativeCore：

| 层 | 承载 | 协议 |
|---|---|---|
| NativeCore | Pandora 自研数据面（`pdnd/kernel`、`pdnd/internal`） | 上述 13 个协议，含 mieru(TCP/UDP)、juicity(TCP/UDP) |
| 兼容层 | xray-core / sing-box（`pdnd/core`） | 尚未完成 NativeCore 验收的组合与边界 |
| 独立进程 | 兼容边界 | 仅保留尚未接管的协议或明确不支持的旧配置回落 |

正在重构为单一内核：协议解码、TLS/HTTP/QUIC 传输、路由与流量统计逐步收回 `pdnd/kernel` 与 `pdnd/internal`。兼容层只作为尚未验收协议的过渡；NativeCore 启动时不会初始化兼容内核，只有明确分派到未接管组合时才按需启动 sing-box/xray。

默认发布二进制通过构建标签隔离兼容层，只链接 NativeCore；迁移用的兼容二进制必须显式 `-tags compat` 构建。内核适配层实现了统一的用户热更新契约 `UpsertUsers`。

### fail closed

正式节点默认按 NativeCore-only 运行：省略根级 `native_only` 即让未经 NativeCore 验收的组合直接失败，不会静默回落到 sing-box/xray。只有迁移阶段使用 `-tags compat` 构建并显式设置 `native_only: false` 才保留兼容回落。NativeCore-only 也会拒绝节点配置中显式指定的 `kernel: "xray-core"` 或 `kernel: "sing-box"`。

### 能力矩阵

能力矩阵可在不读取面板配置、也不启动监听器的情况下查询：

```bash
./pandora-native --capabilities
```

面板应把这份 JSON 作为节点编排前的能力协商结果；未在矩阵中验证的组合必须提示管理员，而不是静默切换到兼容内核。矩阵会发布 `socks4`、`socks4a` 与 `http-forward`。

### 已覆盖的协议与传输

- VLESS `command=UDP` 覆盖 TCP、WebSocket、HTTP Upgrade、gRPC 以及 XHTTP stream/packet 传输；数据报通过统一 DataPlane 路由并计入用户流量，不回落到第三方内核。
- Trojan UDP ASSOCIATE 使用原生地址 / 长度 / CRLF 帧转发并计量。
- VMess 覆盖原生 gRPC（h2c、TLS+h2）与 XHTTP 流、packet-up/reconnect 模式（HTTP/2、HTTP/3）。
- Juicity 已接入 NativeCore 的原生 QUIC / 认证 / TCP / UDP 数据面。
- `socks` 入站原生支持 SOCKS4、SOCKS4a、SOCKS5 CONNECT 和 SOCKS5 UDP ASSOCIATE；`http` 入站支持 HTTP CONNECT 与正向 GET。代理认证绑定面板下发的用户 UUID，未授权请求失败关闭。
- REALITY + XHTTP（TCP/H1/H2/H3）具备 NativeCore 端到端回环；外部客户端互操作状态见“验证状态与门禁”。

## 构建

```bash
cd panel && go build ./...
cd pdnd  && go build -o pdnd .
```

Linux x64/ARM64 发布包使用 `pdnd/release/build.sh`，会生成带 SHA-256 的 `manifest.json`。Ubuntu runner 的 race/vet 与双架构构建门禁见 `.github/workflows/pandora-native.yml`。Windows / macOS 交叉构建不能替代真实 Linux race。

## 部署

一条命令装完。首装和升级是同一个入口——脚本自己判断该走哪条路。

### 1. 打包

在有 Go 1.26+ 与 Node 22.12+/npm 的机器上（不需要是目标机器；`build-release.sh` 先跑 `make frontend-embed` 把前端嵌进网关，没有 npm 即失败）：

```bash
cd panel && ./deploy/build-release.sh /tmp/dist
```

产出 `/tmp/dist/pandora-panel_<版本>_linux_<架构>/`，约 150 MB，内容是自足的：

| 目录 | 内容 |
|---|---|
| `bin/` | 三个网关 + 运维工具 + goose（迁移工具版本随包固定，不跟 `@latest` 漂移） |
| `deploy/` | 安装、迁移、备份、nginx 渲染脚本与 systemd 单元 |
| `migrations/` | 全部 SQL 迁移 |
| `pdnd-dist/` | 节点端二进制 |
| `SHA256SUMS` | 全量校验和 |

默认同时构建 amd64 与 arm64 两种架构；`PANDORA_VERSION` 只指定版本号，例如 `PANDORA_VERSION=v1.0.0 ./deploy/build-release.sh /tmp/dist`。

### 2. 丢过去装

目标机器需要 Linux + root + docker（含 `docker compose` 插件），以及 `openssl sha256sum systemctl install awk sed curl` 和备份加密用的 `age`。

```bash
scp -r /tmp/dist/pandora-panel_*_linux_amd64 root@目标机:/opt/pandora-release/rel
ssh -t root@目标机 'cd /opt/pandora-release/rel/deploy && ./install.sh'
```

发布包装出来的就是生产：首装把 `.env` 定为 `AEGIS_ENV=production`。生产模式下网关要求
`AEGIS_PUBLIC_BASE_URL` 是 `https://公网域名`，否则拒绝启动，所以首装会先问面板的对外地址
（形如 `https://panel.example.com`，不能是 IP、不带端口与路径），不合规就在动手前停下。
无人值守加 `PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://你的域名`。
升级不改现有 `.env` 的运行模式；不是 production 时只打印提示和切换步骤。

节点接入的发布物绑定（节点端两个架构的 SHA-256 与版本）随包生成在 `deploy/release-artifact.env`，
安装到 `/opt/aegispanel/deploy/`，由 `aegis-node` 加载，每次升级随包覆盖；全过程见
[docs/RELEASE-ARTIFACT-BINDING.md](docs/RELEASE-ARTIFACT-BINDING.md)。

安装器拒绝从任何人可写的目录安装（防止有人塞一份假的进来），所以包要放在
root 独占的目录下——直接拿 `/tmp` 里的构建产物去装会被挡住，那是它在正确工作。

脚本按序做完：前置检查 → 生成配置 → 起 PostgreSQL/Valkey → 迁移 →
收窄数据库角色（`aegis_app` 非超级用户、不绕过 RLS）→ 装二进制与 systemd 单元 →
启动 → 健康检查。任一步失败即停，并说清停在哪、怎么恢复。

三条硬约束：

1. **幂等**。已有 `.env` 绝不覆盖——里面是随机生成的密钥，重写一次就再也解不开
   信封加密的字段了。
2. **升级前自动全量备份**，落在 `/var/backups/aegispanel/pre-upgrade-*.dump`。
3. **不替你造管理员**。装完只提示该跑哪条命令——脚本生成并打印密码，等于把它
   写进终端回滚日志和 CI 输出里。

### 3. 装完

三个网关只监听 `127.0.0.1`，公网访问要在前面放反向代理并配 TLS。渲染 nginx 配置：`server_name` 和
Let's Encrypt 证书路径都从 `.env` 的 `AEGIS_PUBLIC_BASE_URL`（首装时填的域名）生成，
没填、仍是示例值、不是 HTTPS 域名时脚本拒绝渲染。公网只听 80（跳 443）与 443，另有本机回环运维入口 `127.0.0.1:9080`。

```bash
/opt/aegispanel/deploy/render-nginx.sh
```

模板 include 的真实来源 IP 信任表 `/etc/aegispanel/cloudflare-realip.conf` 不存在时，渲染器会写一份
**不信任任何代理**的默认文件：nginx 只认 TCP 对端，客户端自己填的 `CF-Connecting-IP` / `X-Real-IP`
一律不采信。站点在 Cloudflare 后面（橙色云）时，渲染后再写入 Cloudflare 的官方网段并重载；
升级重新渲染不会覆盖这个文件，Cloudflare 调整网段时重跑同一条命令即可：

```bash
/opt/aegispanel/deploy/update-cloudflare-realip.sh && nginx -t && systemctl reload nginx
```

管理后台路径是安装时生成的高熵串，装完会打印一次（泄露等同暴露入口）。

```bash
systemctl status aegis-public aegis-admin aegis-node   # 状态
tail -f /var/log/aegis/public.log                      # 日志
cd /opt/aegispanel/deploy && ./psql.sh                 # 数据库
```

### 回归测试

安装链有回归测试，覆盖四种形态：全新安装、升级、老式 `.env`（缺
`AEGIS_MIGRATION_DATABASE_URL`）、备份单元处于 `failed`。后两种都是生产上
真实踩过的坑——第一版 install.sh 只验了全新安装就上生产，结果死在停服之后。

```bash
bash panel/deploy/test-install.sh <发布目录>
```

**破坏性**：会清空本机的 aegis 容器、数据卷与 `/opt/aegispanel`。只在一次性
验证机上跑，脚本开头有护栏挡着生产。

### 配置

真实的 `.env` 不进仓库。模板见 `deploy/.env.example`，所有敏感项都是
`CHANGE_ME`，由 `install.sh` 首装时随机生成。

### 上线前检查

隐藏的后台路径挡不住公网扫描；来源、速率和端口最终要在云防火墙、安全组和反向代理层收住。

1. 只开放 80/443 或明确需要的代理端口；
2. 节点控制端口不直接暴露到公网；
3. 管理路径再加一层 IP 白名单、二次验证或 VPN 入口；
4. 注册、登录、订阅、工单接口配置限速与挑战策略；
5. 管理、节点、数据库日志分开存放，并做 secret scan；
6. 不把密钥、`.env`、私钥和真实备份地址写进仓库。

## 验证状态与门禁

状态标记沿用项目约定：FACT 有命令或源码直接证明；INFERENCE 是基于证据的判断；UNKNOWN 尚缺验收证据。静态对齐、进程存在或 HTTP 200 不等于功能验收。

### 已有证据（FACT）

**CI 门禁（2026-09-26 在 `main` 上全绿：`36230330995` / `36230331009` / `36230331011`）**。

三个 workflow 各有路径过滤：只改仓库根的文档不触发；改 `pdnd/**`、`panel/internal/**`、`panel/web/**`、`panel/frontend/**` 等被过滤目录里的任何文件都会触发对应 workflow。

- **Panel PostgreSQL 18 gates**（`panel-pg18.yml`）：
  - `panel-unit` 跑 panel 全量 build / vet / go test，是 CI 上唯一跑 panel 全部单元测试的地方；
  - `panel-pg18` 在 runner 的 Docker 里起一次性 PG18，用发布包钉死的 goose 从空库套用全部迁移（到 00095），再跑 15 个包的 PG18 集成用例：234 PASS / 0 SKIP / 0 FAIL。有用例跳过、或一个都没跑，同样判失败。
- **Pandora NativeCore**（`pandora-native.yml`）：
  - pdnd：amd64 race / vet、原生 `ubuntu-24.04-arm` 的 ARM64 race、`-tags interop` 外部客户端门（外部 Xray、AnyTLS 客户端，非 race）；
  - 能力矩阵 smoke、H3 探针、默认构建依赖边界（sing-box / xray 不得进默认构建）、compat 编译、amd64 / arm64 双架构发布构建；
  - `check_native_panel_parity.py`（NativeCore、Panel Schema、serving allowlist 各 13 个协议一致）与 nodefabric 契约；
  - `panel-frontend`：新前端 lint / typecheck / vitest（对假后端）/ 双入口构建，再 `make frontend-embed` 用真实产物跑 web、webapp、api 的 Go 契约，占位页没被替换即失败。
- **Panel frontend smoke**（`panel-smoke.yml`）：起一次性 PG18 + 真实网关，经真网关造数据，用前端页面自己的 zod schema 解析真实响应（读表先于写路径），再在同一栈上跑 `tests/` 下五个 e2e 脚本；任一失败即红。
- **Panel deploy script contracts**（`panel-deploy.yml`，2026-09-26 新增）：改 `panel/deploy/**` 或 `panel/migrations/**` 即触发，逐个跑不需要数据库与 root 的 deploy 桩测试；两个迁移脚本（migrate.sh、check-migrations.sh）拿真实 `panel/migrations` 校验文件名与编号（严格递增、不重复，允许 00073、00091、00092 历史空号），拒绝真实目录即变红。

  在此之前 deploy 桩测试 CI 一个都不跑，其中两个对真实目录早已是红的。
- **仓库守卫**（随 `go test ./...`）：panel 与 pdnd 两道 800 行守卫、表登记簿与权限字典契约。第 5 阶段拆分超长文件时，每步都用 `panel/tools/refactorcheck` 证明是纯挪动。
- 2026-09-23 起 workflow 默认 `shell: bash`（`-eo pipefail`）。在此之前 `go test … | tee` 的失败会被 `tee` 吞掉，**那之前的 race 绿灯不能当证据**。

**历史证据**（早期快照，不等于当前 `main`）：

- 2026-08-11：pdnd 在 Linux amd64 隔离目录通过外部 Xray 的 REALITY+XHTTP+H3 互操作测试（`TestExternalXrayVLESSXHTTPH3Interop`），Xray 客户端互操作覆盖 REALITY+XHTTP/H1、H2 与普通 TLS+XHTTP/H3。
- 2026-08-11：在远端旧快照上完成隔离 WebDAV + PostgreSQL 18 备份恢复演练（HTTPS 上传、签名 manifest、SHA-256、Age 加解密、全新实例恢复），RTO 约 2 秒。
- 更早：Debian x86_64 完整 race 与 13 协议逐项测试曾通过。

### 未关闭的门禁（UNKNOWN）

- **新前端没在真实浏览器里对真后端跑过**：CI 只有 vitest 对假后端，以及用页面 schema 解析真网关响应（不渲染页面）。后台只支持 ≥960 宽度，门户各视口都没有浏览器矩阵。
- **当前 `main` 的真实安装式端到端**：`build-release.sh` 全流程、安装脚本、nginx、TLS、systemd、pandora-native 两阶段接入、真客户端连节点，都没在真机上跑过。部署前注意：没划进节点池的节点不服务任何人（契约 R104）。
- **迁移 Down 段**：CI 只跑 Up，Down 没在 CI 上执行过。
- REALITY+XHTTP/H3 的独立公网第三方客户端端点验证；amd64 外部 Xray 测试不能替代。
- 13 个协议的全部传输组合与独立客户端协议矩阵。
- ARM64：CI 原生门已通过，安装式 ARM64 运行仍 UNKNOWN；交叉编译或静态 ELF 检查不能替代。
- 跨进程 SSE：租户 / 节点隔离、Redis 重连、重复信号、watcher 生命周期、慢消费者不阻塞。
- 真实支付、退款、通知外发的独立验收。
- WebDAV 在当前 `main` 上的真实远端恢复演练。
- 冷启动与回滚；G0 release intent 尚未授权。

## 当前进度

截至 2026-09-26：

| 项 | 状态 |
|---|---|
| 仓库基线 | 面板重构第 1–5 阶段于 `d04513e` 合入 `main`，其后只有文档合并 |
| CI | `main` 上三组全绿（`36230330995` / `36230331009` / `36230331011`）：PG18 集成门禁 234 PASS / 0 SKIP，NativeCore（含 race、原生 ARM64、interop），新前端对真实网关的联调冒烟 |
| Xboard 功能验收 | PARTIAL，未 RELEASED |
| 前端 | 旧的手写单页与 React 候选已删除，管理后台与用户门户按设计稿在 `panel/frontend` 重写完成并补齐后端缺口 |
| 部署 | 这一版尚未在任何真实机器上部署或实测；真机测试待换新机器再做 |
| 客户端登录（CLIENT-AUTH） | 2026-10-05 移出主线，专心做面板：设备码登录与设备签名的实现代码、发布门禁脚本和 8 个 `pandora-*` 命令在 tag `archive/client-auth`；三份冻结设计稿和 `frozen-client-auth/` 下两个从未应用的迁移也一并存档在该 tag，以后做客户端时从那里取 |

历史上还有一项未收口的工作：2026-08-09 起的 H-001（验证并收口当时未提交的 SSE / Redis / Node / Portal / NativeCore 集成），当时状态为 PARTIAL at INTEGRATED，之后没有它完成的证据；相关门禁仍列在上文“未关闭的门禁”里的跨进程 SSE 一项。

## 路线图

待定。

## 许可证

整个仓库（panel 与 pdnd）按 GNU General Public License v3.0 发布，全文见根目录 [LICENSE](LICENSE)。

pdnd 本来就必须如此：默认构建链接 GPL-3.0 的 mieru 与 sagernet/sing，compat 构建还链接 sing-box。

fork 进来的第三方代码保留各自的许可证：`pdnd/internal/reality/`（MPL-2.0）与 `pdnd/internal/realityquic/`（MIT），见各目录的 LICENSE，两者都与 GPL-3.0 兼容。

## 相关文档

- [docs/CONFIG-SIGNING-KEY-ROTATION.md](docs/CONFIG-SIGNING-KEY-ROTATION.md)、[docs/RELEASE-ARTIFACT-BINDING.md](docs/RELEASE-ARTIFACT-BINDING.md)：密钥轮换与发布物绑定。
- [panel/deploy/BACKUP.md](panel/deploy/BACKUP.md)：备份与恢复。
- [panel/docs/](panel/docs/)：`redesign/api-contract.md` 前后端接口契约（DASH / CLIENT-AUTH 历史冻结稿在 tag `archive/client-auth`）。
- [pdnd/release/README.md](pdnd/release/README.md)：NativeCore Linux 发布与运行时验收。
