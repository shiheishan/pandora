package tuic

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
)

// referencePack 是上游原来的编码（binary.Write + AddressSerializer），用来对拍
// 手写的 appendTo。
func referencePack(t *testing.T, m *udpMessage) []byte {
	t.Helper()
	buffer := buf.NewSize(m.headerSize() + m.data.Len())
	defer buffer.Release()
	_ = buffer.WriteByte(Version)
	_ = buffer.WriteByte(CommandPacket)
	_ = binary.Write(buffer, binary.BigEndian, m.sessionID)
	_ = binary.Write(buffer, binary.BigEndian, m.packetID)
	_ = binary.Write(buffer, binary.BigEndian, m.fragmentTotal)
	_ = binary.Write(buffer, binary.BigEndian, m.fragmentID)
	_ = binary.Write(buffer, binary.BigEndian, uint16(m.data.Len()))
	if err := AddressSerializer.WriteAddrPort(buffer, m.destination); err != nil {
		t.Fatal(err)
	}
	_, _ = buffer.Write(m.data.Bytes())
	return bytes.Clone(buffer.Bytes())
}

// 手写编解码与 TUIC v5 线格式逐字节一致（对拍上游实现），各种地址类型都覆盖。
func TestUDPMessageWireFormat(t *testing.T) {
	destinations := []M.Socksaddr{
		M.ParseSocksaddr("192.0.2.1:53"),
		M.ParseSocksaddr("[2001:db8::1]:443"),
		M.ParseSocksaddr("example.test:8443"),
		{}, // 后续分片不带地址
	}
	for _, destination := range destinations {
		message := &udpMessage{sessionID: 0x0102, packetID: 0x0304, fragmentTotal: 3, fragmentID: 1, destination: destination, data: buf.As([]byte("payload"))}
		got, err := message.appendTo(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := referencePack(t, message)
		if !bytes.Equal(got, want) {
			t.Fatalf("%v 编码不一致：\n got %x\nwant %x", destination, got, want)
		}
		if len(got) != message.headerSize()+message.data.Len() {
			t.Fatalf("%v 编码长度 %d 与 headerSize 不符", destination, len(got))
		}
		for _, cache := range []*destinationCache{nil, {}} {
			var decoded udpMessage
			if err := decodeUDPMessage(&decoded, got[2:], cache); err != nil {
				t.Fatalf("%v 解码：%v", destination, err)
			}
			if decoded.sessionID != 0x0102 || decoded.packetID != 0x0304 || decoded.fragmentTotal != 3 || decoded.fragmentID != 1 ||
				decoded.destination != destination || string(decoded.data.Bytes()) != "payload" {
				t.Fatalf("%v 解码不一致：%+v", destination, decoded)
			}
			// 与上游解析器结果相同。
			reference, err := AddressSerializer.ReadAddrPort(bytes.NewReader(got[10:]))
			if err != nil || reference != decoded.destination {
				t.Fatalf("%v 与上游解析不一致：%v / %v err=%v", destination, decoded.destination, reference, err)
			}
		}
	}
	// 4in6 按上游一样拆成 IPv4。
	message := &udpMessage{fragmentTotal: 1, destination: M.ParseSocksaddr("[::ffff:192.0.2.7]:53"), data: buf.As([]byte("x"))}
	encoded, err := message.appendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded udpMessage
	if err := decodeUDPMessage(&decoded, encoded[2:], nil); err != nil || decoded.destination != M.ParseSocksaddr("192.0.2.7:53") {
		t.Fatalf("4in6 解码 %v err=%v", decoded.destination, err)
	}
	// 超长域名报错而不是 panic。
	long := &udpMessage{fragmentTotal: 1, destination: M.Socksaddr{Fqdn: string(bytes.Repeat([]byte("a"), 256)), Port: 1}, data: buf.As(nil)}
	if _, err := long.appendTo(nil); err == nil {
		t.Fatal("超长域名应报错")
	}
}

func TestDecodeUDPMessageRejectsMalformed(t *testing.T) {
	good, err := (&udpMessage{fragmentTotal: 1, destination: M.ParseSocksaddr("example.test:53"), data: buf.As([]byte("abc"))}).appendTo(nil)
	if err != nil {
		t.Fatal(err)
	}
	body := good[2:]
	bad := [][]byte{
		body[:7],                       // 头不全
		body[:9],                       // 域名长度缺
		body[:len(body)-1],             // 负载比 SIZE 短
		append(bytes.Clone(body), 'x'), // 负载比 SIZE 长
		append(append(bytes.Clone(body[:8]), 0x07), body[9:]...), // 未知地址类型
	}
	for _, data := range bad {
		var decoded udpMessage
		if err := decodeUDPMessage(&decoded, data, nil); err == nil {
			t.Fatalf("畸形消息 %x 被接受", data)
		}
	}
}

// 目标缓存：同一目标复用，换目标立即更新。
func TestDestinationCacheFollowsChanges(t *testing.T) {
	var cache destinationCache
	encode := func(destination M.Socksaddr) []byte {
		raw, err := appendAddrPort(nil, destination)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, destination := range []M.Socksaddr{M.ParseSocksaddr("example.test:53"), M.ParseSocksaddr("example.test:53"), M.ParseSocksaddr("other.test:53"), M.ParseSocksaddr("192.0.2.2:53")} {
		if got := cache.lookup(encode(destination)); got != destination {
			t.Fatalf("got %v want %v", got, destination)
		}
	}
}

func fragment(packetID uint16, id, total uint8, data string) *udpMessage {
	message := allocMessage()
	message.packetID, message.fragmentID, message.fragmentTotal = packetID, id, total
	message.destination = M.ParseSocksaddr("192.0.2.9:9")
	message.data = buf.As([]byte(data))
	return message
}

func TestUDPDefraggerReassembles(t *testing.T) {
	d := newUDPDefragger()
	if d.feed(fragment(1, 1, 2, "world")) != nil || d.feed(fragment(2, 0, 2, "foo")) != nil {
		t.Fatal("未齐就交出")
	}
	if d.feed(fragment(1, 1, 2, "WORLD")) != nil {
		t.Fatal("重复分片导致交出")
	}
	whole := d.feed(fragment(1, 0, 2, "hello "))
	if whole == nil || string(whole.data.Bytes()) != "hello world" || whole.destination != M.ParseSocksaddr("192.0.2.9:9") {
		t.Fatalf("重组结果 %+v", whole)
	}
	whole.releaseMessage()
	whole = d.feed(fragment(2, 1, 2, "bar"))
	if whole == nil || string(whole.data.Bytes()) != "foobar" {
		t.Fatal("第二个包重组失败")
	}
	if d.feed(fragment(3, 5, 2, "x")) != nil {
		t.Fatal("越界分片被接受")
	}
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
	if d.feed(fragment(0, 1, 2, "y")) != nil {
		t.Fatal("已淘汰的包被错误重组")
	}
}

func newTestPacketConn(queue int) *udpPacketConn {
	return newUDPPacketConn(context.Background(), nil, false, true, func() {}, queue)
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
	if cap(newTestPacketConn(0).data) != DefaultUDPQueueSize {
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
