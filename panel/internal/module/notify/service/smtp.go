package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/google/uuid"
)

// SMTPConfig 是一次发信用的配置（口令为明文，只在内存里用）。
type SMTPConfig struct {
	Host        string
	Port        int
	Encryption  string // settingssvc.MailEncryptionSSL / TLS / None
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

// Message 是一封渲染好的纯文本邮件。
type Message struct {
	To      string
	Subject string
	Body    string
}

// SendError 是发信失败。Stage 是哪一步，Detail 是可以写日志、写库、给管理员看的原因（不含口令和完整地址）；
// Permanent 为真表示重试没有意义。
type SendError struct {
	Stage     string
	Detail    string
	Permanent bool
	err       error
}

func (e *SendError) Error() string { return e.Stage + "：" + e.Detail }
func (e *SendError) Unwrap() error { return e.err }

// smtpSender 用 net/smtp 发信。
type smtpSender struct {
	dialTimeout time.Duration
	ioTimeout   time.Duration
}

// newSMTPSender 返回生产用的发信器：拨号 10 秒、整次会话读写 30 秒超时。
func newSMTPSender() *smtpSender {
	return &smtpSender{dialTimeout: 10 * time.Second, ioTimeout: 30 * time.Second}
}

func (s *smtpSender) Send(ctx context.Context, cfg SMTPConfig, m Message) error {
	raw, err := buildMessage(cfg, m, time.Now())
	if err != nil {
		return &SendError{Stage: "组装邮件", Detail: err.Error(), Permanent: true, err: err}
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	switch cfg.Encryption {
	case settingssvc.MailEncryptionSSL:
		conn, err = (&tls.Dialer{
			NetDialer: &net.Dialer{Timeout: s.dialTimeout},
			Config:    tlsCfg,
		}).DialContext(ctx, "tcp", addr)
	case settingssvc.MailEncryptionTLS, settingssvc.MailEncryptionNone:
		conn, err = (&net.Dialer{Timeout: s.dialTimeout}).DialContext(ctx, "tcp", addr)
	default:
		return &SendError{Stage: "配置", Detail: "加密方式只能是 ssl / tls / none", Permanent: true}
	}
	if err != nil {
		return wrapErr(ctx, "连接", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.ioTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return wrapErr(ctx, "握手", err)
	}
	defer c.Close()

	if cfg.Encryption == settingssvc.MailEncryptionTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return &SendError{Stage: "STARTTLS", Detail: "服务器不支持 STARTTLS，请改用 ssl", Permanent: true}
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return wrapErr(ctx, "STARTTLS", err)
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			_, isTLS := c.TLSConnectionState()
			if !isTLS && !isLocalhost(cfg.Host) {
				return &SendError{Stage: "认证", Detail: "未加密的连接上不发送口令，请改用 ssl 或 tls", Permanent: true}
			}
			if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
				return wrapErr(ctx, "认证", err)
			}
		}
	}

	if err := c.Mail(cfg.FromAddress); err != nil {
		return wrapErr(ctx, "发件人", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return wrapErr(ctx, "收件人", err)
	}
	w, err := c.Data()
	if err != nil {
		return wrapErr(ctx, "发送内容", err)
	}
	if _, err = w.Write(raw); err != nil {
		return wrapErr(ctx, "发送内容", err)
	}
	if err = w.Close(); err != nil {
		return wrapErr(ctx, "发送内容", err)
	}
	_ = c.Quit()
	return nil
}

func wrapErr(ctx context.Context, stage string, err error) *SendError {
	if ctx.Err() != nil {
		return &SendError{Stage: stage, Detail: "已取消", err: ctx.Err()}
	}
	detail, perm := describeError(err)
	return &SendError{Stage: stage, Detail: detail, Permanent: perm, err: err}
}

func describeError(err error) (string, bool) {
	var te *textproto.Error
	if errors.As(err, &te) {
		msg := maskAddresses(truncateRunes(te.Msg, 200))
		return fmt.Sprintf("服务器回复 %d %s", te.Code, msg), te.Code >= 500
	}

	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return "证书不受信任或与服务器名不符", true
	}
	var he x509.HostnameError
	if errors.As(err, &he) {
		return "证书不受信任或与服务器名不符", true
	}
	var cie x509.CertificateInvalidError
	if errors.As(err, &cie) {
		return "证书不受信任或与服务器名不符", true
	}
	var cve *tls.CertificateVerificationError
	if errors.As(err, &cve) {
		return "证书不受信任或与服务器名不符", true
	}

	var rhe tls.RecordHeaderError
	if errors.As(err, &rhe) {
		return "TLS 握手失败，加密方式可能选错", false
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return "TLS 握手失败，加密方式可能选错", false
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "域名解析失败", false
	}

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "超时", false
	}

	if errors.Is(err, syscall.ECONNREFUSED) {
		return "连接被拒绝", false
	}
	if errors.Is(err, io.EOF) {
		return "服务器断开了连接", false
	}

	return "网络或协议错误", false
}

var addrPattern = regexp.MustCompile(`[^\s<>@"']+@[^\s<>@"']+`)

func maskAddresses(s string) string {
	return addrPattern.ReplaceAllString(s, "<地址>")
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func isLocalhost(host string) bool {
	host = strings.Trim(host, "[]")
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// checkBareAddress 检查 s 是不是一个裸邮箱地址（无显示名、无换行、≤254 字节）。
func checkBareAddress(s string) error {
	if strings.ContainsAny(s, "\r\n") {
		return errors.New("invalid")
	}
	if len(s) > 254 {
		return errors.New("invalid")
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return err
	}
	if a.Name != "" || a.Address != s {
		return errors.New("invalid")
	}
	return nil
}

func buildMessage(cfg SMTPConfig, m Message, now time.Time) ([]byte, error) {
	if err := checkBareAddress(m.To); err != nil {
		return nil, errors.New("收件地址格式不正确")
	}
	if err := checkBareAddress(cfg.FromAddress); err != nil {
		return nil, errors.New("发件地址格式不正确")
	}
	if strings.ContainsAny(m.Subject, "\r\n") || strings.ContainsAny(cfg.FromName, "\r\n") {
		return nil, errors.New("邮件头不能换行")
	}

	fromLine := "From: " + (&mail.Address{Name: cfg.FromName, Address: cfg.FromAddress}).String() + "\r\n"
	toLine := "To: " + m.To + "\r\n"
	subjLine := "Subject: " + mime.BEncoding.Encode("UTF-8", m.Subject) + "\r\n"
	dateLine := "Date: " + now.Format(time.RFC1123Z) + "\r\n"

	domain := cfg.FromAddress
	if i := strings.LastIndex(cfg.FromAddress, "@"); i >= 0 && i+1 < len(cfg.FromAddress) {
		domain = cfg.FromAddress[i+1:]
	}
	msgID := "Message-ID: <" + uuid.NewString() + "@" + domain + ">\r\n"

	var b strings.Builder
	b.WriteString(fromLine)
	b.WriteString(toLine)
	b.WriteString(subjLine)
	b.WriteString(dateLine)
	b.WriteString(msgID)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	b.WriteString("\r\n")

	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end])
		b.WriteString("\r\n")
	}

	return []byte(b.String()), nil
}
