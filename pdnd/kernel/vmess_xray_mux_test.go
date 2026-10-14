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
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/vmess/encoding"
)

// muxCoolNewTCPFrame 是 Mux.Cool 的 New 帧（照 Xray common/mux/frame.go）：
// [元数据长度 2][会话 id 2][状态 1][选项 1][网络 1][端口 2][地址类型 1][IPv4 4]
// 选项带 data 时后跟 [数据长度 2][数据]。
func muxCoolNewTCPFrame(id uint16, port int, data []byte) []byte {
	meta := binary.BigEndian.AppendUint16(nil, id)
	meta = append(meta, 0x01, 0x01, 0x01) // New、带 data、TCP
	meta = binary.BigEndian.AppendUint16(meta, uint16(port))
	meta = append(meta, 0x01, 127, 0, 0, 1)
	frame := binary.BigEndian.AppendUint16(nil, uint16(len(meta)))
	frame = append(frame, meta...)
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(data)))
	return append(frame, data...)
}

// readMuxCoolData 读回 Mux.Cool 的帧，直到攒够 want 字节的 data。
func readMuxCoolData(t *testing.T, r io.Reader, want int) []byte {
	t.Helper()
	var out []byte
	for len(out) < want {
		var head [2]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			t.Fatalf("读 mux 帧头（已收 %d/%d）: %v", len(out), want, err)
		}
		meta := make([]byte, binary.BigEndian.Uint16(head[:]))
		if _, err := io.ReadFull(r, meta); err != nil || len(meta) < 4 {
			t.Fatalf("读 mux 元数据: %v", err)
		}
		if meta[3]&0x01 == 0 {
			continue
		}
		if _, err := io.ReadFull(r, head[:]); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, binary.BigEndian.Uint16(head[:]))
		if _, err := io.ReadFull(r, data); err != nil {
			t.Fatal(err)
		}
		out = append(out, data...)
	}
	return out
}

// TestVMessXrayMuxTCP（审查 M3）：Xray 的 mux 客户端第一帧就是「New + 数据」。原先
// 主循环在起了转发协程之后又写一遍 stream.dest，与协程里读 dest 构成数据竞争
// （-race 必现）；这条路径是 VMess 对 Xray 修好之后才第一次被真实客户端走到。
func TestVMessXrayMuxTCP(t *testing.T) {
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
	port := reserveTCPPort(t)
	id := uuid.New()
	adapter := &vmessAdapter{users: make(map[string]vmessUser), active: make(map[net.Conn]struct{})}
	spec := InboundSpec{Config: core.InboundConfig{Protocol: "vmess", Listen: "127.0.0.1", Port: port, Raw: map[string]any{}}}
	if err := adapter.AddUsers([]core.User{{ID: 7404, UUID: id.String()}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := adapter.Start(ctx, spec, AdapterHooks{DataPlane: &vlessTestPlane{target: M.SocksaddrFromNet(upstream.Addr().(*net.TCPAddr)).Unwrap()}}); err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	for round := 0; round < 20; round++ {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		request := xrayVMessRequest(t, id, protocol.SecurityType_AUTO, protocol.RequestCommandMux, 0)
		session := encoding.NewClientSession(context.Background(), 0)
		if err := session.EncodeRequestHeader(request, conn); err != nil {
			t.Fatal(err)
		}
		bodyWriter, err := session.EncodeRequestBody(request, conn)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("xray-mux-tcp-" + itoa(round))
		if err := bodyWriter.WriteMultiBuffer(buf.MergeBytes(nil, muxCoolNewTCPFrame(1, upstream.Addr().(*net.TCPAddr).Port, payload))); err != nil {
			t.Fatal(err)
		}
		reader := &buf.BufferedReader{Reader: buf.NewReader(conn)}
		if _, err := session.DecodeResponseHeader(reader); err != nil {
			t.Fatal(err)
		}
		bodyReader, err := session.DecodeResponseBody(request, reader)
		if err != nil {
			t.Fatal(err)
		}
		if got := readMuxCoolData(t, &buf.BufferedReader{Reader: bodyReader}, len(payload)); !bytes.Equal(got, payload) {
			t.Fatalf("mux 回显 = %q", got)
		}
		_ = conn.Close()
	}
}
