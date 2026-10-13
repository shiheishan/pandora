package service

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGenerateCAProperties(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	ca, keyDER, err := generateCA(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyDER) == 0 {
		t.Fatal("keyDER 为空")
	}
	if !ca.cert.IsCA {
		t.Fatal("IsCA 应为 true")
	}
	if ca.cert.Subject.CommonName != caCommonName {
		t.Fatalf("CN=%q", ca.cert.Subject.CommonName)
	}
	if ca.cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("算法 %v", ca.cert.PublicKeyAlgorithm)
	}
	pub, ok := ca.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatal("公钥须为 P-256")
	}
	wantDur := caValidity + certBackdate
	gotDur := ca.cert.NotAfter.Sub(ca.cert.NotBefore)
	if gotDur < wantDur-time.Minute || gotDur > wantDur+time.Minute {
		t.Fatalf("有效期约 10 年+回拨: got %v want ~%v", gotDur, wantDur)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(ca.Fingerprint()) {
		t.Fatalf("指纹格式: %q", ca.Fingerprint())
	}
}

func TestFingerprintMatchesOpenSSLRecipe(t *testing.T) {
	// 与 openssl x509 -in ca.pem -pubkey -noout | openssl pkey -pubin -outform DER | sha256sum 一致。
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ca, _, err := generateCA(now)
	if err != nil {
		t.Fatal(err)
	}
	hand := sha256.Sum256(ca.cert.RawSubjectPublicKeyInfo)
	want := "sha256:" + hex.EncodeToString(hand[:])
	if SPKIFingerprint(ca.cert) != want {
		t.Fatalf("SPKIFingerprint=%q want %q", SPKIFingerprint(ca.cert), want)
	}
	pub, ok := ca.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatal("非 ECDSA")
	}
	pkixDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pkixDER, ca.cert.RawSubjectPublicKeyInfo) {
		t.Fatal("MarshalPKIXPublicKey 与 RawSubjectPublicKeyInfo 不一致")
	}
}

func TestParseCARoundTrip(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ca1, keyDER, err := generateCA(now)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := parseCA(ca1.certPEM, keyDER)
	if err != nil {
		t.Fatal(err)
	}
	if ca1.Fingerprint() != ca2.Fingerprint() {
		t.Fatal("指纹往返不一致")
	}

	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKCS8PrivateKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseCA(ca1.certPEM, otherDER); err == nil {
		t.Fatal("错配私钥应失败")
	}

	serverID := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	leaf, err := ca1.issueClientCert(&otherKey.PublicKey, serverID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseCA(leaf.PEM, keyDER); err == nil {
		t.Fatal("叶子 PEM 不能当 CA 解析")
	}
}

func TestIssueClientCert(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	ca, _, err := generateCA(now)
	if err != nil {
		t.Fatal(err)
	}
	serverID := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ic1, err := ca.issueClientCert(&nodeKey.PublicKey, serverID, now)
	if err != nil {
		t.Fatal(err)
	}
	wantURI := ServerSPIFFEID(serverID)
	if len(ic1.Cert.URIs) != 1 || ic1.Cert.URIs[0].String() != wantURI {
		t.Fatalf("URI SAN: %v want %s", ic1.Cert.URIs, wantURI)
	}
	gotID, ok := ServerIDFromCert(ic1.Cert)
	if !ok || gotID != serverID {
		t.Fatalf("ServerIDFromCert: %v %v", gotID, ok)
	}
	if len(ic1.Cert.ExtKeyUsage) != 1 || ic1.Cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("EKU: %v", ic1.Cert.ExtKeyUsage)
	}
	if ic1.Cert.IsCA {
		t.Fatal("IsCA 应为 false")
	}
	if ic1.Cert.NotAfter.Sub(ic1.Cert.NotBefore) < ClientCertValidity-time.Minute {
		t.Fatal("有效期应约 30 天")
	}
	if ic1.Cert.SerialNumber.Sign() <= 0 || ic1.Cert.SerialNumber.BitLen() > 128 {
		t.Fatalf("序列号: %s", ic1.Cert.SerialNumber.String())
	}
	if ic1.SerialHex != ic1.Cert.SerialNumber.Text(16) {
		t.Fatalf("SerialHex=%q cert=%q", ic1.SerialHex, ic1.Cert.SerialNumber.Text(16))
	}

	nodeKey2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ic2, err := ca.issueClientCert(&nodeKey2.PublicKey, serverID, now)
	if err != nil {
		t.Fatal(err)
	}
	if ic1.Cert.SerialNumber.Cmp(ic2.Cert.SerialNumber) == 0 {
		t.Fatal("两次签发序列号应不同")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	_, err = ic1.Cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: now,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestServerIDFromCertRejects(t *testing.T) {
	id := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	good, _ := url.Parse(ServerSPIFFEID(id))

	cases := []struct {
		name string
		cert *x509.Certificate
	}{
		{"无 URI", &x509.Certificate{URIs: nil}},
		{"空 URIs", &x509.Certificate{URIs: []*url.URL{}}},
		{"两个 URI", &x509.Certificate{URIs: []*url.URL{good, good}}},
		{"前缀不对", &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://other/server/"+id.String())}}},
		{"大写 uuid", &x509.Certificate{URIs: []*url.URL{mustURL(t, "spiffe://pandora/server/"+strings.ToUpper(id.String()))}}},
	}
	for _, tc := range cases {
		if _, ok := ServerIDFromCert(tc.cert); ok {
			t.Fatalf("%s 应拒绝", tc.name)
		}
	}
}

func TestParseNodeCSR(t *testing.T) {
	makeCSR := func(curve elliptic.Curve) ([]byte, error) {
		key, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return nil, err
		}
		tpl := &x509.CertificateRequest{Subject: pkix.Name{CommonName: "node.example.test"}}
		return x509.CreateCertificateRequest(rand.Reader, tpl, key)
	}

	der, err := makeCSR(elliptic.P256())
	if err != nil {
		t.Fatal(err)
	}
	pub, sum, err := parseNodeCSR(der)
	if err != nil {
		t.Fatal(err)
	}
	// 摘要是公钥 SPKI 的，与用它签出的证书的 spki_sha256 一致；同一把钥匙重签的 CSR 摘要不变
	ca, _, err := generateCA(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	iss, err := ca.issueClientCert(pub, uuid.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sum != iss.SPKISHA256 {
		t.Fatalf("CSR 摘要 %s 与证书 spki %s 不一致", sum, iss.SPKISHA256)
	}

	der384, err := makeCSR(elliptic.P384())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseNodeCSR(der384); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("P-384: %v", err)
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "node.example.test"},
	}, rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseNodeCSR(rsaCSR); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("RSA: %v", err)
	}

	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "node.example.test"},
	}, edPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := parseNodeCSR(edCSR); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("Ed25519: %v", err)
	}

	bad := append([]byte(nil), der...)
	bad[len(bad)-1] ^= 0xff
	if _, _, err := parseNodeCSR(bad); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("坏签名: %v", err)
	}

	if _, _, err := parseNodeCSR(nil); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("空 DER: %v", err)
	}
	long := make([]byte, 4097)
	if _, _, err := parseNodeCSR(long); !errors.Is(err, errCSRInvalid) {
		t.Fatalf("超长 DER: %v", err)
	}
}

func TestIssueServerCert(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	ca, _, err := generateCA(now)
	if err != nil {
		t.Fatal(err)
	}

	tc, err := ca.IssueServerCert("node.example.test", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Leaf.DNSNames) != 1 || tc.Leaf.DNSNames[0] != "node.example.test" {
		t.Fatalf("DNS: %v", tc.Leaf.DNSNames)
	}
	if len(tc.Leaf.IPAddresses) != 0 {
		t.Fatalf("IP 应为空: %v", tc.Leaf.IPAddresses)
	}

	tcIP, err := ca.IssueServerCert("203.0.113.7", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(tcIP.Leaf.IPAddresses) != 1 || tcIP.Leaf.IPAddresses[0].String() != "203.0.113.7" {
		t.Fatalf("IP: %v", tcIP.Leaf.IPAddresses)
	}
	if len(tcIP.Leaf.DNSNames) != 0 {
		t.Fatalf("DNS 应为空: %v", tcIP.Leaf.DNSNames)
	}

	if len(tc.Certificate) != 2 {
		t.Fatalf("链长度 %d", len(tc.Certificate))
	}
	if !bytes.Equal(tc.Certificate[1], ca.cert.Raw) {
		t.Fatal("链第二段应为 CA DER")
	}

	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	_, err = tc.Leaf.Verify(x509.VerifyOptions{
		Roots:       roots,
		DNSName:     "node.example.test",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	})
	if err != nil {
		t.Fatalf("Verify DNS: %v", err)
	}

	if _, err := ca.IssueServerCert("", now); err == nil {
		t.Fatal("空 host 应报错")
	}
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
