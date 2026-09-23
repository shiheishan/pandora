# Pandora Panel 项目册

项目名称：Pandora Panel（AegisPanel / pandora-native）
项目定位：XBoard 类代理订阅面板 + 自研 Pandora NativeCore 节点端
整理日期：2026-08-31（Asia/Shanghai）
当前仓库：C:\Users\AUAS\Documents\AI公司\pandora-repo
当前分支：main
当前提交：2d22f8a（本地领先 origin/main 1 个提交）

> 状态标记：FACT = 源码或命令直接证明；INFERENCE = 基于证据的判断；UNKNOWN = 尚缺验收证据。静态对齐、进程存在或 HTTP 200 不等于完整功能验收。

## 1. 执行摘要

Pandora 已形成面板、节点编排、NativeCore 数据面和 Linux 安装发布链路的完整骨架，目标是提供 XBoard 级运营能力，并逐步把协议、传输、路由和流量统计收回到单一自研内核。

当前交付判断：PARTIAL at INTEGRATED，不是最终 RELEASED。

最新提交收口了 Trojan 的 XBoard 嵌套字段和 TLS/REALITY 映射：

- tls=1：普通 TLS，后端映射 security=none、tls=true；
- tls=2：REALITY，后端映射 security=reality、tls=false；
- 面板选择 REALITY 会锁定 pandora-native / NativeCore；
- 面板 Schema、字段翻译层和专项测试已更新。

这只代表本次字段兼容修复完成，不代表 13 个协议的所有传输组合、第三方客户端矩阵、ARM64 真机、生产部署和回滚全部完成。

## 2. 系统架构

~~~text
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
~~~

### 2.1 目录职责

| 目录 | 职责 |
|---|---|
| panel/ | 用户门户、管理后台、订阅分发、计费、节点编排、审计和部署脚本 |
| pdnd/ | Pandora node；NativeCore 协议入站、认证、路由、用户和流量 |
| nodeagent/ | 节点侧代理/上报辅助能力，含心跳和用户同步 |
| docs/ | 发布、密钥轮换、交接和发布物绑定文档 |
| panel/docs/ | XBoard 对标、实施路线、DASH、CLIENT-AUTH 和架构决策 |
| panel/deploy/ | 安装、迁移、备份、WebDAV、Nginx、systemd、PG18 和 UI 验收 |
| pdnd/release/ | Linux amd64/arm64 构建、能力矩阵和运行时验收 |

## 3. 产品功能总览

### 3.1 用户门户（已有代码）

- 注册、登录、密码策略和密码显示按钮；
- Access/Refresh Token 默认有效期 30 天；
- 套餐浏览、订阅购买、订单状态和支付结果；
- 订阅节点预览、订阅分发和安全字段过滤；
- 工单创建（已简化为直接提交）、查看、回复和撤回；
- 邀请返利、佣金转换余额（CNY 最小货币单位和幂等键）；
- 通知偏好 GET/PUT、事务通知锁定和失败回滚；
- 个人资料、设备/订阅状态和客户端入口。

### 3.2 管理后台（已有代码，但需完整矩阵验收）

- 仪表盘：收入、用户、订单、订阅、节点、流量和工单统计；
- CNY/USD 收入调整及 7/30/90 天趋势；
- 用户、套餐、订单、优惠券、礼品卡和支付渠道管理；
- 节点、服务器、路由、权限组和节点排序；
- 公告、知识库、主题和插件管理；
- 工单管理、审计日志、风控和降级开关；
- 自定义管理员路径、管理员与普通用户入口分离；
- 响应式布局目标：手机、平板、桌面、4K。

### 3.3 节点编排与控制面

- Server -> 多 Node 模型；
- 节点注册/引导、身份文件和 Ed25519 签名请求；
- /v1/nodes/bootstrap 与 UniProxy 兼容入口；
- 节点心跳、在线状态、节点配置和用户同步；
- AddUsers / UpsertUsers 用户属性更新契约；
- SSE/Redis 跨进程广播、节点变更推送和租户隔离；
- 节点配置签名、密钥轮换和 NativeCore 能力协商。

### 3.4 运维和发布

- PostgreSQL 18 + Valkey/Redis；
- 四个网关 systemd 单元；
- 一键安装、升级、迁移、健康检查和日志轮转；
- Nginx 反向代理渲染，网关默认只监听 127.0.0.1；
- 升级前自动备份；
- WebDAV 自动备份、签名清单、保留策略和恢复脚本；
- Linux amd64/arm64 发布包、SHA-256 清单和能力矩阵。

## 4. NativeCore 协议与传输矩阵

### 4.1 稳定协议 allowlist（13 项）

| 协议 | NativeCore 代码 | 面板字段/编排 | 外部完整矩阵 |
|---|---:|---:|---:|
| VLESS | 有 | 有 | 部分待补 |
| VMess | 有 | 有 | 部分待补 |
| Trojan | 有，最新已修复 XBoard 嵌套形状 | 有 | 部分待补 |
| Shadowsocks | 有 | 有 | 部分待补 |
| Hysteria2 | 有 | 有 | 部分待补 |
| TUIC | 有 | 有 | 部分待补 |
| AnyTLS | 有 | 有 | 部分待补 |
| Naive | 有 | 有 | 部分待补 |
| ShadowTLS | 有 | 有 | 部分待补 |
| SOCKS | 有 | 有 | 部分待补 |
| HTTP | 有 | 有 | 部分待补 |
| Mieru | 有 | 有 | 部分待补 |
| Juicity | 有 | 有 | 部分待补 |

### 4.2 已有重点能力

- VLESS command=UDP：TCP、WebSocket、HTTP Upgrade、gRPC、XHTTP stream/packet；
- Trojan UDP ASSOCIATE：原生地址/长度/CRLF 帧转发并计量；
- SOCKS4、SOCKS4a、SOCKS5 CONNECT、SOCKS5 UDP ASSOCIATE；
- HTTP CONNECT 与正向 GET；
- VMess 原生 gRPC（h2c/TLS+h2）、XHTTP stream/packet-up/reconnect；
- Juicity 原生 QUIC/认证/TCP/UDP 数据面；
- REALITY + XHTTP（TCP/H1/H2/H3）内部回环能力；
- 普通 TLS + XHTTP/H3 已有验证路径。

### 4.3 必须保留的边界

- 13 项静态对齐不等于所有协议/传输/客户端组合通过；
- REALITY + XHTTP + H3 仍需独立第三方客户端验收；
- 未在 NativeCore 能力矩阵验证的组合必须 fail closed，不能静默回落到 xray/sing-box；
- 兼容层只应作为迁移过渡，正式 NativeCore-only 构建不得隐式初始化或回退第三方内核；
- xhttp、reality 是传输/安全能力，不应与 13 项稳定协议 allowlist 混为一谈。

## 5. 安全、隐蔽与反扫描设计

### 5.1 已有安全措施

- 网关默认绑定回环地址，公网只暴露反向代理；
- Nginx/TLS 统一入口；
- 管理后台使用安装时生成的高熵路径；
- RLS 租户隔离与复合外键；
- 节点请求使用节点 ID、时间戳、路径、body SHA-256 和 Ed25519 签名；
- 配置签名、密钥轮换、审计日志和风控开关；
- Token 默认 30 天；密码至少 8 位、字母数字；
- 正式 NativeCore 路径 fail closed；
- 安装器拒绝从任意用户可写目录安装，升级前自动全量备份。

### 5.2 上线前仍需配置

隐藏路径不能阻止公网扫描；最终仍需在云防火墙、安全组和反向代理层限制来源、速率和端口。

1. 只开放 80/443 或明确的代理端口；
2. 节点控制端口不直接暴露公网；
3. 管理路径增加 IP allowlist、WebAuthn/二次验证或 VPN 入口；
4. 注册、登录、订阅、工单接口配置速率限制和挑战策略；
5. 管理、节点、数据库日志分离并做 secret scan；
6. 不把 SSH 密码、API Key、.env、私钥和真实备份地址写入仓库或资料包。

## 6. 部署与升级模型

### 6.1 构建

~~~bash
cd panel
PANDORA_VERSION=v1.0.0 ./deploy/build-release.sh /tmp/dist
~~~

发布包包含四个网关、运维工具、迁移文件、部署脚本、NativeCore 节点端和 SHA256SUMS。

### 6.2 安装流程

~~~text
前置检查 -> 生成配置 -> PostgreSQL/Valkey -> 迁移
-> 收窄数据库角色 -> 安装二进制/systemd -> 启动 -> 健康检查
~~~

安装器保持幂等，不覆盖已有 .env；升级前自动生成 PostgreSQL dump；不会自动伪造管理员账号。

### 6.3 生产入口

- 三个网关默认监听 127.0.0.1；
- 通过 render-nginx.sh 配置反向代理；
- 管理路径由安装过程生成并只显示一次；
- 正式节点默认 NativeCore-only；
- -tags compat 和 native_only: false 只能用于明确的迁移阶段。

## 7. 已有验证证据

### 7.1 最新提交

~~~text
Commit: 2d22f8a
Message: 修复 Trojan XBoard 字段与 TLS REALITY 配置
Files: 5
Change: 114 insertions, 25 deletions
~~~

已确认：

- 仅包含预期的 5 个文件；
- git diff --check 通过；
- 管理端嵌入 JavaScript node --check 通过；
- NativeCore/Panel 协议字段静态对齐检查通过；
- 远端 Linux 低并发 go test 通过；
- 远端 Linux go vet 通过；
- 远端测试使用 GOMAXPROCS=1、-p 1、timeout 和隔离目录，测试后清理。

### 7.2 远端验证结果（2026-08-24）

~~~text
ok github.com/aegispanel/aegis/internal/domain/nodefabric 0.024s
ok github.com/aegispanel/aegis/web 0.175s
remote_exit=0
~~~

### 7.3 已有浏览器证据

- Portal 已有 Chrome/Playwright 390、820、1440、3840 宽度验收；
- 覆盖工单撤回、佣金换算、通知偏好、XSS、内部备注过滤和幂等键；
- 后台完整四视口和所有运营模块仍需补齐最终矩阵。

## 8. 未完成任务与发布门禁

### P0：上线前阻塞项

1. 收口跨进程 SSE/Redis/Node/Portal 集成：租户/节点隔离、Redis 重连、重复信号、watcher 生命周期、慢消费者；
2. 完成 REALITY + XHTTP + H3 独立第三方客户端验证；
3. 完成 13 协议的外部客户端和传输组合矩阵；
4. ARM64 真机运行、启动、升级和故障恢复验收；
5. Linux amd64/arm64 发布包、冷启动、升级、回滚证据；
6. 生产前完整安装链和数据库迁移验收。

### P1：产品完整性项

1. 工单 eligible assignee API，以及分配/取消分配 UI；
2. 支付渠道安全配置、加密凭据、连通性测试、轮换和事件钻取；
3. WebDAV 真实远端恢复演练；
4. 后台用户、节点、套餐、订单、支付、优惠券、礼品卡、工单、公告和审计的 390/820/1440/3840 浏览器矩阵；
5. CLIENT-AUTH 正式产品化：正式路由、refresh rotation、网关 E2E、AndroidKeyStore 和故障注入；
6. CA42 authority verified -> authorityprod integration -> runner switch -> Admission enablement 的连续安全门禁。

### P2：客户端与生态

1. Android 专属客户端；
2. Desktop 专属客户端；
3. 公共 SDK、配置导入导出和版本兼容策略；
4. 客户端冷启动、网络切换、节点失败转移和回滚验收。

## 9. CA42/CLIENT-AUTH 状态

CA42 V3 composer/authority 已有隔离、签名、root、ledger、fs-verity 和 E2E 组件，但不能自动等同于生产 Admission 已启用。

尚缺：

- ca42authorityverified 不可伪造 capability 的纯加法提取；
- authorityprod 与生产 authority 的正式接线；
- runner 切换与去重；
- nonce/Journal mutation adapter；
- 可查询 consumer capability；
- Admission coordinator 启用前的崩溃恢复和未知结果查询；
- 真实生产 root、keyring、fs-verity 和目标 Linux 验收；
- 发布、浏览器、回滚和独立 S1/Q3 审查。

在这些门禁完成前，CLIENT-AUTH/CA42 只能标为 REFERENCE 或 PARTIAL，不能写成已生产可用。

## 10. 推荐实施顺序

~~~text
A  H-001 跨进程集成收口
B  NativeCore 外部协议/传输矩阵
C  ARM64 真机与发布包
D  支付安全、工单分配、WebDAV 恢复
E  CLIENT-AUTH/CA42 生产接线
F  后台全视口浏览器验收
G  安装、升级、冷启动、回滚和发布签字
H  Android/Desktop 客户端与 SDK
~~~

每个阶段必须保留：修改文件清单、测试命令和原始输出、运行环境/架构/并发参数、失败/回滚证据、PASS/PARTIAL/UNVERIFIED 标记，以及不触碰生产和无关 dirty worktree 的记录。

## 11. 交接约束

- 继续开发前先读取本项目册、AGENTS.md、当前 handoff、git status 和实际 diff；
- 不从零重做已完成的面板、协议或 CA42 工作；
- 不使用聊天记录里的 SSH 密码、API Key 或任何凭据；
- 不在 Windows 反复运行 Go；Go 测试放到具备工具链的 Linux 隔离目录；
- 低负载验证使用 GOMAXPROCS=1、-p 1 和有界 timeout；
- 不自动部署、提交、推送、迁移或重启；
- 不删除、不覆盖其他未提交工作；
- 远端验证后清理精确可识别的临时目录和测试二进制；
- 最终发布前必须经过独立审查。

## 12. 资料包范围

source-docs/ 目录收录关键规划、交接和运维文档副本：

- README.md、AGENTS.md、CLAUDE.md；
- panel/CLAUDE-HANDOFF-20260804.md；
- panel/docs/潘多拉面板-XBoard功能对标与实施计划.md；
- panel/docs/潘多拉面板-并行实施路线图-20260730.md；
- panel/docs/潘多拉面板-DASH-01冻结契约-20260730.md；
- panel/docs/潘多拉面板-CLIENT-AUTH-01冻结契约-20260730.md；
- panel/docs/潘多拉面板-CLIENT-AUTH-01-R1刷新重放附录-20260730.md；
- panel/docs/潘多拉面板-CLIENT-AUTH-00042实现清单-20260730.md；
- docs/CLAUDE_HANDOFF_2026-08-11.md；
- docs/RELEASE-ARTIFACT-BINDING.md；
- docs/CONFIG-SIGNING-KEY-ROTATION.md；
- panel/deploy/BACKUP.md；
- panel/deploy/LINUX-COMPATIBILITY.md；
- panel/deploy/backup-webdav.example.json；
- pdnd/release/README.md。

明确排除：.env、私钥、真实 API Key、SSH 凭据、数据库转储、备份内容、会话历史、.git 和源码大文件。

## 13. 最终结论

Pandora 已从“面板原型”进入“可持续集成和分阶段发布”阶段。面板业务、节点编排、NativeCore 协议骨架、部署脚本和安全子系统均已有实质代码；但全协议独立客户端验收、ARM64 真机、跨进程集成收口、WebDAV 恢复、CLIENT-AUTH 生产接线、后台完整浏览器矩阵和最终回滚证据仍是上线前主要工作。

当前最准确标记：

~~~text
代码状态：INTEGRATED
当前快照：PARTIAL
协议静态对齐：PASS
关键专项测试：PASS（受影响包、低并发、远端 Linux）
外部全协议矩阵：UNVERIFIED
生产部署：NOT RUN
最终发布：NO-GO（等待 P0 门禁）
~~~
