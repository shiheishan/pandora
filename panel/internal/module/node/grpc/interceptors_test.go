package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestRequiresClientCert(t *testing.T) {
	if requiresClientCert(nodev1.EnrollmentService_Enroll_FullMethodName) {
		t.Fatal("Enroll should not require client cert")
	}
	if !requiresClientCert(nodev1.ControlService_Session_FullMethodName) {
		t.Fatal("Session should require client cert")
	}
}

func serverTLSOnly(ca *testCA) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate())
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: "node.example.test",
	}
}

func clientMTLS(t *testing.T, ca *testCA, serverID uuid.UUID) (*tls.Config, *x509.Certificate) {
	cert := ca.clientCert(t, serverID)
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate())
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		ServerName:   "node.example.test",
		Certificates: []tls.Certificate{cert},
	}, leaf
}

func TestControlRequiresClientCert(t *testing.T) {
	auth := &fakeAuthorizer{}
	ctrl := &fakeControl{}
	lis, ca := startServer(t, &fakeEnroller{}, auth, ctrl, nil)
	conn := dial(t, lis, serverTLSOnly(ca))
	stream, err := nodev1.NewControlServiceClient(conn).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v err=%v", status.Code(err), err)
	}
	if auth.calls != 0 {
		t.Fatalf("auth calls=%d", auth.calls)
	}
}

func TestControlAuthorized(t *testing.T) {
	id := uuid.MustParse("01234567-89ab-7def-8123-456789abcdef")
	auth := &fakeAuthorizer{}
	ctrl := &fakeControl{}
	lis, ca := startServer(t, &fakeEnroller{}, auth, ctrl, nil)
	tlsCfg, leaf := clientMTLS(t, ca, id)
	conn := dial(t, lis, tlsCfg)
	reqID := "01234567-89ab-7def-8123-456789abcdef"
	ctx := metadata.AppendToOutgoingContext(context.Background(), reqid.MetadataKey, reqID)
	stream, err := nodev1.NewControlServiceClient(conn).Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if err != nil && !errors.Is(err, io.EOF) {
		if status.Code(err) != codes.Canceled {
			t.Fatalf("recv err=%v", err)
		}
	}
	if ctrl.calls != 1 || ctrl.serverID != id || ctrl.reqID != reqID {
		t.Fatalf("ctrl calls=%d id=%v req=%q", ctrl.calls, ctrl.serverID, ctrl.reqID)
	}
	if auth.calls != 1 || auth.serverID != id {
		t.Fatalf("auth=%+v", auth)
	}
	if auth.serial == nil || auth.serial.Cmp(leaf.SerialNumber) != 0 {
		t.Fatalf("serial mismatch")
	}
}

func TestControlRevoked(t *testing.T) {
	id := uuid.New()
	auth := &fakeAuthorizer{err: service.ErrSessionRevoked}
	ctrl := &fakeControl{}
	lis, ca := startServer(t, &fakeEnroller{}, auth, ctrl, nil)
	tlsCfg, _ := clientMTLS(t, ca, id)
	conn := dial(t, lis, tlsCfg)
	stream, err := nodev1.NewControlServiceClient(conn).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code=%v", status.Code(err))
	}
	tr := stream.Trailer()
	if len(tr.Get(TrailerReenroll)) != 1 || tr.Get(TrailerReenroll)[0] != "required" {
		t.Fatalf("trailer=%v", tr)
	}
	if ctrl.calls != 0 {
		t.Fatalf("ctrl calls=%d", ctrl.calls)
	}
}

func TestControlAuthorizerError(t *testing.T) {
	id := uuid.New()
	auth := &fakeAuthorizer{err: errors.New("db down")}
	ctrl := &fakeControl{}
	lis, ca := startServer(t, &fakeEnroller{}, auth, ctrl, nil)
	tlsCfg, _ := clientMTLS(t, ca, id)
	conn := dial(t, lis, tlsCfg)
	stream, err := nodev1.NewControlServiceClient(conn).Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%v", status.Code(err))
	}
}

func TestControlRejectsForeignClientCert(t *testing.T) {
	id := uuid.New()
	auth := &fakeAuthorizer{}
	ctrl := &fakeControl{}
	lis, ca := startServer(t, &fakeEnroller{}, auth, ctrl, nil)
	// 客户端信任面板 CA（服务端证书能验过），但出示的是别的 CA 签的客户端证书：握手在服务端被拒
	other := newTestCA(t)
	tlsCfg := serverTLSOnly(ca)
	tlsCfg.Certificates = []tls.Certificate{other.clientCert(t, id)}
	conn := dial(t, lis, tlsCfg)
	stream, err := nodev1.NewControlServiceClient(conn).Session(context.Background())
	if err == nil {
		_, err = stream.Recv()
	}
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("expected failure, got %v", err)
	}
	if ctrl.calls != 0 || auth.calls != 0 {
		t.Fatalf("ctrl calls=%d auth calls=%d", ctrl.calls, auth.calls)
	}
}
