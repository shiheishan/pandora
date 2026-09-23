package sing

// Naive 的 padding 连接实现。
//
// 这段来自上游 sing-box 的 protocol/naive/inbound_conn.go（GPL-3.0），
// 逐字保留而非重写：padding 的字节布局是 naiveproxy 协议的一部分，
// 与 Chrome 的实现严格对应，任何「改进」都会让客户端连不上。
// 之所以要复制，是因为上游把它放在包内未导出，外部拿不到。
//
// 唯一的改动是去掉了上游的 WrapError 钩子（那是给 Windows 特定错误码用的）。

import (
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/baderror"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/rw"
)

// paddingCount 是做 padding 的报文数。前 8 个报文之后就是明文长度，
// 因为流量特征分析主要看握手阶段的头几个包。
const paddingCount = 8

func generatePaddingHeader() string {
	paddingLen := rand.Intn(32) + 30
	padding := make([]byte, paddingLen)
	bits := rand.Uint64()
	for i := range 16 {
		padding[i] = "!#$()+<>?@[]^`{}"[bits&15]
		bits >>= 4
	}
	for i := 16; i < paddingLen; i++ {
		padding[i] = '~'
	}
	return string(padding)
}

type paddingConn struct {
	readPadding      int
	writePadding     int
	readRemaining    int
	paddingRemaining int
}

func (p *paddingConn) readWithPadding(reader io.Reader, buffer []byte) (n int, err error) {
	if p.readRemaining > 0 {
		if len(buffer) > p.readRemaining {
			buffer = buffer[:p.readRemaining]
		}
		n, err = reader.Read(buffer)
		if err != nil {
			return
		}
		p.readRemaining -= n
		return
	}
	if p.paddingRemaining > 0 {
		err = rw.SkipN(reader, p.paddingRemaining)
		if err != nil {
			return
		}
		p.paddingRemaining = 0
	}
	if p.readPadding < paddingCount {
		var paddingHeader []byte
		if len(buffer) >= 3 {
			paddingHeader = buffer[:3]
		} else {
			paddingHeader = make([]byte, 3)
		}
		_, err = io.ReadFull(reader, paddingHeader)
		if err != nil {
			return
		}
		originalDataSize := int(binary.BigEndian.Uint16(paddingHeader[:2]))
		paddingSize := int(paddingHeader[2])
		if len(buffer) > originalDataSize {
			buffer = buffer[:originalDataSize]
		}
		n, err = reader.Read(buffer)
		if err != nil {
			return
		}
		p.readPadding++
		p.readRemaining = originalDataSize - n
		p.paddingRemaining = paddingSize
		return
	}
	return reader.Read(buffer)
}

func (p *paddingConn) writeWithPadding(writer io.Writer, data []byte) (n int, err error) {
	if p.writePadding < paddingCount {
		paddingSize := rand.Intn(256)
		buffer := buf.NewSize(3 + len(data) + paddingSize)
		defer buffer.Release()
		header := buffer.Extend(3)
		binary.BigEndian.PutUint16(header, uint16(len(data)))
		header[2] = byte(paddingSize)
		common.Must1(buffer.Write(data))
		common.Must(buffer.WriteZeroN(paddingSize))
		_, err = writer.Write(buffer.Bytes())
		if err == nil {
			n = len(data)
		}
		p.writePadding++
		return
	}
	return writer.Write(data)
}

func (p *paddingConn) writeBufferWithPadding(writer io.Writer, buffer *buf.Buffer) error {
	if p.writePadding < paddingCount {
		bufferLen := buffer.Len()
		if bufferLen > 65535 {
			_, err := p.writeChunked(writer, buffer.Bytes())
			return err
		}
		paddingSize := rand.Intn(256)
		header := buffer.ExtendHeader(3)
		binary.BigEndian.PutUint16(header, uint16(bufferLen))
		header[2] = byte(paddingSize)
		common.Must(buffer.WriteZeroN(paddingSize))
		p.writePadding++
	}
	return common.Error(writer.Write(buffer.Bytes()))
}

// writeChunked 把超过 65535 字节的数据拆开写。
// padding 头里的长度字段只有 2 字节，装不下更大的块。
func (p *paddingConn) writeChunked(writer io.Writer, data []byte) (n int, err error) {
	for len(data) > 0 {
		var chunk []byte
		if len(data) > 65535 {
			chunk = data[:65535]
			data = data[65535:]
		} else {
			chunk = data
			data = nil
		}
		var written int
		written, err = p.writeWithPadding(writer, chunk)
		n += written
		if err != nil {
			return
		}
	}
	return
}

func (p *paddingConn) frontHeadroom() int {
	if p.writePadding < paddingCount {
		return 3
	}
	return 0
}

func (p *paddingConn) rearHeadroom() int {
	if p.writePadding < paddingCount {
		return 255
	}
	return 0
}

func (p *paddingConn) writerMTU() int {
	if p.writePadding < paddingCount {
		return 65535
	}
	return 0
}

func (p *paddingConn) readerReplaceable() bool { return p.readPadding == paddingCount }
func (p *paddingConn) writerReplaceable() bool { return p.writePadding == paddingCount }

type naiveConn struct {
	net.Conn
	paddingConn
}

func (c *naiveConn) Read(p []byte) (n int, err error) {
	n, err = c.readWithPadding(c.Conn, p)
	return n, baderror.WrapH2(err)
}

func (c *naiveConn) Write(p []byte) (n int, err error) {
	n, err = c.writeChunked(c.Conn, p)
	return n, baderror.WrapH2(err)
}

func (c *naiveConn) WriteBuffer(buffer *buf.Buffer) error {
	defer buffer.Release()
	return baderror.WrapH2(c.writeBufferWithPadding(c.Conn, buffer))
}

func (c *naiveConn) FrontHeadroom() int      { return c.frontHeadroom() }
func (c *naiveConn) RearHeadroom() int       { return c.rearHeadroom() }
func (c *naiveConn) WriterMTU() int          { return c.writerMTU() }
func (c *naiveConn) Upstream() any           { return c.Conn }
func (c *naiveConn) ReaderReplaceable() bool { return c.readerReplaceable() }
func (c *naiveConn) WriterReplaceable() bool { return c.writerReplaceable() }

type naiveH2Conn struct {
	reader        io.Reader
	writer        io.Writer
	flusher       http.Flusher
	remoteAddress net.Addr
	paddingConn
}

func (c *naiveH2Conn) Read(p []byte) (n int, err error) {
	n, err = c.readWithPadding(c.reader, p)
	return n, baderror.WrapH2(err)
}

func (c *naiveH2Conn) Write(p []byte) (n int, err error) {
	n, err = c.writeChunked(c.writer, p)
	if err == nil {
		c.flusher.Flush()
	}
	return n, baderror.WrapH2(err)
}

func (c *naiveH2Conn) WriteBuffer(buffer *buf.Buffer) error {
	defer buffer.Release()
	err := c.writeBufferWithPadding(c.writer, buffer)
	if err == nil {
		c.flusher.Flush()
	}
	return baderror.WrapH2(err)
}

func (c *naiveH2Conn) Close() error {
	return common.Close(c.reader, c.writer)
}

func (c *naiveH2Conn) LocalAddr() net.Addr                { return M.Socksaddr{} }
func (c *naiveH2Conn) RemoteAddr() net.Addr               { return c.remoteAddress }
func (c *naiveH2Conn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *naiveH2Conn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *naiveH2Conn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }
func (c *naiveH2Conn) NeedAdditionalReadDeadline() bool   { return true }
func (c *naiveH2Conn) UpstreamReader() any                { return c.reader }
func (c *naiveH2Conn) UpstreamWriter() any                { return c.writer }
func (c *naiveH2Conn) FrontHeadroom() int                 { return c.frontHeadroom() }
func (c *naiveH2Conn) RearHeadroom() int                  { return c.rearHeadroom() }
func (c *naiveH2Conn) WriterMTU() int                     { return c.writerMTU() }
func (c *naiveH2Conn) ReaderReplaceable() bool            { return c.readerReplaceable() }
func (c *naiveH2Conn) WriterReplaceable() bool            { return c.writerReplaceable() }

// NewNaiveClientConn 把一条已建立的 h2 双向流包成 Naive 的 padding 连接。
//
// 只为测试探针存在：padding 的收发是对称的，客户端与服务端用同一份实现
// 才能保证「测通了」真的等于「协议对」，而不是两边各错一半正好抵消。
func NewNaiveClientConn(reader io.Reader, writer io.Writer, flusher http.Flusher, remote net.Addr) net.Conn {
	return &naiveH2Conn{reader: reader, writer: writer, flusher: flusher, remoteAddress: remote}
}
