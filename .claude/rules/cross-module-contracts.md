---
paths:
  - "proto/**"
  - "nodeapi/**"
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
- 节点安装器写的 systemd 单元：面板 `TestInstallScriptUnitMatchesPackaged`（`panel/internal/module/node/service/pdnd_install_script_test.go`）读 `pdnd/release/pandora-native.service` 逐行比对；在线升级的两个 root 单元 `pandora-native-upgrade.path` / `.service` 由同文件的 `TestInstallScriptUpgradeUnitsMatchPackaged` 比对，`TestPackagedUpgradeUnitsContract` 钉住监视的路径、`ExecStart`、oneshot 与超时。
- 在线升级（`docs/architecture.md` 11.6）：能力名 `upgrade_v1`（pdnd 只有 `panel.FeatureUpgrade` 一份，面板侧一份常量）；发布清单 `pdnd-manifest.json` 与 `.sig` 的格式由 pdnd `upgrade/manifest.go`、`release/signmanifest` 与 `panel/deploy/build-release.sh` 写读，面板只读不验签；原因码表（`upgrade/reason.go`）与面板的状态对账、前端中文文案一一对应，加码要两端一起加；结果文件的 phase 字符串是 `FAILED` / `ROLLED_BACK`，只在节点本机用，上线时翻成 proto 的 `UpgradePhase`。

## 节点入站证书

- 两种来源，协议配置的 `cert_mode`：`acme`（节点端向 Let's Encrypt 自动申请，新建默认）/ `file`（节点机上的 certbot / acme.sh 放进约定目录，缺省）。用户文档 `docs/node-certificates.md`。
- 面板不签发、不下发证书，私钥不离开节点机。面板只做三件事：
  - 校验（`node/protocol/cert.go`、`validate_kernel.go`）；
  - 在配置文档里写 `cert_identifier`（`node/service/config_docs.go`，SNI 否则节点地址）；
  - 收心跳里的 `NodeRuntime.cert` 落 `nodes.cert_*`（迁移 00061，node 模块属主），给后台列表、详情与仪表盘「证书异常」（report 经 node 的 `CertAttentionNodes` 读）。
- 标识符规整两端各一份、必须一致：面板 `protocol.NormalizeCertIdentifier` 与 pdnd `core.NormalizeCertIdentifier`。
- 中转：父节点 acme 时，渲染补 `server_name = 父节点标识符`（IP 也补），SNI 覆盖必须等于它（`relays_sni.go`、`node_source_relay.go`）。
- file 模式的约定目录两端各一份：面板 `node/protocol/validate_policy.go` 的 `nodeCertificateDir`，与文档第 2 节。
- proto 里的 `Certificate` 消息是节点 mTLS 客户端证书的续期结果，不是入站证书，别混用；入站证书状态是 `CertStatus`。
