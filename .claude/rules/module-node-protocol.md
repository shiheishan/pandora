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
- `default` 给推荐且能通过校验的值（uxfix-node 10-21 改）：照缺省预填、折叠隐藏字段后，`Validate` 只许在没有缺省、必须由管理员填的字段（REALITY 目标站 / SNI / 密钥对 / short id、Shadowsocks 加密方式之类）上报错，守卫 `TestSchemaFormDefaultsValidate`。现行：VLESS `tls=2`（REALITY）+ `network=tcp` + `flow=xtls-rprx-vision`；Trojan `tls=1` + `cert_mode=acme`；tuic 拥塞控制缺省 cubic 只写进选项文字；shadowtls method 缺省按 pdnd 是 aes-256-gcm、`wildcard_sni` 缺省 off。改 pdnd 的缺省要同步这里；`note` / `hints` 写清哪种组合「保存会被拒绝」，前端照原文显示。
- 严格解码（`validateKernelShape`）的结构体要收下 `AllowedProperties` 里的每个键：漏收的键表单一填就整份 422「包含不支持的字段」（10-21 前 shadowtls 的 `wildcard_sni` 就是这样，缺省 off 也存不进去）。`wildcard_sni` 只认 off / authed / all（与 pdnd `shadowTLSWildcardSNI` 同组）。
- `Validate` 的 422 键已映射回表单路径（如 REALITY 报 `protocol_config.reality_settings.server_name`，不是内核的 `server_names`），假后端照这个键写。
- 表单表里的额外说明经 `withForms` 补进 `Hints`（只补没有的键）；`schema_form_test.go` 钉住：路径集合与 `AllowedProperties` 相同、必填不能高级、`show_if` / `option_labels` / `default` 的取值合乎枚举与类型。加字段要同时登记表单元数据。

## 其它
- 前端假后端的 `panel/frontend/dev/mock/admin/node-schemas.ts` 是 schema 响应的原样导出：改了 schema 或 handler 的 DTO，`TestMockNodeSchemasMatchGo`（node/handler）会红，按它的提示加 `-update` 重导。
- 表单说明在 schema `Hints`（`schema_hints.go`）；`policy_test.go` 的 `TestSchemaEnumsMatchPolicyLists` 要求 hint 指向真实字段。
- ECH（`protocol/ech.go`）：`GenerateECHKey` 出的 ECHConfigList 与 Xboard 同格式（版本 0xfe0d、X25519、套件 (1,1)(1,3)、max_name_len 0、扩展为空，只放一个 ECHConfig）；`ECHCapable` / `ECHUnsupportedReason` 按**翻译后的内核配置**判（vless/vmess/trojan 要 `tls == true` 且非 REALITY、network 非 xhttp-h3/mkcp；anytls 要有证书；naive 要 TLS），节点端 `kernel/inbound_tls_ech.go` 的取值范围与它一致。
- `country_code` 只在管理端节点 DTO 输出（`panel/internal/module/node/handler/dto_inventory_nodes.go`）；门户与订阅（`panel/internal/module/subscription/`）的任何接口都不得输出（产品「保留规则 3」）。

## 证书来源 cert_mode（10-20）
- 带证书的 8 个协议（hysteria2、juicity、socks、http、naive、tuic、anytls、trojan）有 `cert_mode`：`acme` / `file`，缺省按 file（旧数据不迁移）。vless / vmess 不加（普通 TLS 未开放，xhttp-h3 只手填）。
- schema 的 `Required` 含 `cert_mode` 与证书路径，只给前端用：被 `show_if` 藏起来的必填不拦保存；后端在 `validate_kernel.go` 按模式校验（`certModeOf`：acme 时填了路径反而报错，取值只认两种）。表单新建默认 acme，路径与 `allow_insecure` 只在 file 时显示（`certModeFields`、`clientTLSFieldsForCertMode`）；acme 不许 `allow_insecure=true`（`takeClientTLSHints`）。
- `protocol/cert.go`：`UsesCertificate` / `CertMode` / `CertIdentifier`（SNI 否则节点地址）/ `NormalizeCertIdentifier`（与 pdnd `core` 同口径）/ `CertIdentifierProblem`（拦内网与保留 IP、单段名、通配符、内网后缀）/ `ValidateCertIdentity`（service 保存节点时按地址再查）。
