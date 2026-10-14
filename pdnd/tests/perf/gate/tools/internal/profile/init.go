package profile

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validateRealityDest(dest string) error {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return fmt.Errorf("reality dest 不能为空")
	}
	if _, _, err := net.SplitHostPort(dest); err != nil {
		return fmt.Errorf("reality dest 必须是 host:port: %w", err)
	}
	return nil
}

// Init 生成 CA/叶子证书、REALITY 密钥，写入 profile.json 与 certs/，返回填好 Dir 的 Profile。
func Init(o InitOptions) (*Profile, error) {
	if strings.TrimSpace(o.Dir) == "" {
		return nil, fmt.Errorf("Dir 不能为空")
	}
	if err := validateRealityDest(o.RealityDest); err != nil {
		return nil, err
	}

	seed := o.Seed
	if seed == "" {
		seed = "nodegate"
	}
	users := o.Users
	if users == 0 {
		users = 5000
	}
	deltaSize := o.DeltaSize
	if deltaSize == 0 {
		deltaSize = 500
	}
	portBase := o.PortBase
	if portBase == 0 {
		portBase = 20000
	}
	protocols := o.Protocols
	if len(protocols) == 0 {
		protocols = append([]string(nil), All...)
	}

	p := &Profile{
		Version:    1,
		Seed:       seed,
		Users:      users,
		DeltaSize:  deltaSize,
		PortBase:   portBase,
		Protocols:  protocols,
		ServerName: ServerName,
	}
	if err := p.validate(); err != nil {
		return nil, err
	}

	absDir, err := filepath.Abs(o.Dir)
	if err != nil {
		return nil, err
	}
	p.Dir = absDir

	certDir := filepath.Join(absDir, "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return nil, err
	}

	caDER, caPEM, caKey, err := generateCA()
	if err != nil {
		return nil, err
	}
	leafDER, leafPEM, leafKey, err := generateLeafCert(caDER, caKey)
	if err != nil {
		return nil, err
	}

	caPath := filepath.Join(certDir, "ca.crt")
	nodeCrtPath := filepath.Join(certDir, "node.crt")
	nodeKeyPath := filepath.Join(certDir, "node.key")

	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		return nil, err
	}
	chain := append(leafPEM, caPEM...)
	if err := os.WriteFile(nodeCrtPath, chain, 0o600); err != nil {
		return nil, err
	}
	keyPEM, err := marshalECPrivateKey(leafKey)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(nodeKeyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}

	leafSum := sha256.Sum256(leafDER)
	p.Cert = Cert{
		CAFile:     "certs/ca.crt",
		CertFile:   "certs/node.crt",
		KeyFile:    "certs/node.key",
		LeafSHA256: hex.EncodeToString(leafSum[:]),
	}

	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shortBytes := make([]byte, 8)
	if _, err := rand.Read(shortBytes); err != nil {
		return nil, err
	}
	p.Reality = Reality{
		ServerName: RealityServerName,
		PrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()),
		PublicKey:  base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		ShortID:    hex.EncodeToString(shortBytes),
		Dest:       strings.TrimSpace(o.RealityDest),
	}

	nodeCertDir := strings.TrimSpace(o.NodeCertDir)
	if nodeCertDir == "" {
		nodeCertDir = certDir
	} else {
		nodeCertDir, err = filepath.Abs(nodeCertDir)
		if err != nil {
			return nil, err
		}
	}
	p.NodeCertPath = filepath.Join(nodeCertDir, "node.crt")
	p.NodeKeyPath = filepath.Join(nodeCertDir, "node.key")

	profilePath := filepath.Join(absDir, "profile.json")
	enc, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, err
	}
	enc = append(enc, '\n')
	if err := os.WriteFile(profilePath, enc, 0o600); err != nil {
		return nil, err
	}
	return p, nil
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		panic(err)
	}
	return n.Add(n, big.NewInt(1))
}

func generateCA() ([]byte, []byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "nodegate test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return der, pemBytes, key, nil
}

func generateLeafCert(caDER []byte, caKey *ecdsa.PrivateKey) ([]byte, []byte, *ecdsa.PrivateKey, error) {
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: ServerName},
		DNSNames:     []string{ServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return der, pemBytes, key, nil
}

func marshalECPrivateKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}
