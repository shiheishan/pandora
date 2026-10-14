// Package protocol 是节点协议配置的纯函数包：协议 schema、写入校验、读接口抹敏感值、
// PATCH 补回被抹掉的密钥、同机端口门禁的 L4、REALITY 密钥对、存储形状到内核字段名的翻译。
//
// 不碰数据库、不读配置、不依赖任何模块（depguard 的 pure-packages 规则钉住）。
// 后台编排（node/service）、节点配置下发与订阅线路（KernelConfig）都经这里，同一张映射表。
//
// 库里存的 protocol_config 是后台表单写出的 xboard 形状（tls、reality_settings.*、
// network_settings.*…），KernelConfig 把它翻译成内核字段名。
package protocol

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Schema 是后台可读、不可改的协议约束元数据（GET /v1/node-protocol-schemas 的一项）。
// schema 与二进制一起发布，不让普通配置权限扩大服务端允许面。
type Schema struct {
	NodeType            string              `json:"node_type"`
	Version             int                 `json:"version"`
	Status              string              `json:"status"`
	Required            []string            `json:"required"`
	AllowedProperties   []string            `json:"allowed_properties"`
	Methods             []string            `json:"methods,omitempty"`
	Enums               map[string][]string `json:"enums,omitempty"`
	PropertyTypes       map[string]string   `json:"property_types,omitempty"`
	SensitiveProperties []string            `json:"sensitive_properties,omitempty"`
	// Hints 是表单上挂在字段下的一句说明（按字段路径），前端不硬编码协议知识。
	Hints map[string]string `json:"hints,omitempty"`
}

// 节点入站实际 bind 的 L4。
const (
	ListenTCP = "tcp"
	ListenUDP = "udp"
)

// CanonicalType 是协议名唯一的存储与审计写法（去空白、小写）。
func CanonicalType(nodeType string) string {
	return strings.ToLower(strings.TrimSpace(nodeType))
}

// StableTypes 返回可下发的稳定协议（与 pdnd kernel/capabilities.go、Schemas 三份名单相同）。
func StableTypes() []string {
	return append([]string(nil), stableTypes[:]...)
}

// Schemas 返回全部稳定协议的 schema，按 node_type 排序。
func Schemas() []Schema {
	return stableSchemas()
}

// Validate 校验一次协议写入。nodeType 为空（未配置协议的草稿）返回 version 0、无错误；
// 已填的必须是当前 schema。fields 的键是表单路径（server_port、node_type、
// protocol_config.<键>），值是中文说明；为空即通过。
func Validate(nodeType string, port int, raw json.RawMessage) (version int, fields map[string]string) {
	return validateForm(nodeType, port, raw)
}

// Warnings 返回存量协议配置不满足现行收紧规则的提示（只提示，照常下发）。
func Warnings(nodeType string, raw json.RawMessage) []string {
	return configWarnings(nodeType, raw)
}

// Redact 按敏感键名表在任意深度抹掉敏感值（读接口用）。不是 JSON 对象的原样返回。
func Redact(raw json.RawMessage) json.RawMessage {
	return redact(raw)
}

// PreserveSecrets 用 stored 补回 incoming 里缺席的、被 Redact 抹掉的敏感键（PATCH 整体替换用）。
// 显式给值（含空串、null）以 incoming 为准；数组长度变了不补；挂在开关上的密钥开关变了不补。
// 换了协议类型不补由调用方判断（不调用本函数）。
func PreserveSecrets(stored, incoming json.RawMessage) (json.RawMessage, error) {
	return preserveSecrets(stored, incoming)
}

// ListenL4 推导节点入站实际 bind 的 L4（ListenTCP / ListenUDP），与 pdnd
// kernel/port_claims.go 的 inboundPortKey 同口径。raw 不是对象时当作没有字段。
func ListenL4(nodeType string, raw json.RawMessage) string {
	return listenL4(nodeType, raw)
}

// PortClaimLabel 是端口冲突提示里的「端口/L4」写法。
func PortClaimLabel(port int, l4 string) string {
	return strconv.Itoa(port) + "/" + strings.ToUpper(l4)
}

// GenerateRealityKeypair 生成一对 X25519 密钥（RFC 7748 clamping，base64 raw URL 编码）。
func GenerateRealityKeypair() (privB64, pubB64 string, err error) {
	return generateRealityKeypair()
}

// KernelConfig 把库里存的 xboard 形状翻译成内核字段名（摊平 → 改名 → 按协议调整）。
// 下发给节点的配置文档与订阅渲染都经它，两边必须同一张映射表。nodeType 在这里规范化；
// 已经是内核扁平形状的旧数据原样通过；不改 cfg，返回新 map。
func KernelConfig(nodeType string, cfg map[string]any) map[string]any {
	return toKernelConfig(CanonicalType(nodeType), cfg)
}
