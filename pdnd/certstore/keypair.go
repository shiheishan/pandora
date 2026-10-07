package certstore

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// maxPEMSize 限制单个证书或私钥文件的大小，防止误配到大文件时把内存吃满。
const maxPEMSize = 1 << 20

// loadError 带错误码，供心跳上报。
type loadError struct {
	code string
	err  error
}

func (e *loadError) Error() string { return e.err.Error() }
func (e *loadError) Unwrap() error { return e.err }

func newLoadError(code string, err error) error { return &loadError{code: code, err: err} }

// errorCode 取错误码，未分类的按读失败算。
func errorCode(err error) string {
	var le *loadError
	if errors.As(err, &le) {
		return le.code
	}
	return ErrCodeReadFailed
}

// parseKeyPair 自检证书链与私钥：PEM 可解析、私钥与叶子证书配对、叶子证书在 now 时没过期。
func parseKeyPair(chainPEM, keyPEM []byte, now time.Time) (*loaded, error) {
	if len(chainPEM) > maxPEMSize || len(keyPEM) > maxPEMSize {
		return nil, newLoadError(ErrCodeBadPEM, errors.New("certificate or key file is too large"))
	}
	cert, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		// crypto/tls 对「私钥与证书公钥不配对」给出固定文案，单独归类，便于运维定位。
		if strings.Contains(err.Error(), "does not match") {
			return nil, newLoadError(ErrCodeKeyMismatch, err)
		}
		return nil, newLoadError(ErrCodeBadPEM, err)
	}
	leaf := cert.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return nil, newLoadError(ErrCodeBadPEM, err)
		}
		cert.Leaf = leaf
	}
	if !now.Before(leaf.NotAfter) {
		return nil, newLoadError(ErrCodeExpired, fmt.Errorf("leaf certificate expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339)))
	}
	fp := sha256.Sum256(cert.Certificate[0])
	return &loaded{
		cert: &cert, leaf: leaf, chainSHA256: ChainSHA256(chainPEM),
		fingerprint: hex.EncodeToString(fp[:]), notAfter: leaf.NotAfter.UTC(),
	}, nil
}

// ChainSHA256 是 fullchain PEM 原始字节的 SHA-256，标准 base64，与证书包的 chain_sha256 同口径。
func ChainSHA256(chainPEM []byte) string {
	sum := sha256.Sum256(chainPEM)
	return base64.StdEncoding.EncodeToString(sum[:])
}
