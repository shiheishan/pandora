package tuic

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// captureLogger 记下 Error / Debug 级的日志行。
type captureLogger struct {
	logger.Logger
	mu    sync.Mutex
	lines []string
}

func (l *captureLogger) record(args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprint(args...))
}

func (l *captureLogger) Error(args ...any) { l.record(args...) }
func (l *captureLogger) Debug(args ...any) { l.record(args...) }

func (l *captureLogger) find(substr string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			return line, true
		}
	}
	return "", false
}

// 认证失败的日志不落完整 UUID（review-r3 #7，.claude/rules/pdnd-kernel.md「日志里不落
// 完整用户 IP 与凭据」：用户 UUID 就是口令）。上游写成 "unknown user <uuid>"。
func TestUnknownUserLogOmitsUUID(t *testing.T) {
	log := &captureLogger{Logger: logger.NOP()}
	addr := startTestServiceWithLogger(t, &countingHandler{}, log)
	unknown := [16]byte{0xde, 0xad, 0xbe, 0xef, 0x10, 0x32, 0x54, 0x76, 0x98, 0xba, 0xdc, 0xfe, 0x01, 0x23, 0x45, 0x67}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := NewClient(ClientOptions{
		Context: ctx, Dialer: N.SystemDialer, ServerAddress: M.ParseSocksaddr(addr),
		TLSConfig: &testServerTLS{std: &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}},
		UUID:      unknown, Password: testUserPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, err := client.DialConn(ctx, M.ParseSocksaddr("203.0.113.1:80")); err == nil {
		_, _ = c.Write([]byte("x"))
		defer c.Close()
	}
	var line string
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var ok bool
		if line, ok = log.find("unknown user"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("没等到认证失败的日志：%q", log.lines)
		}
	}
	for _, form := range []string{uuid.UUID(unknown).String(), hex.EncodeToString(unknown[:]), "deadbeef"} {
		if strings.Contains(strings.ToLower(line), form) {
			t.Fatalf("认证失败日志带了 UUID（%s）：%q", form, line)
		}
	}
}
