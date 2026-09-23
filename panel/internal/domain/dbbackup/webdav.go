package dbbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

var backupObjectName = regexp.MustCompile(`^aegis-postgres-[0-9]{8}T[0-9]{6}Z\.dump\.age(?:\.sha256)?$`)
var manifestObjectName = regexp.MustCompile(`^aegis-postgres-[0-9]{8}T[0-9]{6}Z\.manifest\.json$`)

type UploadResult struct {
	ObjectName string
	SHA256     string
	Bytes      int64
}

type remoteObjectState uint8

const (
	remoteMissing remoteObjectState = iota
	remoteExact
	remoteMismatch
)

type moveResult uint8

const (
	movePublished moveResult = iota
	moveConflict
)

type WebDAVClient struct {
	target Target
	http   *http.Client
}

func NewWebDAVClient(target Target) *WebDAVClient {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("WebDAV 目标地址无效")
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(addrs) == 0 {
			return nil, errors.New("无法解析 WebDAV 主机")
		}
		locals, err := localInterfaceAddresses()
		if err != nil {
			return nil, errors.New("无法安全枚举本机网络地址")
		}
		var lastErr error
		for _, addr := range addrs {
			addr = addr.Unmap()
			if !allowedAddress(addr, target.AllowPrivate) {
				return nil, errors.New("WebDAV 主机解析到受保护的网络")
			}
			if _, local := locals[addr]; local {
				return nil, errors.New("WebDAV 主机不能指向本机地址")
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr != nil {
			return nil, errors.New("连接 WebDAV 服务器失败")
		}
		return nil, errors.New("WebDAV 主机没有可用地址")
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &WebDAVClient{target: target, http: client}
}

func localInterfaceAddresses() (map[netip.Addr]struct{}, error) {
	out := map[netip.Addr]struct{}{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, raw := range addrs {
		prefix, err := netip.ParsePrefix(raw.String())
		if err == nil {
			out[prefix.Addr().Unmap()] = struct{}{}
		}
	}
	return out, nil
}

func (c *WebDAVClient) Probe(ctx context.Context) error {
	req, err := c.request(ctx, "PROPFIND", "", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Depth", "0")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("WebDAV 目录探测失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusMultiStatus && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return statusError("探测", resp.StatusCode)
	}
	return nil
}

func (c *WebDAVClient) UploadVerified(ctx context.Context, localPath, objectName string) (UploadResult, error) {
	return c.uploadVerified(ctx, localPath, objectName, "")
}

func (c *WebDAVClient) UploadVerifiedDigest(ctx context.Context, localPath, objectName, expectedDigest string) (UploadResult, error) {
	if len(expectedDigest) != sha256HexLength {
		return UploadResult{}, errors.New("预期备份摘要无效")
	}
	if _, err := hex.DecodeString(expectedDigest); err != nil {
		return UploadResult{}, errors.New("预期备份摘要无效")
	}
	return c.uploadVerified(ctx, localPath, objectName, strings.ToLower(expectedDigest))
}

func (c *WebDAVClient) UploadVerifiedBytes(ctx context.Context, objectName string, payload []byte) (UploadResult, error) {
	if !manifestObjectName.MatchString(objectName) || len(payload) == 0 || len(payload) > maxManifestBytes {
		return UploadResult{}, errors.New("签名备份清单无效")
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	return c.publishVerified(ctx, bytes.NewReader(payload), int64(len(payload)), objectName, digest)
}

func (c *WebDAVClient) uploadVerified(ctx context.Context, localPath, objectName, expectedDigest string) (UploadResult, error) {
	if !backupObjectName.MatchString(objectName) || filepath.Base(localPath) != objectName {
		return UploadResult{}, errors.New("备份文件名不符合固定格式")
	}
	f, info, err := openSecureRegular(localPath, 0, true)
	if err != nil {
		return UploadResult{}, err
	}
	defer f.Close()
	digest, err := hashReader(f)
	if err != nil {
		return UploadResult{}, err
	}
	if expectedDigest != "" && digest != expectedDigest {
		return UploadResult{}, errors.New("本地备份摘要在上传前发生变化")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return UploadResult{}, errors.New("重置本地备份读取位置失败")
	}
	return c.publishVerified(ctx, f, info.Size(), objectName, digest)
}

func (c *WebDAVClient) publishVerified(ctx context.Context, reader io.Reader, size int64, objectName, digest string) (UploadResult, error) {
	finalState, err := c.inspectRemote(ctx, objectName, size, digest)
	if err != nil {
		return UploadResult{}, err
	}
	if finalState == remoteExact {
		return UploadResult{ObjectName: objectName, SHA256: digest, Bytes: size}, nil
	}
	if finalState == remoteMismatch {
		return UploadResult{}, errors.New("WebDAV 最终对象已存在且内容冲突")
	}
	partial := objectName + ".partial." + uuid.NewString()
	defer c.cleanupObject(partial)
	if err := c.put(ctx, reader, partial, size); err != nil {
		return UploadResult{}, err
	}
	if err := c.verify(ctx, partial, size, digest); err != nil {
		return UploadResult{}, err
	}
	moveState, err := c.move(ctx, partial, objectName)
	if err != nil {
		return UploadResult{}, err
	}
	finalState, err = c.inspectRemote(ctx, objectName, size, digest)
	if err != nil {
		if moveState == movePublished {
			c.cleanupObject(objectName)
		}
		return UploadResult{}, err
	}
	if finalState != remoteExact {
		if moveState == movePublished {
			c.cleanupObject(objectName)
		}
		if moveState == moveConflict {
			return UploadResult{}, errors.New("WebDAV 发布冲突且最终对象不匹配")
		}
		return UploadResult{}, errors.New("WebDAV 发布后最终对象校验失败")
	}
	return UploadResult{ObjectName: objectName, SHA256: digest, Bytes: size}, nil
}

func (c *WebDAVClient) cleanupObject(objectName string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.delete(cleanupCtx, objectName)
}

func hashReader(reader io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, reader); err != nil {
		return "", errors.New("计算本地备份摘要失败")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (c *WebDAVClient) put(ctx context.Context, reader io.Reader, objectName string, size int64) error {
	req, err := c.request(ctx, http.MethodPut, objectName, reader)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return errors.New("上传 WebDAV 备份失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError("上传", resp.StatusCode)
	}
	return nil
}

func (c *WebDAVClient) verify(ctx context.Context, objectName string, size int64, digest string) error {
	state, err := c.inspectRemote(ctx, objectName, size, digest)
	if err != nil {
		return err
	}
	if state != remoteExact {
		return errors.New("WebDAV 远端备份校验失败")
	}
	return nil
}

func (c *WebDAVClient) inspectRemote(ctx context.Context, objectName string, size int64, digest string) (remoteObjectState, error) {
	req, err := c.request(ctx, http.MethodGet, objectName, nil)
	if err != nil {
		return remoteMissing, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return remoteMissing, errors.New("重新下载 WebDAV 备份失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return remoteMissing, nil
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return remoteMissing, statusError("校验", resp.StatusCode)
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, io.LimitReader(resp.Body, size+1))
	if copyErr != nil || n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return remoteMismatch, nil
	}
	var extra [1]byte
	if got, readErr := resp.Body.Read(extra[:]); got != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		return remoteMismatch, nil
	}
	return remoteExact, nil
}

// inspectRetentionTriplet reads and hashes the three exact objects bound by a
// validated deletion intent. It never discovers or derives objects from a
// remote listing. The durable retention executor uses this observation as the
// only input to ReconcileRetentionDeletion.
func (c *WebDAVClient) inspectRetentionTriplet(
	ctx context.Context, triplet RetentionDeleteTriplet,
) (RetentionRemoteState, error) {
	if err := validateRetentionTriplet(triplet); err != nil {
		return RetentionRemoteState{}, err
	}
	refs := [...]ArtifactRef{triplet.Manifest, triplet.Checksum, triplet.Archive}
	states := [3]RetentionRemoteObjectState{}
	for i, ref := range refs {
		state, err := c.inspectRemote(ctx, ref.Name, ref.Bytes, ref.SHA256)
		if err != nil {
			return RetentionRemoteState{}, err
		}
		states[i] = retentionRemoteObjectState(state)
	}
	return RetentionRemoteState{Manifest: states[0], Checksum: states[1], Archive: states[2]}, nil
}

// deleteRetentionObjectVerifiedMissing is deliberately unexported: deletion
// authority comes from a durable, validated intent and the reconciliation
// action, not merely from possession of an object name. It first proves that
// the current object is either exact or already missing, then confirms a 404
// after DELETE before the caller may persist progress.
func (c *WebDAVClient) deleteRetentionObjectVerifiedMissing(ctx context.Context, ref ArtifactRef) error {
	if ref.Bytes <= 0 || !validLowerSHA256(ref.SHA256) ||
		(!backupObjectName.MatchString(ref.Name) && !manifestObjectName.MatchString(ref.Name)) {
		return ErrRetentionIntentInvalid
	}
	state, err := c.inspectRemote(ctx, ref.Name, ref.Bytes, ref.SHA256)
	if err != nil {
		return err
	}
	switch state {
	case remoteMissing:
		return nil
	case remoteMismatch:
		return ErrRetentionRemoteMismatch
	case remoteExact:
		if err := c.delete(ctx, ref.Name); err != nil {
			return err
		}
	default:
		return ErrRetentionRemoteAmbiguous
	}
	state, err = c.inspectRemote(ctx, ref.Name, ref.Bytes, ref.SHA256)
	if err != nil {
		return err
	}
	if state != remoteMissing {
		return ErrRetentionDeleteUnconfirmed
	}
	return nil
}

func retentionRemoteObjectState(state remoteObjectState) RetentionRemoteObjectState {
	switch state {
	case remoteMissing:
		return RetentionRemoteMissing
	case remoteExact:
		return RetentionRemoteExact
	case remoteMismatch:
		return RetentionRemoteMismatch
	default:
		return RetentionRemoteUnknown
	}
}

func (c *WebDAVClient) move(ctx context.Context, source, destination string) (moveResult, error) {
	req, err := c.request(ctx, "MOVE", source, nil)
	if err != nil {
		return movePublished, err
	}
	destinationURL, err := c.objectURL(destination)
	if err != nil {
		return movePublished, err
	}
	req.Header.Set("Destination", destinationURL)
	req.Header.Set("Overwrite", "F")
	resp, err := c.http.Do(req)
	if err != nil {
		return movePublished, errors.New("发布 WebDAV 备份失败")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusNoContent {
		return movePublished, nil
	}
	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusPreconditionFailed {
		return moveConflict, nil
	}
	return movePublished, statusError("发布", resp.StatusCode)
}

func (c *WebDAVClient) delete(ctx context.Context, objectName string) error {
	req, err := c.request(ctx, http.MethodDelete, objectName, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return statusError("清理", resp.StatusCode)
}

func (c *WebDAVClient) request(ctx context.Context, method, objectName string, body io.Reader) (*http.Request, error) {
	remote, err := c.objectURL(objectName)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, remote, body)
	if err != nil {
		return nil, errors.New("创建 WebDAV 请求失败")
	}
	if c.target.username != "" || c.target.password != "" {
		req.SetBasicAuth(c.target.username, c.target.password)
	}
	return req, nil
}

func (c *WebDAVClient) objectURL(objectName string) (string, error) {
	if objectName != "" {
		base := objectName
		if at := strings.Index(base, ".partial."); at >= 0 {
			base = base[:at]
			if _, err := uuid.Parse(objectName[at+len(".partial."):]); err != nil {
				return "", errors.New("WebDAV 临时对象名无效")
			}
		}
		if !backupObjectName.MatchString(base) && !manifestObjectName.MatchString(base) {
			return "", errors.New("WebDAV 对象名无效")
		}
	}
	u := *c.target.Origin
	u.Path = c.target.BasePath
	if objectName != "" {
		u.Path += "/" + objectName
	}
	return u.String(), nil
}

func statusError(operation string, status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("WebDAV %s失败：认证失败", operation)
	case http.StatusNotFound:
		return fmt.Errorf("WebDAV %s失败：远端目录不存在", operation)
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return fmt.Errorf("WebDAV %s失败：服务器不支持所需方法", operation)
	case http.StatusLocked:
		return fmt.Errorf("WebDAV %s失败：远端对象已锁定", operation)
	case http.StatusTooManyRequests:
		return fmt.Errorf("WebDAV %s失败：请求过于频繁", operation)
	case http.StatusInsufficientStorage:
		return fmt.Errorf("WebDAV %s失败：远端空间不足", operation)
	default:
		return fmt.Errorf("WebDAV %s失败：HTTP %d", operation, status)
	}
}
