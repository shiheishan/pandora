package kernel

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type grpcDuplexConn struct {
	ctx      context.Context
	body     io.ReadCloser
	writer   http.ResponseWriter
	readMu   sync.Mutex
	writeMu  sync.Mutex
	pending  []byte
	maxFrame uint32
	encoding string
}

func newGRPCDuplexConn(ctx context.Context, body io.ReadCloser, writer http.ResponseWriter, maxFrame uint32) net.Conn {
	return newGRPCDuplexConnWithEncoding(ctx, body, writer, maxFrame, "")
}

func newGRPCDuplexConnWithEncoding(ctx context.Context, body io.ReadCloser, writer http.ResponseWriter, maxFrame uint32, encoding string) net.Conn {
	return &grpcDuplexConn{ctx: ctx, body: body, writer: writer, maxFrame: maxFrame, encoding: strings.ToLower(strings.TrimSpace(encoding))}
}

func (c *grpcDuplexConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.pending) == 0 {
		select {
		case <-c.ctx.Done():
			return 0, c.ctx.Err()
		default:
		}
		var header [5]byte
		if _, err := io.ReadFull(c.body, header[:]); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length > c.maxFrame {
			return 0, fmt.Errorf("grpc message exceeds %d bytes", c.maxFrame)
		}
		frame := make([]byte, length)
		if _, err := io.ReadFull(c.body, frame); err != nil {
			c.pending = nil
			return 0, err
		}
		if header[0] == 0 {
			payload, err := decodeGunHunk(frame)
			if err != nil {
				return 0, err
			}
			c.pending = payload
			continue
		}
		if header[0] != 1 || c.encoding != "gzip" {
			return 0, fmt.Errorf("grpc compressed message encoding %q is unsupported", c.encoding)
		}
		reader, err := gzip.NewReader(bytes.NewReader(frame))
		if err != nil {
			return 0, fmt.Errorf("grpc gzip message: %w", err)
		}
		decoded, readErr := io.ReadAll(io.LimitReader(reader, int64(c.maxFrame)+1))
		closeErr := reader.Close()
		if readErr != nil {
			return 0, fmt.Errorf("grpc gzip message: %w", readErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("grpc gzip message: %w", closeErr)
		}
		if uint64(len(decoded)) > uint64(c.maxFrame) {
			return 0, fmt.Errorf("grpc decompressed message exceeds %d bytes", c.maxFrame)
		}
		payload, err := decodeGunHunk(decoded)
		if err != nil {
			return 0, err
		}
		c.pending = payload
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *grpcDuplexConn) Write(p []byte) (int, error) {
	select {
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	default:
	}
	if uint64(len(p)) > uint64(c.maxFrame) {
		return 0, fmt.Errorf("grpc message exceeds %d bytes", c.maxFrame)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// gRPC 消息头（压缩标志 + 长度）之后是 gun 的 Hunk：字段 1、长度定界
	var hunkHeader [1 + binary.MaxVarintLen64]byte
	hunkHeader[0] = gunHunkTag
	hunkLen := 1 + binary.PutUvarint(hunkHeader[1:], uint64(len(p)))
	overhead := 5 + hunkLen
	frame := make([]byte, overhead+len(p))
	binary.BigEndian.PutUint32(frame[1:5], uint32(hunkLen+len(p)))
	copy(frame[5:], hunkHeader[:hunkLen])
	copy(frame[overhead:], p)
	n, err := c.writer.Write(frame)
	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if n < overhead {
		return 0, err
	}
	return n - overhead, err
}

// gunHunkTag 是 protobuf 字段 1、wire type 2（长度定界）的键：Hunk.data 与
// MultiHunk.data 都是它。
const gunHunkTag = 0x0a

// decodeGunHunk 从一条 gRPC 消息里取出 gun 传输的数据。
//
// Xray / V2Ray 定义的 gun 传输（sing-box、mihomo 的 gRPC 都按它实现）每条 gRPC
// 消息是 protobuf 的 Hunk{bytes data = 1}，TunMulti 是 MultiHunk{repeated bytes
// data = 1}——两者的线格式都是若干个「0x0a + varint 长度 + 数据」。以前这里把
// 消息体当裸数据，任何标准客户端连上来，协议层读到的第一个字节都是 0x0a：
// VLESS 报版本无效、Trojan 报口令格式错、VMess 认证失败。
func decodeGunHunk(message []byte) ([]byte, error) {
	var out []byte
	for first := true; len(message) > 0; first = false {
		if message[0] != gunHunkTag {
			return nil, fmt.Errorf("grpc gun message has unexpected field tag 0x%02x", message[0])
		}
		length, n := binary.Uvarint(message[1:])
		if n <= 0 || length > uint64(len(message)-1-n) {
			return nil, fmt.Errorf("grpc gun hunk length is invalid")
		}
		start := 1 + n
		chunk := message[start : start+int(length)]
		message = message[start+int(length):]
		if first && len(message) == 0 {
			// 常见情形：一条消息一个 Hunk，直接用切片，不复制
			return chunk, nil
		}
		out = append(out, chunk...)
	}
	return out, nil
}

func (c *grpcDuplexConn) Close() error                     { return c.body.Close() }
func (c *grpcDuplexConn) LocalAddr() net.Addr              { return xhttpAddr("pandora-grpc") }
func (c *grpcDuplexConn) RemoteAddr() net.Addr             { return xhttpAddr("grpc-client") }
func (c *grpcDuplexConn) SetDeadline(time.Time) error      { return nil }
func (c *grpcDuplexConn) SetReadDeadline(time.Time) error  { return nil }
func (c *grpcDuplexConn) SetWriteDeadline(time.Time) error { return nil }

func parseGRPCPath(raw map[string]any) (string, string, error) {
	path := strings.TrimSpace(rawString(raw, "grpc_path"))
	service := strings.TrimSpace(rawString(raw, "grpc_service_name"))
	if path == "" {
		if service == "" {
			service = "GunService"
		}
		path = "/" + strings.Trim(service, "/") + "/Tun"
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n") || len(path) > 512 {
		return "", "", fmt.Errorf("grpc path is invalid")
	}
	host := strings.TrimSpace(rawString(raw, "host"))
	if strings.ContainsAny(host, "\r\n") {
		return "", "", fmt.Errorf("grpc host contains control characters")
	}
	return path, host, nil
}

func serveNativeGRPC(listener net.Listener, path, host string, maxFrame uint32, h2cMode bool, onConn func(context.Context, net.Conn)) (*http.Server, error) {
	return serveNativeGRPCHosts(listener, path, []string{host}, maxFrame, h2cMode, onConn)
}

// serveNativeGRPCHosts 同 serveNativeGRPC，但 :authority 可以是几个名字之一
// （hosts 里有空串表示不检查）。VLESS REALITY 用它：标准客户端（sing-box、
// mihomo、Xray）把 REALITY 的 server name 当 :authority 发，而那个名字已经由
// REALITY 握手对照 server_names 校验过。
func serveNativeGRPCHosts(listener net.Listener, path string, hosts []string, maxFrame uint32, h2cMode bool, onConn func(context.Context, net.Conn)) (*http.Server, error) {
	if listener == nil || onConn == nil {
		return nil, fmt.Errorf("native grpc server requires listener and handler")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		contentType := strings.ToLower(req.Header.Get("Content-Type"))
		if req.Method != http.MethodPost || req.URL == nil || req.URL.Path != path || !requestHostMatchesAny(req.Host, hosts) || !strings.HasPrefix(contentType, "application/grpc") {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "grpc-status")
		encoding := strings.ToLower(strings.TrimSpace(req.Header.Get("grpc-encoding")))
		if encoding != "" && encoding != "identity" && encoding != "gzip" {
			http.Error(w, "unsupported grpc encoding", http.StatusUnsupportedMediaType)
			return
		}
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		onConn(req.Context(), newGRPCDuplexConnWithEncoding(req.Context(), req.Body, w, maxFrame, encoding))
		w.Header().Set("grpc-status", "0")
	})
	server := newInboundHTTPServer(handler, 64<<10)
	if h2cMode {
		server.Handler = h2c.NewHandler(handler, &http2.Server{})
	} else {
		if err := http2.ConfigureServer(server, &http2.Server{}); err != nil {
			return nil, err
		}
	}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}
