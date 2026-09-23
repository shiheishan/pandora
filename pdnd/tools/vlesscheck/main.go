// vlesscheck 是一个最小 VLESS 客户端，用来验证节点端真的在转发流量。
//
// 存在的理由：端口在监听、用户已下发，都不等于「能用」。
// 只有一条真实连接从客户端穿过内核到达目标站点，
// 并且流量被记到正确的用户名下，这条链路才算通了。
package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
)

func main() {
	var server, uid, target, host, bindAddr string
	flag.StringVar(&server, "s", "127.0.0.1:28443", "节点地址")
	flag.StringVar(&uid, "u", "", "用户 UUID")
	flag.StringVar(&target, "t", "93.184.215.14:80", "目标地址")
	flag.StringVar(&host, "H", "example.com", "HTTP Host 头")
	// 绑定源地址，用来模拟不同设备。
	// Linux 把整个 127.0.0.0/8 都当本地地址，所以 127.0.0.2、127.0.0.3
	// 这些不需要额外配网卡就能直接绑，是本机造多个来源最省事的办法。
	flag.StringVar(&bindAddr, "bind", "", "绑定的源 IP（用于模拟不同设备）")
	flag.Parse()

	if uid == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -u")
		os.Exit(2)
	}

	d := net.Dialer{Timeout: 10 * time.Second}
	if bindAddr != "" {
		d.LocalAddr = &net.TCPAddr{IP: net.ParseIP(bindAddr)}
	}
	raw, err := d.Dial("tcp", server)
	if err != nil {
		fail("连接节点", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(20 * time.Second))

	client, err := vless.NewClient(uid, "", logger.NOP())
	if err != nil {
		fail("创建客户端", err)
	}
	conn, err := client.DialConn(raw, M.ParseSocksaddr(target))
	if err != nil {
		fail("VLESS 握手", err)
	}

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: aegis-vlesscheck\r\n\r\n", host)
	if _, err := io.WriteString(conn, req); err != nil {
		fail("发送请求", err)
	}

	body, err := io.ReadAll(io.LimitReader(conn, 64<<10))
	// EOF 之前读到的内容才是重点：超时也可能带回部分数据，
	// 只要有响应就说明链路是通的
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
