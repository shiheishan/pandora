// [INPUT]: 依赖 sing-shadowtls v3 客户端与 sing-shadowsocks，命令行给出节点地址、-p ShadowTLS 密码、-u 内层密码
// [OUTPUT]: 手工运维工具：连上节点、解开外壳后经内层发一次 HTTP 请求，证明转发真的通了
// [POS]: pdnd/tools 的节点端连通性探针，不参与构建产物；两个密码都没有默认值，缺一个就退出
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// shadowtlscheck 是一个最小 ShadowTLS + Shadowsocks 客户端，
// 验证外壳解开之后内层真的在转发。
//
// 只测监听和证书伪装是不够的：那两项即使内层完全没接通也照样成立 ——
// 握手是转发给真实网站完成的，所以「看起来像那个网站」本来就不依赖代理能用。
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowtls"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

func main() {
	var server, password, sni, uid, method, target, hostHdr string
	flag.StringVar(&server, "s", "127.0.0.1:28447", "节点地址")
	flag.StringVar(&password, "p", "", "ShadowTLS 外层密码（节点配置里的 password）")
	flag.StringVar(&sni, "sni", "www.microsoft.com", "伪装的握手目标")
	flag.StringVar(&uid, "u", "", "用户 UUID（内层 Shadowsocks 密码）")
	flag.StringVar(&method, "m", "aes-256-gcm", "内层加密方式")
	flag.StringVar(&target, "t", "1.1.1.1:80", "目标地址")
	flag.StringVar(&hostHdr, "H", "one.one.one.one", "HTTP Host 头")
	flag.Parse()
	// 两个密码都不给默认值：工具随仓库公开，默认值只能是某个真实节点的密码
	if uid == "" || password == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -u 和 -p")
		os.Exit(2)
	}

	client, err := shadowtls.NewClient(shadowtls.ClientConfig{
		Version:  3,
		Password: password,
		Server:   M.ParseSocksaddr(server),
		Dialer:   N.SystemDialer,
		// 证书校验交给真实网站：这里连的本来就是被伪装的那个站点，
		// 用它自己的证书链验证是成立的
		TLSHandshake: shadowtls.DefaultTLSHandshakeFunc(password, &tls.Config{ServerName: sni}),
		// 不设会在握手路径上空指针崩溃
		Logger: logger.NOP(),
	})
	if err != nil {
		fail("创建 ShadowTLS 客户端", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	outer, err := client.DialContext(ctx)
	if err != nil {
		fail("ShadowTLS 握手", err)
	}
	defer outer.Close()
	_ = outer.SetDeadline(time.Now().Add(25 * time.Second))

	ssMethod, err := shadowaead.New(method, nil, uid)
	if err != nil {
		fail("构造内层加密", err)
	}
	conn := ssMethod.DialEarlyConn(outer, M.ParseSocksaddr(target))

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: aegis-shadowtlscheck\r\n\r\n", hostHdr)
	if _, err := io.WriteString(conn, req); err != nil {
		fail("发送请求", err)
	}

	body, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	if len(body) == 0 {
		fail("读取响应", err)
	}
	head := body
	if len(head) > 120 {
		head = head[:120]
	}
	fmt.Printf("收到 %d 字节\n%s\n", len(body), head)
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s 失败: %v\n", what, err)
	os.Exit(1)
}
