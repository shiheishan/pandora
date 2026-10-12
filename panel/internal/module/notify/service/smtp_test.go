package service

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
)

func TestBuildMessageSubjectDecode(t *testing.T) {
	cfg := SMTPConfig{FromAddress: "noreply@example.com", FromName: ""}
	m := Message{To: "u@example.com", Subject: "你好", Body: "hi"}
	raw, err := buildMessage(cfg, m, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	block := string(raw)
	for _, line := range strings.Split(block, "\r\n") {
		if strings.HasPrefix(line, "Subject: ") {
			dec := new(mime.WordDecoder)
			got, err := dec.DecodeHeader(line[len("Subject: "):])
			if err != nil {
				t.Fatal(err)
			}
			if got != "你好" {
				t.Fatalf("subject %q", got)
			}
			return
		}
	}
	t.Fatal("no Subject")
}

func TestBuildMessageBodyBase64CRLF(t *testing.T) {
	cfg := SMTPConfig{FromAddress: "a@b.com"}
	m := Message{To: "c@d.com", Subject: "s", Body: "line1\nline2"}
	raw, err := buildMessage(cfg, m, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(string(raw), "\r\n\r\n")
	if len(parts) < 2 {
		t.Fatal("no body")
	}
	b64 := strings.ReplaceAll(parts[1], "\r\n", "")
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\r\nline2\r\n"
	if string(dec) != want {
		t.Fatalf("body %q want %q", dec, want)
	}
}

func TestBuildMessageFromChineseName(t *testing.T) {
	cfg := SMTPConfig{FromAddress: "noreply@example.com", FromName: "发件人"}
	m := Message{To: "u@example.com", Subject: "s", Body: "b"}
	raw, err := buildMessage(cfg, m, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "From: ") {
			a, err := mail.ParseAddress(line[len("From: "):])
			if err != nil {
				t.Fatal(err)
			}
			if a.Name != "发件人" || a.Address != "noreply@example.com" {
				t.Fatalf("from %+v", a)
			}
			return
		}
	}
	t.Fatal("no From")
}

func TestBuildMessageBadTo(t *testing.T) {
	cfg := SMTPConfig{FromAddress: "a@b.com"}
	cases := []string{
		"a@b.com\r\nBcc: x@y.com",
		"Name <a@b.com>",
		"bad",
	}
	for _, to := range cases {
		_, err := buildMessage(cfg, Message{To: to, Subject: "s", Body: "b"}, time.Now())
		if err == nil {
			t.Fatalf("to %q", to)
		}
	}
}

func TestBuildMessageSubjectNewline(t *testing.T) {
	cfg := SMTPConfig{FromAddress: "a@b.com"}
	_, err := buildMessage(cfg, Message{To: "c@d.com", Subject: "a\nb", Body: "b"}, time.Now())
	if err == nil || err.Error() != "邮件头不能换行" {
		t.Fatalf("err %v", err)
	}
}

func TestValidAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"a@b.com": true, "a..b@x.com": false, "a,b@x.com": false, "a@b@x.com": false,
		"Name <a@b.com>": false, "a@b.com\r\nBcc: c@d.com": false,
	} {
		if got := ValidAddress(addr); got != want {
			t.Errorf("ValidAddress(%q) = %v", addr, got)
		}
	}
}

func TestDescribeError(t *testing.T) {
	d, perm := describeError(&textproto.Error{Code: 550, Msg: "5.1.1 <user@example.com>: Recipient address rejected"})
	if perm != true || strings.Contains(d, "user@example.com") || !strings.Contains(d, "<地址>") {
		t.Fatalf("550: %q perm=%v", d, perm)
	}
	_, perm = describeError(&textproto.Error{Code: 421, Msg: "try later"})
	if perm {
		t.Fatalf("421 permanent")
	}
	d, _ = describeError(&net.DNSError{})
	if d != "域名解析失败" {
		t.Fatalf("dns %q", d)
	}
	d, _ = describeError(io.EOF)
	if d != "服务器断开了连接" {
		t.Fatalf("eof %q", d)
	}
	d, _ = describeError(errors.New("dial tcp 192.0.2.1:25: boom"))
	if strings.Contains(d, "192.0.2.1") {
		t.Fatalf("leak %q", d)
	}
}

func TestSMTPSendIntegration(t *testing.T) {
	t.Run("none_ok", func(t *testing.T) {
		got := make(chan string, 1)
		host, port, stop := startFakeSMTP(t, fakeSMTPHandlers{
			startTLS:   false,
			onData:     func(s string) { got <- s },
			expectAuth: true,
		})
		defer stop()

		s := testSender()
		err := s.Send(context.Background(), SMTPConfig{
			Host:        host,
			Port:        port,
			Encryption:  settingssvc.MailEncryptionNone,
			Username:    "user",
			Password:    "secret",
			FromAddress: "from@example.com",
		}, Message{To: "to@example.com", Subject: "subj", Body: "body"})
		if err != nil {
			t.Fatal(err)
		}
		var data string
		select {
		case data = <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("服务器没收到 DATA")
		}
		if !strings.Contains(data, "Subject:") {
			t.Fatal("no subject in data")
		}
		if !strings.Contains(data, base64.StdEncoding.EncodeToString([]byte("body\r\n"))) {
			t.Fatal("no base64 body")
		}
	})

	t.Run("tls_no_starttls", func(t *testing.T) {
		host, port, stop := startFakeSMTP(t, fakeSMTPHandlers{startTLS: false})
		defer stop()

		s := testSender()
		err := s.Send(context.Background(), SMTPConfig{
			Host: host, Port: port, Encryption: settingssvc.MailEncryptionTLS,
			FromAddress: "from@example.com",
		}, Message{To: "to@example.com", Subject: "s", Body: "b"})
		var se *SendError
		if !errors.As(err, &se) || se.Stage != "STARTTLS" || !se.Permanent {
			t.Fatalf("err %v", err)
		}
	})

	t.Run("rcpt_550", func(t *testing.T) {
		host, port, stop := startFakeSMTP(t, fakeSMTPHandlers{
			startTLS: false,
			rcptCode: 550,
			rcptMsg:  "5.1.1 <a@b.com> rejected",
		})
		defer stop()

		s := testSender()
		err := s.Send(context.Background(), SMTPConfig{
			Host: host, Port: port, Encryption: settingssvc.MailEncryptionNone,
			FromAddress: "from@example.com",
		}, Message{To: "to@example.com", Subject: "s", Body: "b"})
		var se *SendError
		if !errors.As(err, &se) || se.Stage != "收件人" || !se.Permanent {
			t.Fatalf("err %v", err)
		}
		if strings.Contains(se.Detail, "a@b.com") {
			t.Fatalf("detail %q", se.Detail)
		}
	})

	t.Run("conn_refused", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		_, portStr, _ := net.SplitHostPort(ln.Addr().String())
		port, _ := net.LookupPort("tcp", portStr)
		_ = ln.Close()

		s := testSender()
		err = s.Send(context.Background(), SMTPConfig{
			Host: "127.0.0.1", Port: port, Encryption: settingssvc.MailEncryptionNone,
			FromAddress: "from@example.com",
		}, Message{To: "to@example.com", Subject: "s", Body: "b"})
		var se *SendError
		if !errors.As(err, &se) || se.Stage != "连接" {
			t.Fatalf("err %v", err)
		}
		if strings.Contains(se.Detail, "127.0.0.1") {
			t.Fatalf("detail %q", se.Detail)
		}
	})
}

// testSender 是超时较短的发信器，测试卡住时尽快失败。
func testSender() *smtpSender {
	return &smtpSender{dialTimeout: 2 * time.Second, ioTimeout: 5 * time.Second}
}

type fakeSMTPHandlers struct {
	startTLS   bool
	expectAuth bool
	rcptCode   int
	rcptMsg    string
	onData     func(string)
}

func startFakeSMTP(t *testing.T, h fakeSMTPHandlers) (host string, port int, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = net.LookupPort("tcp", portStr)
	if h.rcptCode == 0 {
		h.rcptCode = 250
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)
		writeLine(conn, "220 ok\r\n")
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			upper := strings.ToUpper(line)
			switch {
			case strings.HasPrefix(upper, "EHLO") || strings.HasPrefix(upper, "HELO"):
				if h.startTLS {
					writeLine(conn, "250-x\r\n250 STARTTLS\r\n")
				} else {
					writeLine(conn, "250-x\r\n250 AUTH PLAIN\r\n")
				}
			case strings.HasPrefix(upper, "STARTTLS"):
				writeLine(conn, "220 ready\r\n")
			case strings.HasPrefix(upper, "AUTH"):
				if h.expectAuth && !strings.HasPrefix(upper, "AUTH PLAIN") {
					t.Errorf("expected AUTH PLAIN, got %q", line)
				}
				writeLine(conn, "235 ok\r\n")
			case strings.HasPrefix(upper, "MAIL"):
				writeLine(conn, "250 ok\r\n")
			case strings.HasPrefix(upper, "RCPT"):
				if h.rcptCode >= 400 {
					writeLine(conn, fmtRcpt(h.rcptCode, h.rcptMsg))
				} else {
					writeLine(conn, "250 ok\r\n")
				}
			case upper == "DATA":
				writeLine(conn, "354 go\r\n")
				var msg strings.Builder
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" || l == ".\n" {
						break
					}
					msg.WriteString(l)
				}
				if h.onData != nil {
					h.onData(msg.String())
				}
				writeLine(conn, "250 ok\r\n")
			case strings.HasPrefix(upper, "QUIT"):
				writeLine(conn, "221 bye\r\n")
				return
			default:
				writeLine(conn, "502 unimplemented\r\n")
			}
		}
	}()
	return host, port, func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

func writeLine(c net.Conn, s string) {
	_, _ = c.Write([]byte(s))
}

func fmtRcpt(code int, msg string) string {
	if msg == "" {
		msg = "error"
	}
	return fmt.Sprintf("%d %s\r\n", code, msg)
}
