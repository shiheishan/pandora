package outbound

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"

	utls "github.com/metacubex/utls"
)

// uTLS 指纹。
//
// 中转到第三方机房、或链路上有做 TLS 指纹识别的中间设备时，Go 自带的
// ClientHello 是一眼可辨的特征——扩展顺序、密码套件集合都和任何真实浏览器
// 不同。uTLS 把 ClientHello 伪装成指定浏览器的样子。
//
// 只列 _Auto 系列：具体版本号（HelloChrome_133）会随浏览器更新而过时，
// 面板上让人选版本号只会产生一堆没人维护的旧指纹。
var fingerprints = map[string]utls.ClientHelloID{
	"chrome":     utls.HelloChrome_Auto,
	"firefox":    utls.HelloFirefox_Auto,
	"safari":     utls.HelloSafari_Auto,
	"ios":        utls.HelloIOS_Auto,
	"edge":       utls.HelloEdge_Auto,
	"360":        utls.Hello360_Auto,
	"qq":         utls.HelloQQ_Auto,
	"random":     utls.HelloRandomized,
	"randomized": utls.HelloRandomized,
}

// Fingerprints 返回支持的指纹名，供配置校验与错误提示。
func Fingerprints() []string {
	out := make([]string, 0, len(fingerprints))
	for k := range fingerprints {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func utlsClient(ctx context.Context, conn net.Conn, cfg *tls.Config, name string) (net.Conn, error) {
	id, ok := fingerprints[name]
	if !ok {
		return nil, fmt.Errorf("不认识的 TLS 指纹 %q（支持：%v）", name, Fingerprints())
	}
	// utls 有自己的 Config 类型，字段同名但不是同一个类型，逐个搬。
	uc := &utls.Config{
		ServerName:         cfg.ServerName,
		NextProtos:         cfg.NextProtos,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		RootCAs:            cfg.RootCAs,
		MinVersion:         cfg.MinVersion,
	}
	c := utls.UClient(conn, uc, id)
	if err := c.HandshakeContext(ctx); err != nil {
		return nil, fmt.Errorf("TLS 握手失败（指纹 %s）: %w", name, err)
	}
	return c, nil
}
