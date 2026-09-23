// naivecheck 是一个最小 Naive 客户端，验证 naive 入站真的在转发。
package main

import (
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/aegispanel/nodeagent/core/sing"
)

func main() {
	var server, uid, target, hostHdr string
	flag.StringVar(&server, "s", "127.0.0.1:28445", "节点地址")
	flag.StringVar(&uid, "u", "", "用户 UUID（同时作为用户名与密码）")
	flag.StringVar(&target, "t", "1.1.1.1:80", "目标地址")
	flag.StringVar(&hostHdr, "H", "one.one.one.one", "HTTP Host 头")
	flag.Parse()
	if uid == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -u")
		os.Exit(2)
	}

	// 自签证书场景下跳过校验：这里验证的是转发链路，不是证书链
	raw, err := tls.Dial("tcp", server, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{http2.NextProtoTLS},
	})
	if err != nil {
		fail("TLS 连接", err)
	}
	defer raw.Close()
	if proto := raw.ConnectionState().NegotiatedProtocol; proto != http2.NextProtoTLS {
		fail("ALPN 协商", fmt.Errorf("拿到 %q，期望 h2", proto))
	}

	tr := new(http2.Transport)
	cc, err := tr.NewClientConn(raw)
	if err != nil {
		fail("建立 h2 连接", err)
	}

	// CONNECT 的请求体就是上行通道，用管道把写入接进去
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodConnect, "https://"+server, pr)
	req.Host = target
	req.Header.Set("Padding", strings.Repeat("~", 48))
	req.Header.Set("Proxy-Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte(uid+":"+uid)))

	resp, err := cc.RoundTrip(req)
	if err != nil {
		fail("CONNECT 请求", err)
	}
	if resp.StatusCode != http.StatusOK {
		fail("CONNECT 响应", fmt.Errorf("HTTP %d", resp.StatusCode))
	}
	if resp.Header.Get("Padding") == "" {
		fail("CONNECT 响应", fmt.Errorf("缺少 Padding 头"))
	}

	conn := sing.NewNaiveClientConn(resp.Body, pw, nopFlusher{}, raw.RemoteAddr())
	// 兜底关闭上行管道，避免读侧无限等下去
	go func() {
		time.Sleep(15 * time.Second)
		_ = pw.Close()
	}()

	httpReq := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: aegis-naivecheck\r\n\r\n", hostHdr)
	if _, err := conn.Write([]byte(httpReq)); err != nil {
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

// nopFlusher 什么都不做：http2.Transport 的请求体是管道，
// 写进去就会被 h2 层自己排程发出，不需要额外 flush。
type nopFlusher struct{}

func (nopFlusher) Flush() {}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s 失败: %v\n", what, err)
	os.Exit(1)
}
