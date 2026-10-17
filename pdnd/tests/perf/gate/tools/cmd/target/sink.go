package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	sinkHeaderMax     = 128
	sinkWriteChunk    = 32 << 10
	sinkBucketCap     = 64 << 10
	sinkTick          = time.Millisecond
	sinkDownSecsMax   = 3600
	sinkPeerCloseWait = 5 * time.Second
)

var sinkFill = make([]byte, sinkWriteChunk)

// parseSinkHeader 解析 sink 请求头一行（不含换行）。
func parseSinkHeader(line string) (mode string, bps uint64, secs int, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", 0, 0, fmt.Errorf("空行")
	}
	if line == "UP" {
		return "UP", 0, 0, nil
	}
	if !strings.HasPrefix(line, "DOWN ") {
		return "", 0, 0, fmt.Errorf("未知模式")
	}
	rest := strings.TrimSpace(line[len("DOWN "):])
	parts := strings.Fields(rest)
	if len(parts) != 2 {
		return "", 0, 0, fmt.Errorf("DOWN 参数个数不对")
	}
	var b uint64
	if _, err := fmt.Sscanf(parts[0], "%d", &b); err != nil {
		return "", 0, 0, fmt.Errorf("bps 无效")
	}
	var s int
	if _, err := fmt.Sscanf(parts[1], "%d", &s); err != nil {
		return "", 0, 0, fmt.Errorf("seconds 无效")
	}
	if s <= 0 || s > sinkDownSecsMax {
		return "", 0, 0, fmt.Errorf("seconds 越界")
	}
	return "DOWN", b, s, nil
}

// drainCount 读到 EOF 并丢弃，返回字节数。
func drainCount(r io.Reader) int64 {
	n, _ := io.Copy(io.Discard, r)
	return n
}

// pacedWrite 在时长 d 内向 w 写入数据；bps 为每秒比特，0 表示不限速尽快写。
func pacedWrite(w io.Writer, bps uint64, d time.Duration) (int64, error) {
	deadline := time.Now().Add(d)
	var total int64

	if bps == 0 {
		for time.Now().Before(deadline) {
			n, err := w.Write(sinkFill)
			if n > 0 {
				total += int64(n)
			}
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}

	// 每毫秒注入的字节数（比特/8/1000）
	bytesPerTick := float64(bps) / 8.0 / 1000.0
	// 桶上限至少容得下 4 格：速率高到每格超过 64KB 时，固定 64KB 的上限会把实际速率截在 512Mbps
	bucketCap := float64(sinkBucketCap)
	if 4*bytesPerTick > bucketCap {
		bucketCap = 4 * bytesPerTick
	}
	var bucket float64
	nextTick := time.Now()

	for time.Now().Before(deadline) {
		now := time.Now()
		for !now.Before(nextTick) && now.Before(deadline) {
			bucket += bytesPerTick
			if bucket > bucketCap {
				bucket = bucketCap
			}
			nextTick = nextTick.Add(sinkTick)
			now = time.Now()
		}
		if bucket >= float64(sinkWriteChunk) {
			n, err := w.Write(sinkFill)
			if n > 0 {
				total += int64(n)
				bucket -= float64(n)
			}
			if err != nil {
				return total, err
			}
		} else {
			sleep := nextTick.Sub(time.Now())
			if sleep > 0 {
				time.Sleep(sleep)
			}
		}
	}
	return total, nil
}

// serveSink 处理一条 sink 连接。
func serveSink(conn net.Conn) {
	br := bufio.NewReader(conn)
	line, err := readSinkLine(br)
	if err != nil {
		conn.Close()
		return
	}
	mode, bps, secs, err := parseSinkHeader(line)
	if err != nil {
		conn.Close()
		return
	}

	switch mode {
	case "UP":
		n := drainCount(br)
		_, _ = fmt.Fprintf(conn, "%d\n", n)
		conn.Close()
	case "DOWN":
		dur := time.Duration(secs) * time.Second
		_, _ = pacedWrite(conn, bps, dur)
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		_ = conn.SetReadDeadline(time.Now().Add(sinkPeerCloseWait))
		_, _ = io.Copy(io.Discard, br)
		conn.Close()
	default:
		conn.Close()
	}
}

func readSinkLine(br *bufio.Reader) (string, error) {
	var buf []byte
	for len(buf) < sinkHeaderMax {
		b, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			return string(buf), nil
		}
		buf = append(buf, b)
	}
	return "", fmt.Errorf("请求头过长")
}
