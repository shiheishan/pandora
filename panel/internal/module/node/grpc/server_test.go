package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1 << 20

// testCA 是测试用自签 CA，实现 CertAuthority。
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "example.test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

func (c *testCA) Certificate() *x509.Certificate {
	return c.cert
}

func (c *testCA) IssueServerCert(host string, now time.Time) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "example.test server"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
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

func (c *testCA) fingerprint() string {
	sum := sha256.Sum256(c.cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (c *testCA) clientCert(t *testing.T, serverID uuid.UUID) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(service.ServerSPIFFEID(serverID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: serverID.String()},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{u},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

type fakeEnroller struct {
	lastIn  service.EnrollInput
	lastReq string
	result  service.EnrollResult
	err     error
	panic   bool
	calls   int
}

func (f *fakeEnroller) Enroll(ctx context.Context, in service.EnrollInput) (service.EnrollResult, error) {
	f.calls++
	if f.panic {
		panic("enroll panic")
	}
	f.lastIn = in
	f.lastReq = reqid.From(ctx)
	if f.err != nil {
		return service.EnrollResult{}, f.err
	}
	return f.result, nil
}

type fakeAuthorizer struct {
	err      error
	serverID uuid.UUID
	serial   *big.Int
	calls    int
}

func (f *fakeAuthorizer) AuthorizeSession(ctx context.Context, serverID uuid.UUID, certSerial *big.Int) error {
	f.calls++
	f.serverID = serverID
	f.serial = certSerial
	return f.err
}

type fakeControl struct {
	nodev1.UnimplementedControlServiceServer
	serverID uuid.UUID
	reqID    string
	calls    int
}

func (f *fakeControl) Session(stream grpc.BidiStreamingServer[nodev1.SessionRequest, nodev1.SessionResponse]) error {
	f.calls++
	f.serverID, _ = ServerIDFrom(stream.Context())
	f.reqID = reqid.From(stream.Context())
	return nil
}

func startServer(t *testing.T, enr enroller, auth sessionAuthorizer, ctrl nodev1.ControlServiceServer, limiter *ratelimit.Limiter) (*bufconn.Listener, *testCA) {
	t.Helper()
	ca := newTestCA(t)
	lis := bufconn.Listen(bufSize)
	opts := Options{
		PublicHost: "node.example.test",
		CA:         ca,
		Logger:     zap.NewNop(),
		Limiter:    limiter,
	}
	if limiter != nil {
		opts.EnrollPerMinute = 2
	}
	srv, err := newServer(opts, enr, auth, ctrl)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = srv.ServeListener(lis)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return lis, ca
}

func dial(t *testing.T, lis *bufconn.Listener, tlsCfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func pinnedTLS(host, fp string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		ServerName:         host,
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			return verifyPinnedChain(cs.PeerCertificates, host, fp)
		},
	}
}

func verifyPinnedChain(certs []*x509.Certificate, host, fp string) error {
	if len(certs) == 0 {
		return errors.New("没有证书")
	}
	var ca *x509.Certificate
	for _, c := range certs {
		if c.IsCA && service.SPKIFingerprint(c) == fp {
			ca = c
			break
		}
	}
	if ca == nil {
		return errors.New("指纹不匹配")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err := certs[0].Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   host,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	return err
}

func TestEnrollPinnedFingerprintOK(t *testing.T) {
	id := uuid.MustParse("01234567-89ab-7def-8123-456789abcdef")
	enr := &fakeEnroller{
		result: service.EnrollResult{
			ServerID: id,
			ChainPEM: []byte("chain"),
			CAPEM:    []byte("ca"),
		},
	}
	lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, nil)
	conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
	reqID := "01234567-89ab-7def-8123-456789abcdef"
	ctx := metadata.AppendToOutgoingContext(context.Background(), reqid.MetadataKey, reqID)
	resp, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(ctx, &nodev1.EnrollRequest{
		Token:        "tok-example",
		CsrDer:       []byte{1, 2, 3},
		RequestId:    reqID,
		AgentVersion: "pdnd/1.0",
		Host: &nodev1.HostInfo{
			Hostname:    "host.example.test",
			Os:          "linux",
			Arch:        "amd64",
			CpuCores:    4,
			MemoryBytes: 8,
			DiskBytes:   9,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetServerId() != id.String() || resp.GetCertChainPem() == nil {
		t.Fatalf("resp=%v", resp)
	}
	if enr.lastIn.Token != "tok-example" || enr.lastReq != reqID {
		t.Fatalf("in=%+v req=%q", enr.lastIn, enr.lastReq)
	}
	if enr.lastIn.Host.Hostname != "host.example.test" || enr.lastIn.Host.OS != "linux" ||
		enr.lastIn.Host.Arch != "amd64" || enr.lastIn.Host.CPUCores != 4 ||
		enr.lastIn.Host.MemoryBytes != 8 || enr.lastIn.Host.DiskBytes != 9 {
		t.Fatalf("host=%+v", enr.lastIn.Host)
	}
}

func TestEnrollWrongFingerprintRejected(t *testing.T) {
	enr := &fakeEnroller{result: service.EnrollResult{ServerID: uuid.New()}}
	lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, nil)
	other := newTestCA(t)
	conn := dial(t, lis, pinnedTLS("node.example.test", other.fingerprint()))
	_, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t"})
	if err == nil {
		t.Fatal("expected error")
	}
	if enr.calls != 0 {
		t.Fatalf("enroller calls=%d", enr.calls)
	}
	_ = ca
}

func TestEnrollErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code codes.Code
	}{
		{service.ErrEnrollDenied, codes.PermissionDenied},
		{service.ErrEnrollInvalid, codes.InvalidArgument},
		{fmt.Errorf("x: %w", service.ErrEnrollDenied), codes.PermissionDenied},
		{errs.New(errs.Unavailable), codes.Unavailable},
		{errors.New("db down"), codes.Internal},
	}
	for _, tc := range cases {
		enr := &fakeEnroller{err: tc.err}
		lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, nil)
		conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
		_, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t"})
		if status.Code(err) != tc.code {
			t.Fatalf("err=%v code=%v want %v", err, status.Code(err), tc.code)
		}
	}
}

func TestEnrollPanicRecovered(t *testing.T) {
	enr := &fakeEnroller{panic: true}
	lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, nil)
	conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
	cli := nodev1.NewEnrollmentServiceClient(conn)
	_, err := cli.Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code=%v", status.Code(err))
	}
	enr.panic = false
	enr.result = service.EnrollResult{ServerID: uuid.New()}
	_, err = cli.Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t2"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnrollRateLimited(t *testing.T) {
	_, rdb := testkit.Redis(t)
	lim := ratelimit.New(rdb)
	enr := &fakeEnroller{result: service.EnrollResult{ServerID: uuid.New()}}
	lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, lim)
	conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
	cli := nodev1.NewEnrollmentServiceClient(conn)
	for i := 0; i < 2; i++ {
		_, err := cli.Enroll(context.Background(), &nodev1.EnrollRequest{Token: fmt.Sprintf("t%d", i)})
		if err != nil {
			t.Fatalf("i=%d err=%v", i, err)
		}
	}
	_, err := cli.Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t3"})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code=%v", status.Code(err))
	}
}

func TestEnrollReqIDGenerated(t *testing.T) {
	enr := &fakeEnroller{result: service.EnrollResult{ServerID: uuid.New()}}
	lis, ca := startServer(t, enr, &fakeAuthorizer{}, &fakeControl{}, nil)
	conn := dial(t, lis, pinnedTLS("node.example.test", ca.fingerprint()))
	var hdr metadata.MD
	_, err := nodev1.NewEnrollmentServiceClient(conn).Enroll(context.Background(), &nodev1.EnrollRequest{Token: "t"}, grpc.Header(&hdr))
	if err != nil {
		t.Fatal(err)
	}
	if !reqid.Valid(enr.lastReq) {
		t.Fatalf("req_id=%q", enr.lastReq)
	}
	if v := hdr.Get(reqid.MetadataKey); len(v) != 1 || v[0] != enr.lastReq {
		t.Fatalf("header=%v last=%q", v, enr.lastReq)
	}
}
