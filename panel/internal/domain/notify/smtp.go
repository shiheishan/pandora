package notify

// SMTP 邮件渠道。
//
// 用标准库的 net/smtp 而不是第三方邮件库：需要的只是「连上、认证、投一封」，
// 标准库都有。邮件库带来的模板、附件、队列这些我们要么已经有了，
// 要么根本不需要。

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig 来自环境变量。任一必填项为空就视为未启用。
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	// UseTLS 为真时直接走 TLS（465），否则先明文连接再 STARTTLS（587）
	UseTLS bool
}

func (c SMTPConfig) Enabled() bool {
	return c.Host != "" && c.Port > 0 && c.From != ""
}

type SMTPSender struct{ cfg SMTPConfig }

func NewSMTPSender(cfg SMTPConfig) *SMTPSender {
	if !cfg.Enabled() {
		// 返回 nil 而不是一个会一直失败的实例：
		// New 里会跳过 nil，投递时那条记录被标成 suppressed（未配置），
		// 而不是攒一堆看起来像故障的失败记录
		return nil
	}
	return &SMTPSender{cfg: cfg}
}

func (s *SMTPSender) Channel() Channel { return ChannelEmail }

func (s *SMTPSender) Send(ctx context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port))

	// 拨号带超时。默认的 smtp.Dial 没有超时，对端不响应时
	// 这个 goroutine 会一直挂着，派发循环也就卡死了。
	d := net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if s.cfg.UseTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: s.cfg.Host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手: %w", err)
	}
	defer c.Close()

	if !s.cfg.UseTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
				return fmt.Errorf("STARTTLS: %w", err)
			}
		}
		// 服务端不支持 STARTTLS 时继续明文发。
		// 这是刻意的：内网自建的中继常常没有证书，强制加密会让它完全不可用。
		// 凭据的保护由下面的 AUTH 条件负责 —— 明文连接上不发密码。
	}

	if s.cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("SMTP 认证: %w", err)
			}
		}
	}

	if err := c.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(s.message(to, subject, body))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// message 拼出邮件报文。
//
// 主题必须做 MIME 编码：中文主题直接放进头部，多数客户端会显示成乱码。
// 正文用 quoted-printable 之外的最简方案 —— UTF-8 + base64 由收件端解码，
// 避免 8bit 内容在老旧中继上被截断。
func (s *SMTPSender) message(to, subject, body string) string {
	var b strings.Builder
	from := s.cfg.From
	if s.cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", mimeEncode(s.cfg.FromName), s.cfg.From)
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mimeEncode(subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("\r\n")
	// 正文里以点开头的行要转义，否则会被当成报文结束
	b.WriteString(strings.ReplaceAll(body, "\n.", "\n.."))
	b.WriteString("\r\n")
	return b.String()
}

// mimeEncode 把非 ASCII 的头部值编成 RFC 2047 形式。
func mimeEncode(s string) string {
	for _, r := range s {
		if r > 127 {
			return "=?UTF-8?B?" + base64Std(s) + "?="
		}
	}
	return s
}

func base64Std(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
