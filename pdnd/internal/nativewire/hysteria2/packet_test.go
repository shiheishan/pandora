package hysteria2

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/sagernet/quic-go/quicvarint"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

// 手写编解码与 Hysteria2 线格式逐字节一致：会话 ID、包 ID、分片号、分片数、
// varint 长度的目标地址、负载。
func TestUDPMessageWireFormat(t *testing.T) {
	message := &udpMessage{sessionID: 0x01020304, packetID: 0x0506, fragmentID: 1, fragmentTotal: 3, destination: "example.test:443", data: buf.As([]byte("payload"))}
	want := binary.BigEndian.AppendUint32(nil, 0x01020304)
	want = binary.BigEndian.AppendUint16(want, 0x0506)
	want = append(want, 1, 3)
	want = quicvarint.Append(want, uint64(len("example.test:443")))
	want = append(want, "example.test:443"...)
	want = append(want, "payload"...)
	got := message.appendTo(nil)
	if !bytes.Equal(got, want) {
		t.Fatalf("编码不一致：\n got %x\nwant %x", got, want)
	}
	for _, cache := range []*destinationCache{nil, {}} {
		var decoded udpMessage
		if err := decodeUDPMessage(&decoded, got, cache); err != nil {
			t.Fatal(err)
		}
		if decoded.sessionID != message.sessionID || decoded.packetID != message.packetID || decoded.fragmentID != 1 || decoded.fragmentTotal != 3 ||
			decoded.destination != message.destination || string(decoded.data.Bytes()) != "payload" {
			t.Fatalf("解码不一致：%+v", decoded)
		}
		if decoded.socksaddr() != M.ParseSocksaddr("example.test:443").Unwrap() {
			t.Fatalf("目标解析不一致：%v", decoded.socksaddr())
		}
	}
	for _, bad := range [][]byte{got[:7], got[:9], append(got[:8:8], 0x40)} {
		var decoded udpMessage
		if err := decodeUDPMessage(&decoded, bad, nil); err == nil {
			t.Fatalf("截断的消息 %x 被接受", bad)
		}
	}
}

// 目标缓存：同一目标复用，换目标立即更新。
func TestDestinationCacheFollowsChanges(t *testing.T) {
	var cache destinationCache
	text, addr := cache.lookup([]byte("192.0.2.1:53"))
	if text != "192.0.2.1:53" || addr != M.ParseSocksaddr("192.0.2.1:53") {
		t.Fatalf("got %q %v", text, addr)
	}
	text, addr = cache.lookup([]byte("192.0.2.2:53"))
	if text != "192.0.2.2:53" || addr != M.ParseSocksaddr("192.0.2.2:53") {
		t.Fatalf("换目标后 got %q %v", text, addr)
	}
}

func fragment(packetID uint16, id, total uint8, data string) *udpMessage {
	message := allocMessage()
	message.packetID, message.fragmentID, message.fragmentTotal = packetID, id, total
	message.destination = "192.0.2.9:9"
	message.data = buf.As([]byte(data))
	return message
}

func TestUDPDefraggerReassembles(t *testing.T) {
	d := newUDPDefragger()
	// 乱序、交错两个包。
	if d.feed(fragment(1, 1, 2, "world")) != nil || d.feed(fragment(2, 0, 2, "foo")) != nil {
		t.Fatal("未齐就交出")
	}
	// 重复分片丢弃。
	if d.feed(fragment(1, 1, 2, "WORLD")) != nil {
		t.Fatal("重复分片导致交出")
	}
	whole := d.feed(fragment(1, 0, 2, "hello "))
	if whole == nil || string(whole.data.Bytes()) != "hello world" || whole.destination != "192.0.2.9:9" {
		t.Fatalf("重组结果 %+v", whole)
	}
	whole.releaseMessage()
	whole = d.feed(fragment(2, 1, 2, "bar"))
	if whole == nil || string(whole.data.Bytes()) != "foobar" {
		t.Fatalf("第二个包重组失败")
	}
	// 越界分片号丢弃。
	if d.feed(fragment(3, 5, 2, "x")) != nil {
		t.Fatal("越界分片被接受")
	}
	// 分片数前后不一致：以新的为准重新开始。
	_ = d.feed(fragment(4, 0, 3, "a"))
	if d.feed(fragment(4, 0, 2, "b")) != nil {
		t.Fatal("分片数变更后未重新开始")
	}
	if whole := d.feed(fragment(4, 1, 2, "c")); whole == nil || string(whole.data.Bytes()) != "bc" {
		t.Fatal("分片数变更后重组错误")
	}
}

// 乱发 packetID 只会轮换固定数量的槽位，不会无限增长。
func TestUDPDefraggerBounded(t *testing.T) {
	d := newUDPDefragger()
	for id := 0; id < 10000; id++ {
		_ = d.feed(fragment(uint16(id), 0, 2, "x"))
	}
	used := 0
	for _, slot := range d.slots {
		if slot.used {
			used++
		}
	}
	if used > defragSlots {
		t.Fatalf("槽位 %d 超过上限 %d", used, defragSlots)
	}
	// 被挤掉的旧包即使后半片到了也不会凑出错误数据。
	if d.feed(fragment(0, 1, 2, "y")) != nil {
		t.Fatal("已淘汰的包被错误重组")
	}
}

func newTestPacketConn(queue int) *udpPacketConn {
	return newUDPPacketConn(context.Background(), nil, func() {}, queue)
}

// 队列满时丢弃并计数，不阻塞收包循环；TryReadPacket 不阻塞。
func TestUDPPacketConnQueueOverflow(t *testing.T) {
	conn := newTestPacketConn(4)
	for i := 0; i < 10; i++ {
		conn.inputPacket(fragment(uint16(i), 0, 1, "p"))
	}
	if conn.Dropped() != 6 {
		t.Fatalf("dropped=%d want 6", conn.Dropped())
	}
	buffer := buf.NewPacket()
	defer buffer.Release()
	for i := 0; i < 4; i++ {
		buffer.Reset()
		if _, ok := conn.TryReadPacket(buffer); !ok || string(buffer.Bytes()) != "p" {
			t.Fatalf("第 %d 个包读取失败", i)
		}
	}
	if _, ok := conn.TryReadPacket(buffer); ok {
		t.Fatal("空队列 TryReadPacket 返回了包")
	}
	if newTestPacketConn(0).data == nil || cap(newTestPacketConn(0).data) != DefaultUDPQueueSize {
		t.Fatal("非正队列长度应回落到默认值")
	}
}

// 空闲超时：有包就续命，空闲满时长才关闭；Close 后定时器不再触发。
func TestUDPPacketConnIdleTimeout(t *testing.T) {
	conn := newTestPacketConn(8)
	if conn.SetTimeout(0) {
		t.Fatal("非正超时应交还给 canceler")
	}
	if !conn.SetTimeout(150 * time.Millisecond) {
		t.Fatal("SetTimeout 应接管")
	}
	if conn.Timeout() != 150*time.Millisecond {
		t.Fatalf("Timeout()=%v", conn.Timeout())
	}
	buffer := buf.NewPacket()
	defer buffer.Release()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		conn.inputPacket(fragment(1, 0, 1, "keepalive"))
		buffer.Reset()
		if _, err := conn.ReadPacket(buffer); err != nil {
			t.Fatalf("活跃期间被关闭：%v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	select {
	case <-conn.ctx.Done():
		t.Fatal("活跃期间 ctx 被取消")
	default:
	}
	select {
	case <-conn.ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("空闲超时未关闭会话")
	}
	if _, err := conn.ReadPacket(buffer); err == nil {
		t.Fatal("超时后仍可读")
	}
	closed := newTestPacketConn(8)
	closed.SetTimeout(50 * time.Millisecond)
	_ = closed.Close()
	time.Sleep(120 * time.Millisecond)
	if closed.idle.enabled.Load() {
		t.Fatal("Close 后空闲定时器仍在")
	}
}
