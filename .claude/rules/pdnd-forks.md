---
paths:
  - "pdnd/internal/reality/**"
  - "pdnd/internal/realityquic/**"
  - "pdnd/internal/nativewire/shadowtls/**"
---

# pdnd 里 fork 来的第三方代码

## 共同约束

- 保持上游的文件划分，不按本仓库习惯拆分、合并或重命名文件：为了能对照上游、合上游。`internal/reality` 与 `internal/realityquic` 因此整目录豁免 800 行规则（pdnd 根 `linelimit_test.go` 的 `lineLimitExemptDirs`）；目录改名或删除要同步那张表。
- 许可证文件原样保留：`internal/reality` 的 `LICENSE`（MPL-2.0，fork 自 XTLS/REALITY）与 `LICENSE-Go`（它本身基于 Go crypto/tls）两份并存；`internal/realityquic` 保留 apernet/quic-go 的 MIT `LICENSE`；`internal/nativewire/shadowtls` 保留 sing-shadowtls 的 GPL-3.0 `LICENSE`，连同上游的 README、Makefile 不动。

## realityquic（fork 自 apernet/quic-go）

- 存在的理由是把 TLS 事件接到 `internal/reality` 而不是标准库 crypto/tls。
- 从上游同步时只改 Go import 路径与 `go:generate` 的包路径；指向上游 issue / PR / wiki 的网页链接保持指向 quic-go 原项目（细节见目录内 README）。
- 目录内不带测试；升级后要重跑 `kernel` 的 `TestNativeRealityH3RoundTrip`。

## nativewire/shadowtls（fork 自 sagernet/sing-shadowtls）

- 唯一生产调用方是 `kernel/shadowtls.go`，只走 v3；v1/v2 与客户端只留给测试与对拍，不对外暴露。
- 本仓库加的改动是认证判定前限时（`ServiceConfig.HandshakeTimeout`）。限时原则：认证判定之前等对端的步骤按 HandshakeTimeout 超时；回落到诱饵、以及对外看起来就是诱饵会话的中继（v2 握手中继、v3 等首个 HMAC 帧）一律不限时——真站点不会 10 秒掐断空闲 TLS 会话，掐了就是指纹。守卫在 `service_test.go`（`TestServiceDecoyRelayOutlivesHandshakeTimeout` 等）。
- `DefaultHandshakeTimeout` 只是兜底，口径以 kernel 显式传入的 `inboundHandshakeTimeout` 为准。
