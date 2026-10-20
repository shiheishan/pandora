package service

// 读发布清单与签名、给出节点在线升级的目标版本；面板不验签（信任根在节点内置公钥）。
// 清单与签名按（大小、修改时间）缓存原字节，与二进制摘要缓存同口径，文件变了重读。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// PdndManifestFile / PdndSignatureFile 是发布包目录里清单与签名文件名。
const (
	PdndManifestFile  = "pdnd-manifest.json"
	PdndSignatureFile = "pdnd-manifest.json.sig"
)

const (
	pdndManifestMaxBytes  = 64 << 10
	pdndSignatureMaxBytes = 4 << 10
)

// pdndFileCache 按（大小、修改时间）缓存清单与签名的原字节（同 pdndDigestCache 的口径）。
type pdndFileCache struct {
	mu      sync.Mutex
	entries map[string]pdndFile
}

type pdndFile struct {
	size    int64
	modTime time.Time
	data    []byte
}

// readSmall 读发布包目录里的一个小文件（清单或签名）：缺、过大、不是普通文件都按「没有」处理（缺以外的原因记 Warn）；
// （大小、修改时间）没变就用缓存的原字节。返回的切片被所有调用方共享，只读不改。
func (d pdndDist) readSmall(ctx context.Context, name string, maxBytes int64) (data []byte, found bool) {
	if d.dir == "" {
		return nil, false
	}
	f, err := os.OpenInRoot(d.dir, name)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			plog.From(ctx).Warn("读取节点端发布清单失败", zap.String("file", name), zap.Error(err))
		}
		return nil, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		plog.From(ctx).Warn("读取节点端发布清单失败", zap.String("file", name), zap.Error(err))
		return nil, false
	}
	if info.IsDir() || info.Size() > maxBytes {
		plog.From(ctx).Warn("节点端发布清单文件过大或不是普通文件", zap.String("file", name))
		return nil, false
	}

	if d.files != nil {
		d.files.mu.Lock()
		e, ok := d.files.entries[name]
		d.files.mu.Unlock()
		if ok && e.size == info.Size() && e.modTime.Equal(info.ModTime()) {
			return e.data, true
		}
	}

	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		plog.From(ctx).Warn("读取节点端发布清单失败", zap.String("file", name), zap.Error(err))
		return nil, false
	}
	if int64(len(raw)) > maxBytes {
		plog.From(ctx).Warn("节点端发布清单文件过大或不是普通文件", zap.String("file", name))
		return nil, false
	}

	if d.files != nil {
		d.files.mu.Lock()
		d.files.entries[name] = pdndFile{size: info.Size(), modTime: info.ModTime(), data: raw}
		d.files.mu.Unlock()
	}
	return raw, true
}

// pdndManifestDoc 是 pdnd-manifest.json 里面板要读的字段；不拒绝未知字段（节点侧以后可能加），签名覆盖的是原字节。
type pdndManifestDoc struct {
	Schema    int                             `json:"schema"`
	Version   string                          `json:"version"`
	Serial    uint64                          `json:"serial"`
	Artifacts map[string]pdndManifestArtifact `json:"artifacts"`
}

// pdndManifestArtifact 是清单里一个架构的产物。
type pdndManifestArtifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// pdndVersionPattern 是清单 version 的合法形状（如 v2026.10.21）。
var pdndVersionPattern = regexp.MustCompile(`^[0-9A-Za-z._+-]{1,64}$`)

// parsePdndManifest 解析并校验清单：schema 为 1、version 合法、serial 在 1..MaxInt64（库里是 bigint）；
// 只认面板能托管的 linux-amd64 / linux-arm64（别的架构跳过），产物名必须是白名单里对应的那个、摘要是小写 hex；一个都不剩算不合法。
func parsePdndManifest(raw []byte) (version string, serial uint64, arts map[string]PdndTargetArtifact, ok bool) {
	var doc pdndManifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", 0, nil, false
	}
	if doc.Schema != 1 {
		return "", 0, nil, false
	}
	if !pdndVersionPattern.MatchString(doc.Version) {
		return "", 0, nil, false
	}
	if doc.Serial == 0 || doc.Serial > uint64(math.MaxInt64) {
		return "", 0, nil, false
	}

	out := make(map[string]PdndTargetArtifact)
	for key, a := range doc.Artifacts {
		switch key {
		case "linux-amd64", "linux-arm64":
		default:
			continue
		}
		wantFile := "pandora-native-" + key
		if a.File != wantFile || !sha256Hex.MatchString(a.SHA256) {
			return "", 0, nil, false
		}
		out[key] = PdndTargetArtifact{File: a.File, SHA256: a.SHA256}
	}
	if len(out) == 0 {
		return "", 0, nil, false
	}
	return doc.Version, doc.Serial, out, true
}

// PdndTarget 读发布包里的清单与签名，给出目标版本；不验签，信任根在节点。
// 后台列表每次请求调一次，文件与二进制摘要都有缓存。
func (s *Service) PdndTarget(ctx context.Context) PdndTarget {
	manifest, ok := s.pdnd.readSmall(ctx, PdndManifestFile, pdndManifestMaxBytes)
	if !ok || len(manifest) == 0 {
		return PdndTarget{Reason: UpgradeBlockManifestMissing}
	}
	sig, ok := s.pdnd.readSmall(ctx, PdndSignatureFile, pdndSignatureMaxBytes)
	if !ok || len(bytes.TrimSpace(sig)) == 0 {
		return PdndTarget{Reason: UpgradeBlockSignatureMissing}
	}

	version, serial, arts, parsed := parsePdndManifest(manifest)
	if !parsed {
		return PdndTarget{Reason: UpgradeBlockManifestInvalid}
	}

	anyAvailable := false
	for key, art := range arts {
		a, err := s.OpenPdndArtifact(ctx, art.File)
		if err != nil {
			art.Available = false
			art.Reason = UpgradeBlockBinaryMismatch
			arts[key] = art
			continue
		}
		_ = a.File.Close()
		if a.SHA256 == art.SHA256 {
			art.Available = true
			art.Reason = ""
			anyAvailable = true
		} else {
			art.Available = false
			art.Reason = UpgradeBlockBinaryMismatch
		}
		arts[key] = art
	}

	target := PdndTarget{
		Version:   version,
		Serial:    serial,
		Artifacts: arts,
		Manifest:  manifest,
		Signature: sig,
	}
	if anyAvailable {
		target.Available = true
	} else {
		target.Available = false
		target.Reason = UpgradeBlockBinaryMismatch
	}
	return target
}

// pdndArchKey 把节点自报的 runtime.GOARCH 转成清单里的架构键。
func pdndArchKey(arch string) string {
	return "linux-" + strings.ToLower(strings.TrimSpace(arch))
}
