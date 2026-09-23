// Package mkcp 实现 mKCP 的线格式。
//
// mKCP 是 xray 在 KCP 之上改出来的一套，段结构和标准 KCP 不同：会话号只有
// 两字节、命令与选项各占一字节、数据段带 SendingNext 用来推进对端的发送
// 窗口。所以不能直接套现成的 kcp-go——那个库编出来的字节 xray 认不出来。
//
// 这一层只负责字节的编解码，不含拥塞控制和重传。分开是为了能单独对拍：
// 传输层的行为可以慢慢调，线格式错一个字节就是完全连不上，必须先钉死。
package mkcp

import (
	"encoding/binary"
	"errors"
)

// Command 是段的用途。取值与 xray 一致，不能改。
type Command byte

const (
	CommandACK       Command = 0
	CommandData      Command = 1
	CommandTerminate Command = 2
	CommandPing      Command = 3
)

// Option 目前只有一个：告诉对端这条连接要关了。
type Option byte

const OptionClose Option = 1

// 各段的固定开销。数据段 18 字节：会话 2 + 命令 1 + 选项 1 +
// 时间戳 4 + 序号 4 + 发送游标 4 + 数据长度 2。
const (
	DataSegmentOverhead = 18
	// ackNumberLimit 是一个 ACK 段里最多带几个序号。数量用一个字节存，
	// 所以物理上限是 255；xray 取 128，跟着它——超出这个数对端会把多出来
	// 的部分当成下一个段的开头去解析。
	ackNumberLimit = 128
)

var (
	ErrShortBuffer = errors.New("mkcp: 缓冲区不足以容纳该段")
	ErrMalformed   = errors.New("mkcp: 段格式非法")
	ErrTooManyAcks = errors.New("mkcp: ACK 序号超出单段上限")
)

// Segment 是所有段的公共行为。
type Segment interface {
	Conversation() uint16
	Command() Command
	ByteSize() int
	// Serialize 把段写进 b，b 必须至少有 ByteSize() 那么大。
	Serialize(b []byte) error
}

//------------------------------------------------------------------------------
// 数据段
//------------------------------------------------------------------------------

type DataSegment struct {
	Conv   uint16
	Option Option
	// Timestamp 是发送时刻，对端原样回显在 ACK 里，用来算 RTT。
	Timestamp uint32
	Number    uint32
	// SendingNext 是发送方下一个要发的序号。接收方据此知道自己落后多少，
	// 不用等超时就能发现丢包。
	SendingNext uint32
	Data        []byte
}

func (s *DataSegment) Conversation() uint16 { return s.Conv }
func (*DataSegment) Command() Command       { return CommandData }
func (s *DataSegment) ByteSize() int        { return DataSegmentOverhead + len(s.Data) }

func (s *DataSegment) Serialize(b []byte) error {
	if len(b) < s.ByteSize() {
		return ErrShortBuffer
	}
	// 数据长度用两字节存，超过 65535 编出去会被截断成另一个长度，
	// 对端解出一段错位的数据——宁可在这里报错。
	if len(s.Data) > 0xFFFF {
		return ErrMalformed
	}
	binary.BigEndian.PutUint16(b[0:], s.Conv)
	b[2] = byte(CommandData)
	b[3] = byte(s.Option)
	binary.BigEndian.PutUint32(b[4:], s.Timestamp)
	binary.BigEndian.PutUint32(b[8:], s.Number)
	binary.BigEndian.PutUint32(b[12:], s.SendingNext)
	binary.BigEndian.PutUint16(b[16:], uint16(len(s.Data)))
	copy(b[18:], s.Data)
	return nil
}

// parse 从 b 读出段体（会话号、命令、选项已由 ReadSegment 取走）。
// 返回剩余未消费的字节。
func (s *DataSegment) parse(conv uint16, opt Option, b []byte) ([]byte, bool) {
	if len(b) < 14 {
		return nil, false
	}
	s.Conv, s.Option = conv, opt
	s.Timestamp = binary.BigEndian.Uint32(b[0:])
	s.Number = binary.BigEndian.Uint32(b[4:])
	s.SendingNext = binary.BigEndian.Uint32(b[8:])
	dataLen := int(binary.BigEndian.Uint16(b[12:]))
	b = b[14:]
	if len(b) < dataLen {
		return nil, false
	}
	// 拷贝而不是切片引用：调用方通常在复用收包缓冲，留引用会让这段数据
	// 在下一个包到达时被悄悄改写。
	s.Data = append(s.Data[:0], b[:dataLen]...)
	return b[dataLen:], true
}

//------------------------------------------------------------------------------
// ACK 段
//------------------------------------------------------------------------------

type AckSegment struct {
	Conv            uint16
	Option          Option
	ReceivingWindow uint32
	ReceivingNext   uint32
	Timestamp       uint32
	NumberList      []uint32
}

func (s *AckSegment) Conversation() uint16 { return s.Conv }
func (*AckSegment) Command() Command       { return CommandACK }
func (s *AckSegment) ByteSize() int        { return 17 + len(s.NumberList)*4 }

func (s *AckSegment) Serialize(b []byte) error {
	if len(s.NumberList) > ackNumberLimit {
		return ErrTooManyAcks
	}
	if len(b) < s.ByteSize() {
		return ErrShortBuffer
	}
	binary.BigEndian.PutUint16(b[0:], s.Conv)
	b[2] = byte(CommandACK)
	b[3] = byte(s.Option)
	binary.BigEndian.PutUint32(b[4:], s.ReceivingWindow)
	binary.BigEndian.PutUint32(b[8:], s.ReceivingNext)
	binary.BigEndian.PutUint32(b[12:], s.Timestamp)
	b[16] = byte(len(s.NumberList))
	n := 17
	for _, number := range s.NumberList {
		binary.BigEndian.PutUint32(b[n:], number)
		n += 4
	}
	return nil
}

func (s *AckSegment) parse(conv uint16, opt Option, b []byte) ([]byte, bool) {
	if len(b) < 13 {
		return nil, false
	}
	s.Conv, s.Option = conv, opt
	s.ReceivingWindow = binary.BigEndian.Uint32(b[0:])
	s.ReceivingNext = binary.BigEndian.Uint32(b[4:])
	s.Timestamp = binary.BigEndian.Uint32(b[8:])
	count := int(b[12])
	b = b[13:]
	if len(b) < count*4 {
		return nil, false
	}
	s.NumberList = s.NumberList[:0]
	for i := 0; i < count; i++ {
		s.NumberList = append(s.NumberList, binary.BigEndian.Uint32(b[i*4:]))
	}
	return b[count*4:], true
}

//------------------------------------------------------------------------------
// 命令段（Ping / Terminate）
//------------------------------------------------------------------------------

type CmdOnlySegment struct {
	Conv          uint16
	Cmd           Command
	Option        Option
	SendingNext   uint32
	ReceivingNext uint32
	// PeerRTO 把自己算出的重传超时告诉对端，让它的重传节奏跟上链路状况。
	PeerRTO uint32
}

func (s *CmdOnlySegment) Conversation() uint16 { return s.Conv }
func (s *CmdOnlySegment) Command() Command     { return s.Cmd }
func (*CmdOnlySegment) ByteSize() int          { return 16 }

func (s *CmdOnlySegment) Serialize(b []byte) error {
	if len(b) < s.ByteSize() {
		return ErrShortBuffer
	}
	binary.BigEndian.PutUint16(b[0:], s.Conv)
	b[2] = byte(s.Cmd)
	b[3] = byte(s.Option)
	binary.BigEndian.PutUint32(b[4:], s.SendingNext)
	binary.BigEndian.PutUint32(b[8:], s.ReceivingNext)
	binary.BigEndian.PutUint32(b[12:], s.PeerRTO)
	return nil
}

func (s *CmdOnlySegment) parse(conv uint16, cmd Command, opt Option, b []byte) ([]byte, bool) {
	if len(b) < 12 {
		return nil, false
	}
	s.Conv, s.Cmd, s.Option = conv, cmd, opt
	s.SendingNext = binary.BigEndian.Uint32(b[0:])
	s.ReceivingNext = binary.BigEndian.Uint32(b[4:])
	s.PeerRTO = binary.BigEndian.Uint32(b[8:])
	return b[12:], true
}

//------------------------------------------------------------------------------
// 读取
//------------------------------------------------------------------------------

// ReadSegment 从 b 头部读出一个段，返回它和剩余字节。
//
// 解不出来时返回 (nil, nil) 而不是部分结果：一个包里可能连着多个段，
// 中间有一个坏了，后面的偏移就全错了，继续解只会得到看似合法的垃圾。
func ReadSegment(b []byte) (Segment, []byte) {
	if len(b) < 4 {
		return nil, nil
	}
	conv := binary.BigEndian.Uint16(b[0:])
	cmd := Command(b[2])
	opt := Option(b[3])
	body := b[4:]

	switch cmd {
	case CommandData:
		seg := new(DataSegment)
		rest, ok := seg.parse(conv, opt, body)
		if !ok {
			return nil, nil
		}
		return seg, rest
	case CommandACK:
		seg := new(AckSegment)
		rest, ok := seg.parse(conv, opt, body)
		if !ok {
			return nil, nil
		}
		return seg, rest
	case CommandTerminate, CommandPing:
		seg := new(CmdOnlySegment)
		rest, ok := seg.parse(conv, cmd, opt, body)
		if !ok {
			return nil, nil
		}
		return seg, rest
	default:
		// 未知命令一律拒绝。
		//
		// 这里本来写的是「按命令段长度跳过，保持向前兼容」，但 mKCP 的
		// 命令集是定死的，上游也没有在加新命令——换来的兼容性是假的，
		// 代价却是实的：段格式没有魔数也没有校验和，放行未知命令等于
		// 任何四字节以上的 UDP 包都能在监听端口上建出一条会话。
		//
		// 收紧到已知的四个命令，随机字节撞上的概率从 100% 降到 1/64。
		// 剩下那 1/64 靠监听器的会话数上限兜底——无认证的协议里没法
		// 做得更好，这也正是上游要在外面套一层 udp mask 的原因。
		return nil, nil
	}
}
