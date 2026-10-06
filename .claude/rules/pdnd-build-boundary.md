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

## 默认构建只有 NativeCore

- 默认构建（`!compat`）由 `runtime_native.go` 提供 `newRuntime`，只链接 `kernel/` 的 NativeCore；`native_only:false` 在默认构建里直接报错，不回落。
- `core/multi`、`core/sing`、`core/xray` 与 sing-box、xray-core 只允许经 `runtime_compat.go`（`-tags compat`）链进来。生产代码里不要 import 它们；测试文件引用不影响发布物。
- 守卫：`release/build.sh` 对 `go list -deps .` 检查这几个包不出现，CI `pandora-native.yml` 有同样的检查；运行时选择由 `TestDefaultRuntimeIsNativeCoreOnly` / `TestCompatibilityRuntimeIsOptIn` 钉住。
- `core/` 里唯一允许进默认构建的是 `core/mieru`：`kernel/mieru.go` 把它包成 NativeCore 适配器，出站经注入的 Transport 走 NativeCore 的 DataPlane，失败经 `SetConnErrorHandler` 交给 OnConnError。
- `core/external/juicity.go` 托管的 juicity 是 AGPL：只能进程外运行，不能链接进二进制，也因此不能按用户计流量。其缺省工作目录必须落在 `release/pandora-native.service` 的 `ReadWritePaths` 之内（`TestJuicityDefaultWorkDirIsWritableByService`）。
- `core/multi` 是过渡期分派器，生产 NativeCore-only 路径不经过它。

## 能力矩阵是唯一事实源

- `kernel/capabilities.go` 的矩阵同时供 `--capabilities` 打印、`selfcheck.go` 对照默认适配器注册表自检、面板做编排前协商。不在矩阵里的组合 fail closed，绝不静默回落到 `core/` 的兼容内核。
- 增删协议要同时改三处，否则 `release/check_native_panel_parity.py` 报 DRIFT：`kernel/capabilities.go`、面板 `panel/internal/domain/nodefabric/protocol_schema.go` 的 stable schema、`node_admin.go` 的 `stableProtocolTypes`。
- CI capability smoke 按字面量检查 `reality-h3-experimental` 与 `external-reality-xhttp-h3-unverified` 两个串，改名要同步 `pandora-native.yml`。
- `cmd/pandora-h3-probe` 只证明「我方客户端 ↔ 我方服务端」，不构成 REALITY+XHTTP+H3 的第三方独立验证；在独立客户端跑通之前不得因它解除 `external-reality-xhttp-h3-unverified` 边界（见 `release/README.md`）。
- 探针只依赖 `internal/reality` 与 `internal/realityquic/http3` 的线协议，刻意不经 NativeCore 运行时，作为日后桌面/移动客户端的验收缝；不要把它改成调 `kernel`。

## 发布物

- `release/runtime-acceptance.sh` 的模拟面板是 `release/acceptancepanel`（Go 写，因为签名通道要 Ed25519），规则见 pdnd-node-panel 规则文件的签名契约一节。
- `main_test.go` 钉住缺省配置路径与 `release/pandora-native.service` 一致、身份路径缺省值；改 systemd 单元或安装路径要一起改。
