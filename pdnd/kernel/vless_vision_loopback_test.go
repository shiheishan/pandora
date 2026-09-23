package kernel

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/google/uuid"
	M "github.com/sagernet/sing/common/metadata"
)

// VLESS + Vision 的自环测试。
//
// 在此之前 Vision 只有两类验证：拿 state 层自己 pad 再自己 unpad，以及
// 外部 Mihomo 互操作。前者证明不了接入层把帧放对了位置，后者是黑盒——
// 失败时只看得到「连接被重置」，看不到哪一步错。这个测试补中间那层：
// 走真实的 vlessAdapter，客户端按协议手工拼字节，任一步错都能指出来。
func TestVLESSVisionLoopback(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()

	port := reserveTCPPort(t)
	id := uuid.New()
	spec := InboundSpec{Config: core.InboundConfig{
		Protocol: "vless", Listen: "127.0.0.1", Port: port,
		Raw: map[string]any{"flow": FlowVision},
	}}
	adapterValue, err := newVLESSAdapter(spec)
	if err != nil {
		t.Fatal(err)
	}
	adapter := adapterValue.(*vlessAdapter)
	if err := adapter.Validate(spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.AddUsers([]core.User{{ID: 9001, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var serverErrs []string
	if err := adapter.Start(ctx, spec, AdapterHooks{
		DataPlane: &vlessTestPlane{
			target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap(),
		},
		OnConnError: func(ce ConnError) {
			serverErrs = append(serverErrs, ce.Stage+": "+ce.Err.Error())
		},
	}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()

	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	// 1) 裸 VLESS 请求头。Vision 不包这一段——它是从请求头之后才开始的，
	//    这一点是拿真实客户端的字节确认过的。
	raw := id
	target := upstream.Addr().(*net.TCPAddr)
	addons := EncodeVLESSAddons(VLESSAddons{Flow: FlowVision})
	header := []byte{0}
	header = append(header, raw[:]...)
	header = append(header, byte(len(addons)))
	header = append(header, addons...)
	header = append(header, vlessTCP)
	var portBuf [2]byte
	binary.BigEndian.PutUint16(portBuf[:], uint16(target.Port))
	header = append(header, portBuf[:]...)
	header = append(header, 1) // IPv4
	header = append(header, target.IP.To4()...)
	if _, err := conn.Write(header); err != nil {
		t.Fatal(err)
	}

	// 2) 请求头之后的数据走 Vision 帧。客户端上行首帧带 UUID 前缀。
	//
	// 按小块写出，模拟真实 TCP 分段：真实客户端的一帧内容可以有几千
	// 字节，必然跨多个报文到达。一次性写完的话，服务端「边收边解、
	// 内容没齐就先转发已解出部分」这条路径根本不会被走到。
	clientOut := newVisionState([][]byte{raw[:]}).asClient(raw[:])
	payload := bytes.Repeat([]byte("pandora-vision-loopback-"), 60)
	var wire []byte
	for _, frame := range clientOut.buildPaddedFrames(payload) {
		wire = append(wire, frame...)
	}
	const chunk = 137 // 故意取个不对齐帧边界的大小
	for i := 0; i < len(wire); i += chunk {
		end := i + chunk
		if end > len(wire) {
			end = len(wire)
		}
		if _, err := conn.Write(wire[i:end]); err != nil {
			t.Fatal(err)
		}
	}

	// 3) 读 VLESS 响应头：[version][addonsLen]，两个字节，裸的。
	respHead := make([]byte, 2)
	if _, err := io.ReadFull(conn, respHead); err != nil {
		t.Fatalf("读响应头失败：%v（服务端：%v）", err, serverErrs)
	}
	if respHead[0] != vlessVersion {
		t.Fatalf("响应版本 = %d，期望 %d", respHead[0], vlessVersion)
	}
	if respHead[1] != 0 {
		t.Fatalf("响应 addons 长度 = %d，期望 0", respHead[1])
	}

	// 4) 响应头之后是服务端的 Vision 帧，同样带 UUID 前缀。
	clientIn := newVisionState([][]byte{raw[:]})
	got := make([]byte, 0, len(payload))
	buf := make([]byte, 16*1024)
	deadline := time.Now().Add(8 * time.Second)
	for len(got) < len(payload) && time.Now().Before(deadline) {
		n, readErr := conn.Read(buf)
		if n > 0 {
			got = append(got, clientIn.unpad(buf[:n])...)
		}
		if readErr != nil {
			break
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("回声不一致：拿到 %d 字节，期望 %d；服务端错误 %v",
			len(got), len(payload), serverErrs)
	}
}
