---
paths:
  - "pdnd/*.go"
  - "pdnd/go.mod"
  - "pdnd/core/**"
  - "pdnd/kernel/capabilities.go"
  - "pdnd/kernel/selfcheck.go"
  - "pdnd/kernel/runtime.go"
  - "pdnd/cmd/**"
  - "pdnd/release/**"
---

# pdnd 构建边界与能力矩阵

## 只有 NativeCore，没有兼容层

- 产品 2026-10-10 定：不支持第三方节点端。节点端只有 `kernel/` 的 NativeCore 一个数据面，`main.go` 直接起它；没有构建标签切换，没有 `native_only` 之类的回落开关（留在配置里按未知字段报错）。兼容层（`core/multi`、`core/sing`、`core/xray`、`core/external`、`runtime_compat.go`）已于 10-14 删除，不再恢复。
- pdnd 的任何二进制（含测试）都不许链 `github.com/sagernet/sing-box`：它在 Go 1.27 下用 `go:linkname` 取 x/net 的私有符号，链接失败。守卫：CI `pandora-native.yml` 的「Default dependency boundary」对 `go list -deps -test ./...` 查，`release/build.sh` 对发布物 `go list -deps .` 查。要 sing-box 当参照只能放在独立 go.mod 的 `.agents/skills/subscription-e2e/tools`。
- xray-core、sing-quic、sing-anytls、daeuniverse/outbound 等只在测试里当互通参照客户端，生产代码不 import。
- `core/` 只剩编排层抽象（`core.go`、转发、计数、限速）与 `core/mieru`：`kernel/mieru.go` 把它包成 NativeCore 适配器，出站经注入的 Transport 走 NativeCore 的 DataPlane（没注入时拒绝转发、报 `mieru.ErrNoTransport`，不直接拨号），失败经 `SetConnErrorHandler` 交给 OnConnError。

## 能力矩阵是唯一事实源

- `kernel/capabilities.go` 的矩阵同时供 `--capabilities` 打印、`selfcheck.go` 对照默认适配器注册表自检、面板做编排前协商。不在矩阵里的组合 fail closed，直接报错。
- 增删协议要同时改三处，否则 `release/check_native_panel_parity.py` 报 DRIFT：`kernel/capabilities.go`、面板 `panel/internal/module/node/protocol/schema.go` 的 stable schema（`stableSchemas`）与下发名单 `stableTypes`（同一个文件）。
- CI capability smoke 按字面量检查 `reality-h3-experimental` 与 `external-reality-xhttp-h3-unverified` 两个串，改名要同步 `pandora-native.yml`。
- `cmd/pandora-h3-probe` 只证明「我方客户端 ↔ 我方服务端」，不构成 REALITY+XHTTP+H3 的第三方独立验证；在独立客户端跑通之前不得因它解除 `external-reality-xhttp-h3-unverified` 边界（见 `release/README.md`）。
- 探针只依赖 `internal/reality` 与 `internal/realityquic/http3` 的线协议，刻意不经 NativeCore 运行时，作为日后桌面/移动客户端的验收缝；不要把它改成调 `kernel`。

## 发布物

- `release/runtime-acceptance.sh` 的模拟面板是 `release/acceptancepanel`（Go 写的假 gRPC 面板：自签 CA、Enroll 与 Session），按 pdnd-node-panel 规则文件的通道契约独立实现，不 import 节点代码。
- `main_test.go` 钉住缺省配置路径与 `release/pandora-native.service` 一致、身份路径缺省值，以及配置严格解析（已删除的字段报错）；改 systemd 单元或安装路径要一起改。
- `build.sh` 写的 `manifest.json` 由 `verify.sh` 校验（`runtime` 必须是 `pandora-native`、Go 工具链与 go.mod 一致、各产物 SHA-256）。

## lint 与死代码

- golangci-lint 配置在 `pdnd/.golangci.yml`（手写，与面板同版本 v2.5.0）：bodyclose、errcheck、govet、ineffassign、staticcheck、unused；不开 gosec（协议实现里整型转换、TLS 选项是常态，噪音盖过信号），依赖边界仍由上面的 `go list -deps` 检查管。fork 来的目录整目录排除（pdnd-forks 规则文件）。CI 是 pandora-native.yml 的 pdnd-lint，只跑 linux 缺省 tag；改了带 build tag（interop、interop_external、interop_mihomo、race）的测试，本机加 `--build-tags=<tag>` 自查。
- 死代码：pdnd-lint 在仓库根跑 `bash .github/scripts/deadcode.sh pdnd . ./cmd/... ./release/...`，从节点端、h3 探测、发布工具与模拟器的 main 出发，只剩测试在调的函数也算死代码。给测试用的构造函数、访问器写在同包 `_test.go`；别的包的测试要用，走生产 API（如 `NewNativeCoreWithLogger`、`upgrade.LoadKeyring`）。误报写进 `pdnd/.deadcode-ignore` 并注明理由。
