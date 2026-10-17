package panel

// 测 helloSendError：Send Hello 返回 io.EOF 时再 Recv 读出真实状态与 reenroll trailer。

import (
	"errors"
	"io"
	"testing"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeHelloStream struct {
	recvErr   error
	trailer   metadata.MD
	recvCalls int
}

func (f *fakeHelloStream) Recv() (*nodev1.SessionResponse, error) {
	f.recvCalls++
	return nil, f.recvErr
}

func (f *fakeHelloStream) Trailer() metadata.MD {
	return f.trailer
}

func TestHelloSendError(t *testing.T) {
	t.Run("eof_reenroll_trailer", func(t *testing.T) {
		st := &fakeHelloStream{
			recvErr: status.Error(codes.PermissionDenied, "x"),
			trailer: metadata.Pairs(trailerReenroll, "required"),
		}
		err := helloSendError(io.EOF, st)
		if !errors.Is(err, errReenrollRequired) {
			t.Fatalf("errors.Is(errReenrollRequired)=false, err=%v", err)
		}
		if st.recvCalls != 1 {
			t.Fatalf("recvCalls=%d", st.recvCalls)
		}
	})

	t.Run("eof_unavailable_no_trailer", func(t *testing.T) {
		rerr := status.Error(codes.Unavailable, "y")
		st := &fakeHelloStream{recvErr: rerr}
		err := helloSendError(io.EOF, st)
		if errors.Is(err, errReenrollRequired) {
			t.Fatal("不应识别为 reenroll")
		}
		if !errors.Is(err, rerr) {
			t.Fatalf("errors.Is(rerr)=false, err=%v", err)
		}
		if st.recvCalls != 1 {
			t.Fatalf("recvCalls=%d", st.recvCalls)
		}
	})

	t.Run("non_eof_no_recv", func(t *testing.T) {
		boom := errors.New("boom")
		st := &fakeHelloStream{}
		err := helloSendError(boom, st)
		if !errors.Is(err, boom) {
			t.Fatalf("errors.Is(boom)=false, err=%v", err)
		}
		if st.recvCalls != 0 {
			t.Fatalf("recvCalls=%d", st.recvCalls)
		}
	})

	t.Run("eof_permission_denied_without_trailer", func(t *testing.T) {
		rerr := status.Error(codes.PermissionDenied, "z")
		st := &fakeHelloStream{recvErr: rerr}
		err := helloSendError(io.EOF, st)
		if errors.Is(err, errReenrollRequired) {
			t.Fatal("没有 trailer 不应识别为 reenroll")
		}
		if !errors.Is(err, rerr) {
			t.Fatalf("errors.Is(rerr)=false, err=%v", err)
		}
	})
}
