// mierucheck 是一个最小 mieru 客户端，用来验证 mieru 入站真的在转发。
// 与 vlesscheck 同理：监听不等于能用，只有真实流量走通才算数。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/enfein/mieru/v3/apis/client"
	"github.com/enfein/mieru/v3/apis/model"
	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
	"google.golang.org/protobuf/proto"
)

func main() {
	var host, uid, target, hostHdr string
	var port int
	flag.StringVar(&host, "s", "127.0.0.1", "节点 IP")
	flag.IntVar(&port, "p", 28444, "节点端口")
	flag.StringVar(&uid, "u", "", "用户 UUID（同时作为用户名与密码）")
	flag.StringVar(&target, "t", "1.1.1.1:80", "目标地址")
	flag.StringVar(&hostHdr, "H", "one.one.one.one", "HTTP Host 头")
	flag.Parse()

	if uid == "" {
		fmt.Fprintln(os.Stderr, "必须指定 -u")
		os.Exit(2)
	}

	c := client.NewClient()
	if err := c.Store(&client.ClientConfig{
		Profile: &appctlpb.ClientProfile{
			ProfileName: proto.String("check"),
			User: &appctlpb.User{
				Name:     proto.String(uid),
				Password: proto.String(uid),
			},
			Servers: []*appctlpb.ServerEndpoint{{
				IpAddress: proto.String(host),
				PortBindings: []*appctlpb.PortBinding{{
					Port:     proto.Int32(int32(port)),
					Protocol: appctlpb.TransportProtocol_TCP.Enum(),
				}},
			}},
		},
	}); err != nil {
		fail("写入客户端配置", err)
	}
	if err := c.Start(); err != nil {
		fail("启动客户端", err)
	}
	defer c.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var addr model.AddrSpec
	if err := addr.From(target); err != nil {
		fail("解析目标地址", err)
	}
	conn, err := c.DialContext(ctx, model.NetAddrSpec{AddrSpec: addr, Net: "tcp"})
	if err != nil {
		fail("建立代理连接", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	req := fmt.Sprintf("GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: aegis-mierucheck\r\n\r\n", hostHdr)
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
