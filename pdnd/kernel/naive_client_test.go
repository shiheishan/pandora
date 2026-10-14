package kernel

// 测试用 Naive 客户端 padding 连接：自 sing-box protocol/naive/inbound_conn.go（GPL-3.0）
// 的客户端侧实现搬来，只供本包测试互操作。
// 与 kernel 自己的 naivePaddingState 是两套独立实现，可互相验证协议字节布局。

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
	"github.com/sagernet/sing/common/rw"
)

// naiveTestClientPaddingCount 前若干报文带 padding；之后为明文长度。
const naiveTestClientPaddingCount = 8

type naiveTestClientPadding struct {
	readPadding      int
	writePadding     int
	readRemaining    int
	paddingRemaining int
}

func (p *naiveTestClientPadding) readWithPadding(reader io.Reader, buffer []byte) (n int, err error) {
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
	if p.readPadding < naiveTestClientPaddingCount {
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

func (p *naiveTestClientPadding) writeWithPadding(writer io.Writer, data []byte) (n int, err error) {
	if p.writePadding < naiveTestClientPaddingCount {
		paddingSize := rand.Intn(256)
		packet := make([]byte, 3+len(data)+paddingSize)
		binary.BigEndian.PutUint16(packet[:2], uint16(len(data)))
		packet[2] = byte(paddingSize)
		copy(packet[3:], data)
		_, err = writer.Write(packet)
		if err == nil {
			n = len(data)
		}
		p.writePadding++
		return
	}
	return writer.Write(data)
}

func (p *naiveTestClientPadding) writeChunked(writer io.Writer, data []byte) (n int, err error) {
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

type naiveTestClientConn struct {
	reader        io.Reader
	writer        io.Writer
	flusher       http.Flusher
	remoteAddress net.Addr
	naiveTestClientPadding
}

func (c *naiveTestClientConn) Read(p []byte) (n int, err error) {
	n, err = c.readWithPadding(c.reader, p)
	return n, baderror.WrapH2(err)
}

func (c *naiveTestClientConn) Write(p []byte) (n int, err error) {
	n, err = c.writeChunked(c.writer, p)
	if err == nil {
		c.flusher.Flush()
	}
	return n, baderror.WrapH2(err)
}

func (c *naiveTestClientConn) Close() error {
	return common.Close(c.reader, c.writer)
}

func (c *naiveTestClientConn) LocalAddr() net.Addr                { return naiveAddr("naive-test-client") }
func (c *naiveTestClientConn) RemoteAddr() net.Addr               { return c.remoteAddress }
func (c *naiveTestClientConn) SetDeadline(t time.Time) error      { return os.ErrInvalid }
func (c *naiveTestClientConn) SetReadDeadline(t time.Time) error  { return os.ErrInvalid }
func (c *naiveTestClientConn) SetWriteDeadline(t time.Time) error { return os.ErrInvalid }

// newNaiveTestClientConn 把已建立的 h2 双向流包成 Naive 客户端 padding 连接（仅测试）。
func newNaiveTestClientConn(reader io.Reader, writer io.Writer, flusher http.Flusher, remote net.Addr) net.Conn {
	return &naiveTestClientConn{reader: reader, writer: writer, flusher: flusher, remoteAddress: remote}
}
