package service

// 节点端一键安装的分发：产物白名单、打开与校验和、安装脚本渲染。
// 面板自己发二进制（落地机只需要面板地址，不需要任何代码仓库凭据）；产物名只认白名单，公开端点不拼用户给的路径。

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
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
}

// UsePdndDist 只在启动装配时调用：dir 是二进制目录，publicBaseURL 是 config 的 public_base_url。
func (s *Service) UsePdndDist(dir, publicBaseURL string) {
	d := pdndDist{dir: dir}
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
}

// OpenPdndArtifact 打开白名单里的产物。
func (s *Service) OpenPdndArtifact(name string) (*PdndArtifact, error) {
	if !PdndArtifactAllowed(name) {
		return nil, errs.New(errs.NotFound)
	}
	// 没装配目录时不能落到相对路径（进程工作目录）上去
	if s.pdnd.dir == "" {
		return nil, errPdndArtifactMissing
	}
	path := filepath.Join(s.pdnd.dir, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, errPdndArtifactMissing
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, errPdndArtifactMissing
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, errPdndArtifactMissing
	}
	return &PdndArtifact{File: f, Size: info.Size(), ModTime: info.ModTime()}, nil
}

// PdndChecksum 返回 "<64 位小写 hex>  <name>\n"（两个空格，与 sha256sum 输出一致）。
func (s *Service) PdndChecksum(name string) (string, error) {
	a, err := s.OpenPdndArtifact(name)
	if err != nil {
		return "", err
	}
	defer a.File.Close()
	h := sha256.New()
	if _, err := io.Copy(h, a.File); err != nil {
		return "", errs.Wrap(errs.Internal, err)
	}
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(h.Sum(nil)), name), nil
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
