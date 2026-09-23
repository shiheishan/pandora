# Pandora Panel（潘多拉面板）

Xboard 类代理订阅面板 + 自研节点端（Pandora NativeCore）。目标是提供 XBoard 级运营能力，并把协议、传输、路由与流量统计逐步收回单一自研内核。别名：AegisPanel / pandora-native。

本文件是项目事实的唯一入口：架构、功能、构建、部署、验证状态、进度与路线图都在这里。AI 协作规则不在这里：根目录 [CLAUDE.md](CLAUDE.md) 是 GEB 分形文档协议与 L1 地图，各模块目录的 CLAUDE.md 是 L2 成员清单，操作红线见 [docs/CONSTRAINTS.md](docs/CONSTRAINTS.md)。

## 架构

技术栈：**Go 1.26 + PostgreSQL 18 + Valkey 8（Redis 兼容）**，选型理由见 [ADR-0001](panel/docs/adr/0001-technology-stack.md)。

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

面板按域拆成四个独立的网关进程：

| 进程 | 职责 |
|---|---|
| `aegis-public` | 用户门户与订阅分发 |
| `aegis-admin` | 管理后台 |
| `aegis-node` | 节点接入（UniProxy 兼容） |
| `aegis-agent` | 节点引导 |

数据层用行级安全（RLS）做租户隔离；审计、账本、流量重置等表是追加写，由数据库触发器强制。应用角色 `aegis_app` 是非超级用户且不绕过 RLS。管理端、门户与节点接入都提供 SSE 事件流，跨进程分发走 Redis/Valkey（验证状态见下文门禁）。

## 仓库结构

| 路径 | 内容 |
|---|---|
| `panel/` | 面板：用户门户、管理后台、订阅分发、计费、节点编排、审计 |
| `panel/cmd/` | 各域网关与运维工具的可执行入口 |
| `panel/internal/` | `api/`（public、admin、node 路由与处理器）、`domain/`（业务域）、`middleware/`（认证、限流、幂等、租户注入）、`platform/`（配置、数据库、加密、日志、审计、令牌） |
| `panel/tests/` | 数据层不变量 SQL 与端到端脚本 |
| `panel/frontend/` | React + TypeScript + Vite 候选前端；`make frontend-embed` 嵌入网关，在 `/app/` 下发，尚未切为生产入口；待接后端契约登记在 `src/core/contracts.ts`，旧页独有操作登记在 `tests/legacy-parity.ts` |
| `panel/web/` | 网关下发的全部前端，`go:embed` 进二进制：`/` 下是生产手写单页，`/app/` 下是 React 候选产物（仓库只存占位入口） |
| `panel/migrations/` | SQL 迁移，按序号递增，当前 00001–00067 共 67 个；00067 删除 21 张无依赖孤儿表（未在任何环境执行）；`RESERVED-TABLES.md` 登记其余 16 张 Go 从不引用的表及锁定原因 |
| `panel/deploy/` | 安装、迁移、备份、WebDAV、Nginx、systemd、PG18 与 UI 验收脚本 |
| `panel/docs/` | XBoard 对标与实施计划、DASH / CLIENT-AUTH 冻结契约、ADR |
| `pdnd/` | Pandora node（pdnd / pandora-native）：NativeCore 协议入站、认证、路由、用户与流量 |
| `pdnd/kernel/`、`pdnd/internal/` | NativeCore 自研数据面 |
| `pdnd/core/` | 内核适配层：xray-core / sing-box 兼容与外部进程 |
| `pdnd/release/` | Linux amd64/arm64 构建、能力矩阵一致性检查、运行时验收 |
| `nodeagent/` | `pdnd/` 的陈旧祖先：两个 go.mod 同为 `github.com/aegispanel/nodeagent`，`panel/deploy/systemd/` 仍留有 `aegis-nodeagent.service` 与 `overrides/aegis-nodeagent-small-shared-host.conf`，但 `build-release.sh` 不打包它、所有安装脚本都不安装不启用它（`install-native.sh` 的 `systemd/*.service` 通配会把该单元拷进 `/opt/pandora/deploy/`，不启用）；测试机上的 `aegis-nodeagent` 进程来源待查证，查清前保留 |
| `docs/` | 密钥轮换、发布物绑定、交接记录、约束清单 |
| `.githooks/`、`.gitleaks.toml` | 提交前密钥扫描：clone 后执行 `git config core.hooksPath .githooks` 启用，需先 `brew install gitleaks`；未装 gitleaks 时拒绝提交 |
| `.github/workflows/` | pdnd 的 Ubuntu race / vet 与双架构构建门禁；panel 的 web 嵌入页、表登记簿、权限字典契约测试，React 候选前端 typecheck / vitest（含旧页迁移清单）/ 构建，以及真实产物嵌入后的 `/app/` 下发测试 |
| `CLAUDE.md`（根目录及各模块目录） | GEB 分形文档地图：根为 L1 项目宪法，模块目录为 L2 成员清单，源文件头部为 L3 契约 |

本地快照不含 `.env`、密钥、私钥和编译产物（二进制、`node_modules`、`dist`）。

### 源码与运维的边界

这个仓库是产品：任何人下载后，填上自己的域名就能部署。所以仓库里只有三样东西——

- **代码**；
- **模板**：只含占位符，例如 nginx 模板里的域名写成 `__AEGIS_DOMAIN__`，安装时从 `.env` 填入；
- **自动化测试**：检查代码对不对，和被测代码放在一起，只用虚构数据（测试夹具里的密钥都是新生成的假值），不进入发布物。

每一套部署自己的值都在安装时产生，不进仓库：域名由部署者写进 `.env`，主密钥、JWT 密钥、数据库口令和后台路径前缀由 `install.sh` 首装生成。

维护者操作自己服务器的东西——测试机一次性脚本、安装日志、真实服务器地址——放在被 git 忽略的 `ops-local/`，只存在于维护者本机；这些脚本要用的密钥从 1Password 读取，不写进任何文件。提交前的 gitleaks 钩子（见上表 `.githooks/`）会拦下密钥、服务器 IP、后台前缀这类内容，误写进源码也提交不上去。

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
- 节点、服务器、路由、权限组、节点排序（Node Fabric）。
- 公告、知识库、主题、插件管理。
- 工单、审计日志、风控与降级开关。管理后台路径是安装时生成的高熵串。

礼品卡、知识库、主题、插件、套餐、订单操作均已有真实代码；早期文档里“仅占位”的说法已过时。

前端有两套实现并存：生产入口 `/` 下发的是 `panel/web` 的手写单页；`panel/frontend` 的 React 工程是候选，已能从同一个二进制在 `/app/` 下发（旧控制台顶栏“试用新版”），尚未切为入口。旧页有而 React 没有的操作（管理端 27 条、门户 13 条 v1 路径）登记在 `panel/frontend/tests/legacy-parity.ts`，清单归零才切换。React 工程里按未落地后端写的入口（退款工作台、主动查单、共享路由组、优惠券编辑、模板预览、调账回查）已登记为待接后端契约并默认关闭，见 `panel/frontend/README.md` 的“待接后端契约”；`npm test` 里的 API 契约检查会拒绝任何未登记的前后端漂移。

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
| 四域令牌不可交叉 | 每域独立 HMAC 密钥，域名参与签名 | EXT-001 |

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
make frontend-check    # React 候选前端：npm ci + typecheck + vitest（含 API 契约检查）+ 构建
make frontend-embed    # 构建 React 候选并同步进 web/*/app，由网关在 /app/ 下发；release-linux 会先跑它
make e2e               # 端到端链路：注册→下单→支付→账本→订阅→配置
make verify            # vet + check-migrations + invariants，提交前跑
```

`make help` 列出全部目标。`panel/tests/` 下另有 admin、node、uniproxy、epay、support 的端到端脚本。

### 端口

全部只绑 `127.0.0.1`，公网访问由反向代理接入。地址可用 `AEGIS_*_ADDR` 环境变量覆盖，见 `panel/deploy/.env.example`。

| 端口 | 用途 |
|---|---|
| 9000 | Public API |
| 9001 | Admin API |
| 9002 | Client API |
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

在有 Go 工具链的机器上（不需要是目标机器）：

```bash
cd panel && ./deploy/build-release.sh /tmp/dist
```

产出 `/tmp/dist/pandora-panel_<版本>_linux_<架构>/`，约 150 MB，内容是自足的：

| 目录 | 内容 |
|---|---|
| `bin/` | 四个网关 + 运维工具 + goose（迁移工具版本随包固定，不跟 `@latest` 漂移） |
| `deploy/` | 安装、迁移、备份、nginx 渲染脚本与 systemd 单元 |
| `migrations/` | 全部 SQL 迁移 |
| `pdnd-dist/` | 节点端二进制 |
| `SHA256SUMS` | 全量校验和 |

同时构建两种架构：`PANDORA_VERSION=v1.0.0 ./deploy/build-release.sh /tmp/dist`。

### 2. 丢过去装

目标机器需要 Linux + root + docker，以及 `openssl sha256sum systemctl install awk sed curl`。

```bash
scp -r /tmp/dist/pandora-panel_*_linux_amd64 root@目标机:/opt/pandora-release/rel
ssh root@目标机 'cd /opt/pandora-release/rel/deploy && ./install.sh'
```

无人值守加 `PANDORA_ASSUME_YES=1`。

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

三个网关只监听 `127.0.0.1`，公网访问要在前面放反向代理并配 TLS。先把 `/opt/aegispanel/deploy/.env` 里的
`AEGIS_PUBLIC_BASE_URL` 改成 `https://你的域名`，再渲染 nginx 配置：`server_name` 和 Let's Encrypt 证书路径都从这个域名生成，
没填、仍是示例值、不是 HTTPS 域名时脚本拒绝渲染。

```bash
/opt/aegispanel/deploy/render-nginx.sh
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

## 验证状态与门禁

状态标记沿用项目约定：FACT 有命令或源码直接证明；INFERENCE 是基于证据的判断；UNKNOWN 尚缺验收证据。静态对齐、进程存在或 HTTP 200 不等于功能验收。

### 已有证据（FACT）

- Portal 真实 Chrome Playwright 通过 390 / 820 / 1440 / 3840 四个宽度，覆盖工单撤回、佣金换算、通知偏好、XSS、内部备注过滤和稳定幂等键。
- 2026-09-21：`python pdnd/release/check_native_panel_parity.py` 在本快照上输出 `NATIVE_PANEL_PARITY_OK`（NativeCore、Panel Schema、serving allowlist 各 13 个协议一致）。
- Xray 客户端互操作：REALITY+XHTTP/H1、H2 与普通 TLS+XHTTP/H3。
- 2026-08-11：当时工作树的 pdnd 在 Linux amd64 隔离目录通过外部 Xray 的 REALITY+XHTTP+H3 互操作测试（`TestExternalXrayVLESSXHTTPH3Interop`）；panel、pdnd、pdnd `-tags compat` 的 `go test -p 1` 与 `go vet` 本地通过。详见 [docs/CLAUDE_HANDOFF_2026-08-11.md](docs/CLAUDE_HANDOFF_2026-08-11.md)。
- 2026-08-11：在远端旧快照上完成隔离 WebDAV + PostgreSQL 18 备份恢复演练（HTTPS 上传、签名 manifest、SHA-256、Age 加解密、全新实例恢复），RTO 约 2 秒。这是旧快照证据，不等于当前工作树的生产验收。
- 历史 Debian x86_64 完整 race 与 13 协议逐项测试曾通过；同样是早期快照，不等于当前工作树。
- 未提交的 SSE / Node 集成曾在 Debian 做过受影响包的 compile-only 验证。
- 2026-09-23：GitHub Actions 首跑 `35827175294`（推送 `f1390b3` 触发）失败。逐步日志：`Native capability smoke` 仍 grep 旧名 `"reality-h3"`（能力矩阵发布的是 `reality-h3-experimental`），其后 self-check、H3 探针、依赖边界、compat 编译、外部 Xray 五步从未执行；React candidate 有 10 个用例超 vitest 默认 5s。看板上显示为绿的 amd64 / arm64 两条 race 步骤实际也失败：`cmd/pandora-h3-probe` 的 `TestProcessProbeRoundTrip` 两架构都在 20.1s 超时，arm64 另有 `kernel` 的 `TestAnyTLSNativeClientTCPAndUOTUDP` 报 `DATA RACE`；当时 workflow 用默认 shell（无 `pipefail`），`go test … | tee` 的失败被 `tee` 吞掉。**因此在 workflow 加上 `shell: bash`（`-eo pipefail`）之前的 race 绿灯不能当证据。**
- 同日修复（`fix/ci-green`，本机 macOS arm64、Go 1.27.1 验证，CI 以推送后的运行为准）：workflow 默认 `shell: bash`；capability smoke 改 grep `reality-h3-experimental`，并加 grep `external-reality-xhttp-h3-unverified` 守住 H3 未独立验证的边界；H3 探针测试先 `go build`（3 分钟上限）再以 20s 上限运行产物；AnyTLS 客户端往返测试的竞争位于 `sing-anytls` 客户端内部（`session/stream.go` 的 `closeLocally` 与 `Write`，v0.0.11 本机 3/3 复现、升到 v0.0.13 后 10 次仍有 1 次），不升级依赖，改为 `-tags interop` 的非 race 选跑门（`kernel/anytls_client_interop_test.go`），与外部 Xray 客户端同一先例；vitest 全局 `testTimeout: 30_000`；`tests/node-pools.test.tsx` 的删除失败提示用例根因是断言时 antd Modal 仍在 zoom 入场阶段（`opacity: 0`），测试改为先等弹窗可见、再以 `waitFor` 断言提示，组件不动。修复推送后运行 `35833526284`（`e7c9737`）九个 job 八绿：amd64 race / vet 与原生门禁全过，ARM64 原生 13 个包 race 全 ok（`kernel` 13.8s、`cmd/pandora-h3-probe` 1.7s）；唯一红是 React candidate 156 个用例挂 1 个，`tests/refund-execution.test.tsx` 的 `findByRole` 等确认按钮超时，根因是 testing-library 的 `asyncUtilTimeout` 默认 1s，与 vitest 的 `testTimeout` 无关；修复为 `tests/setup.ts` 全局设 10s，两处逐用例 `timeout` 随之删除。
- 2026-09-23：ARM64 原生门（GitHub `ubuntu-24.04-arm` 上 race / vet / self-check / capabilities）在 `pipefail` 生效后的运行 `35833526284` 中通过。

### 未关闭的门禁（UNKNOWN）

- REALITY+XHTTP/H3 的独立公网第三方客户端端点验证；amd64 外部 Xray 测试不能替代。
- 13 个协议的全部传输组合与独立客户端协议矩阵。
- ARM64：CI 原生门已通过（见上文 FACT），安装式或生产 ARM64 运行仍 UNKNOWN，交叉编译或静态 ELF 检查不能替代。
- 当前 `main` 的真实安装式 TLS + PostgreSQL 18 + systemd + NativeCore 端到端。
- 跨进程 SSE：租户 / 节点隔离、Redis 重连、重复信号、watcher 生命周期、慢消费者不阻塞。当时快照的完整 race 曾超过 4 分钟被停止，不是通过。
- 真实支付、退款、通知外发的独立验收。
- WebDAV 在当前 `main` 上的真实远端恢复演练。
- CLIENT-AUTH：正式路由未接通，外部 manifest 仍为 `PLACEHOLDER_NO_GO`；历史 CA42 / CA43 辅助门缺少当前 handoff，不得误报通过。
- 生产冷启动与回滚；G0 release intent 尚未授权。
- 2026-09-21 新增的四条契约测试：表登记簿与权限字典两条 Go 测试已在 CI 首跑中通过；前端 api-surface、typecheck 与构建在本机 2026-09-22 实跑通过，CI 运行 `35833526284` 中 React candidate 156 个用例 155 过，唯一失败是 `findBy` 默认 1s 等待超时（与这四条契约无关），修复见上文 FACT。
- 2026-09-22 本机实跑（macOS arm64，Go 1.27.1、Node 22.23.2；CI 仍是 Go 1.26，`GOMAXPROCS=1 -p 1`）：PASS `internal/platform/webapp`、`internal/platform/db`（含 Up 段重放的表登记簿）、`internal/api/admin|node|public`（PG18 夹具测试因未设环境变量跳过）、`web/app_test.go` 占位与真实产物两种形态、`make frontend-embed`、`tests/api-surface.test.ts` 8 项。同日修掉三处早于本轮的红：删除 admin 页无调用方的旧分流编辑器（194 行，`embed_test.go` 的 `rtState.rowVersion` 断言随之删除）；删除 `tests/manual-order.test.tsx` 中要求旧页写恢复记录的用例（旧页从不写）；`tests/node-pools.test.tsx` 见上文 FACT。NOT RUN：`deploy/build-release.sh` 全流程、00067 的迁移演练（需 Linux + Docker）；00067 未在任何数据库执行。

## 当前进度

截至 2026-09-23：

| 项 | 状态 |
|---|---|
| 当前版本 | r54 管理端已发布（2026-09-09）；后端、迁移、NativeCore 沿用 r53 |
| 仓库基线 | GitHub `main` 三个提交：`f1390b3` 导入 → `3283da8` → `64c0b21`，工作树干净 |
| r55 | 已随 `f1390b3` 入库，未部署 |
| CI | 首跑 `35827175294` 失败；`35833526284`（`e7c9737`）八绿一红，唯一红为 React candidate 1/156 的 `findBy` 超时；改全局 `asyncUtilTimeout` 后 `35835685398`（`05a2aa3`）九个 job 全绿，React candidate 156/156，race 日志无 `DATA RACE`；原因与修复见上文 FACT |
| Xboard 功能验收 | PARTIAL，未 RELEASED |
| 生产运行 | 台湾生产机：aegis-public / admin / node + pandora-native + pandora-rust 均 active |

未收口的历史工作：2026-08-09 起的 H-001（验证并收口当时未提交的 SSE / Redis / Node / Portal / NativeCore 集成）当时状态为 PARTIAL at INTEGRATED，目标是把快照推到 VERIFIED。之后的交接记录没有它完成的证据，相关门禁仍列在上文“未关闭的门禁”里的跨进程 SSE 一项。

r55 随 `f1390b3` 入库的内容：

- 结算与安全：secure webhook、money retries、checkout、commission、coupon、topup、giftcard redeem；
- 节点编排：nodefabric（enrollment、node/server admin、service）、plan_wizard_update；
- 平台层：config 重构、geoip、quicklogin、invite；
- 新增迁移：00064 nodes server delete silence、00065 gift cards tenant FK、00066 invite owner active unique、00067 drop orphan tables（21 张孤儿表，未在任何数据库执行）。

既定路线：按固定 Xboard commit `4f48e61a2cbc6db5338872b6bdb45ef954ec1256` 与差异矩阵逐页验收，优先节点列表 / 创建 / 编辑 / 权限组，逐项证明保存及实际生效。保持 Go / PostgreSQL / NativeCore 不变。

另有一个并行的 Rust 全量重构候选（pandora-rust，axum/sqlx），仅作技术 spike，不是当前生产形态。

## 路线图

按优先级：

1. 支付渠道安全配置、加密凭据、连通性测试、轮换与事件钻取。
2. 订单退款、主动查单、优惠券编辑、共享路由组等待接后端契约（清单见 `panel/frontend/src/core/contracts.ts`），补齐后从清单删除并打开入口。
   同时按页把旧单页独有操作迁进 React（清单见 `panel/frontend/tests/legacy-parity.ts`），归零后 `/` 切到 React、旧页挪到 `/legacy/` 保留一个版本。
3. CLIENT-AUTH 产品化。
4. Android / Desktop 专属客户端及公共 SDK。
5. 全后台四视口浏览器矩阵。
6. WebDAV 真实备份恢复演练。
7. ARM64：CI 原生门已在 `35833526284` 通过；下一步安装式 ARM64、独立客户端协议矩阵、冷启动 / 回滚与最终发布。

工单 eligible assignee 接口与分配 UI 已存在（`GET /v1/tickets/assignees`、`POST /v1/tickets/{id}/assign`），2026-09-21 从路线图移除。

## 相关文档

- [PANDORA_PROJECT_DOSSIER_20260831.md](PANDORA_PROJECT_DOSSIER_20260831.md)：2026-08-31 的完整项目册，功能清单更细。
- [docs/CLAUDE_HANDOFF_2026-08-11.md](docs/CLAUDE_HANDOFF_2026-08-11.md)：最近一次验证交接记录。
- [docs/CONFIG-SIGNING-KEY-ROTATION.md](docs/CONFIG-SIGNING-KEY-ROTATION.md)、[docs/RELEASE-ARTIFACT-BINDING.md](docs/RELEASE-ARTIFACT-BINDING.md)：密钥轮换与发布物绑定。
- [panel/deploy/BACKUP.md](panel/deploy/BACKUP.md)：备份与恢复。
- [panel/docs/](panel/docs/)：XBoard 对标与实施计划、DASH / CLIENT-AUTH 冻结契约、ADR。
- [pdnd/release/README.md](pdnd/release/README.md)：NativeCore Linux 发布与运行时验收。
- [panel/docs/adr/0001-technology-stack.md](panel/docs/adr/0001-technology-stack.md)：技术选型决策记录。
