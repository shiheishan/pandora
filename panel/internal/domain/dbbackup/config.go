package dbbackup

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const DefaultWebDAVConfigPath = "/etc/aegispanel/backup-webdav.json"

type WebDAVFileConfig struct {
	Endpoint                  string `json:"endpoint"`
	BasePath                  string `json:"base_path"`
	Username                  string `json:"username"`
	PasswordFile              string `json:"password_file"`
	AllowPrivateNetwork       bool   `json:"allow_private_network"`
	ManifestSigningKeyFile    string `json:"manifest_signing_key_file"`
	ManifestCheckpointFile    string `json:"manifest_checkpoint_file"`
	CheckpointReplicationHook string `json:"checkpoint_replication_hook"`
}

type WebDAVRuntimeConfig struct {
	Target                    Target
	ManifestSigningKeyFile    string
	ManifestCheckpointFile    string
	CheckpointReplicationHook string
}

func LoadWebDAVTarget(ctx context.Context, configPath string) (Target, error) {
	return loadWebDAVTarget(ctx, net.DefaultResolver, configPath)
}

func LoadWebDAVRuntimeConfig(ctx context.Context, configPath string) (WebDAVRuntimeConfig, error) {
	return loadWebDAVRuntimeConfig(ctx, net.DefaultResolver, configPath)
}

func loadWebDAVTarget(ctx context.Context, resolver Resolver, configPath string) (Target, error) {
	runtimeConfig, err := loadWebDAVRuntimeConfig(ctx, resolver, configPath)
	return runtimeConfig.Target, err
}

func loadWebDAVRuntimeConfig(ctx context.Context, resolver Resolver, configPath string) (WebDAVRuntimeConfig, error) {
	if !filepath.IsAbs(configPath) {
		return WebDAVRuntimeConfig{}, errors.New("WebDAV 配置路径必须是绝对路径")
	}
	raw, err := readPrivateFile(configPath, 64<<10)
	if err != nil {
		return WebDAVRuntimeConfig{}, err
	}
	var cfg WebDAVFileConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return WebDAVRuntimeConfig{}, errors.New("WebDAV 配置不是有效 JSON")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return WebDAVRuntimeConfig{}, err
	}
	if !filepath.IsAbs(cfg.ManifestSigningKeyFile) || !filepath.IsAbs(cfg.ManifestCheckpointFile) ||
		!filepath.IsAbs(cfg.CheckpointReplicationHook) {
		return WebDAVRuntimeConfig{}, errors.New("备份清单签名密钥、检查点和独立复制 Hook 必须使用绝对路径")
	}
	if err := validateCheckpointHookPath(cfg.CheckpointReplicationHook); err != nil {
		return WebDAVRuntimeConfig{}, err
	}
	if err := ensureDistinctSecurePaths(configPath, cfg.PasswordFile, cfg.ManifestSigningKeyFile,
		cfg.ManifestCheckpointFile, cfg.CheckpointReplicationHook); err != nil {
		return WebDAVRuntimeConfig{}, err
	}
	password := ""
	if cfg.PasswordFile != "" {
		if !filepath.IsAbs(cfg.PasswordFile) {
			return WebDAVRuntimeConfig{}, errors.New("WebDAV 密码文件必须是绝对路径")
		}
		secret, err := readPrivateFile(cfg.PasswordFile, 4<<10)
		if err != nil {
			return WebDAVRuntimeConfig{}, err
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(secret), "\n"), "\r")
		if password == "" {
			return WebDAVRuntimeConfig{}, errors.New("WebDAV 密码文件为空")
		}
	}
	target, err := ParseTarget(ctx, resolver, cfg.Endpoint, cfg.BasePath, cfg.Username, password, cfg.AllowPrivateNetwork)
	if err != nil {
		return WebDAVRuntimeConfig{}, err
	}
	return WebDAVRuntimeConfig{Target: target, ManifestSigningKeyFile: cfg.ManifestSigningKeyFile,
		ManifestCheckpointFile: cfg.ManifestCheckpointFile, CheckpointReplicationHook: cfg.CheckpointReplicationHook}, nil
}

func ensureDistinctSecurePaths(paths ...string) error {
	seen := map[string]struct{}{}
	infos := make([]os.FileInfo, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		key := strings.ToLower(clean)
		if _, exists := seen[key]; exists {
			return errors.New("备份配置、密码、签名密钥、检查点和独立 Hook 必须使用不同路径")
		}
		seen[key] = struct{}{}
		info, err := os.Stat(clean)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return errors.New("无法验证备份私密路径")
		}
		for _, previous := range infos {
			if os.SameFile(info, previous) {
				return errors.New("备份密码、签名密钥和检查点不能共用同一文件")
			}
		}
		infos = append(infos, info)
	}
	return nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("WebDAV 配置包含多余内容")
	}
	return nil
}

func readPrivateFile(name string, limit int64) ([]byte, error) {
	f, _, err := openSecureRegular(name, limit, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, errors.New("WebDAV 私密配置过大或无法读取")
	}
	return raw, nil
}

type LocalPair struct {
	ArchiveSHA256  string
	ChecksumSHA256 string
	BackupID       string
	Archive        ArtifactRef
	Checksum       ArtifactRef
}

func VerifyLocalPair(archive, checksum string) (LocalPair, error) {
	if !filepath.IsAbs(archive) || !filepath.IsAbs(checksum) || filepath.Dir(archive) != filepath.Dir(checksum) ||
		checksum != archive+".sha256" || !backupObjectName.MatchString(filepath.Base(archive)) ||
		!backupObjectName.MatchString(filepath.Base(checksum)) {
		return LocalPair{}, errors.New("备份文件对不符合固定路径契约")
	}
	checksumFile, checksumInfo, err := openSecureRegular(checksum, 256, true)
	if err != nil {
		return LocalPair{}, err
	}
	defer checksumFile.Close()
	raw, err := io.ReadAll(io.LimitReader(checksumFile, 257))
	if err != nil || len(raw) > 256 {
		return LocalPair{}, errors.New("读取备份校验文件失败")
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[1] != filepath.Base(archive) || len(fields[0]) != sha256HexLength {
		return LocalPair{}, errors.New("备份校验文件格式无效")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return LocalPair{}, errors.New("备份校验摘要格式无效")
	}
	archiveFile, archiveInfo, err := openSecureRegular(archive, 0, true)
	if err != nil {
		return LocalPair{}, err
	}
	defer archiveFile.Close()
	got, err := hashReader(archiveFile)
	if err != nil || !strings.EqualFold(got, fields[0]) {
		return LocalPair{}, errors.New("本地备份 SHA256 校验失败")
	}
	if _, err := checksumFile.Seek(0, io.SeekStart); err != nil {
		return LocalPair{}, errors.New("重置备份校验文件失败")
	}
	checksumDigest, err := hashReader(checksumFile)
	if err != nil {
		return LocalPair{}, errors.New("计算备份校验文件摘要失败")
	}
	archiveDigest := strings.ToLower(got)
	base := filepath.Base(archive)
	backupID := strings.TrimSuffix(strings.TrimPrefix(base, "aegis-postgres-"), ".dump.age")
	return LocalPair{
		ArchiveSHA256:  archiveDigest,
		ChecksumSHA256: checksumDigest,
		BackupID:       backupID,
		Archive:        ArtifactRef{Name: base, Bytes: archiveInfo.Size(), SHA256: archiveDigest},
		Checksum:       ArtifactRef{Name: filepath.Base(checksum), Bytes: checksumInfo.Size(), SHA256: checksumDigest},
	}, nil
}

const sha256HexLength = 64

func openSecureRegular(name string, maxSize int64, requireNonEmpty bool) (*os.File, os.FileInfo, error) {
	if _, err := validateSecureParent(name); err != nil {
		return nil, nil, err
	}
	f, err := openPathReadOnly(name)
	if err != nil {
		return nil, nil, errors.New("打开私密文件失败")
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = f.Close()
		}
	}()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() ||
		(requireNonEmpty && opened.Size() <= 0) || (maxSize > 0 && opened.Size() > maxSize) {
		return nil, nil, errors.New("私密文件必须是非共享的普通文件")
	}
	if err := requireSecureFileMode(opened); err != nil {
		return nil, nil, err
	}
	current, err := os.Lstat(name)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return nil, nil, errors.New("私密文件在打开期间发生变化")
	}
	if err := requireSecureFileOwner(opened); err != nil {
		return nil, nil, err
	}
	if err := requireSecureSingleLink(opened); err != nil {
		return nil, nil, err
	}
	closeOnError = false
	return f, opened, nil
}

func validateSecureParent(name string) (string, error) {
	if !filepath.IsAbs(name) {
		return "", errors.New("私密文件路径必须是绝对路径")
	}
	parent := filepath.Clean(filepath.Dir(name))
	parentInfo, err := os.Stat(parent)
	if err != nil || !parentInfo.IsDir() {
		return "", errors.New("私密文件父目录必须是受保护目录")
	}
	if err := requireSecureParentMode(parentInfo); err != nil {
		return "", err
	}
	if err := requireSecureFileOwner(parentInfo); err != nil {
		return "", err
	}
	if err := requireSecureResolvedParent(parent); err != nil {
		return "", err
	}
	return parent, nil
}
