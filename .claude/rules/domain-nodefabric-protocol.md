---
paths:
  - "panel/internal/domain/nodefabric/protocol_*.go"
  - "panel/internal/domain/nodefabric/node_admin*.go"
  - "pdnd/release/check_native_panel_parity.py"
---

# 节点协议配置：对齐门、敏感键与节点端常量

- CI 的协议对齐门 `pdnd/release/check_native_panel_parity.py` 按文件路径和正则读 Go 源码：`ProtocolSchemas` 的稳定项必须留在 protocol_schema.go，并保持 `{NodeType: "x", Version: 1, Status: "stable"` 这个字面形状；`stableProtocolTypes` 必须留在 node_admin.go，并保持 `var stableProtocolTypes = [...]string{` 的写法。挪文件、改成别的声明方式，对齐门就读不到
- 三份名单（pdnd 的 `kernel/capabilities.go`、面板稳定 schema、下发白名单 `stableProtocolTypes`）必须完全相同，加协议要三处一起改
- 读接口经 `RedactProtocolConfig` 按键名表 `sensitiveProtocolKey` 抹掉敏感值。这张表必须覆盖每个 schema 的 `SensitiveProperties`（守卫 `protocol_schema_test.go:TestSensitivePropertiesAreRedacted`）；给 schema 加敏感字段时，同时把键名加进表
- PATCH 的 `protocol_config` 是整体替换，请求里缺席的敏感键由 `PreserveRedactedProtocolSecrets` 从旧值补回。它只补被抹掉的那几条路径：显式给值（含空串、null）以请求为准；数组长度变了不补；换了协议类型不补
- 挂在开关上的密钥登记在 `secretGates`（如 `mask_password` 跟着 `mask`），开关变了就不补。开关键本身不能进敏感表（守卫 `TestSecretGatesPointAtSensitiveKeys`）
- mKCP 的 MTU 边界（576–1460）与掩码每包开销，和 pdnd 的 `kernel/mkcp_transport.go`、udpmask 实现是同一组值。测试 `TestMKCPMTUBoundsMatchNodeAgent` 只把数值写死，不读 pdnd，两边要手动同步
- `country_code` 只进管理端响应，门户与订阅的任何接口都不得输出（产品「保留规则 3」，与设计稿的国家徽标不同）
