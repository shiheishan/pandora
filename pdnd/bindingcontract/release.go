package bindingcontract

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxReleaseArtifacts 限制一份发布清单的产物数。
const MaxReleaseArtifacts = 32

// AgentVersion 是 vMAJOR.MINOR.PATCH 形式的官方版本号，不带预发布与构建后缀。
type AgentVersion struct{ Major, Minor, Patch uint32 }

func (v AgentVersion) String() string {
	return "v" + strconv.FormatUint(uint64(v.Major), 10) + "." + strconv.FormatUint(uint64(v.Minor), 10) + "." +
		strconv.FormatUint(uint64(v.Patch), 10)
}

// ParseAgentVersion 只接受规范的 vX.Y.Z：各段十进制、无前导零、不超过 uint32。
// git describe 产出的 v1.2.3-4-gabcdef、dev 之类一律拒绝——只有正式打过 tag 的版本能参与升级。
func ParseAgentVersion(s string) (AgentVersion, error) {
	rest, ok := strings.CutPrefix(s, "v")
	parts := strings.Split(rest, ".")
	if !ok || len(parts) != 3 {
		return AgentVersion{}, errors.New("version must be vMAJOR.MINOR.PATCH")
	}
	var out [3]uint32
	for i, p := range parts {
		if p == "" || len(p) > 1 && p[0] == '0' {
			return AgentVersion{}, errors.New("version segments must be decimal without leading zeros")
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return AgentVersion{}, errors.New("version segments must be decimal uint32")
		}
		out[i] = uint32(n)
	}
	v := AgentVersion{out[0], out[1], out[2]}
	if v.String() != s {
		return AgentVersion{}, errors.New("version is not canonical")
	}
	return v, nil
}

// CompareAgentVersions 返回 -1、0、1，分别表示 a 低于、等于、高于 b；任一方不规范即报错。
func CompareAgentVersions(a, b string) (int, error) {
	va, err := ParseAgentVersion(a)
	if err != nil {
		return 0, err
	}
	vb, err := ParseAgentVersion(b)
	if err != nil {
		return 0, err
	}
	for _, pair := range [][2]uint32{{va.Major, vb.Major}, {va.Minor, vb.Minor}, {va.Patch, vb.Patch}} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}
	return 0, nil
}

// ReleaseArtifact 是发布清单里的一个产物。
type ReleaseArtifact struct {
	Name   string // 如 pandora-native-linux-amd64
	Size   int64
	SHA256 string // 64 位小写十六进制，与 sha256sum 输出一致
}

// ReleaseManifestFields 是官方发布清单签名覆盖的全部字段。产物按 name 严格升序给出。
type ReleaseManifestFields struct {
	Product      string // 只能是 pandora-native
	Version      string // vX.Y.Z
	Commit       string // 40 位小写十六进制
	ReleasedAt   string
	ReleaseKeyID string // 发布签名公钥的 key id
	Artifacts    []ReleaseArtifact
}

// ReleaseManifestPreimage 返回维护者用离线发布私钥签名、pdnd 用内置发布公钥验签的原像。
// 发布清单不过期：同一版本永远是同一份字节，旧版本被「只升不降」挡住，不靠过期时间。
func ReleaseManifestPreimage(in ReleaseManifestFields) ([]byte, error) {
	if in.Product != ReleaseProduct {
		return nil, fmt.Errorf("product must be %s", ReleaseProduct)
	}
	if _, err := ParseAgentVersion(in.Version); err != nil {
		return nil, fmt.Errorf("version: %w", err)
	}
	if len(in.Commit) != 40 || checkLowerHex(in.Commit) != nil {
		return nil, errors.New("commit must be 40 lowercase hex characters")
	}
	if _, err := canonicalTime("released_at", in.ReleasedAt); err != nil {
		return nil, err
	}
	if err := checkKeyID("release_key_id", in.ReleaseKeyID); err != nil {
		return nil, err
	}
	if len(in.Artifacts) == 0 || len(in.Artifacts) > MaxReleaseArtifacts {
		return nil, fmt.Errorf("artifacts must list 1 to %d entries", MaxReleaseArtifacts)
	}
	var b strings.Builder
	b.Grow(320 + 160*len(in.Artifacts))
	b.WriteString(ReleaseManifestContract)
	b.WriteByte('\n')
	line(&b, "product", in.Product)
	line(&b, "version", in.Version)
	line(&b, "commit", in.Commit)
	line(&b, "released_at", in.ReleasedAt)
	line(&b, "release_key_id", in.ReleaseKeyID)
	line(&b, "artifact_count", strconv.Itoa(len(in.Artifacts)))
	for i, a := range in.Artifacts {
		if !isToken(a.Name, 64, true) || strings.Contains(a.Name, "..") {
			return nil, fmt.Errorf("artifacts[%d].name must match [a-z0-9][a-z0-9.-]{0,63} without ..", i)
		}
		if i > 0 && a.Name <= in.Artifacts[i-1].Name {
			return nil, errors.New("artifacts must be sorted by name, strictly ascending")
		}
		if a.Size <= 0 {
			return nil, fmt.Errorf("artifacts[%d].size must be positive", i)
		}
		if err := checkSHA256Hex("sha256", a.SHA256); err != nil {
			return nil, fmt.Errorf("artifacts[%d]: %w", i, err)
		}
		b.WriteString("artifact name=")
		b.WriteString(a.Name)
		b.WriteString(" size=")
		b.WriteString(formatInt(a.Size))
		b.WriteString(" sha256=")
		b.WriteString(a.SHA256)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func checkLowerHex(s string) error {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return errors.New("not lowercase hex")
		}
	}
	return nil
}

// UpgradeOrderFields 是面板「升级到官方版本 X」指令签名覆盖的全部字段。
// 指令只点名版本与发布清单的哈希，不携带任何程序字节：程序必须另由官方发布签名证明。
type UpgradeOrderFields struct {
	TenantID              string
	ServerID              string
	Serial                int64
	OrderID               string // 面板生成的 UUID，pdnd 按它幂等并在上报里回报结果
	TargetVersion         string // vX.Y.Z
	ReleaseManifestSHA256 string // base64(sha256(发布清单 JSON 原始字节))
	RequestNonce          string // 携带本指令的那次清单请求的 X-Server-Nonce
	KeyID                 string // 面板配置签名公钥的 key id
	IssuedAt              string
	ExpiresAt             string
}

// UpgradeOrderPreimage 返回面板配置私钥签名、pdnd 独立重建的升级指令原像。
func UpgradeOrderPreimage(in UpgradeOrderFields) ([]byte, error) {
	if err := checkUUID("tenant_id", in.TenantID); err != nil {
		return nil, err
	}
	if err := checkUUID("server_id", in.ServerID); err != nil {
		return nil, err
	}
	if err := checkSerial(in.Serial); err != nil {
		return nil, err
	}
	if err := checkUUID("order_id", in.OrderID); err != nil {
		return nil, err
	}
	if _, err := ParseAgentVersion(in.TargetVersion); err != nil {
		return nil, fmt.Errorf("target_version: %w", err)
	}
	if err := checkSHA256B64("release_manifest_sha256", in.ReleaseManifestSHA256); err != nil {
		return nil, err
	}
	if err := checkNonce("request_nonce", in.RequestNonce); err != nil {
		return nil, err
	}
	if err := checkKeyID("key_id", in.KeyID); err != nil {
		return nil, err
	}
	if err := canonicalWindow(in.IssuedAt, in.ExpiresAt); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(480)
	b.WriteString(UpgradeOrderContract)
	b.WriteByte('\n')
	line(&b, "tenant_id", in.TenantID)
	line(&b, "server_id", in.ServerID)
	line(&b, "serial", formatInt(in.Serial))
	line(&b, "order_id", in.OrderID)
	line(&b, "target_version", in.TargetVersion)
	line(&b, "release_manifest_sha256", in.ReleaseManifestSHA256)
	line(&b, "request_nonce", in.RequestNonce)
	line(&b, "key_id", in.KeyID)
	line(&b, "issued_at", in.IssuedAt)
	line(&b, "expires_at", in.ExpiresAt)
	return []byte(b.String()), nil
}
