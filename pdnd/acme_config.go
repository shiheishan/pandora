package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aegispanel/nodeagent/certs"
)

// acmeConfig 是自动申请入站证书（Let's Encrypt）的可选设置，见 docs/node-certificates.md。
// 生产上整段不写：用 Let's Encrypt 生产环境、本机 80 端口做 HTTP-01 验证、系统根证书。
// 这几项只给验收与测试（pebble、LE staging）改。
type acmeConfig struct {
	// DirectoryURL 是 ACME 目录地址（https）；空 = Let's Encrypt 生产环境。
	DirectoryURL string `json:"directory_url"`
	// HTTPPort 是 HTTP-01 验证在本机监听的端口；0 = 80。CA 总是连公网 80，只有 80 前面另有转发时才改。
	HTTPPort int `json:"http_port"`
	// CAFile 是额外信任的 CA 证书（PEM），连测试 CA 时用；空 = 只信系统根。
	CAFile string `json:"ca_file"`
}

// validate 只查形状；ca_file 读不读得了在 newCertManager 里报。
func (a acmeConfig) validate() error {
	if s := strings.TrimSpace(a.DirectoryURL); s != "" {
		u, err := url.Parse(s)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("acme.directory_url 必须是 https:// 开头的完整地址")
		}
	}
	if a.HTTPPort < 0 || a.HTTPPort > 65535 {
		return fmt.Errorf("acme.http_port 超出范围：%d", a.HTTPPort)
	}
	return nil
}

// acmeDir 是自动申请证书与 ACME 账户的落盘目录：<cache_dir>/acme（生产 /var/lib/pandora-native/acme）。
func (c *config) acmeDir() string { return filepath.Join(c.stateDir(), "acme") }

// newCertManager 按配置建证书管理（还没联网：账户等第一次要下单时才注册）。
func newCertManager(c *config, log *slog.Logger) (*certs.Manager, error) {
	opts := certs.Options{
		Dir:          c.acmeDir(),
		DirectoryURL: strings.TrimSpace(c.ACME.DirectoryURL),
		HTTPPort:     c.ACME.HTTPPort,
		Logger:       log,
	}
	if path := strings.TrimSpace(c.ACME.CAFile); path != "" {
		pemBytes, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("acme.ca_file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("acme.ca_file: %s 里没有可用的 PEM 证书", path)
		}
		opts.RootCAs = pool
	}
	return certs.New(opts)
}
