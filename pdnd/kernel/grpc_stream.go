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
			c.pending = frame
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
		c.pending = decoded
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
	frame := make([]byte, 5+len(p))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(p)))
	copy(frame[5:], p)
	n, err := c.writer.Write(frame)
	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if n < 5 {
		return 0, err
	}
	return n - 5, err
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
	if listener == nil || onConn == nil {
		return nil, fmt.Errorf("native grpc server requires listener and handler")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		contentType := strings.ToLower(req.Header.Get("Content-Type"))
		if req.Method != http.MethodPost || req.URL == nil || req.URL.Path != path || (host != "" && !strings.EqualFold(strings.TrimSpace(req.Host), host)) || !strings.HasPrefix(contentType, "application/grpc") {
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
	server := &http.Server{Handler: handler, MaxHeaderBytes: 64 << 10}
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
