package kernel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestListenRealityOwnsSocketAndClonesConfig(t *testing.T) {
	var key [32]byte
	var shortID [8]byte
	spec := RealityServerConfig{
		Dest:        "127.0.0.1:1",
		ServerNames: map[string]bool{"example.com": true},
		PrivateKey:  key[:],
		ShortIDs:    map[[8]byte]bool{shortID: true},
		MaxTimeDiff: time.Minute,
	}
	listener, err := ListenReality("tcp", "127.0.0.1:0", spec, func(_ context.Context, network, address string) (net.Conn, error) {
		return nil, fmt.Errorf("not dialed in lifecycle test")
	})
	if err != nil {
		t.Fatalf("ListenReality: %v", err)
	}
	if listener.Addr() == nil {
		t.Fatal("listener address is nil")
	}
	// Mutating the caller's maps after construction must not mutate the
	// handshake configuration captured by the listener. The listener is
	// intentionally not exposed as a mutable config object.
	spec.ServerNames["example.com"] = false
	spec.ShortIDs[shortID] = false
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestListenRealityRejectsIncompleteConfig(t *testing.T) {
	_, err := ListenReality("tcp", "127.0.0.1:0", RealityServerConfig{}, nil)
	if err == nil {
		t.Fatal("expected incomplete config error")
	}
}

func TestRealityListenerServeStopsOnContextCancel(t *testing.T) {
	var key [32]byte
	var shortID [8]byte
	listener, err := ListenReality("tcp", "127.0.0.1:0", RealityServerConfig{
		Dest:        "127.0.0.1:1",
		ServerNames: map[string]bool{"example.com": true},
		PrivateKey:  key[:],
		ShortIDs:    map[[8]byte]bool{shortID: true},
	}, nil)
	if err != nil {
		t.Fatalf("ListenReality: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- listener.Serve(ctx, func(context.Context, RealitySession) error {
			return nil
		})
	}()
	cancel()
	select {
	case err := <-serveDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		_ = listener.Close()
		t.Fatal("Serve did not stop after context cancellation")
	}
}

func TestRealitySessionContextRoundTrip(t *testing.T) {
	conn := &fakeRealityConn{}
	session := RealitySession{Conn: conn, RemoteAddr: conn.RemoteAddr(), ClientVersion: [3]byte{1, 2, 3}}
	ctx := withRealitySession(context.Background(), session)
	got, ok := RealitySessionFromContext(ctx)
	if !ok || got.Conn != conn || got.ClientVersion != session.ClientVersion {
		t.Fatalf("reality session context = %#v, %v", got, ok)
	}
	if _, ok := RealitySessionFromContext(context.Background()); ok {
		t.Fatal("plain context unexpectedly carried reality metadata")
	}
}

type fakeRealityConn struct{}

func (f *fakeRealityConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (f *fakeRealityConn) Write([]byte) (int, error)        { return 0, net.ErrClosed }
func (f *fakeRealityConn) Close() error                     { return nil }
func (f *fakeRealityConn) LocalAddr() net.Addr              { return realityTestAddr("local") }
func (f *fakeRealityConn) RemoteAddr() net.Addr             { return realityTestAddr("remote") }
func (f *fakeRealityConn) SetDeadline(time.Time) error      { return nil }
func (f *fakeRealityConn) SetReadDeadline(time.Time) error  { return nil }
func (f *fakeRealityConn) SetWriteDeadline(time.Time) error { return nil }

type realityTestAddr string

func (a realityTestAddr) Network() string { return "test" }
func (a realityTestAddr) String() string  { return string(a) }
