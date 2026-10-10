---
paths:
  - "pdnd/internal/reality/**"
  - "pdnd/internal/realityquic/**"
  - "pdnd/internal/nativewire/shadowtls/**"
  - "pdnd/internal/nativewire/anytls/**"
  - "pdnd/internal/nativewire/hysteria2/**"
  - "pdnd/internal/nativewire/tuic/**"
---

# pdnd 里 fork 来的第三方代码

## 上游基点

基点 = 2026-09-22 初次导入（f1390b3）时的 fork 文件与上游逐个候选 diff（只比 fork 里有的文件，先把上游 import 路径换成本仓库路径），差异最小的那个；2026-10-08 核实。go.mod 里同名模块是对照测试或 compat 构建用的，版本**不等于**基点（anytls 即是反例）。合上游时从这里的基点取「基点..目标」的 diff，合完更新本表并在提交说明写上目标 commit。

| fork 目录（pdnd/internal/） | 上游 | 基点 | 导入时相对基点的差异 |
|---|---|---|---|
| `reality` | github.com/XTLS/REALITY | commit 9234c772ba8f（2026-03-22，即 go.mod 的 `v0.0.0-20260322125925-9234c772ba8f`）；下一个提交 393f8de 把 `tls.go` 的 8192 缓冲改成 17KiB，fork 仍是 8192 | 164 行：删 `config.Show` 调试打印、`config.Clone()`、伪造 NewSessionTicket 的 `pskModeDHE` 判断、原生 REALITY 客户端字段、`QUICServer` 构造；独有 `handoff.go`、`quic_reality.go`、`reality_client.go` 等 |
| `realityquic` | github.com/apernet/quic-go 的 `v0.59.0-mod-rename` 分支（无 tag；master 停在 v0.51） | commit db4786c77a22（2026-02-17，即 go.mod 的 `v0.59.1-0.20260217092621-db4786c77a22`）；下一个提交 20e2c69 在 `connection.go` 多一行 `SetMaxDatagramSize`，fork 没有 | 114 行：约 20 个文件把 `crypto/tls` 换成 `internal/reality`、`connection.go` 的 `mtuDiscoverer != nil` 判断、`http3/transport.go` 改用 `standardTLSState`；独有 `http3/tls_state.go`、README |
| `nativewire/shadowtls` | github.com/SagerNet/sing-shadowtls | commit fcd445d33c11（2025-05-03，`v0.2.1-0.20250503051639-fcd445d33c11`，sing-box v1.13.14 同版本） | 0 行（逐字节一致） |
| `nativewire/anytls` | github.com/anytls/sing-anytls | **v0.0.11**（130d2e61b889）。go.mod 的对照客户端是 v0.0.13；v0.0.12 起上游把 `pipe/`、`skiplist/` 挪进 `internal/`，fork 仍是旧布局 | 9 行：`service.go` 口令查找加 `userAccess` 读写锁 |
| `nativewire/hysteria2` | github.com/SagerNet/sing-quic 的 `hysteria2/` | v0.6.1（907fec5e8e6b，2026-03-30；与 go.mod 一致） | 11 行：`service.go` 用户表读写锁、import 排序 |
| `nativewire/tuic` | github.com/SagerNet/sing-quic 的 `tuic/` | v0.6.1（同上；单看 tuic 与相邻的 ec3b222、2afc335 并列，按 hysteria2 定） | 8 行：`service.go` 用户表读写锁 |

导入之后本仓库在基点之上的改动（合上游时要保住）：

- reality：d3119d3（拒绝的握手转发到 dest）、eb24309（Vision 切原始 socket）、c6291f8、09317d5；新增 `coalesce_test.go`。
- realityquic：只有 137dcf1、bad0abf 改注释与链接。
- shadowtls：f8173f0（认证前限时）；新增 `service_test.go`。
- anytls：8da0e00（超 64KB 拆帧、控制帧 deadline）；其中 `stream.go` 的 FIN 与 dieHook 顺序、`util/version.go` 版本串是从 v0.0.13 手工同步的，不是整体升到 v0.0.13。
- hysteria2：0b8a840、d7991ac、c8b3871、b525f86、17f7af1、1acb247、5daa37c（UDP 热路径、增量用户表、`dgram` 分片、空闲回收）；新增 `idle.go`、`salamander_batch_*.go`。8350c9e（并发 /auth：`authenticated` 改 `atomic.Bool`，认证在 `connAccess` 内判定、只成功一次，之后的 /auth 照旧回 233 但不改 `authUser`，`loopMessages` 只起一个；回归用例 `kernel/quic_dualauth_test.go` 的 `TestHysteria2ConcurrentDualAuthKeepsFirstUser`，靠 -race 抓）。3e0b2ef（`MaxIncomingStreams` 由 1<<60 改为导出常量 `ServerMaxIncomingStreams` = 1024，挡认证前 http3 每流一个 goroutine 的放大；守卫 `kernel/quic_preauth_hy2_test.go`）。cb75d0f（认证前约束：`handleConnection` 不再调 `http3.Server.ServeQUICConn`，改用公开的 `NewRawServerConn` 自己收流——控制流与 SETTINGS 与原来一样，单向流照旧交 `HandleUnidirectionalStream`，双向流在 `serveStream` 里先 Peek 帧类型再走 `dispatchStream` / `HandleRequestStream`；认证前每条请求流读请求头限时 `ServiceOptions.PreAuthHeaderTimeout`（缺省 10 秒，超时只拒那条流，`ServeHTTP` 与 `dispatchStream` 进入后撤掉）、未认证连接按空闲关 `ServiceOptions.PreAuthIdleTimeout`（缺省 75 秒，取 nginx keepalive_timeout；没有在途请求才计时，有请求在途的不关；r5 起取代 r4 的「握手后 10 秒未认证即关」）、认证前在途请求上限 `preAuthMaxInflight`（32，多出的流 H3_REQUEST_REJECTED）、认证前 HEADERS 声明长度上限 `preAuthMaxHeaderBytes`（32KB，H3_EXCESSIVE_LOAD）；守卫 `preauth_test.go`、`preauth_idle_test.go`）。合上游时若上游改了 http3 的 `handleConn`，对照 `serveStream` 同步。
- tuic：74d7256、c8b3871、b525f86、1acb247、5daa37c；新增 `idle.go`。1281aac（认证前不放大：`MaxIncomingStreams` / `MaxIncomingUniStreams` 由 1<<60 改为 1024；认证完成前单向流在 `loopUniStreams` 里逐条只读 2 字节头、认证流当场校验、Packet / Dissociate 流暂存到认证后再起 goroutine，双向流认证后才 Accept；单向流头按字节读、不再借 32KB 缓冲）；新增 `preauth_test.go`。同一提交顺带消掉上游的并发认证 panic（两条认证流各起 goroutine、都过「未认证」检查，第二次 `close(authDone)` 即 panic；回归用例 `kernel/quic_dualauth_test.go` 的 `TestTUICConcurrentDualAuthDoesNotPanic`）。91b6eec 加认证前暂存上限 `preAuthMaxParkedUniStreams`（64，超出即关连接；读完 FIN 的暂存流不再占流额度，不设上限会涨到认证超时）。 9b28a93（认证失败「unknown user」不再带 UUID：用户 UUID 就是口令；守卫 `authlog_test.go`）。合上游时 `service.go` 的 `loopUniStreams`、`handleUniStream`、`loopStreams` 冲突要保住这几点。

## 共同约束

- 保持上游的文件划分，不按本仓库习惯拆分、合并或重命名文件：为了能对照上游、合上游。`internal/reality` 与 `internal/realityquic` 因此整目录豁免 800 行规则（pdnd 根 `linelimit_test.go` 的 `lineLimitExemptDirs`）；目录改名或删除要同步那张表。
- 许可证文件原样保留：`internal/reality` 的 `LICENSE`（MPL-2.0，fork 自 XTLS/REALITY）与 `LICENSE-Go`（它本身基于 Go crypto/tls）两份并存；`internal/realityquic` 保留 apernet/quic-go 的 MIT `LICENSE`；`internal/nativewire/shadowtls` 保留 sing-shadowtls 的 GPL-3.0 `LICENSE`，连同上游的 README、Makefile 不动；`anytls`、`hysteria2`、`tuic` 各自目录下的 `LICENSE` 同样保留。
- govulncheck 看不到 fork：上游（或 crypto/tls、quic-go）出公告时，要人工到对应目录查同样的代码（对照表见 deps-upgrade skill 第 1 节）。

## realityquic（fork 自 apernet/quic-go）

- 存在的理由是把 TLS 事件接到 `internal/reality` 而不是标准库 crypto/tls。
- 从上游同步时只改 Go import 路径与 `go:generate` 的包路径；指向上游 issue / PR / wiki 的网页链接保持指向 quic-go 原项目（细节见目录内 README）。
- 目录内不带测试；升级后要重跑 `kernel` 的 `TestNativeRealityH3RoundTrip`。

## nativewire/shadowtls（fork 自 sagernet/sing-shadowtls）

- 唯一生产调用方是 `kernel/shadowtls.go`，只走 v3；v1/v2 与客户端只留给测试与对拍，不对外暴露。
- 本仓库加的改动是认证判定前限时（`ServiceConfig.HandshakeTimeout`）。限时原则：认证判定之前等对端的步骤按 HandshakeTimeout 超时；回落到诱饵、以及对外看起来就是诱饵会话的中继（v2 握手中继、v3 等首个 HMAC 帧）一律不限时——真站点不会 10 秒掐断空闲 TLS 会话，掐了就是指纹。守卫在 `service_test.go`（`TestServiceDecoyRelayOutlivesHandshakeTimeout` 等）。
- `DefaultHandshakeTimeout` 只是兜底，口径以 kernel 显式传入的 `inboundHandshakeTimeout` 为准。

## anytls、hysteria2、tuic（nativewire 下的另三个 fork）

- 边界与 `dgram/` 等自研包的区别见 `internal/nativewire/README.md`；生产调用方是 `kernel/` 里对应的适配器。
- 共同的本仓库改动是用户表的同步发布（热更新用户与认证读不竞争）；hysteria2 / tuic 的 UDP 路径改动多，合上游时冲突主要在 `packet.go`、`service_packet.go`。
- 改完要跑 `internal/nativewire/...`、`-tags interop` 的 AnyTLS 组（`TestAnyTLSNativeClientTCPAndUOTUDP`），hysteria2 / tuic 还要按 node-accept 在 Linux 上复测 UDP。
