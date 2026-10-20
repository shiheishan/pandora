package upgrade

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

var (
	sha256HexRegex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fileNameRegex  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

// Artifact 描述单个目标架构的发布二进制文件名与期望的 sha256 摘要。
type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Manifest 是发布清单内容（原字节为签名对象，不重新序列化）。
type Manifest struct {
	Schema    int                 `json:"schema"`
	Version   string              `json:"version"`
	Serial    uint64              `json:"serial"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

// SignatureFile 是签名文件 manifest.json.sig 的结构。
type SignatureFile struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"`
}

// ParseManifest 解析并校验发布清单：
// 长度必须 ≤ MaxManifestBytes；
// 必须且仅包含单个 JSON 对象（多余内容报错）；未知字段忽略。
// 要求：Schema == 1；Version 非空且 ≤ 64 字节；Serial > 0；Artifacts 非空；
// Artifacts 每项 SHA256 为 64 位小写十六进制、File 匹配 ^[A-Za-z0-9._-]{1,128}$。
// 任一不满足返回 *Error{Code: ReasonManifestInvalid}。
func ParseManifest(raw []byte) (*Manifest, error) {
	if int64(len(raw)) > MaxManifestBytes {
		return nil, Errorf(ReasonManifestInvalid, "发布清单超过最大大小限制（%d 字节）", MaxManifestBytes)
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, Errorf(ReasonManifestInvalid, "解析清单 JSON 失败: %v", err)
	}

	// 确保没有多余的非空白内容
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Errorf(ReasonManifestInvalid, "清单 JSON 后面存在多余内容")
	}

	if m.Schema != 1 {
		return nil, Errorf(ReasonManifestInvalid, "清单 schema 版本不合规（期望 1，实际 %d）", m.Schema)
	}
	if m.Version == "" || len(m.Version) > 64 {
		return nil, Errorf(ReasonManifestInvalid, "清单 version 为空或超过 64 字节: %q", m.Version)
	}
	if m.Serial == 0 {
		return nil, Errorf(ReasonManifestInvalid, "清单 serial 必须大于 0")
	}
	if len(m.Artifacts) == 0 {
		return nil, Errorf(ReasonManifestInvalid, "清单 artifacts 为空")
	}

	for arch, art := range m.Artifacts {
		if !sha256HexRegex.MatchString(art.SHA256) {
			return nil, Errorf(ReasonManifestInvalid, "架构 %s 的 sha256 格式不合规（须为 64 位小写十六进制）: %s", arch, art.SHA256)
		}
		if !fileNameRegex.MatchString(art.File) {
			return nil, Errorf(ReasonManifestInvalid, "架构 %s 的文件名不合规: %s", arch, art.File)
		}
	}

	return &m, nil
}

// ArchKey 拼接架构键：linux- + goarch（例如 linux-amd64）。
func ArchKey(goarch string) string {
	return "linux-" + goarch
}

// Artifact 获取本机架构对应的发布文件项。
// 若清单中没有该架构，返回 *Error{Code: ReasonManifestInvalid, Detail: "清单里没有 linux-<arch>"}。
func (m *Manifest) Artifact(goarch string) (Artifact, error) {
	key := ArchKey(goarch)
	art, ok := m.Artifacts[key]
	if !ok {
		return Artifact{}, Errorf(ReasonManifestInvalid, "清单里没有 %s", key)
	}
	return art, nil
}

// Check 是节点进程与 root 助手共用的校验四步，顺序固定：
//  1. 验签：k.Verify(manifest, sig)
//  2. 解析：ParseManifest(manifest)
//  3. 本机架构：m.Artifact(goarch)
//  4. serial 检查：
//     buildSerial == 0（dev 构建）一律 ReasonSerialNotNewer（说明 dev 构建不接受升级）；
//     m.Serial <= buildSerial 也是 ReasonSerialNotNewer。
//
// 任何一步失败直接返回该步的错误。
func Check(k *Keyring, manifest, sig []byte, buildSerial uint64, goarch string) (*Manifest, Artifact, error) {
	// 1. 验签
	if err := k.Verify(manifest, sig); err != nil {
		return nil, Artifact{}, err
	}

	// 2. 解析清单
	m, err := ParseManifest(manifest)
	if err != nil {
		return nil, Artifact{}, err
	}

	// 3. 检查本机架构
	art, err := m.Artifact(goarch)
	if err != nil {
		return nil, Artifact{}, err
	}

	// 4. 检查 serial
	if buildSerial == 0 {
		return nil, Artifact{}, Errorf(ReasonSerialNotNewer, "当前为开发构建（buildSerial=0），不接受在线升级")
	}
	if m.Serial <= buildSerial {
		return nil, Artifact{}, Errorf(ReasonSerialNotNewer, "清单版本 serial (%d) 不大于本机构建 serial (%d)", m.Serial, buildSerial)
	}

	return m, art, nil
}
