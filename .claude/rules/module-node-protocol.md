---
paths:
  - "panel/internal/module/node/protocol/**"
  - "pdnd/release/check_native_panel_parity.py"
---

# 节点协议配置

## 对齐门（CI）
- `pdnd/release/check_native_panel_parity.py` 按路径与正则读 Go：`stableSchemas` 稳定项须保持 `{NodeType: "…", Version: 1, Status: "stable"` 字面形状；`stableTypes` 须保持 `var stableTypes = [...]string{` 写法（`protocol/schema.go`）。挪文件或改声明方式对齐门会 `PARITY_INPUT_ERROR`。
- 三份名单必须相同：`pdnd/kernel/capabilities.go`、`protocol/schema.go` 的 `stableSchemas` 与 `stableTypes`；加协议三处一起改。

## 敏感键与 PATCH
- 读接口用 `Redact`（`protocol/protocol.go`），键表 `sensitiveProtocolKey`（`schema.go`）；须覆盖各 schema 的 `SensitiveProperties`（`schema_test.go` 的 `TestSensitivePropertiesAreRedacted`）。
- PATCH 的 `protocol_config` 整体替换；缺席的敏感键由 `PreserveSecrets` 从旧值补回（`secrets.go`），规则与 `Redact` 对称；显式给的值（含空）以请求为准；数组长度变了不补；`secretGates`（如 `mask_password`↔`mask`）开关变了不补。
- 开关键本身不要进 `sensitiveProtocolKey`（`secrets_test.go` 的 gated 用例）。

## 校验与内核
- 收紧规则在 `validate_policy.go`、`validate_kernel.go` 等。节点没有 `kernel` 字段（2026-10-14 迁移 00034 删列）：节点端只有 NativeCore，`Validate` 不再收内核参数。
- `fallback` 形状与 pdnd `kernel/probe_fallback.go:parseProbeFallback` 一致（`validate_policy.go` 注释）；面板另拒回环/私网；Trojan 回落只在 tcp（`validateTrojanFallbackNetwork`）。回落只进节点配置文档，不进订阅（`policy_test.go` 下发形状断言）。
- mKCP MTU 576–1460 与 `validate_mkcp.go` 的 `mkcpMinMTU`/`mkcpMaxMTU` 须与 pdnd `kernel/mkcp_transport.go` 一致；`TestMKCPMTUBoundsMatchNodeAgent` 数值写死，改一边要改另一边。

## Shadowsocks 加密方式
- `shadowsocksMethods` = 经典 AEAD 三种（`shadowsocksAEADMethods`）+ `ss2022Methods`（`2022-blake3-aes-128-gcm` 16 字节、`2022-blake3-aes-256-gcm` 32 字节，`protocol/ss2022.go` 的 `SS2022KeyLen`）。`2022-blake3-chacha20-poly1305` 不开放：多用户靠 SIP023 身份头，身份头用 AES 块加密，chacha20 版做不了，客户端给多段密钥直接报错。
- ShadowTLS 的内层 `method` 只收经典三种（schema 枚举与 `validate_kernel.go` 都用 `shadowsocksAEADMethods`）。
- schema 仍不收 `password`：2022 的服务端身份密钥由 node service 派生后只进配置文档与订阅线路（`node/service/ss2022.go`）。表单说明 `ss2022CipherHint`（`schema_hints.go`）。

## 表单元数据（10-16）
- `Schema.Note` 与 `Schema.Fields`（`schema_form.go` 的 `protocolForms`，`Schemas()` 经 `withForms` 挂上；不写进 `schema.go` 的字面量，免得碰对齐门）。`Fields` 按显示顺序、每个 `AllowedProperties` 恰好一项：`label` 中文名、`advanced` 高级参数（默认折叠）、`show_if`（键是另一字段路径，值按表单字符串比较，布尔 "true"/"false"、未填 ""，各键同时满足才显示）、`default`、`placeholder`、`option_labels`（"" 键是选填枚举「不选」项的文字）。
- `default` 只给节点端实际缺省或唯一可选的值（vless / trojan 的 network=tcp 与表单一贯把缺省当 tcp 一致；tls 三态不给默认；tuic 拥塞控制缺省 cubic 只写进选项文字；shadowtls method 缺省按 pdnd 是 aes-256-gcm）。改 pdnd 的缺省要同步这里。
- 表单表里的额外说明经 `withForms` 补进 `Hints`（只补没有的键）；`schema_form_test.go` 钉住：路径集合与 `AllowedProperties` 相同、必填不能高级、`show_if` / `option_labels` / `default` 的取值合乎枚举与类型。加字段要同时登记表单元数据。

## 其它
- 前端假后端的 `panel/frontend/dev/mock/admin/node-schemas.ts` 是 schema 响应的原样导出：改了 schema 或 handler 的 DTO，`TestMockNodeSchemasMatchGo`（node/handler）会红，按它的提示加 `-update` 重导。
- 表单说明在 schema `Hints`（`schema_hints.go`）；`policy_test.go` 的 `TestSchemaEnumsMatchPolicyLists` 要求 hint 指向真实字段。
- ECH（`protocol/ech.go`）：`GenerateECHKey` 出的 ECHConfigList 与 Xboard 同格式（版本 0xfe0d、X25519、套件 (1,1)(1,3)、max_name_len 0、扩展为空，只放一个 ECHConfig）；`ECHCapable` / `ECHUnsupportedReason` 按**翻译后的内核配置**判（vless/vmess/trojan 要 `tls == true` 且非 REALITY、network 非 xhttp-h3/mkcp；anytls 要有证书；naive 要 TLS），节点端 `kernel/inbound_tls_ech.go` 的取值范围与它一致。
- `country_code` 只在管理端节点 DTO 输出（`panel/internal/module/node/handler/dto_inventory_nodes.go`）；门户与订阅（`panel/internal/module/subscription/`）的任何接口都不得输出（产品「保留规则 3」）。
