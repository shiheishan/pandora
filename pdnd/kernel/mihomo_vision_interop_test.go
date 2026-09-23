//go:build interop_mihomo

package kernel

// 用官方 Mihomo 二进制当独立客户端，验证 VLESS + REALITY + XTLS Vision
// 这条组合能真的跑通。
//
// 为什么非要外部客户端：Vision 是 wire format，自己写的测试只能证明
// 「我编的和我解的一致」，证明不了「别人按上游规范编的，我能解」。这两件
// 事差一个字节就是天壤之别，而症状是握手通过、数据全乱——最难查的那类。
//
// 同目录的 mihomo_external_interop_test.go 测的是 REALITY+XHTTP/H3，
// 那个组合 Mihomo 自己不支持（会退化成 dial tcp 一个 UDP 端口）。这里
// 换成它确实支持的组合，才谈得上是互操作验证。
//
// 和那个文件一样：opt-in 构建标签 + MIHOMO_BIN 环境变量，二进制自己下、
// 自己核对哈希，永远不会变成 Pandora NativeCore 的生产依赖。
// # 排查结论（2026-08-06，Mihomo v1.19.10）
//
// 这条链路一度失败，症状极具误导性：REALITY 认证成功、Vision 首帧被
// 正确识别，然后连接被 reset，服务端一行错误都没有，客户端也不打日志。
// 真正的根因有两个，都在 REALITY 的握手尾巴上，跟 Vision 一点关系都
// 没有——Vision 只是唯一会踩到它的组合。
//
// 一、decoy 的记录装不下合成票据时，不能硬塞
//
//	REALITY: synthesize server handshake: payload[0]: 4, padding: -144
//
// REALITY 把自己的握手记录填充到与 decoy 站点等长，而 decoy 的
// NewSessionTicket 记录比合成的小 144 字节。上游 xtls/reality 写法相同，
// 不是抄错，是 decoy 记录过小这个场景没被覆盖。装不下就跳过——会话恢复
// 对 REALITY 数据连接不是必需的。
//
// 二、绝不能发标准的真票据（这条是最终阻塞项）
//
// conn.go 里把票据伪装成 application_data 的那段，靠改写 record[5] 和
// record[6] 两个字节做到——它假定 payload 只有一字节，也就是 REALITY
// 自己那个合成票据。Go TLS 标准路径另外发的真票据有 122 字节，同样两个
// 字节改下去只是改坏内容，内层类型仍是 handshake，对端收到的是一条首
// 字节非法的握手消息，回一个 unexpected_message 就走人。
//
// 只在 Mihomo 上暴露，是因为它的 REALITY 客户端设了
// SessionTicketsDisabled；xray 客户端不禁用，所以一直没测出来。修法是
// 服务端 config 置 SessionTicketsDisabled（关掉真票据），而合成票据改为
// 只看客户端的 pskModes（RFC 8446 4.6.1），伪装记录得以保留。
//
// # 方法上的教训
//
// 这个 bug 前后猜错五次，每次都言之成理，每次都被新证据推翻。共同点是
// 从**间接观察**推断协议行为：拿包在 net.Conn 外面的字节计数去猜 TLS
// 记录边界，拿实验性补丁下的结果去反推正常行为。转折点是三件事：
//
//   - TestVLESSVisionLoopback：不依赖外部二进制、能在 Go 里调试的自环，
//     证明 Vision 服务端自洽（含 TCP 分段）
//   - 同目录 mihomo_vision_tls_interop_test.go：把 REALITY 换成普通 TLS
//     其余不变，一刀把「Vision 的锅」和「REALITY 的锅」切开
//   - 给记录层写方向补 trace（读方向早就有）。发出去的记录类型序列一
//     打出来，那条多余的 typ=22 len=122 立刻就现形了
//
// 结论：查互操作问题，要在出问题的那一层看真实字节，别在外面数数。
//
// 留着这个测试而不是修完就删：它把「REALITY 没和第三方客户端验证过」
// 从文档里的一句话变成了可执行的检查。带 build tag，默认不参与 CI。

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

func TestExternalMihomoVLESSRealityVisionInterop(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("MIHOMO_BIN"))
	if bin == "" {
		t.Skip("设置 MIHOMO_BIN 指向自行下载并核对过哈希的 Mihomo 二进制")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("MIHOMO_BIN: %v", err)
	}
	requireExternalBinarySHA256(t, bin, "MIHOMO_SHA256")

	// 回声服务，充当「目标网站」。
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	// REALITY 借用的真实 TLS 站点。
	decoyRaw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer decoyRaw.Close()
	decoyTLS := tls.NewListener(decoyRaw, testXHTTPServerTLSConfig(t))
	defer decoyTLS.Close()
	go func() {
		for {
			conn, acceptErr := decoyTLS.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const shortID = "0908070605040302"
	serverPort := reserveTCPPort(t)
	mixedPort := reserveTCPPort(t)
	u := uuid.New()

	spec := InboundSpec{Config: core.InboundConfig{
		Tag: "external-mihomo-vision", Protocol: "vless",
		Listen: "127.0.0.1", Port: serverPort,
		Raw: map[string]any{
			"security": "reality",
			"dest":     decoyRaw.Addr().String(), "server_names": []string{"example.com"},
			"private_key": base64.RawURLEncoding.EncodeToString(key.Bytes()),
			"short_ids":   []string{shortID},
			"flow":        FlowVision,
		},
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 7302, UUID: u.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 把服务端侧的失败原因抓出来。没有这个，客户端只看得到 EOF，
	// 而 EOF 可以是握手不过、鉴权失败、协议解析错——方向完全不同。
	var serverErrs []string
	var serverErrMu sync.Mutex
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{
			target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap(),
		},
		OnConnError: func(ce ConnError) {
			serverErrMu.Lock()
			serverErrs = append(serverErrs,
				fmt.Sprintf("[%s] %v", ce.Stage, ce.Err))
			serverErrMu.Unlock()
		},
	}); err != nil {
		t.Fatal(err)
	}
	dumpServerErrs := func() string {
		serverErrMu.Lock()
		defer serverErrMu.Unlock()
		if len(serverErrs) == 0 {
			return "(服务端没报错)"
		}
		return strings.Join(serverErrs, "\n  ")
	}
	defer adapter.Close()

	// flow: xtls-rprx-vision 是这个测试的重点。
	config := fmt.Sprintf(`mixed-port: %d
allow-lan: false
mode: rule
log-level: debug
proxies:
  - name: pandora
    type: vless
    server: 127.0.0.1
    port: %d
    uuid: "%s"
    udp: false
    tls: true
    flow: xtls-rprx-vision
    servername: example.com
    client-fingerprint: chrome
    reality-opts:
      public-key: "%s"
      short-id: "%s"
    network: tcp
proxy-groups:
  - name: pandora-group
    type: select
    proxies:
      - pandora
rules:
  - MATCH,pandora-group
`, mixedPort, serverPort, u.String(),
		base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), shortID)

	configPath := filepath.Join(t.TempDir(), "mihomo.yaml")
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	clientCtx, clientCancel := context.WithCancel(context.Background())
	defer clientCancel()
	cmd := exec.CommandContext(clientCtx, bin, "-f", configPath)
	logFile, err := os.Create(filepath.Join(t.TempDir(), "mihomo.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		clientCancel()
		_ = cmd.Wait()
	}()

	var conn net.Conn
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.DialTimeout("tcp",
			net.JoinHostPort("127.0.0.1", itoa(mixedPort)), 250*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("连接 Mihomo 混合端口失败：%v；日志=%s", err, contents)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	// 走 SOCKS5 让 Mihomo 把流量经我们的节点转出去。
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	methodReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodReply); err != nil {
		t.Fatal(err)
	}
	if methodReply[0] != 5 || methodReply[1] != 0 {
		t.Fatalf("Mihomo SOCKS 方法协商回包=%v", methodReply)
	}
	destination := upstream.Addr().(*net.TCPAddr)
	request := []byte{5, 1, 0, 1}
	request = append(request, destination.IP.To4()...)
	request = append(request, byte(destination.Port>>8), byte(destination.Port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0 {
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("Mihomo SOCKS 连接回包=%v\n服务端错误:\n  %s\n客户端日志=%s",
			reply, dumpServerErrs(), contents)
	}

	// 发一段够长的数据：Vision 的填充分支和分片逻辑要被真的走到，
	// 几十字节的短包只能覆盖最平凡的那条路径。
	payload := []byte(strings.Repeat("pandora-vision-interop-", 400))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		// Mihomo 把连接 reset 掉的那一刻，它自己的错误日志还没落盘。
		// 立刻读文件只能拿到 reset 之前的部分——也就是最关键的那条
		// 缺席。等一下再读。
		time.Sleep(500 * time.Millisecond)
		contents, _ := os.ReadFile(logFile.Name())
		t.Fatalf("读回声失败：%v\n服务端错误:\n  %s\n客户端日志=%s",
			err, dumpServerErrs(), contents)
	}
	if string(got) != string(payload) {
		t.Fatalf("回声不一致：收到 %d 字节，期望 %d", len(got), len(payload))
	}
}
