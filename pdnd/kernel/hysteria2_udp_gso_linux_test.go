//go:build linux

package kernel

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/outbound"
	M "github.com/sagernet/sing/common/metadata"
	"golang.org/x/net/ipv4"
)

// gsoPayload 造一个长度为 size、内容可辨认的包。
func gsoPayload(index, size int) []byte {
	payload := bytes.Repeat([]byte{byte('a' + index%26)}, size)
	copy(payload, fmt.Sprintf("%03d", index))
	return payload
}

// readDatagrams 从 sink 读 n 个包（内核按 GSO 段长切开后的边界）。
func readDatagrams(t *testing.T, sink net.PacketConn, n int) [][]byte {
	t.Helper()
	out := make([][]byte, 0, n)
	buffer := make([]byte, 65536)
	for len(out) < n {
		_ = sink.SetReadDeadline(time.Now().Add(2 * time.Second))
		k, _, err := sink.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("只收到 %d/%d 个包：%v", len(out), n, err)
		}
		out = append(out, append([]byte(nil), buffer[:k]...))
	}
	return out
}

// 上行合包：同目标的连续等长包合成 GSO 消息，最后一段可更短；换目标、换更长
// 的包、超过段长上限都另起一条。对端收到的包边界与内容与逐包发送完全相同。
func TestHy2BatchWriterGSOKeepsDatagramBoundaries(t *testing.T) {
	sinks := make([]net.PacketConn, 2)
	for i := range sinks {
		sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer sink.Close()
		_ = sink.(*net.UDPConn).SetReadBuffer(4 << 20)
		sinks[i] = sink
	}
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	upstream := hy2LoadRawPacketConn{conn}
	writer := newHy2BatchWriter(upstream, newHy2UDPUpstream(upstream).batch)
	if writer.batch == nil || !writer.gso {
		t.Fatal("Linux 裸 socket 应走批量 + GSO")
	}
	// 目标与长度序列：0..5 同目标 1200 字节（最后一个 700 收尾），6 换目标，
	// 7..8 回到第一个目标但更长（2000 超过段长上限，单发），9..12 等长 1200。
	type item struct{ sink, size int }
	plan := []item{{0, 1200}, {0, 1200}, {0, 1200}, {0, 1200}, {0, 1200}, {0, 700}, {1, 1200}, {0, 2000}, {0, 1300}, {0, 1200}, {0, 1200}, {0, 1200}, {0, 1200}}
	resolver := &hy2UDPResolver{ctx: context.Background()}
	writer.reset()
	var want int64
	perSink := map[int][][]byte{}
	for i, p := range plan {
		payload := gsoPayload(i, p.size)
		want += int64(len(payload))
		perSink[p.sink] = append(perSink[p.sink], payload)
		writer.add(resolver, payload, M.SocksaddrFromNet(sinks[p.sink].LocalAddr()), M.Socksaddr{})
	}
	written, err := writer.flush()
	if err != nil || written != want {
		t.Fatalf("写出 %d/%d err=%v", written, want, err)
	}
	if !writer.gso {
		t.Fatal("本机回环不应让 GSO 失败回退")
	}
	// 合出的消息：[0..5] [6] [7] [8] [9..12]
	if got := len(writer.messages); got != 5 {
		t.Fatalf("消息数 %d，期望 5（同目标等长连续包合成一条）", got)
	}
	for index, wantPayloads := range perSink {
		got := readDatagrams(t, sinks[index], len(wantPayloads))
		for i := range wantPayloads {
			if !bytes.Equal(got[i], wantPayloads[i]) {
				t.Fatalf("sink%d 第 %d 包长 %d 内容不符（期望长 %d）", index, i, len(got[i]), len(wantPayloads[i]))
			}
		}
	}
}

// 默认拦私网：GSO 消息经出站的带检查批量接口发出，私网目标整条丢弃、照常计为
// 已发出；放开后同一条通道照常送达，包边界不变。
func TestHy2GuardedBatchGSO(t *testing.T) {
	upstream := guardedUpstream(t)
	u := newHy2UDPUpstream(upstream)
	if u.batch == nil {
		t.Fatal("默认配置应走带检查的批量通道")
	}
	sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	target := M.SocksaddrFromNet(sink.LocalAddr())
	send := func() (*hy2BatchWriter, int64) {
		writer := newHy2BatchWriter(upstream, u.batch)
		resolver := &hy2UDPResolver{ctx: context.Background()}
		writer.reset()
		var want int64
		for i := 0; i < 8; i++ {
			payload := gsoPayload(i, 1000)
			want += int64(len(payload))
			writer.add(resolver, payload, target, M.Socksaddr{})
		}
		written, err := writer.flush()
		if err != nil || written != want {
			t.Fatalf("写出 %d/%d err=%v", written, want, err)
		}
		return writer, written
	}
	writer, _ := send()
	if len(writer.messages) != 1 || len(writer.messages[0].OOB) == 0 {
		t.Fatalf("8 个同目标等长包应合成 1 条 GSO 消息，实际 %d 条", len(writer.messages))
	}
	_ = sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, _, err := sink.ReadFrom(make([]byte, 2048)); err == nil {
		t.Fatal("私网目标不该收到包")
	}
	outbound.SetBlockPrivateDestinations(false)
	defer outbound.SetBlockPrivateDestinations(true)
	send()
	got := readDatagrams(t, sink, 8)
	for i := range got {
		if !bytes.Equal(got[i], gsoPayload(i, 1000)) {
			t.Fatalf("第 %d 包不符", i)
		}
	}
}

// rejectGSOBatch 模拟不支持 GSO 的内核或网卡：带控制信息的消息一律 EIO。
type rejectGSOBatch struct{ hy2UDPBatchIO }

func (b rejectGSOBatch) WriteBatch(ms []ipv4.Message, flags int) (int, error) {
	for i := range ms {
		if len(ms[i].OOB) > 0 {
			if i == 0 {
				return 0, os.NewSyscallError("sendmmsg", syscall.EIO)
			}
			n, err := b.hy2UDPBatchIO.WriteBatch(ms[:i], flags)
			return n, err
		}
	}
	return b.hy2UDPBatchIO.WriteBatch(ms, flags)
}

// GSO 被内核拒绝（老内核、出口无校验和卸载）时，本会话关掉 GSO 并从失败那条
// 起逐条重发，不丢包。
func TestHy2BatchWriterGSOFallbackOnKernelReject(t *testing.T) {
	sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	upstream := hy2LoadRawPacketConn{conn}
	writer := newHy2BatchWriter(upstream, rejectGSOBatch{newHy2UDPUpstream(upstream).batch})
	resolver := &hy2UDPResolver{ctx: context.Background()}
	writer.reset()
	var want int64
	for i := 0; i < 6; i++ {
		payload := gsoPayload(i, 900)
		want += int64(len(payload))
		writer.add(resolver, payload, M.SocksaddrFromNet(sink.LocalAddr()), M.Socksaddr{})
	}
	written, err := writer.flush()
	if err != nil || written != want {
		t.Fatalf("写出 %d/%d err=%v", written, want, err)
	}
	if writer.gso {
		t.Fatal("GSO 被拒后本会话应关掉 GSO")
	}
	got := readDatagrams(t, sink, 6)
	for i := range got {
		if !bytes.Equal(got[i], gsoPayload(i, 900)) {
			t.Fatalf("第 %d 包不符", i)
		}
	}
}
