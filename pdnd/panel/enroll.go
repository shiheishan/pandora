package panel

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// EnrollOptions 是接入参数。
type EnrollOptions struct {
	// Panel 是面板节点通道地址 host:port；只给 host 时补 DefaultPanelPort。
	Panel string
	// CAFingerprint 是安装命令里给的面板 CA 指纹 sha256:<64 位小写 hex>。
	CAFingerprint string
	// Token 是一次性接入令牌；不打印、不进错误信息、不落盘（日志里只存它的哈希）。
	Token string
	// Dir 是身份目录，空即 DefaultIdentityDir。
	Dir          string
	AgentVersion string
	// Host 是机器信息，可为 nil。
	Host *nodev1.HostInfo
	// Dialer 只给测试注入；nil 用系统拨号。
	Dialer Dialer
	// Timeout 是单次 RPC 上限，0 即 30 秒。
	Timeout time.Duration
}

// enrollJournal 是接入日志：发请求之前落盘，崩溃或回包丢失后同令牌重跑原样重发同一个 request_id
// 与同一份 CSR（不重签：ECDSA 签名带随机数，重签出的 CSR 字节不同），面板按 request_id 加公钥比对，
// 幂等地返回同一张证书。不存明文令牌。
type enrollJournal struct {
	Format      int    `json:"format"`
	Panel       string `json:"panel"`
	CASHA256    string `json:"ca_sha256"`
	TokenSHA256 string `json:"token_sha256"`
	RequestID   string `json:"request_id"`
	KeyPEM      string `json:"key_pem"`
	// CSRDER 是第一次生成的 CSR 原字节（JSON 里是 base64）。
	CSRDER    []byte    `json:"csr_der"`
	CreatedAt time.Time `json:"created_at"`
}

const journalFormat = 2

// Enroll 用一次性令牌接入面板：按 CA 指纹钉住面板证书，提交本地生成的 CSR，拿回客户端证书写进身份目录。
func Enroll(ctx context.Context, o EnrollOptions) (*Identity, error) {
	token := strings.TrimSpace(o.Token)
	if token == "" {
		return nil, errors.New("接入令牌为空")
	}
	fp, err := ParseCAFingerprint(o.CAFingerprint)
	if err != nil {
		return nil, err
	}
	hostport, host, err := PanelHost(o.Panel)
	if err != nil {
		return nil, err
	}
	dir := o.Dir
	if dir == "" {
		dir = DefaultIdentityDir
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// MkdirAll 不改已有目录的权限：身份目录里有私钥，统一收紧到 0700。
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}

	j, err := loadOrCreateJournal(dir, hostport, fp, token)
	if err != nil {
		return nil, err
	}
	keyPEM := []byte(j.KeyPEM)

	opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(pinnedTLSConfig(host, fp)))}
	if o.Dialer != nil {
		opts = append(opts, grpc.WithContextDialer(o.Dialer))
	}
	conn, err := grpc.NewClient("passthrough:///"+hostport, opts...)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(rctx, &nodev1.EnrollRequest{
		Token:        token,
		CsrDer:       j.CSRDER,
		RequestId:    j.RequestID,
		Host:         o.Host,
		AgentVersion: o.AgentVersion,
	})
	if err != nil {
		// 日志留着：同令牌重跑沿用同一个 request_id，面板若已签发会返回同一张证书。
		return nil, fmt.Errorf("接入请求失败（可用同一令牌重跑）: %w", err)
	}

	serverID := resp.GetServerId()
	if _, err := uuid.Parse(serverID); err != nil {
		return nil, fmt.Errorf("面板返回的 server_id 不合法: %w", err)
	}
	if !bundleHasCA(resp.GetCaBundlePem(), fp) {
		return nil, errors.New("面板返回的 CA 与安装命令里的指纹不符")
	}
	if _, _, err := validateIdentity(serverID, resp.GetCertChainPem(), resp.GetCaBundlePem(), keyPEM); err != nil {
		return nil, fmt.Errorf("面板返回的证书不可用: %w", err)
	}
	if err := stageAndPromote(dir, map[string][]byte{
		certFile:     resp.GetCertChainPem(),
		caFile:       resp.GetCaBundlePem(),
		keyFile:      keyPEM,
		serverIDFile: []byte(serverID + "\n"),
	}); err != nil {
		return nil, err
	}
	if err := os.Remove(filepath.Join(dir, journalFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return LoadIdentity(dir)
}

// loadOrCreateJournal 读出同一次接入（面板、指纹、令牌都相同）的日志；没有或不是同一次就新建并先落盘。
func loadOrCreateJournal(dir, hostport, fp, token string) (*enrollJournal, error) {
	path := filepath.Join(dir, journalFile)
	tokenSum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(tokenSum[:])
	if body, err := os.ReadFile(path); err == nil {
		var j enrollJournal
		if json.Unmarshal(body, &j) == nil && j.Format == journalFormat &&
			j.Panel == hostport && j.CASHA256 == fp && j.TokenSHA256 == tokenHash &&
			j.RequestID != "" && csrMatchesKey(j.CSRDER, []byte(j.KeyPEM)) == nil {
			return &j, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// CN 留空：接入时还不知道 server_id。CSR 只在这里生成一次，之后重试原样重发。
	keyPEM, csr, err := newKeyAndCSR("")
	if err != nil {
		return nil, err
	}
	j := &enrollJournal{
		Format:      journalFormat,
		Panel:       hostport,
		CASHA256:    fp,
		TokenSHA256: tokenHash,
		RequestID:   uuid.NewString(),
		KeyPEM:      string(keyPEM),
		CSRDER:      csr,
		CreatedAt:   time.Now().UTC(),
	}
	body, err := json.Marshal(j)
	if err != nil {
		return nil, err
	}
	if err := WriteFileAtomic(path, body, 0o600); err != nil {
		return nil, err
	}
	return j, nil
}

// parseECKeyPEM 解析 PEM 编码的 EC 私钥。
func parseECKeyPEM(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("不是 PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

// csrMatchesKey 核对日志里的 CSR 签名完好、公钥就是日志里私钥的公钥；不符的日志作废重建。
func csrMatchesKey(csrDER, keyPEM []byte) error {
	key, err := parseECKeyPEM(keyPEM)
	if err != nil {
		return err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return err
	}
	if err := csr.CheckSignature(); err != nil {
		return err
	}
	if !key.PublicKey.Equal(csr.PublicKey) {
		return errors.New("CSR 的公钥与私钥不配")
	}
	return nil
}

// bundleHasCA 判断 PEM 里有没有一张指纹为 fp 的 CA 证书。
func bundleHasCA(bundle []byte, fp string) bool {
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err == nil && c.IsCA && CAFingerprint(c) == fp {
			return true
		}
	}
}

// CAFingerprint 返回 CA 证书的指纹：sha256: + 小写 hex(SHA-256(SPKI DER))。
func CAFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseCAFingerprint 校验指纹格式，返回规范化（小写）的 "sha256:<hex>"。
func ParseCAFingerprint(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	h, ok := strings.CutPrefix(s, "sha256:")
	if !ok {
		return "", errors.New("CA 指纹要以 sha256: 开头")
	}
	if len(h) != sha256.Size*2 {
		return "", fmt.Errorf("CA 指纹应为 %d 位十六进制", sha256.Size*2)
	}
	if _, err := hex.DecodeString(h); err != nil {
		return "", errors.New("CA 指纹不是十六进制")
	}
	return s, nil
}

// PanelHost 补缺省端口，返回拨号用的 host:port 与主机部分（IPv6 去方括号）。
func PanelHost(addr string) (hostport, host string, err error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "", errors.New("面板地址为空")
	}
	if strings.Contains(addr, "://") || strings.ContainsAny(addr, "/?#@ ") {
		return "", "", fmt.Errorf("面板地址 %q 应为 host 或 host:port", addr)
	}
	h, p, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		// 没有端口：裸主机、裸 IPv4、带或不带方括号的 IPv6。
		h = strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
		if strings.Contains(h, ":") && net.ParseIP(h) == nil {
			return "", "", fmt.Errorf("面板地址 %q 不合法: %v", addr, splitErr)
		}
		p = DefaultPanelPort
	}
	if h == "" {
		return "", "", fmt.Errorf("面板地址 %q 缺主机", addr)
	}
	if p == "" {
		return "", "", fmt.Errorf("面板地址 %q 缺端口", addr)
	}
	return net.JoinHostPort(h, p), h, nil
}

// pinnedTLSConfig 是接入用的 TLS 配置：不信系统根，只信指纹为 fp 的那张 CA。
// InsecureSkipVerify 只是跳过系统根校验，真正的校验在 VerifyConnection（握手完成、发令牌之前）。
func pinnedTLSConfig(host, fp string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         host,
		InsecureSkipVerify: true, //nolint:gosec // 由 VerifyConnection 按 CA 指纹校验
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPinnedChain(cs.PeerCertificates, host, fp)
		},
	}
}

// verifyPinnedChain 在面板发来的链里找指纹匹配的 CA，再用它（只用它）校验叶子的主机名与用途。
func verifyPinnedChain(certs []*x509.Certificate, host, fp string) error {
	if len(certs) == 0 {
		return errors.New("面板没有出示证书")
	}
	var ca *x509.Certificate
	for _, c := range certs {
		if c.IsCA && CAFingerprint(c) == fp {
			ca = c
			break
		}
	}
	if ca == nil {
		return errors.New("面板证书链里没有指纹匹配的 CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := certs[0].Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   host,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return fmt.Errorf("面板证书校验失败: %w", err)
	}
	return nil
}
