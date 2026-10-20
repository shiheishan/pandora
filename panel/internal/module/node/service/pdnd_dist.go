package service

// 节点端一键安装的分发：产物白名单、打开与校验和、安装脚本渲染。
// 面板自己发二进制（落地机只需要面板地址，不需要任何代码仓库凭据）；产物名只认白名单，公开端点不拼用户给的路径。

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

//go:embed pdnd_install.sh.tmpl
var pdndInstallTemplate string

// pdndOriginPlaceholder 是模板里的面板地址占位符。
const pdndOriginPlaceholder = "@@PANEL_ORIGIN@@"

// PdndArtifactAMD64 / PdndArtifactARM64 是仅有的两个可下载产物名。
const (
	PdndArtifactAMD64 = "pandora-native-linux-amd64"
	PdndArtifactARM64 = "pandora-native-linux-arm64"
)

// errPdndArtifactMissing 是产物不在磁盘上：消息不带目录路径。
var errPdndArtifactMissing = errs.Newf(errs.NotFound, "节点端安装包还没放到面板上，请联系管理员")

// pdndOriginSafe 是能原样写进 bash 双引号字符串的字符（没有 $、`、"、\、空白）。
var pdndOriginSafe = regexp.MustCompile(`^[A-Za-z0-9._~:/\[\]-]+$`)

type pdndDist struct {
	dir       string
	origin    string // 规范化后的面板地址；originErr 非空时为空
	originErr error
	digests   *pdndDigestCache
	files     *pdndFileCache // 发布清单与签名的原字节缓存，pdnd_manifest.go
}

// pdndDigestCache 按（大小、修改时间）缓存产物的 SHA-256：公开的校验和接口不能每个匿名请求都把几十 MB 读一遍。
// 发布时整份替换文件，大小或修改时间必变；万一都没变（同大小且保留了旧时间戳），最坏是旧摘要配新文件，安装端校验失败、不会装错。
type pdndDigestCache struct {
	mu      sync.Mutex
	entries map[string]pdndDigest
}

type pdndDigest struct {
	size    int64
	modTime time.Time
	hex     string
}

// UsePdndDist 只在启动装配时调用：dir 是二进制目录，publicBaseURL 是 config 的 public_base_url。
func (s *Service) UsePdndDist(dir, publicBaseURL string) {
	d := pdndDist{
		dir:     dir,
		digests: &pdndDigestCache{entries: map[string]pdndDigest{}},
		files:   &pdndFileCache{entries: map[string]pdndFile{}},
	}
	d.origin, d.originErr = pdndOrigin(publicBaseURL)
	s.pdnd = d
}

// PdndArtifactAllowed 产物白名单：只认上面两个确切的名字。
func PdndArtifactAllowed(name string) bool {
	switch name {
	case PdndArtifactAMD64, PdndArtifactARM64:
		return true
	}
	return false
}

// PdndArtifact 是打开的产物文件，调用方负责 Close。
type PdndArtifact struct {
	File    *os.File
	Size    int64
	ModTime time.Time
	SHA256  string // 小写 hex；handler 拿它做强 ETag，CDN 重新验证时二进制与校验和不会错配
}

// OpenPdndArtifact 打开白名单里的产物并带上（缓存的）摘要；文件读位置在开头。
func (s *Service) OpenPdndArtifact(ctx context.Context, name string) (*PdndArtifact, error) {
	if !PdndArtifactAllowed(name) {
		return nil, errs.New(errs.NotFound)
	}
	// 没装配目录时不能落到相对路径（进程工作目录）上去
	if s.pdnd.dir == "" {
		return nil, errPdndArtifactMissing
	}
	// 名字已过白名单；OpenInRoot 再保证打开的文件跑不出分发目录（含符号链接）
	f, err := os.OpenInRoot(s.pdnd.dir, name)
	if err != nil {
		// 不存在之外的原因（权限、指向目录外的符号链接）对外同样是 404，但要留日志给运维
		if !errors.Is(err, os.ErrNotExist) {
			plog.From(ctx).Warn("打开节点端安装包失败", zap.String("artifact", name), zap.Error(err))
		}
		return nil, errPdndArtifactMissing
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		_ = f.Close()
		if err != nil {
			plog.From(ctx).Warn("读取节点端安装包信息失败", zap.String("artifact", name), zap.Error(err))
		}
		return nil, errPdndArtifactMissing
	}
	sum, err := s.pdnd.digest(name, f, info)
	if err != nil {
		_ = f.Close()
		return nil, errs.Wrap(errs.Internal, err)
	}
	return &PdndArtifact{File: f, Size: info.Size(), ModTime: info.ModTime(), SHA256: sum}, nil
}

// digest 取产物摘要：（大小、修改时间）没变就用缓存，否则读一遍算并把读位置倒回开头。
func (d pdndDist) digest(name string, f *os.File, info os.FileInfo) (string, error) {
	if d.digests != nil {
		d.digests.mu.Lock()
		e, ok := d.digests.entries[name]
		d.digests.mu.Unlock()
		if ok && e.size == info.Size() && e.modTime.Equal(info.ModTime()) {
			return e.hex, nil
		}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if d.digests != nil {
		d.digests.mu.Lock()
		d.digests.entries[name] = pdndDigest{size: info.Size(), modTime: info.ModTime(), hex: sum}
		d.digests.mu.Unlock()
	}
	return sum, nil
}

// PdndChecksum 返回 "<64 位小写 hex>  <name>\n"（两个空格，与 sha256sum 输出一致）。
func (s *Service) PdndChecksum(ctx context.Context, name string) (string, error) {
	a, err := s.OpenPdndArtifact(ctx, name)
	if err != nil {
		return "", err
	}
	_ = a.File.Close()
	return fmt.Sprintf("%s  %s\n", a.SHA256, name), nil
}

// PdndInstallScript 返回渲染好的安装脚本。
func (s *Service) PdndInstallScript() (string, error) {
	if s.pdnd.originErr != nil {
		return "", errs.Wrap(errs.Unavailable, s.pdnd.originErr)
	}
	if s.pdnd.origin == "" {
		return "", errs.Wrap(errs.Unavailable, errors.New("节点端安装分发没有装配（UsePdndDist）"))
	}
	return strings.ReplaceAll(pdndInstallTemplate, pdndOriginPlaceholder, s.pdnd.origin), nil
}

// pdndOrigin 把 public_base_url 规范成写进安装脚本的面板地址：https（本机开发可用回环 http）、
// 无用户信息与查询片段、去掉末尾 /，且只含 pdndOriginSafe 里的字符。
func pdndOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("public_base_url 不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("public_base_url 不是合法 URL：%w", err)
	}
	if u.User != nil {
		return "", errors.New("public_base_url 不能包含用户名或密码")
	}
	if u.RawQuery != "" {
		return "", errors.New("public_base_url 不能包含查询参数")
	}
	if u.Fragment != "" {
		return "", errors.New("public_base_url 不能包含片段")
	}
	if u.ForceQuery {
		return "", errors.New("public_base_url 格式无效")
	}
	if u.Opaque != "" {
		return "", errors.New("public_base_url 不能使用不透明 URL")
	}
	if u.Host == "" {
		return "", errors.New("public_base_url 缺少主机")
	}
	scheme := strings.ToLower(u.Scheme)
	host := u.Hostname()
	switch scheme {
	case "https":
	case "http":
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return "", errors.New("非本机开发时 public_base_url 必须使用 https")
		}
	default:
		return "", errors.New("public_base_url 的协议必须是 https，或本机开发用的 http")
	}
	out := scheme + "://" + u.Host + strings.TrimSuffix(u.EscapedPath(), "/")
	if !pdndOriginSafe.MatchString(out) {
		return "", errors.New("public_base_url 含有不能写进安装脚本的字符")
	}
	return out, nil
}
