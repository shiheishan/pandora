package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 节点 CA 与证书（docs/architecture.md 第 11.1 节与节点通道契约）：CA 指纹 = "sha256:" + hex(SHA-256(SPKI DER))。

const (
	// caKeyAAD 是封存 CA 私钥的 AAD（用途串，改了就打不开库里的私钥）
	caKeyAAD     = "pandora/node-ca/key/v1"
	caCommonName = "Pandora Node CA"
	caValidity   = 10 * 365 * 24 * time.Hour
	// ClientCertValidity 是节点客户端证书有效期
	ClientCertValidity = 30 * 24 * time.Hour
	// ServerCertValidity 是 gRPC 服务端证书有效期（内存里签，到期前换新）
	ServerCertValidity = 30 * 24 * time.Hour
	// certBackdate 是 NotBefore 往前拨的量，容忍时钟偏差
	certBackdate = 5 * time.Minute
	spiffePrefix = "spiffe://pandora/server/"
)

// errCSRInvalid 是节点送来的 CSR 不合法（解析失败、签名坏、不是 ECDSA P-256）。
var errCSRInvalid = errors.New("node: CSR 不合法")

// CA 是面板的节点 CA；明文私钥只在内存里。
type CA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
	fp      string
}

// Certificate 返回 CA 证书。
func (c *CA) Certificate() *x509.Certificate {
	return c.cert
}

// CertPEM 返回 CA 证书 PEM 的副本。
func (c *CA) CertPEM() []byte {
	return append([]byte(nil), c.certPEM...)
}

// Fingerprint 返回 CA 指纹 "sha256:<64 位小写 hex>"。
func (c *CA) Fingerprint() string {
	return c.fp
}

// SPKIFingerprint 按契约算证书公钥指纹："sha256:" + 小写 hex(SHA-256(RawSubjectPublicKeyInfo))。
func SPKIFingerprint(cert *x509.Certificate) string {
	return "sha256:" + spkiSHA256Hex(cert)
}

// spkiSHA256Hex 返回 SPKI 的 SHA-256 小写 hex（不含 "sha256:" 前缀）。
func spkiSHA256Hex(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// ServerSPIFFEID 返回服务器的 SPIFFE ID：spiffe://pandora/server/<uuid>。
func ServerSPIFFEID(serverID uuid.UUID) string {
	return spiffePrefix + serverID.String()
}

// ServerIDFromCert 从客户端证书的 URI SAN 取服务器 id：恰好一个 URI，形如 spiffe://pandora/server/<小写 uuid>。
func ServerIDFromCert(cert *x509.Certificate) (uuid.UUID, bool) {
	if cert == nil || len(cert.URIs) != 1 {
		return uuid.Nil, false
	}
	u := cert.URIs[0]
	if u == nil || u.Scheme != "spiffe" || u.Host != "pandora" {
		return uuid.Nil, false
	}
	const pathPrefix = "/server/"
	if !strings.HasPrefix(u.Path, pathPrefix) || strings.Contains(u.Path[len(pathPrefix):], "/") {
		return uuid.Nil, false
	}
	uuidStr := u.Path[len(pathPrefix):]
	if uuidStr == "" {
		return uuid.Nil, false
	}
	parsed, err := uuid.Parse(uuidStr)
	if err != nil {
		return uuid.Nil, false
	}
	// 整串必须与规范形式一字不差（挡住大写 uuid、端口、查询串、片段这类变体）
	if parsed.String() != uuidStr || u.String() != ServerSPIFFEID(parsed) {
		return uuid.Nil, false
	}
	return parsed, true
}

// generateCA 现生成一个自签 CA 与 PKCS#8 私钥 DER。
func generateCA(now time.Time) (*CA, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	notBefore := now.Add(-certBackdate)
	notAfter := now.Add(caValidity)
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: caCommonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	ca := &CA{
		cert:    cert,
		certPEM: certPEM,
		key:     key,
		fp:      SPKIFingerprint(cert),
	}
	return ca, keyDER, nil
}

// parseCA 从证书 PEM 与 PKCS#8 私钥 DER 还原 CA。
func parseCA(certPEM, keyDER []byte) (*CA, error) {
	block, rest := pem.Decode(certPEM)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "CERTIFICATE" {
		return nil, errors.New("node: CA 证书 PEM 无效")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("node: 解析 CA 证书失败: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("node: 证书不是 CA")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, errors.New("node: CA 私钥无效")
	}
	key, ok := parsedKey.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("node: CA 私钥须为 ECDSA P-256")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("node: CA 私钥与证书公钥不匹配")
	}
	return &CA{
		cert:    cert,
		certPEM: append([]byte(nil), certPEM...),
		key:     key,
		fp:      SPKIFingerprint(cert),
	}, nil
}

// parseNodeCSR 解析并校验节点 CSR（DER）。
func parseNodeCSR(der []byte) (*ecdsa.PublicKey, string, error) {
	if len(der) == 0 || len(der) > 4096 {
		return nil, "", fmt.Errorf("%w: CSR 长度非法", errCSRInvalid)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, "", fmt.Errorf("%w: 解析失败", errCSRInvalid)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", fmt.Errorf("%w: 签名校验失败", errCSRInvalid)
	}
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, "", fmt.Errorf("%w: 须为 ECDSA P-256", errCSRInvalid)
	}
	sum := sha256.Sum256(der)
	return pub, hex.EncodeToString(sum[:]), nil
}

// randomSerial 返回随机的 128 位正整数序列号。
func randomSerial() (*big.Int, error) {
	for {
		buf := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, buf); err != nil {
			return nil, err
		}
		n := new(big.Int).SetBytes(buf)
		if n.Sign() > 0 {
			return n, nil
		}
	}
}

// issuedCert 是一张刚签好的客户端证书。
type issuedCert struct {
	Cert       *x509.Certificate
	PEM        []byte
	SerialHex  string
	SPKISHA256 string
}

// issueClientCert 用 CA 给服务器签客户端证书。
func (c *CA) issueClientCert(pub *ecdsa.PublicKey, serverID uuid.UUID, now time.Time) (issuedCert, error) {
	serial, err := randomSerial()
	if err != nil {
		return issuedCert{}, err
	}
	spiffe, err := url.Parse(ServerSPIFFEID(serverID))
	if err != nil {
		return issuedCert{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverID.String()},
		NotBefore:             now.Add(-certBackdate),
		NotAfter:              now.Add(ClientCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
		URIs:                  []*url.URL{spiffe},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, pub, c.key)
	if err != nil {
		return issuedCert{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return issuedCert{}, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return issuedCert{
		Cert:       leaf,
		PEM:        pemBytes,
		SerialHex:  leaf.SerialNumber.Text(16),
		SPKISHA256: spkiSHA256Hex(leaf),
	}, nil
}

// IssueServerCert 给 gRPC 服务端签一张证书（内存持有）。
func (c *CA) IssueServerCert(host string, now time.Time) (*tls.Certificate, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("node: 服务端证书 host 为空")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Pandora Node Gateway"},
		NotBefore:             now.Add(-certBackdate),
		NotAfter:              now.Add(ServerCertValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else {
		tpl.DNSNames = []string{host}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{
		Certificate: [][]byte{leafDER, c.cert.Raw},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}

// certChainPEM 返回叶子 PEM 后接 CA PEM。
func certChainPEM(c *CA, leafPEM []byte) []byte {
	out := make([]byte, 0, len(leafPEM)+len(c.certPEM))
	out = append(out, leafPEM...)
	out = append(out, c.certPEM...)
	return out
}
