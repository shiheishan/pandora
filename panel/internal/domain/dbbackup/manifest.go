package dbbackup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ManifestSchema     = "aegispanel.webdav-backup-manifest/v1"
	SignatureAlgorithm = "Ed25519"
	manifestSignDomain = "AegisPanel-WebDAV-Backup-Manifest-v1\x00"
	maxManifestBytes   = 16 << 10
	zeroManifestHash   = "0000000000000000000000000000000000000000000000000000000000000000"
)

var backupIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)

type ArtifactRef struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type BackupManifestPayload struct {
	Schema                 string      `json:"schema"`
	SignatureAlgorithm     string      `json:"signature_algorithm"`
	SigningKeyID           string      `json:"signing_key_id"`
	Sequence               uint64      `json:"sequence"`
	BackupID               string      `json:"backup_id"`
	CreatedAt              string      `json:"created_at"`
	PreviousManifestSHA256 string      `json:"previous_manifest_sha256"`
	Archive                ArtifactRef `json:"archive"`
	Checksum               ArtifactRef `json:"checksum"`
}

type SignedBackupManifest struct {
	Payload   BackupManifestPayload `json:"payload"`
	Signature string                `json:"signature"`
}

type ManifestTransaction struct {
	Bytes          []byte
	ObjectName     string
	checkpointPath string
	lock           *os.File
	closed         bool
}

func ManifestKeyID(publicKey ed25519.PublicKey) string {
	sum := sha256.Sum256(publicKey)
	return "ed25519-sha256:" + hex.EncodeToString(sum[:])
}

func LoadManifestPrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := readPrivateFile(path, 256)
	if err != nil {
		return nil, err
	}
	const prefix = "AEPB-ED25519-SEED-V1 "
	line := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if !strings.HasPrefix(line, prefix) {
		return nil, errors.New("备份清单签名密钥格式无效")
	}
	seed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(line, prefix))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("备份清单签名密钥格式无效")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func LoadManifestPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := readPrivateFile(path, 256)
	if err != nil {
		return nil, err
	}
	const prefix = "AEPB-ED25519-PUBLIC-V1 "
	line := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if !strings.HasPrefix(line, prefix) {
		return nil, errors.New("备份清单公钥格式无效")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(line, prefix))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("备份清单公钥格式无效")
	}
	return ed25519.PublicKey(publicKey), nil
}

func InitializeManifestSigningKey(path string) (string, error) {
	if _, err := validateSecureParent(path); err != nil {
		return "", err
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return "", errors.New("生成备份清单签名密钥失败")
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", errors.New("备份清单签名密钥已存在或无法创建")
	}
	line := "AEPB-ED25519-SEED-V1 " + base64.RawStdEncoding.EncodeToString(seed) + "\n"
	if _, err := io.WriteString(f, line); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", errors.New("写入备份清单签名密钥失败")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", errors.New("同步备份清单签名密钥失败")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", errors.New("关闭备份清单签名密钥失败")
	}
	if err := syncCheckpointDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(path)
		return "", errors.New("持久化备份清单签名密钥失败")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return "AEPB-ED25519-PUBLIC-V1 " + base64.RawStdEncoding.EncodeToString(publicKey), nil
}

func BeginManifestTransaction(pair LocalPair, signingKeyFile, checkpointPath string) (*ManifestTransaction, error) {
	if pair.BackupID == "" || pair.Archive.Name == "" || pair.Checksum.Name == "" {
		return nil, errors.New("备份清单缺少本地制品信息")
	}
	privateKey, err := LoadManifestPrivateKey(signingKeyFile)
	if err != nil {
		return nil, err
	}
	if _, err := validateSecureParent(checkpointPath); err != nil {
		return nil, err
	}
	lock, err := openAndLockCheckpoint(checkpointPath + ".lock")
	if err != nil {
		return nil, err
	}
	tx := &ManifestTransaction{checkpointPath: checkpointPath, lock: lock}
	previousRaw, err := loadCheckpointIfPresent(checkpointPath)
	if err != nil {
		_ = tx.Close()
		return nil, err
	}
	manifestRaw, manifest, err := BuildSignedManifest(pair, previousRaw, privateKey)
	if err != nil {
		_ = tx.Close()
		return nil, err
	}
	tx.Bytes = manifestRaw
	tx.ObjectName = "aegis-postgres-" + manifest.Payload.BackupID + ".manifest.json"
	return tx, nil
}

func BuildSignedManifest(pair LocalPair, previousRaw []byte, privateKey ed25519.PrivateKey) ([]byte, *SignedBackupManifest, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("备份清单签名密钥无效")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := ManifestKeyID(publicKey)
	createdAt, err := time.Parse("20060102T150405Z", pair.BackupID)
	if err != nil || !validArtifactPair(pair) {
		return nil, nil, errors.New("备份清单制品绑定无效")
	}
	sequence := uint64(1)
	previousHash := zeroManifestHash
	if len(previousRaw) > 0 {
		previous, err := ParseAndVerifyManifest(previousRaw, publicKey)
		if err != nil {
			return nil, nil, errors.New("备份清单可信检查点无效")
		}
		if previous.Payload.SigningKeyID != keyID {
			return nil, nil, errors.New("备份清单签名密钥与检查点不一致")
		}
		if previous.Payload.BackupID == pair.BackupID {
			if previous.Payload.Archive != pair.Archive || previous.Payload.Checksum != pair.Checksum {
				return nil, nil, errors.New("同一备份 ID 的制品发生冲突")
			}
			return append([]byte(nil), previousRaw...), previous, nil
		}
		previousTime, _ := time.Parse("20060102T150405Z", previous.Payload.BackupID)
		if !createdAt.After(previousTime) || previous.Payload.Sequence == math.MaxUint64 {
			return nil, nil, errors.New("备份清单序列不允许倒退或溢出")
		}
		sequence = previous.Payload.Sequence + 1
		sum := sha256.Sum256(previousRaw)
		previousHash = hex.EncodeToString(sum[:])
	}
	payload := BackupManifestPayload{
		Schema: ManifestSchema, SignatureAlgorithm: SignatureAlgorithm, SigningKeyID: keyID,
		Sequence: sequence, BackupID: pair.BackupID, CreatedAt: createdAt.UTC().Format(time.RFC3339),
		PreviousManifestSHA256: previousHash, Archive: pair.Archive, Checksum: pair.Checksum,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, errors.New("序列化备份清单失败")
	}
	signature := ed25519.Sign(privateKey, append([]byte(manifestSignDomain), payloadJSON...))
	manifest := &SignedBackupManifest{Payload: payload, Signature: base64.RawStdEncoding.EncodeToString(signature)}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return nil, nil, errors.New("序列化签名备份清单失败")
	}
	canonical = append(canonical, '\n')
	return canonical, manifest, nil
}

func ParseAndVerifyManifest(raw []byte, publicKey ed25519.PublicKey) (*SignedBackupManifest, error) {
	if len(raw) == 0 || len(raw) > maxManifestBytes || len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("签名备份清单无效")
	}
	var manifest SignedBackupManifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return nil, errors.New("签名备份清单 JSON 无效")
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, errors.New("签名备份清单包含多余内容")
	}
	canonical, err := json.Marshal(&manifest)
	if err != nil {
		return nil, errors.New("签名备份清单无法重新编码")
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) || !validManifestPayload(manifest.Payload, publicKey) {
		return nil, errors.New("签名备份清单不是规范可信格式")
	}
	signature, err := base64.RawStdEncoding.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, errors.New("备份清单签名格式无效")
	}
	payloadJSON, _ := json.Marshal(manifest.Payload)
	if !ed25519.Verify(publicKey, append([]byte(manifestSignDomain), payloadJSON...), signature) {
		return nil, errors.New("备份清单签名验证失败")
	}
	return &manifest, nil
}

func VerifyManifestAgainstCheckpoint(manifestRaw, checkpointRaw []byte, publicKey ed25519.PublicKey) error {
	if _, err := ParseAndVerifyManifest(checkpointRaw, publicKey); err != nil {
		return errors.New("可信备份检查点验证失败")
	}
	if _, err := ParseAndVerifyManifest(manifestRaw, publicKey); err != nil {
		return err
	}
	if !bytes.Equal(manifestRaw, checkpointRaw) {
		return errors.New("备份清单与 WebDAV 之外的可信检查点不一致")
	}
	return nil
}

func VerifyRecoveryBundle(archivePath, checksumPath, manifestPath, publicKeyPath, checkpointPath string) error {
	pair, err := VerifyLocalPair(archivePath, checksumPath)
	if err != nil {
		return err
	}
	manifestRaw, err := readPrivateFile(manifestPath, maxManifestBytes)
	if err != nil {
		return err
	}
	checkpointRaw, err := readPrivateFile(checkpointPath, maxManifestBytes)
	if err != nil {
		return err
	}
	publicKey, err := LoadManifestPublicKey(publicKeyPath)
	if err != nil {
		return err
	}
	if err := VerifyManifestAgainstCheckpoint(manifestRaw, checkpointRaw, publicKey); err != nil {
		return err
	}
	manifest, err := ParseAndVerifyManifest(manifestRaw, publicKey)
	if err != nil {
		return err
	}
	if manifest.Payload.BackupID != pair.BackupID || manifest.Payload.Archive != pair.Archive || manifest.Payload.Checksum != pair.Checksum ||
		filepath.Base(manifestPath) != "aegis-postgres-"+pair.BackupID+".manifest.json" {
		return errors.New("备份制品与签名清单不一致")
	}
	return nil
}

func (tx *ManifestTransaction) Commit() error {
	if tx == nil || tx.closed || len(tx.Bytes) == 0 {
		return errors.New("备份清单事务无效")
	}
	return writeSecureAtomic(tx.checkpointPath, tx.Bytes)
}

func (tx *ManifestTransaction) Close() error {
	if tx == nil || tx.closed {
		return nil
	}
	tx.closed = true
	return unlockAndCloseCheckpoint(tx.lock)
}

func validArtifactPair(pair LocalPair) bool {
	if !backupIDPattern.MatchString(pair.BackupID) || pair.Archive.Bytes <= 0 || pair.Checksum.Bytes <= 0 || pair.Checksum.Bytes > 256 {
		return false
	}
	archiveName := "aegis-postgres-" + pair.BackupID + ".dump.age"
	return pair.Archive.Name == archiveName && pair.Checksum.Name == archiveName+".sha256" &&
		validLowerSHA256(pair.Archive.SHA256) && validLowerSHA256(pair.Checksum.SHA256)
}

func validManifestPayload(payload BackupManifestPayload, publicKey ed25519.PublicKey) bool {
	if payload.Schema != ManifestSchema || payload.SignatureAlgorithm != SignatureAlgorithm || payload.SigningKeyID != ManifestKeyID(publicKey) ||
		payload.Sequence == 0 || !backupIDPattern.MatchString(payload.BackupID) || !validLowerSHA256(payload.PreviousManifestSHA256) {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339, payload.CreatedAt)
	wantTime, wantErr := time.Parse("20060102T150405Z", payload.BackupID)
	if err != nil || wantErr != nil || !createdAt.Equal(wantTime) {
		return false
	}
	pair := LocalPair{BackupID: payload.BackupID, Archive: payload.Archive, Checksum: payload.Checksum}
	if !validArtifactPair(pair) {
		return false
	}
	if payload.Sequence == 1 {
		return payload.PreviousManifestSHA256 == zeroManifestHash
	}
	return payload.PreviousManifestSHA256 != zeroManifestHash
}

func validLowerSHA256(value string) bool {
	if len(value) != sha256HexLength || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func loadCheckpointIfPresent(path string) ([]byte, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errors.New("读取备份清单检查点失败")
	}
	return readPrivateFile(path, maxManifestBytes)
}

func openAndLockCheckpoint(path string) (*os.File, error) {
	if _, err := validateSecureParent(path); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("打开备份清单锁失败")
	}
	opened, statErr := f.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) ||
		requireSecureFileMode(opened) != nil || requireSecureFileOwner(opened) != nil || requireSecureSingleLink(opened) != nil {
		_ = f.Close()
		return nil, errors.New("备份清单锁文件不安全")
	}
	if err := acquireCheckpointLock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func unlockAndCloseCheckpoint(f *os.File) error {
	if f == nil {
		return nil
	}
	unlockErr := releaseCheckpointLock(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func writeSecureAtomic(path string, payload []byte) error {
	parent, err := validateSecureParent(path)
	if err != nil {
		return err
	}
	temp := filepath.Join(parent, "."+filepath.Base(path)+".tmp."+uuid.NewString())
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("创建备份清单临时检查点失败")
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(temp)
		}
	}()
	if _, err := io.Copy(f, bytes.NewReader(payload)); err != nil {
		return errors.New("写入备份清单检查点失败")
	}
	if err := f.Sync(); err != nil {
		return errors.New("同步备份清单检查点失败")
	}
	if err := f.Close(); err != nil {
		return errors.New("关闭备份清单检查点失败")
	}
	if err := replaceCheckpoint(temp, path); err != nil {
		return err
	}
	cleanup = false
	return syncCheckpointDirectory(parent)
}
