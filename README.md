# Pandora Panel（潘多拉面板）

Xboard 类代理订阅面板 + 自研节点端（Pandora NativeCore）。目标是提供 XBoard 级运营能力，并把协议、传输、路由与流量统计逐步收回单一自研内核。别名：AegisPanel / pandora-native。

本文件是项目事实的唯一入口：架构、功能、构建、部署、验证状态、进度与路线图都在这里。AI 协作规则不在这里：根目录 [CLAUDE.md](CLAUDE.md) 是给 Claude Code 的全局约定，仓库公开、不写部署专属值的红线在它的「红线：仓库公开」一节；按目录生效的模块约定在 [.claude/rules/](.claude/rules/)，验证流程与 CI 说明在 [.agents/skills/verify/](.agents/skills/verify/SKILL.md)。

## 架构

面板是一个 Go 二进制 `pandora`，节点端是另一个 Go 二进制 `pandora-native`（pdnd）。设计全文与每条取舍的理由见 [docs/architecture.md](docs/architecture.md)。

| 方面 | 用什么 |
|---|---|
| 语言 | Go 1.26（`go.mod` 的 go 指令），静态编译，无运行时依赖 |
| Web 框架 | gin；public（门户）与 admin（后台）各一个引擎、各一个监听 |
| 命令行与配置 | cobra 子命令；viper 读 `/etc/pandora/config.yaml`，再用 `PANDORA_` 前缀的环境变量覆盖；秘密只能来自环境变量（`secrets.env`） |
| 数据库 | PostgreSQL 18，经 GORM 访问；迁移是 goose 版本化 SQL（`panel/db/migrations/`，嵌入二进制，每个都有 Down），不用 AutoMigrate |
| 缓存、限流、会话 | Redis（go-redis）：会话、refresh、登录失败计数、限流（GCRA）、实时推送、节点下发纪元 |
| 队列与定时任务 | asynq，队列在 Redis 里；Scheduler 只在 worker 里跑一份 |
| 日志 | zap（生产 JSON）+ lumberjack 滚动；每条带 req_id |
| 鉴权 | 15 分钟 JWT（HS256，门户与后台各一组密钥）+ Redis 里的可吊销会话；refresh 放 HttpOnly cookie，轮换带重放检测 |
| 节点 ↔ 面板 | gRPC 双向流 + protobuf（`proto/`，生成代码在 `nodeapi/`），面板自带 CA，mTLS 由面板进程自己终结 |
| 钱 | 库里 `NUMERIC(20,2)`（元），Go 里 shopspring/decimal；`/v1` 的 JSON 仍是整数分；账本只追加、提交时库内配平 |

```text
用户浏览器 / 专属客户端                  节点机：pandora-native（pdnd）
          |                                         |
   nginx（80/443，TLS）                    gRPC 双向流 + mTLS（:9443，直连，不经 nginx）
          |                                         |
  +-------+--------+                                |
  |                |                                |
public HTTP     admin HTTP                          |
127.0.0.1:9000  127.0.0.1:9001                      |
  +-------+--------+--------------------------------+
          |
   pandora serve  ——  pandora worker（asynq 任务与定时调度）
          |                 |
   PostgreSQL 18 ——————— Redis（会话、限流、队列、实时推送、下发纪元）
```

### 一个二进制、几个子命令

| 子命令 | 做什么 |
|---|---|
| `pandora serve` | 门户 HTTP、后台 HTTP、节点 gRPC 三个监听；SSE（`GET /v1/events`）；节点会话与名单下发 |
| `pandora worker` | asynq Server 与 Scheduler：到期、配额滚动、查单、佣金结算、通知派发、证书签发、保留期清理等 |
| `pandora migrate up / down / status` | goose 迁移，用属主角色的连接串（`database.migration_url`） |
| `pandora admin …` | create、reset-password、grant、revoke、list、has-admin；口令只从标准输入读 |
| `pandora backup …` | 备份脚本用的本地工具：封条、核验、WebDAV 异地上传与签名清单 |

生产上 `serve` 与 `worker` 是两个 systemd 服务（`pandora-serve`、`pandora-worker`），以专用用户 `pandora` 运行。

### 模块与分层

```text
panel/internal/
  pkg/      与业务无关的基础设施：config、log、db、tx、redisx、errs、httpx、reqid、jwtx、authn、crypto、
            ratelimit、pagination、money、jobs、realtime、route …
  module/   业务模块，每个模块拆成 model / repo / service / handler / task 子包
  app/      子命令与装配（wire.go 构造各模块 service 并注入彼此的接口）
```

- handler 只做参数绑定与校验、调 service、写响应；每个接口有自己的请求与响应 DTO。
- service 放业务规则和事务边界（`pkg/tx`，提交后的动作用 `tx.AfterCommit` 登记）。
- repo 是唯一使用 GORM 的地方。每张表只有一个属主模块，别的模块要数据就调属主模块的 service。
- 这些规则由 golangci-lint 的 depguard 钉住：`panel/.golangci.yml` 由 `go run ./tools/depguardgen` 从模块目录生成，不手改；CI 检查生成结果没有漂移。

模块：identity、auth、settings、audit、idempotency、catalog、order、payment、ledger、promo、commission、giftcard、subscription、node、certs、notify、support、content、appearance、plugin、report、realtime。各模块拥有哪些表见 [docs/architecture.md](docs/architecture.md) 第 4 节。

### 数据约束

约束类的规则放在库里，业务状态机与编排放在 Go service 里：

- NOT NULL、CHECK（状态枚举、金额非负、订单金额恒等式、库存与额度上限等）、UNIQUE、外键能加就加；
- 账本、审计、订阅事件、签发流水等表只追加：触发器拒绝改删，运行角色也收回 UPDATE / DELETE / TRUNCATE；
- 账本每笔交易提交时（DEFERRABLE 约束触发器）检查借贷配平、单一币种；
- 支付回调按 `(provider_id, provider_event_id)` 唯一，重放只记一笔账。

两个数据库角色：`pandora_owner` 是库属主，只用来跑迁移；服务以 `pandora_app` 运行，不是超级用户、不是表属主，权限由 `panel/deploy/configure-app-role.sql` 收窄。

## 仓库结构

| 路径 | 内容 |
|---|---|
| `panel/` | 面板：用户门户、管理后台、订阅分发、计费、节点编排、审计 |
| `panel/cmd/pandora/` | 唯一的可执行入口 |
| `panel/internal/pkg/`、`app/`、`module/` | 基础设施、子命令与装配、业务模块（见上文「模块与分层」） |
| `panel/db/migrations/` | goose 迁移，按模块一个文件，嵌入 `pandora migrate`；数量与最大号以目录为准 |
| `panel/tools/` | `depguardgen`（生成 depguard 规则）、`refactorcheck`（800 行守卫与纯挪动比对） |
| `panel/tests/` | 端到端脚本（`*_e2e.sh`，对着新二进制跑）与数据层不变量 `invariants.sql` |
| `panel/frontend/` | 面板前端（React + TypeScript + Vite，管理后台与用户门户双入口），构建后经 `make frontend-embed` 嵌入 `panel/web/` |
| `panel/web/` | 前端的 `go:embed` 嵌入点：public 与 admin 在根 `/` 下发各自的入口；仓库只存占位入口，由 `make frontend-embed` 覆盖 |
| `panel/dev/` | 本机开发数据基座（`docker-compose.yml`：PostgreSQL 18 + Redis 8）与环境变量样例 `.env.example` |
| `panel/deploy/` | 发布包构建、安装与升级、nginx 与 HTTPS 边缘、systemd 单元、备份与恢复、巡检脚本，以及它们的桩测试；运维手册 `RUNBOOK.md` |
| `proto/`、`nodeapi/` | 节点协议的 protobuf 源文件（buf）与生成的 Go 代码（单独的 Go module，panel 与 pdnd 用 replace 引用） |
| `pdnd/` | Pandora node（pdnd / pandora-native）：NativeCore 协议入站、认证、路由、用户与流量 |
| `pdnd/kernel/`、`pdnd/internal/` | NativeCore 自研数据面 |
| `pdnd/core/` | 内核适配层：xray-core / sing-box 兼容与外部进程 |
| `pdnd/release/` | Linux amd64/arm64 构建、能力矩阵一致性检查、运行时验收、节点模拟器 `nodesim` |
| `docs/` | 设计稿与专题文档（见文末「相关文档」） |
| `.githooks/`、`.gitleaks.toml` | 提交前密钥扫描：clone 后执行 `git config core.hooksPath .githooks` 启用，需先装 gitleaks；未装时拒绝提交 |
| `.github/workflows/` | CI，各管什么见下文「验证状态与门禁」 |
| `CLAUDE.md`、`.claude/rules/`、`.agents/skills/` | 给 Claude Code 的说明：根 CLAUDE.md 是全局约定与红线，rules 是按路径自动加载的模块约定，skills 是验证与常用流程（`.claude/skills` 是指向 `.agents/skills` 的链接） |

本地快照不含 `.env`、密钥、私钥和编译产物（二进制、`node_modules`、`dist`）。

### 源码与运维的边界

这个仓库是产品：任何人下载后，填上自己的域名就能部署。所以仓库里只有三样东西——

- **代码**；
- **模板**：只含占位符，例如 nginx 模板里的域名写成 `__PANDORA_DOMAIN__`、后台路径写成 `__PANDORA_ADMIN_PATH__`，安装时从 `/etc/pandora/` 下的配置填入；
- **自动化测试**：检查代码对不对，和被测代码放在一起，只用虚构数据（测试夹具里的密钥都是新生成的假值），不进入发布物。

每一套部署自己的值都在安装时产生，不进仓库：对外地址由部署者在首装时给出，主密钥、两把 JWT 密钥、数据库与 Redis 口令、后台路径前缀由安装器（`install.sh`）首装生成。

维护者操作自己服务器的东西——一次性运维脚本、安装日志、真实服务器地址——放在被 git 忽略的 `ops-local/`（按需创建，只存在于维护者本机）；脚本要用的密钥从 1Password 读取，不写进任何文件。提交前的 gitleaks 钩子（见上表 `.githooks/`）会拦下密钥、服务器 IP、后台前缀这类内容，误写进源码也提交不上去。

## panel：面板

### 已实现功能

用户门户（Portal）：

- 注册、登录、找回密码、密码策略；登录和改密页有密码显示按钮。
- 15 分钟访问令牌 + refresh cookie，门户会话 30 天；改密时保留当前会话、吊销其余会话。
- 套餐浏览、订阅购买、续费、换套餐（看报价）、流量包、余额抵扣；订单与支付结果；订阅链接、线路预览与换新链接。
- 工单创建、查看、回复与撤回。
- 邀请返利；佣金转余额、提现申请。
- 站内信、通知偏好；事务类通知锁定。
- 实时推送：`GET /v1/events`（SSE），数据变了页面自己刷新。

管理后台（Admin）：

- 仪表盘：收入、用户、订单、订阅、节点、流量、工单统计；收入调整；趋势；节点 / 用户流量排行。
- 用户（含批量建用户、风控与 IP 归属地）、套餐、订单、优惠券、礼品卡、支付渠道、待复核佣金与提现。
- 服务器与节点（gRPC 接入、一键安装命令、会话状态）、节点池、路由、在线设备与设备数限制、节点入站证书（ACME DNS-01）。
- 公告、知识库、主题、插件、工单、审计日志、降级开关。
- 后台权限只分有和没有：是管理员就能做全部后台操作；会话绝对寿命 7 天、空闲 2 小时退出；管理后台路径是安装时生成的高熵串。

前端在 `panel/frontend`，2026-09-23 起按设计稿从零重写、2026-09-26 完成；换栈时不换前端，接口的 JSON 形状保持不变。发布包构建时把前端嵌入二进制，public 与 admin 的 `/` 分别下发用户门户与管理后台；仓库里 `panel/web/` 只存占位入口。

运维：加密备份（age）、本地封条与 WebDAV 异地签名清单、保留策略、恢复脚本、巡检告警，都在 `panel/deploy/`（`backup-postgres.sh`、`verify-backup.sh`、`restore-postgres.sh`、`healthcheck.sh`），排障见 [panel/deploy/RUNBOOK.md](panel/deploy/RUNBOOK.md)。

### 本地开发

本机开发只需要 Go、Node 22.12+ 与 Docker（数据基座用）。命令都在 `panel/` 下执行，`make help` 列出全部目标。

第一次用：

```bash
cd panel
cp dev/.env.example dev/.env   # 把各处 CHANGE_ME 换成随机串（URL-safe；连接串里的口令要与对应口令项一致）
make up                        # 起开发数据基座：PostgreSQL 18 + Redis 8（只绑 127.0.0.1:5433 / 6380）
make migrate                   # pandora migrate up
```

`dev/.env` 被 git 忽略；`make migrate / serve / worker` 先把它导进环境再 `go run ./cmd/pandora`，`deploy/psql.sh` 也读它。开发模式（`PANDORA_ENV=development`）直接以库属主 `pandora_owner` 跑，不建 `pandora_app`。

日常：

```bash
make serve             # 门户 :9000、后台 :9001、节点 gRPC :9443
make worker            # 队列与定时任务
make migrate-status    # 迁移状态
make down              # 停数据基座（保留数据卷）；make logs 看日志
make test              # Go 单测；要数据库的 *_pg_test.go 读 PANDORA_TEST_DATABASE_URL，没设就跳过
make vet
make invariants        # 数据层不变量 tests/invariants.sql（要先 make up、make migrate）
make frontend-check    # 前端：npm ci + lint + typecheck + vitest + 双入口构建
make frontend-embed    # 构建前端并同步进 web/{admin,portal}
```

建管理员（口令从标准输入读）：

```bash
read -rsp '密码：' p && echo && printf '%s\n' "$p" | (set -a && . ./dev/.env && set +a && go run ./cmd/pandora admin create --email admin@example.test --password-stdin); unset p
```

分层规则改了模块目录（加模块、加子包）后，重新生成 depguard 配置并跑 lint：

```bash
go run ./tools/depguardgen
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.5.0 run ./...
```

本机没有 Docker 时，`make up` 与要数据库的测试都跑不了；跳过不等于通过，以 CI 为准。前端只看页面时可以不起后端：`cd frontend && npm run dev:admin`（或 `dev:portal`）用内置的假后端。

### 端口

| 端口 | 用途 |
|---|---|
| 127.0.0.1:9000 | 门户 HTTP（`http.public.addr`）；生产必须绑回环，前面是 nginx |
| 127.0.0.1:9001 | 后台 HTTP（`http.admin.addr`）；同上 |
| :9443 | 节点 gRPC（`grpc.node.addr`），对公网开放，面板自己终结 mTLS，不经 nginx |
| 80 / 443 | nginx：80 跳 443（ACME 验证除外），443 反代门户与后台 |
| 127.0.0.1:9080 | nginx 的本机回环运维入口（安装与巡检的收尾验证走它） |
| 5433 / 6380 | 开发数据基座的 PostgreSQL / Redis（`panel/dev/docker-compose.yml`）；生产机上它们是系统服务 |

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
cd panel && go build ./...          # 或 make build，产物在 panel/bin/pandora
cd pdnd  && go build -o pdnd .
```

Go 版本按各自 `go.mod` 的 go 指令（现在是 1.26.9）。更新的 Go 1.27 下 pdnd 链接失败（sing-box 用 `go:linkname` 取 x/net 的私有符号），本机工具链更新时加 `GOTOOLCHAIN=go1.26.9`。

pdnd 的 Linux x64/ARM64 发布包用 `pdnd/release/build.sh`，生成带 SHA-256 的 `manifest.json`。Ubuntu runner 的 race/vet 与双架构构建门禁见 `.github/workflows/pandora-native.yml`。Windows / macOS 交叉构建不能替代真实 Linux race。

## 部署

一条命令装完。首装和升级是同一个入口：安装器 `install.sh` 按 `/etc/pandora/secrets.env` 在不在判断走哪条路。生产机不用 Docker。两条路装出来一样：第 1 节从 GitHub Release 一条命令装，第 2 节自己从源码打包。

目标机器：Debian 12–14 / Ubuntu 22.04–26.04（x86_64 或 aarch64）、systemd、root，外加 `curl` 与 `openssl`；安装器用 apt 装好 PostgreSQL 18（PGDG 源）、发行版的 `redis-server`（要求 6.0 以上）、nginx 与 age。

### 1. 一条命令装（GitHub Release）

```bash
curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh | sudo bash
```

`get.sh` 依次：

1. 检查平台（Linux x86_64 / aarch64）、root 权限与依赖（`curl`、`tar`、`gzip`、`sha256sum`）；
2. 解析版本（`PANDORA_VERSION` 或 GitHub `releases/latest` 跳转，不调 API）；
3. 下载对应架构的 `.tar.gz` 与 `.sha256` 并核对；
4. 解到 `/opt/pandora-release/<版本>/`（先临时目录再改名；同版本已存在时用发布的 `.manifest.sha256` 与目录内 `SHA256SUMS` 核对，一致则复用，不一致则停下、不覆盖）；
5. 运行包内 `deploy/install.sh`，环境变量原样传入（安装器八步见第 3 节）。

首装会问：面板对外地址（`https://域名` 或 `https://公网IPv4`，不带端口与路径；直接回车用本机公网 IPv4；不合规在动手前停下；节点连面板的 gRPC 主机缺省取该地址的主机部分）；第一个管理员的邮箱与口令（口令不回显，直接回车跳过，之后用收尾提示里的命令手工建）。`curl … | sudo bash` 时脚本是从管道读进来的，`get.sh` 会把终端接给 `install.sh` 提问；没有终端（ssh 未带 `-t`、cloud-init、CI）且未设 `PANDORA_ASSUME_YES=1` 或 `PANDORA_PUBLIC_BASE_URL` 时会停下并给出两种写法；设了 `PANDORA_ADMIN_EMAIL` 却用 `curl … | bash` 时也会停下，提示先下载 `get.sh` 再运行（`install.sh` 要从标准输入读口令，管道模式下标准输入是脚本本身）。

无人值守时变量要写在 `sudo` 后面（`sudo` 默认不把调用者的环境变量带过去）：

```bash
curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh \
  | sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash
```

无人值守同时建第一个管理员：`install.sh` 从标准输入读一行口令，而 `curl … | bash` 时标准输入就是脚本本身，所以先下载 `get.sh` 再运行：

```bash
curl -fsSLo get.sh https://github.com/shiheishan/pandora/releases/latest/download/get.sh
read -rsp '管理员口令：' pw && echo
printf '%s\n' "$pw" | sudo PANDORA_ADMIN_EMAIL=admin@example.test PANDORA_ASSUME_YES=1 \
  PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash get.sh
unset pw
```

其余开关（`PANDORA_GRPC_PUBLIC_HOST`、`PANDORA_SKIP_NGINX`、`PANDORA_ACME*`、`PANDORA_SYSTEMD_HARDENING`）见 `install.sh` 头注释。

升级：再跑同一条 `curl … | sudo bash`；取到新版本按升级走（停服 → 升级前全量备份 → 换程序 → 迁移 → 起服务）；同一版本再跑也会走一遍幂等的升级流程。

指定版本、fork：`curl … | sudo PANDORA_VERSION=v1.2.3 bash`（用来装预发布或某个新版本；**不能用来回退**：装旧版本会走升级流程把旧程序换上去，而迁移不会自动降级，旧程序会跑在新库结构上。回退按 `panel/deploy/RUNBOOK.md` 先恢复升级前备份或回退迁移，再装旧包）；带连字符的预发布标签（如 `v1.3.0-rc.1`）不会出现在 `latest`，只能这样装；`PANDORA_REPO=<owner>/<repo>` 从 fork 装（缺省 `shiheishan/pandora`）。

`get.sh` 不自动删旧版本目录；`/opt/pandora-release` 下 `v*` 目录超过 3 个时会打印删除命令。

核对来源：`.sha256` 与包同源，只防下载损坏；防篡改用 `gh attestation verify` 核对 GitHub Actions 出的来源证明（两个架构包、`get.sh`、`SHA256SUMS` 均有）；`install.sh` 运行时再按包内 `SHA256SUMS` 逐文件核对，并拒绝不归 root 或别人可写的目录。

```bash
curl -fsSLO https://github.com/shiheishan/pandora/releases/download/v1.2.3/pandora-panel_v1.2.3_linux_amd64.tar.gz
gh attestation verify pandora-panel_v1.2.3_linux_amd64.tar.gz --repo shiheishan/pandora \
  --signer-workflow shiheishan/pandora/.github/workflows/release.yml --source-ref refs/tags/v1.2.3
sudo mkdir -p /opt/pandora-release/v1.2.3
sudo tar -xzf pandora-panel_v1.2.3_linux_amd64.tar.gz -C /opt/pandora-release/v1.2.3
sudo bash /opt/pandora-release/v1.2.3/pandora-panel_v1.2.3_linux_amd64/deploy/install.sh
```

须先安装 `gh` 并 `gh auth login`。`--signer-workflow` 与 `--source-ref` 限定证明必须由这个仓库的 `release.yml` 在该版本标签上签出，别的分支、别的 workflow 签的不算。核过后直接用这份已核对的包装（上面最后三行，解到 root 独占的目录，安装器才肯装），不要再经 `get.sh` 重新下载：发布后附件仍可能被替换，重新下载的就不是核过的那份。`get.sh`、`SHA256SUMS` 也可同样核对。

发版（维护者）：在 `main` 上打 `v*` 标签并推送，`.github/workflows/release.yml` 构建 amd64 / arm64 包、出来源证明、建 Release；标签提交必须已在 `main` 上，否则 workflow 拒绝；带连字符的标签发成预发布；手动触发（`workflow_dispatch`，输入版本号，缺省 `v0.0.0-dryrun`）只演练构建，产物在该次 run 的 artifact 里，不建 Release；手动触发要等 `release.yml` 进了 `main`（GitHub 的限制），之后可用 `gh workflow run release.yml --ref <分支>` 演练任一分支。

### 2. 从源码打包

在有 Go、Node 22.12+/npm、GNU tar、curl 的机器上（不需要是目标机器）：

```bash
cd panel && ./deploy/build-release.sh /path/to/dist
```

`build-release.sh` 先跑 `make frontend-embed` 把前端嵌进二进制（没有 npm 即失败），再按 `go.mod` 固定的 Go 版本交叉编译。默认同时出 amd64 与 arm64 两份；`PANDORA_VERSION=v1.0.0` 指定版本号，不给就取 `v*` 标签或提交号。每个架构产出一个目录和同名 `.tar.gz`（附 `.sha256` 与 `.manifest.sha256`）：

| 内容 | 说明 |
|---|---|
| `bin/pandora` | 面板单二进制 |
| `pdnd-dist/` | 两个架构的节点端，节点一键安装时由面板下发 |
| `geoip/ip2region_v4.xdb` | IP 归属地离线库（构建时按固定提交下载并核对 SHA-256） |
| `deploy/` | 安装与运维脚本、`config.example.yaml`、`secrets.env.example`、nginx 与 PostgreSQL 配置、`configure-app-role.sql`、`RUNBOOK.md`、systemd 单元 |
| `SHA256SUMS` | 全量校验和；安装器先核对它 |

丢到目标机，解到与 `get.sh` 相同的布局（`/opt/pandora-release/<版本>/`）再运行安装器：

```bash
scp pandora-panel_<版本>_linux_amd64.tar.gz root@目标机:/root/
ssh -t root@目标机 'mkdir -p /opt/pandora-release/<版本> \
  && tar -xzf /root/pandora-panel_<版本>_linux_amd64.tar.gz -C /opt/pandora-release/<版本> \
  && bash /opt/pandora-release/<版本>/pandora-panel_<版本>_linux_amd64/deploy/install.sh'
```

安装器拒绝从不归 root 或别人可写的目录安装（防止有人塞一份假的进来），所以包要放在 root 独占的目录下；归档里的属主与权限已经按这个要求打好。

无人值守：`sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash <发布目录>/deploy/install.sh`。

### 3. 安装器做什么

脚本分八步，任一步失败即停，并说清停在哪、怎么恢复：

1. 装系统依赖，核对 Redis 版本；
2. 建用户 `pandora` 与目录；
3. 准备 PostgreSQL 18 集群；
4. 升级时停服并做升级前全量备份（`/var/backups/pandora/`）；
5. 首装生成 `/etc/pandora/{config.yaml,secrets.env,deploy.env}` 与备份加密密钥，调 PostgreSQL 参数，给 Redis 写口令与 pandora 配置块（`noeviction`、AOF 每秒刷盘、不做 RDB、禁 FLUSHALL / FLUSHDB）；
6. 建库角色（`pandora_owner`、`pandora_app`）→ 换程序到 `/opt/pandora`（上一版留作 `bin/pandora.prev`）→ 以属主角色 `pandora migrate up` → 用 `configure-app-role.sql` 收窄 `pandora_app`；迁移失败不起服务；
7. 装 systemd 单元并启动 `pandora-serve`、`pandora-worker`、`pandora-health.timer`，查两个 `/readyz` 与 worker 心跳，在 ufw / firewalld 上放行 80、443、9443；
8. 建第一个管理员（终端里现场问邮箱与口令，不回显；无人值守只打印该跑的命令），配 HTTPS 边缘（证书 → 渲染 nginx → 续期 timer，`deploy/edge-tls.sh`）。

三条硬约束：

1. **幂等**。三个配置文件首装生成、升级不改：里面是随机生成的密钥，重写一次就再也解不开封存的字段了。
2. **升级前自动全量备份**；迁移失败时上一版程序与备份路径都会打印出来。
3. **不替你造管理员**。

### 4. 装完

装完会打印后台地址（路径是安装时生成的高熵串，泄露等同暴露入口），之后用 `sudo /opt/pandora/deploy/admin-url.sh` 重看。

| 是什么 | 在哪 |
|---|---|
| 程序与脚本 | `/opt/pandora/bin/pandora`、`/opt/pandora/deploy/` |
| 配置 | `/etc/pandora/config.yaml`（非秘密，`root:pandora` 0640）；`secrets.env`（服务的连接串、主密钥、JWT 密钥）；`deploy.env`（部署脚本用的超级用户口令、迁移连接串、后台路径前缀，root 0600） |
| 日志 | `/var/log/pandora/serve.log`、`worker.log`（进程自己滚动）；巡检 `/var/log/pandora/health.log` |
| 备份 | `/var/backups/pandora/`；备份解密私钥 `/etc/pandora/backup-age.key`，要另存到别处 |
| systemd | `pandora-serve`、`pandora-worker`；timer：`pandora-backup`、`pandora-health`、`pandora-tls-renew` |

```bash
systemctl status pandora-serve pandora-worker           # 状态
journalctl -u pandora-serve -n 100                      # 启动与崩溃
tail -f /var/log/pandora/serve.log                      # 运行日志
sudo /opt/pandora/deploy/psql.sh                        # 进库（超级用户）
sudo /opt/pandora/deploy/backup-postgres.sh             # 立刻做一份加密备份；每日：systemctl enable --now pandora-backup.timer
sudo /opt/pandora/deploy/edge-tls.sh status             # 面板 HTTPS 证书
```

`pandora admin …` 要用服务的身份和秘密跑：

```bash
sudo systemd-run --pipe --quiet --uid=pandora -p EnvironmentFile=/etc/pandora/secrets.env /opt/pandora/bin/pandora admin list
```

nginx 只反代门户与后台的 HTTP；节点 gRPC（9443）由面板直接对外。模板 include 的真实来源 IP 信任表 `/etc/pandora/cloudflare-realip.conf` 不存在时，渲染器写一份**不信任任何代理**的默认文件：nginx 只认 TCP 对端，客户端自己填的 `CF-Connecting-IP` / `X-Real-IP` 一律不采信。站点在 Cloudflare 后面（橙色云）时，渲染后再写入 Cloudflare 的官方网段并重载；升级重新渲染不会覆盖这个文件，Cloudflare 调整网段时重跑同一条命令即可：

```bash
/opt/pandora/deploy/update-cloudflare-realip.sh && nginx -t && systemctl reload nginx
```

节点接入：后台建服务器后生成一次性安装命令（`curl -fsSL '<面板地址>/pdnd/install.sh' | sudo bash -s -- --panel '<gRPC 主机>:9443' --ca-sha256 'sha256:<指纹>' --token '<令牌>'`），节点按 CA 指纹钉住面板、用令牌经 gRPC 换来客户端证书。没划进节点池的节点不服务任何人。

排障按症状查 [panel/deploy/RUNBOOK.md](panel/deploy/RUNBOOK.md)。

### 回归测试

安装链的回归测试是 `panel/deploy/` 下的 `*_mock_test.sh`、`*_static_test.sh` 等：把 systemctl、apt、psql 等换成桩，不需要目标机器（个别项要 root 或 PostgreSQL，只在 CI 里跑），随 CI 的 `panel-deploy.yml` 跑。真机上的首装与原地升级用一次性测试机验证。

### 上线前检查

隐藏的后台路径挡不住公网扫描；来源、速率和端口最终要在云防火墙、安全组和反向代理层收住。

1. 只开放 80、443 与节点 gRPC 端口（9443）；
2. PostgreSQL、Redis 与面板的 HTTP 端口只绑回环，不对外；
3. 管理路径再加一层 IP 白名单、二次验证或 VPN 入口；
4. 注册、登录、订阅、工单接口配置限速与挑战策略；
5. 把备份解密私钥另存到机器之外；
6. 不把密钥、`/etc/pandora/` 下的文件、私钥和真实备份地址写进仓库。

## 验证状态与门禁

状态标记沿用项目约定：FACT 有命令或源码直接证明；INFERENCE 是基于证据的判断；UNKNOWN 尚缺验收证据。静态对齐、进程存在或 HTTP 200 不等于功能验收。

### 已有证据（FACT）

**CI 门禁**：各 workflow 管什么、改了什么要等哪个结论、PG 用例不许跳过，一律以 `.agents/skills/verify/SKILL.md` 为准，这里只列名字（数字和 job 清单一改就过时）：

- `panel-newstack.yml`：golangci-lint 与 depguard 漂移检查；PostgreSQL 18 + Redis 上的 `go test ./...`（含迁移往返）；新二进制上的 e2e（`panel/tests/*.sh`）与节点通道 e2e（gRPC 节点模拟器冒烟、到期、真 pdnd 删人断线）。
- `panel-smoke.yml`：前端 schema 对新二进制真实响应的冒烟，加 Playwright 浏览器购买路径。
- `panel-deploy.yml`：部署脚本的桩测试、Redis 加固块的多版本检查、PG18 上的角色与授权。
- `pandora-native.yml`：pdnd 门禁、面板前端检查与嵌入、NativeCore 与面板协议对齐、双架构发布构建。
- `nodeapi.yml`：buf lint、格式、生成无差异、`buf breaking`。
- `go-vulncheck.yml`：Go 依赖漏洞扫描。

只改仓库根的文档不触发 workflow。

- **仓库守卫**（随 `go test ./...`）：panel 与 pdnd 两道 800 行守卫（`panel/tools/refactorcheck/linelimit_test.go`、`pdnd/linelimit_test.go`）；panel 已没有行数豁免，pdnd 只豁免 fork 来的 reality 代码。
- 2026-09-23 起 workflow 默认 `shell: bash`（`-eo pipefail`）。在此之前 `go test … | tee` 的失败会被 `tee` 吞掉，**那之前的 race 绿灯不能当证据**。

**历史证据**（早期快照，不等于当前代码）：

- 2026-09-26：旧栈 `main` 上三组 workflow 全绿（run `36230330995` / `36230331009` / `36230331011`）。
- 2026-08-11：pdnd 在 Linux amd64 隔离目录通过外部 Xray 的 REALITY+XHTTP+H3 互操作测试（`TestExternalXrayVLESSXHTTPH3Interop`），Xray 客户端互操作覆盖 REALITY+XHTTP/H1、H2 与普通 TLS+XHTTP/H3。
- 2026-08-11：在远端旧快照上完成隔离 WebDAV + PostgreSQL 18 备份恢复演练（HTTPS 上传、签名 manifest、SHA-256、Age 加解密、全新实例恢复），RTO 约 2 秒。
- 更早：Debian x86_64 完整 race 与 13 协议逐项测试曾通过。

### 未关闭的门禁（UNKNOWN）

- **新安装脚本的真机端到端**：`install.sh` 首装与原地升级、nginx、LE IP 证书、systemd、加密备份、节点一键安装与 gRPC 接入已在全新测试机上跑通（P8，10-14），`build-release.sh` 含打包归档的整条构建也已在 Linux 上跑过。没覆盖到的：没有用真客户端连节点测数据面。
- 门户各视口没有浏览器矩阵；后台只支持 ≥960 宽度。
- REALITY+XHTTP/H3 的独立公网第三方客户端端点验证；amd64 外部 Xray 测试不能替代。
- 13 个协议的全部传输组合与独立客户端协议矩阵。
- ARM64：CI 原生门已通过，安装式 ARM64 运行仍 UNKNOWN；交叉编译或静态 ELF 检查不能替代。
- 实时推送：Redis 断线重连、慢消费者不阻塞，在真实负载下的表现。
- 真实支付、通知外发的独立验收。
- WebDAV 在新栈上的真实远端恢复演练。
- 换栈后的性能与占用：P8 第一轮测（[docs/perf-test-plan.md](docs/perf-test-plan.md)）。

## 当前进度

截至 2026-10-13：

| 项 | 状态 |
|---|---|
| 方向 | 后端已换成主流技术栈（见「架构」与 [docs/architecture.md](docs/architecture.md)）；还没有任何生产部署，新栈直接替换，不做数据迁移与兼容期 |
| 换栈 | P0–P7 完成（2026-10-13）：全部模块在单二进制 `pandora` 上；旧栈代码、旧迁移、旧部署脚本与旧 CI 已删；安装脚本改为单二进制直装；e2e、前端冒烟与 Playwright 购买路径都对新二进制跑。真机安装验证随 P8 |
| 分支 | `main` 是发布分支，发版在 `main` 上打 `v*` 标签（`.github/workflows/release.yml` 构建、出来源证明、建 Release）；日常开发在 `feat/panel-redesign` |
| CI | 以 `feat/panel-redesign` 上的最新结论为准（见上文门禁清单） |
| 前端 | `panel/frontend` 不换，接口 JSON 形状保持不变；鉴权改为短期 access 令牌 + refresh cookie |
| 客户端登录（CLIENT-AUTH） | 2026-10-05 移出主线，专心做面板：设备码登录与设备签名的实现代码、发布门禁脚本和 8 个 `pandora-*` 命令在 tag `archive/client-auth`；三份冻结设计稿和两个从未应用的迁移也一并存档在该 tag，以后做客户端时从那里取 |

## 路线图

换栈分步走，每步的内容与完成标准见 [docs/architecture.md](docs/architecture.md) 第 17 节：

| 步 | 内容 | 状态 |
|---|---|---|
| P0 | 设计定稿 | 完成 |
| P1 | 骨架：`pandora` 单二进制、基础设施包、asynq、goose、lint 与 CI | 完成 |
| P2 | 鉴权与用户：identity、auth、settings、audit、idempotency；refresh；前端令牌改造 | 完成 |
| P3 | 交易：目录、订单、支付、账本、优惠、佣金、礼品卡（decimal） | 完成 |
| P4 | 订阅：订阅模块、订阅链接渲染、到期与配额任务 | 完成 |
| P5 | 节点：gRPC 双向流 + mTLS，pdnd 客户端改造 | 完成：CI 上 gRPC 节点模拟器冒烟（200 用户 5 节点零 5xx）、到期 e2e、真 pdnd + sing-box 删人断线全绿 |
| P6 | 其余模块：证书、通知、工单、内容、外观、插件、报表；实时推送 | 完成 |
| P7 | 切换与删旧：安装脚本改单二进制与 Redis，删除旧代码与旧迁移，重写文档 | 完成（10-13）；真机安装验证 10-14 随 P8 完成 |
| P8 | 换栈后的性能与占用优化 | 第一轮测试完成（10-14，5000 用户、200 台）：空载固定约单核 4.6%、每台服务器约单核 0.05%；到期突发、真节点名单应用正常；面板重启重连 200 台约 30 秒、订阅拉取 2 核约 50 次/秒饱和；优化项待定（[docs/perf-test-plan.md](docs/perf-test-plan.md)） |

## 许可证

整个仓库（panel 与 pdnd）按 GNU General Public License v3.0 发布，全文见根目录 [LICENSE](LICENSE)。

pdnd 本来就必须如此：默认构建链接 GPL-3.0 的 mieru 与 sagernet/sing，compat 构建还链接 sing-box。

fork 进来的第三方代码保留各自的许可证：`pdnd/internal/reality/`（MPL-2.0）与 `pdnd/internal/realityquic/`（MIT），见各目录的 LICENSE，两者都与 GPL-3.0 兼容。


## 相关文档

- [docs/architecture.md](docs/architecture.md)：换栈的设计全文（技术选型、模块与表属主、鉴权与会话、钱、节点通道、任务、配置、分步计划与产品结论）。
- [docs/node-certificates.md](docs/node-certificates.md)：节点入站证书（file 模式与面板集中签发）。
- [docs/perf-test-plan.md](docs/perf-test-plan.md)：P8 第一轮性能测试范围。
- [docs/backlog.md](docs/backlog.md)：面板待办清单；前后端接口以代码为准：后端看各模块 `panel/internal/module/*/handler`，前端看 `panel/frontend/src/core/api.ts` 与各页面的 `api.ts`。
- [pdnd/release/README.md](pdnd/release/README.md)：NativeCore Linux 发布与运行时验收。
- [panel/deploy/RUNBOOK.md](panel/deploy/RUNBOOK.md)：已装面板的排障手册（按症状分章，巡检告警逐条有对应章节）。
