package config

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
)

//------------------------------------------------------------------------------
// 节点证书签发：非生产环境的 ACME 目录覆盖（pebble 等测试 CA）
//------------------------------------------------------------------------------

// ACME 是节点证书签发的部署侧覆盖项。只给开发与测试环境把签发打到本地测试 CA（pebble）；
// 生产环境设了就拒绝启动——签发目录只能由后台的 ACME 设置决定，一个漏删的环境变量
// 不能把真证书的签发悄悄换成测试 CA。
type ACME struct {
	// DirectoryOverride 非空时所有签发都打到这个 ACME 目录（证书版本记为 ca=custom）
	DirectoryOverride string
	// TrustedRoots 是信任覆盖目录 HTTPS 证书用的根（AEGIS_ACME_TRUSTED_ROOTS 指向的 PEM 文件）；
	// 为空时用系统根
	TrustedRoots *x509.CertPool
	// LibraryEnv 是进程环境里设了的、lego 会自己去读的调试与行为开关（LEGO_DEBUG_* 等）。面板不用它们，
	// 设了会改变签发行为或把 DNS 服务商 API 的请求（可能含令牌）打进日志，aegis-admin 启动时告警。
	LibraryEnv []string
}

// legoEnvExact 是 lego 在面板用到的包里自己读的、不带 LEGO_DEBUG_ 前缀的开关。
var legoEnvExact = []string{"LEGO_DISABLE_CNAME_SUPPORT", "LEGO_EXPERIMENTAL_DNS_TCP_ONLY"}

// lookupLibraryEnv 列出设了的 lego 开关名（只回名字，不回值）。
func lookupLibraryEnv(environ []string) []string {
	var out []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "LEGO_DEBUG_") || slices.Contains(legoEnvExact, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

const (
	acmeDirectoryOverrideEnv = "AEGIS_ACME_DIRECTORY_OVERRIDE"
	acmeTrustedRootsEnv      = "AEGIS_ACME_TRUSTED_ROOTS"
)

func loadACME(production bool) (ACME, error) {
	dir := trimmedEnv(acmeDirectoryOverrideEnv, "")
	roots := trimmedEnv(acmeTrustedRootsEnv, "")
	libEnv := lookupLibraryEnv(os.Environ())
	if dir == "" && roots == "" {
		return ACME{LibraryEnv: libEnv}, nil
	}
	if production {
		return ACME{}, fmt.Errorf("%s / %s 只用于开发与测试环境，生产环境不能设置", acmeDirectoryOverrideEnv, acmeTrustedRootsEnv)
	}
	if dir == "" {
		return ACME{}, fmt.Errorf("设置了 %s 却没有 %s", acmeTrustedRootsEnv, acmeDirectoryOverrideEnv)
	}
	u, err := url.Parse(dir)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ACME{}, fmt.Errorf("%s=%q 必须是 https:// 开头的 ACME 目录地址", acmeDirectoryOverrideEnv, dir)
	}
	out := ACME{DirectoryOverride: dir, LibraryEnv: libEnv}
	if roots != "" {
		pem, err := os.ReadFile(roots)
		if err != nil {
			return ACME{}, fmt.Errorf("读取 %s: %w", acmeTrustedRootsEnv, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return ACME{}, errors.New(acmeTrustedRootsEnv + " 里没有可用的 PEM 证书")
		}
		out.TrustedRoots = pool
	}
	return out, nil
}
