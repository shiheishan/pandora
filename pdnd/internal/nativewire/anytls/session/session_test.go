package session

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/internal/nativewire/anytls/padding"
	satomic "github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/logger"
)

func defaultPadding() *satomic.TypedValue[*padding.PaddingFactory] {
	var pad satomic.TypedValue[*padding.PaddingFactory]
	padding.UpdatePaddingScheme(padding.DefaultPaddingScheme, &pad)
	return &pad
}

// 单次 Stream.Write 超过 65535 字节（帧长字段上限）要拆成多帧，对端逐字节读回
// 同样的内容（移植自上游 sing-anytls v0.0.13 的 TestStreamWriteOver64KiB）。
// 修之前长度被截成 uint16 而负载照写，线上是乱帧，对端读挂。两个方向都测：
// 客户端 → 服务端是上行，服务端流 → 客户端是下行（服务端拆帧）。
func TestStreamWriteOver64KiB(t *testing.T) {
	const size = 256 * 1024 // 4 倍单帧上限，必须拆
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	for _, dir := range []string{"up", "down"} {
		t.Run(dir, func(t *testing.T) {
			cli, srv := net.Pipe()
			defer cli.Close()
			defer srv.Close()
			pad := defaultPadding()
			clientSess := NewClient(context.Background(), logger.NOP(), func(ctx context.Context) (net.Conn, error) {
				return cli, nil
			}, pad, time.Minute, time.Minute, 0)
			defer clientSess.Close()

			got := make(chan []byte, 1)
			serverSess := NewServerSession(srv, func(stream *Stream) {
				if dir == "up" {
					buffer := make([]byte, size)
					if _, err := io.ReadFull(stream, buffer); err != nil {
						t.Errorf("服务端读：%v", err)
					}
					got <- buffer
					return
				}
				// 下行：先读客户端的一个字节确认流已建立，再一次写 256KB。
				var one [1]byte
				if _, err := io.ReadFull(stream, one[:]); err != nil {
					t.Errorf("服务端读起始字节：%v", err)
					return
				}
				if n, err := stream.Write(payload); err != nil || n != size {
					t.Errorf("服务端写 n=%d err=%v", n, err)
				}
			}, pad, logger.NOP())
			go serverSess.Run()
			defer serverSess.Close()

			stream, err := clientSess.CreateStream(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if dir == "up" {
				if n, err := stream.Write(payload); err != nil || n != size {
					t.Fatalf("客户端写 n=%d err=%v", n, err)
				}
			} else {
				if _, err := stream.Write([]byte{'d'}); err != nil {
					t.Fatal(err)
				}
				go func() {
					buffer := make([]byte, size)
					_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.ReadFull(stream, buffer); err != nil {
						t.Errorf("客户端读：%v", err)
					}
					got <- buffer
				}()
			}
			select {
			case buffer := <-got:
				if !bytes.Equal(buffer, payload) {
					t.Fatal("收到的内容与发出的不一致")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等拆开的帧超时")
			}
		})
	}
}

// 拆开的多帧在一次底层写里连续发出（移植自上游）。
func TestWriteDataFrameWritesSplitFramesContiguously(t *testing.T) {
	conn := &recordingConn{}
	sess := &Session{conn: conn}
	payload := make([]byte, maxFrameDataLen+1)
	written, err := sess.writeDataFrame(7, payload)
	if err != nil || written != len(payload) {
		t.Fatalf("writeDataFrame n=%d err=%v", written, err)
	}
	if len(conn.writes) != 1 {
		t.Fatalf("底层写了 %d 次，期望 1 次", len(conn.writes))
	}
	got := conn.writes[0]
	if len(got) != len(payload)+2*headerOverHeadSize {
		t.Fatalf("底层写长度 %d", len(got))
	}
	if got[0] != cmdPSH || got[headerOverHeadSize+maxFrameDataLen] != cmdPSH {
		t.Fatal("拆开的数据帧没有连续写出")
	}
	if n, err := sess.writeDataFrame(7, nil); n != 0 || err != nil || len(conn.writes) != 1 {
		t.Fatalf("空写不该产生帧：n=%d err=%v writes=%d", n, err, len(conn.writes))
	}
}

type recordingConn struct {
	net.Conn
	writes [][]byte
}

func (c *recordingConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, bytes.Clone(b))
	return len(b), nil
}

func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

// blockingConn 的 Write 阻塞到 release 关闭，模拟对端读得慢、写卡在 TCP 背压上；
// 写阻塞期间若有人给连接设了非零写截止时间，记一次违规——那个截止时间会算到
// 这次数据写头上，写超时就把整个会话断了。
type blockingConn struct {
	net.Conn
	release   chan struct{}
	writing   atomic.Bool
	violated  atomic.Bool
	deadlines atomic.Int32
	mu        sync.Mutex
	written   int
}

func (c *blockingConn) Write(b []byte) (int, error) {
	c.writing.Store(true)
	<-c.release
	c.writing.Store(false)
	c.mu.Lock()
	c.written += len(b)
	c.mu.Unlock()
	return len(b), nil
}

func (c *blockingConn) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() {
		c.deadlines.Add(1)
		if c.writing.Load() {
			c.violated.Store(true)
		}
	}
	return nil
}

func (c *blockingConn) SetDeadline(t time.Time) error { return c.SetWriteDeadline(t) }
func (c *blockingConn) Close() error                  { return nil }

// 控制帧（FIN、心跳回应等）的 5 秒写截止时间只能覆盖它自己的写：必须先抢到
// 连接锁再设。上游先设后抢，正在锁里阻塞的大块数据写会被这 5 秒误伤。
func TestControlFrameDeadlineDoesNotCoverInFlightDataWrite(t *testing.T) {
	conn := &blockingConn{release: make(chan struct{})}
	sess := &Session{conn: conn, die: make(chan struct{})}
	dataDone := make(chan error, 1)
	go func() {
		_, err := sess.writeDataFrame(1, make([]byte, 128<<10))
		dataDone <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !conn.writing.Load() {
		if time.Now().After(deadline) {
			t.Fatal("数据写没有开始")
		}
		time.Sleep(time.Millisecond)
	}
	controlDone := make(chan error, 1)
	go func() {
		_, err := sess.writeControlFrame(newFrame(cmdFIN, 3))
		controlDone <- err
	}()
	// 给控制帧足够时间走到抢锁处。
	time.Sleep(50 * time.Millisecond)
	if conn.deadlines.Load() != 0 {
		t.Fatal("数据写还在锁里，控制帧就给连接设了写截止时间")
	}
	close(conn.release)
	for _, ch := range []chan error{dataDone, controlDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("写没有完成")
		}
	}
	if conn.violated.Load() {
		t.Fatal("写截止时间落在了进行中的数据写上")
	}
	if conn.deadlines.Load() != 1 {
		t.Fatalf("控制帧应设一次写截止时间，实际 %d 次", conn.deadlines.Load())
	}
	frames := (128<<10 + maxFrameDataLen - 1) / maxFrameDataLen
	if conn.written != 128<<10+frames*headerOverHeadSize+headerOverHeadSize {
		t.Fatalf("底层写入 %d 字节", conn.written)
	}
}

// 控制帧写失败要关会话（行为不变）。
func TestControlFrameWriteFailureClosesSession(t *testing.T) {
	conn := &failingConn{}
	sess := &Session{conn: conn, die: make(chan struct{}), streams: map[uint32]*Stream{}}
	if _, err := sess.writeControlFrame(newFrame(cmdFIN, 1)); err == nil {
		t.Fatal("写失败应返回错误")
	}
	if !sess.IsClosed() {
		t.Fatal("控制帧写失败后会话没有关闭")
	}
}

type failingConn struct{ net.Conn }

func (failingConn) Write([]byte) (int, error)        { return 0, errors.New("broken") }
func (failingConn) SetWriteDeadline(time.Time) error { return nil }
func (failingConn) SetDeadline(time.Time) error      { return nil }
func (failingConn) Close() error                     { return nil }
