package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// StageRequest 是面板下发的单次暂存升级请求。
type StageRequest struct {
	JobID            string
	Manifest         []byte
	Signature        []byte
	BinaryURL        string
	ConfirmWithinSec uint32
}

// Agent 在 pandora 用户态目录里验签、下载并写出 request，供 path 单元拉起助手。
type Agent struct {
	Dir          string
	Keyring      *Keyring
	BuildSerial  uint64
	GOARCH       string
	BinarySHA256 string
	AllowHTTP    bool
	Transport    http.RoundTripper
	MaxBytes     int64
	Log          *slog.Logger
}

func (a *Agent) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

func (a *Agent) maxBytes() int64 {
	if a.MaxBytes > 0 {
		return a.MaxBytes
	}
	return MaxBinaryBytes
}

func (a *Agent) removeStagingFiles() {
	for _, name := range []string{FileNextTmp, FileNext, FileManifest, FileSignature} {
		_ = os.Remove(filepath.Join(a.Dir, name))
	}
}

// Stage 验签后下载二进制并原子写出 manifest、签名与 request。
func (a *Agent) Stage(ctx context.Context, req StageRequest) error {
	if !ValidJobID(req.JobID) {
		return Errorf(ReasonManifestInvalid, "任务 id 格式不对")
	}
	if int64(len(req.Manifest)) > MaxManifestBytes {
		return Errorf(ReasonManifestInvalid, "发布清单超过最大大小限制（%d 字节）", MaxManifestBytes)
	}
	if int64(len(req.Signature)) > MaxSignatureBytes {
		return Errorf(ReasonSignatureInvalid, "签名文件超过最大大小限制（%d 字节）", MaxSignatureBytes)
	}

	if err := os.MkdirAll(a.Dir, 0o700); err != nil {
		return Errorf(ReasonDownloadFailed, "建暂存目录失败：%v", err)
	}

	if _, err := os.Lstat(filepath.Join(a.Dir, FileRequest)); err == nil {
		return Errorf(ReasonBusy, "已有一次升级在进行")
	}
	if _, err := os.Lstat(filepath.Join(a.Dir, FileApplying)); err == nil {
		return Errorf(ReasonBusy, "已有一次升级在进行")
	}

	m, art, err := Check(a.Keyring, req.Manifest, req.Signature, a.BuildSerial, a.GOARCH)
	if err != nil {
		return err
	}

	u, err := url.Parse(req.BinaryURL)
	if err != nil {
		// url.Parse 的错误里带完整地址（查询串可能有令牌），不往外带。
		return Errorf(ReasonDownloadFailed, "下载地址无效")
	}
	if !schemeAllowed(u.Scheme, a.AllowHTTP) || u.Host == "" {
		return Errorf(ReasonDownloadFailed, "下载地址必须是 https")
	}

	a.removeStagingFiles()
	committed := false
	defer func() {
		if !committed {
			a.removeStagingFiles()
		}
	}()

	tmpPath := filepath.Join(a.Dir, FileNextTmp)
	digest, err := a.download(ctx, req.BinaryURL, tmpPath, a.maxBytes())
	if err != nil {
		return err
	}
	if digest != art.SHA256 {
		return Errorf(ReasonSHA256Mismatch, "下载的二进制摘要与清单不符")
	}

	if err := os.Rename(tmpPath, filepath.Join(a.Dir, FileNext)); err != nil {
		return Errorf(ReasonDownloadFailed, "重命名暂存二进制失败：%v", err)
	}
	if err := writeFileAtomic(a.Dir, FileManifest, req.Manifest, 0o644); err != nil {
		return Errorf(ReasonDownloadFailed, "写入清单失败：%v", err)
	}
	if err := writeFileAtomic(a.Dir, FileSignature, req.Signature, 0o644); err != nil {
		return Errorf(ReasonDownloadFailed, "写入签名失败：%v", err)
	}

	reqBody, err := json.Marshal(Request{
		JobID:            req.JobID,
		ConfirmWithinSec: req.ConfirmWithinSec,
	})
	if err != nil {
		return Errorf(ReasonDownloadFailed, "序列化 request 失败：%v", err)
	}
	if err := writeFileAtomic(a.Dir, FileRequest, reqBody, 0o644); err != nil {
		return Errorf(ReasonDownloadFailed, "写入 request 失败：%v", err)
	}

	committed = true

	a.logger().Info("升级暂存完成",
		slog.String("job_id", req.JobID),
		slog.String("version", m.Version),
		slog.Uint64("serial", m.Serial),
	)
	return nil
}

// MarkHealthy 若 applying 中的摘要与本进程一致，则写出 healthy。
func (a *Agent) MarkHealthy() (bool, error) {
	path := filepath.Join(a.Dir, FileApplying)
	data, err := readSmallFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return false, fmt.Errorf("解析 applying 失败: %w", err)
	}
	if !ValidJobID(m.JobID) || m.SHA256 == "" || m.SHA256 != a.BinarySHA256 {
		return false, nil
	}

	body, err := json.Marshal(Marker{JobID: m.JobID, SHA256: m.SHA256})
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(a.Dir, FileHealthy, body, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// Result 读取助手写入的结果文件；不存在时返回 (nil, nil)。
func (a *Agent) Result() (*Result, error) {
	path := filepath.Join(a.Dir, FileResult)
	data, err := readSmallFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("解析 result 失败: %w", err)
	}
	if r.Phase != PhaseFailed && r.Phase != PhaseRolledBack {
		return nil, fmt.Errorf("result phase 无效: %q", r.Phase)
	}
	return &r, nil
}

// RemoveResult 仅当磁盘上的 result 与 r 四字段完全一致时才删除。
func (a *Agent) RemoveResult(r *Result) error {
	if r == nil {
		return nil
	}
	cur, err := a.Result()
	if err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	if cur.JobID != r.JobID || cur.Phase != r.Phase || cur.ReasonCode != r.ReasonCode || cur.Detail != r.Detail {
		return nil
	}
	return os.Remove(filepath.Join(a.Dir, FileResult))
}

// writeFileAtomic 在 dir 下原子写入 name。
func writeFileAtomic(dir, name string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// readSmallFile 读取普通小文件；不存在时返回可被 errors.Is(..., fs.ErrNotExist) 识别的错误。
func readSmallFile(path string) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("不是普通文件: %s", path)
	}
	if st.Size() > MaxSmallFileBytes {
		return nil, fmt.Errorf("文件超过大小上限 %d 字节", MaxSmallFileBytes)
	}
	return os.ReadFile(path)
}
