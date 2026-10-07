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
- 「存得进去就连得上、不安全的存不进去」的收紧规则集中在 `protocol_validate_policy.go`（2026-10 w4proto）：vless/vmess 裸 tcp 明文拒绝、REALITY 只许 tcp/grpc/xhttp、flow 与 utls 枚举、short_id 必填、dest 与 fallback 拒本机/内网（只做字面与后缀判断，不解析 DNS）、证书路径必须在 `/etc/pandora-native/certs/` 下、kernel 只收 auto / pandora-native。规则只拦新写入：存量不改写、照常下发（`EffectiveKernel`、`ProtocolConfigWarnings`），渲染矩阵的 `legacyFixtures` 守着「旧形状照样渲染」
- `fallback` 的形状与 pdnd `kernel/probe_fallback.go:parseProbeFallback` 一致（host:port，不收 URL / 路径），面板另拒回环、私网、链路本地；Trojan 只在 tcp 上生效。回落只下发给节点，不进订阅
- 校验器的错误键带 `protocol_config.` 前缀，`renameErrorFieldsToXboard` 去掉前缀查逆表再加回；改动这里要跑 `protocol_policy_test.go`，它按表单路径断言错误键
- 表单说明放 schema 的 `Hints`（`protocol_schema_hints.go`）；守卫 `TestSchemaEnumsMatchPolicyLists` 要求每条说明都指向 schema 里有的字段
