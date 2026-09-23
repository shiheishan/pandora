//go:build interop

package mkcp

import (
	"bytes"
	"testing"

	xraykcp "github.com/xtls/xray-core/transport/internet/kcp"
)

// 与 xray 的 mKCP 实现双向对拍。
//
// 线格式只有两种验证方式：拿真实对端跑，或者拿对端的编解码器对拍。前者
// 要把整个传输层写完才做得了，后者现在就能做——而且失败时直接指出是哪个
// 字段错位，比看着连接超时去猜强得多。
//
// 这是 opt-in 的：xray 只在 interop 标签下引入，默认构建不牵它进来。

// 我们编码 → xray 解码。这个方向证明我们发出去的包 xray 认得。
func TestDataSegmentEncodedByUsDecodesInXray(t *testing.T) {
	ours := &DataSegment{
		Conv: 0xBEEF, Option: OptionClose,
		Timestamp: 0x11223344, Number: 0x55667788, SendingNext: 0x99AABBCC,
		Data: []byte("pandora-mkcp-payload"),
	}
	buf := make([]byte, ours.ByteSize())
	if err := ours.Serialize(buf); err != nil {
		t.Fatal(err)
	}

	seg, rest := xraykcp.ReadSegment(buf)
	if seg == nil {
		t.Fatal("xray 解不出我们编的数据段")
	}
	if len(rest) != 0 {
		t.Fatalf("xray 解析后还剩 %d 字节，说明长度算错了", len(rest))
	}
	got, ok := seg.(*xraykcp.DataSegment)
	if !ok {
		t.Fatalf("xray 解出的类型是 %T", seg)
	}
	if got.Conv != ours.Conv {
		t.Errorf("会话号 = %#x，期望 %#x", got.Conv, ours.Conv)
	}
	if got.Timestamp != ours.Timestamp || got.Number != ours.Number ||
		got.SendingNext != ours.SendingNext {
		t.Errorf("时间戳/序号/发送游标 = %#x/%#x/%#x，期望 %#x/%#x/%#x",
			got.Timestamp, got.Number, got.SendingNext,
			ours.Timestamp, ours.Number, ours.SendingNext)
	}
	if !bytes.Equal(got.Data().Bytes(), ours.Data) {
		t.Errorf("载荷 = %q，期望 %q", got.Data().Bytes(), ours.Data)
	}
}

// xray 编码 → 我们解码。这个方向证明对端发来的包我们认得。
func TestDataSegmentEncodedByXrayDecodesHere(t *testing.T) {
	theirs := xraykcp.NewDataSegment()
	theirs.Conv = 0x1234
	theirs.Timestamp = 0xDEADBEEF
	theirs.Number = 42
	theirs.SendingNext = 43
	theirs.Data().Write([]byte("from-xray"))

	buf := make([]byte, theirs.ByteSize())
	theirs.Serialize(buf)

	seg, rest := ReadSegment(buf)
	if seg == nil {
		t.Fatal("我们解不出 xray 编的数据段")
	}
	if len(rest) != 0 {
		t.Fatalf("解析后还剩 %d 字节", len(rest))
	}
	got, ok := seg.(*DataSegment)
	if !ok {
		t.Fatalf("解出的类型是 %T", seg)
	}
	if got.Conv != theirs.Conv || got.Number != theirs.Number ||
		got.Timestamp != theirs.Timestamp || got.SendingNext != theirs.SendingNext {
		t.Errorf("字段不一致：%+v", got)
	}
	if string(got.Data) != "from-xray" {
		t.Errorf("载荷 = %q", got.Data)
	}
}

func TestAckSegmentRoundTripsWithXray(t *testing.T) {
	ours := &AckSegment{
		Conv: 0x4321, ReceivingWindow: 128, ReceivingNext: 77,
		Timestamp: 0xCAFEBABE, NumberList: []uint32{1, 2, 3, 65535, 0xFFFFFFFF},
	}
	buf := make([]byte, ours.ByteSize())
	if err := ours.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	seg, rest := xraykcp.ReadSegment(buf)
	if seg == nil || len(rest) != 0 {
		t.Fatalf("xray 解不出我们编的 ACK（剩余 %d 字节）", len(rest))
	}
	got := seg.(*xraykcp.AckSegment)
	if got.ReceivingWindow != ours.ReceivingWindow || got.ReceivingNext != ours.ReceivingNext {
		t.Errorf("窗口/游标 = %d/%d", got.ReceivingWindow, got.ReceivingNext)
	}
	if len(got.NumberList) != len(ours.NumberList) {
		t.Fatalf("序号个数 = %d，期望 %d", len(got.NumberList), len(ours.NumberList))
	}
	for i, n := range ours.NumberList {
		if got.NumberList[i] != n {
			t.Errorf("第 %d 个序号 = %d，期望 %d", i, got.NumberList[i], n)
		}
	}
}

func TestCmdOnlySegmentRoundTripsWithXray(t *testing.T) {
	for _, cmd := range []Command{CommandPing, CommandTerminate} {
		ours := &CmdOnlySegment{
			Conv: 7, Cmd: cmd, SendingNext: 100, ReceivingNext: 200, PeerRTO: 300,
		}
		buf := make([]byte, ours.ByteSize())
		if err := ours.Serialize(buf); err != nil {
			t.Fatal(err)
		}
		seg, rest := xraykcp.ReadSegment(buf)
		if seg == nil || len(rest) != 0 {
			t.Fatalf("cmd=%d：xray 解不出（剩余 %d 字节）", cmd, len(rest))
		}
		got := seg.(*xraykcp.CmdOnlySegment)
		if byte(got.Command()) != byte(cmd) {
			t.Errorf("命令 = %d，期望 %d", got.Command(), cmd)
		}
		if got.SendingNext != 100 || got.ReceivingNext != 200 || got.PeerRTO != 300 {
			t.Errorf("字段不一致：%+v", got)
		}
	}
}

// 一个 UDP 包里可以连着多个段，xray 就是这么发的。解析必须能顺着往下走。
func TestMultipleSegmentsInOnePacket(t *testing.T) {
	data := &DataSegment{Conv: 1, Number: 1, Data: []byte("first")}
	ack := &AckSegment{Conv: 1, ReceivingNext: 2, NumberList: []uint32{1}}
	ping := &CmdOnlySegment{Conv: 1, Cmd: CommandPing}

	buf := make([]byte, 0, 256)
	for _, s := range []Segment{data, ack, ping} {
		b := make([]byte, s.ByteSize())
		if err := s.Serialize(b); err != nil {
			t.Fatal(err)
		}
		buf = append(buf, b...)
	}

	// 我们自己顺着解
	var kinds []Command
	rest := buf
	for len(rest) > 0 {
		var seg Segment
		seg, rest = ReadSegment(rest)
		if seg == nil {
			t.Fatal("中途解析失败")
		}
		kinds = append(kinds, seg.Command())
	}
	want := []Command{CommandData, CommandACK, CommandPing}
	if len(kinds) != len(want) {
		t.Fatalf("解出 %d 个段，期望 %d 个", len(kinds), len(want))
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("第 %d 个段命令 = %d，期望 %d", i, kinds[i], want[i])
		}
	}

	// xray 也要能顺着解同一串字节
	rest = buf
	count := 0
	for len(rest) > 0 {
		var seg xraykcp.Segment
		seg, rest = xraykcp.ReadSegment(rest)
		if seg == nil {
			t.Fatalf("xray 在第 %d 个段上解析失败", count)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("xray 解出 %d 个段，期望 3 个", count)
	}
}

// 截断的包不能解出「看似合法」的东西——那会让上层拿到一段错位的数据。
func TestTruncatedInputRejected(t *testing.T) {
	full := &DataSegment{Conv: 1, Number: 1, Data: []byte("0123456789")}
	buf := make([]byte, full.ByteSize())
	if err := full.Serialize(buf); err != nil {
		t.Fatal(err)
	}
	for cut := 1; cut < len(buf); cut++ {
		if seg, _ := ReadSegment(buf[:cut]); seg != nil {
			t.Fatalf("截到 %d 字节仍解出了段", cut)
		}
	}
}
