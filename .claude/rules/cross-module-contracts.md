---
paths:
  - "proto/**"
  - "nodeapi/**"
  - "pdnd/certbundle/**"
  - "pdnd/certstore/**"
  - "pdnd/kernel/port_claims*.go"
  - "pdnd/core/device_key*.go"
  - "panel/internal/module/node/protocol/port_l4*.go"
  - "docs/node-certificates.md"
---

# 面板与 pdnd 两端共用的契约

panel 与 pdnd 是两个独立的 Go module。两端之间的契约分两类：经 proto 生成的线格式，和两端各写一份、靠用例对齐的口径。

## 线格式：proto 与 nodeapi

- 节点通道只有一份定义：`proto/pandora/node/v1/*.proto`。生成代码放仓库根的独立 module `nodeapi/`，panel 与 pdnd 的 go.mod 都 `replace` 到 `../nodeapi`，两端用同一份生成代码，不各抄一份结构体。
- 改了 proto 就重新生成并和 proto 一起提交：`cd proto && go run github.com/bufbuild/buf/cmd/buf@v1.71.0 generate`（插件版本钉在 `proto/buf.gen.yaml`）。CI `nodeapi.yml` 查 lint、格式、生成结果无差异、对上一次提交不做破坏性改动（`buf.yaml` 的 breaking 规则 `WIRE_JSON`）。
- 破坏性改动（删字段、改编号、改类型）会被 CI 拦下；确实要改，新开字段编号，旧字段保留到两端都不再读。
- 语义改动两端一起改：pdnd 侧的约定见 `pdnd-node-panel.md`，面板侧在 `panel/internal/module/node/grpc/` 与 `panel/internal/module/node/service/session*.go`。

## 两端各写一份的口径（改一边必须改另一边）

- 端口占用的 L4：面板 `panel/internal/module/node/protocol/port_l4.go` 的 `listenL4` 与 pdnd `kernel/port_claims.go` 的 `inboundPortKey` 同一套规则。面板的 `TestListenL4MatchesPdndPortClaims` 直接读 pdnd 的 `TestInboundPortKeyDerivation` 用例表，pdnd 加用例面板自动跟上。
- 设备键：面板 `panel/internal/module/node/service/online.go` 的 `deviceKey` 与 pdnd `core.DeviceKey`（IPv4 按地址、IPv6 按 /64）。两边各有一张 `TestDeviceKey` 用例表，不互相读，改口径要两张表一起改。
- 稳定协议名单：pdnd `kernel/capabilities.go` 与面板 `panel/internal/module/node/protocol/schema.go` 的 `stableTypes` / `stableSchemas`，由 `pdnd/release/check_native_panel_parity.py` 比对（CI pandora-native）。
- 节点安装器写的 systemd 单元：面板 `TestInstallScriptUnitMatchesPackaged`（`panel/internal/module/node/service/pdnd_install_script_test.go`）读 `pdnd/release/pandora-native.service` 逐行比对。

## 证书包（certbundle）

- 面板侧实现已随旧栈删除。`pdnd/certbundle` 现在只剩节点侧实现与金样本 `pdnd/certbundle/testdata/certbundle-v1-golden.json`，没有面板侧对照，也没有调用方；托管入站证书下发到节点还没做（`docs/node-certificates.md` 第 3 节）。
- 要恢复面板托管证书下发时，先定新栈的下发方式（走 gRPC 通道还是保留这份 HPKE 契约），再决定这个包留不留；不要只改 pdnd 一侧的格式。
- proto 里的 `Certificate` 消息是节点 mTLS 客户端证书的续期结果，不是入站证书，别混用。
