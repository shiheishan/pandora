package ca42runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

type measuredReaderAt struct {
	data       []byte
	maxRequest int
}

func (r *measuredReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if len(p) > r.maxRequest {
		r.maxRequest = len(p)
	}
	if offset >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[offset:])
	if n != len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestHashReaderAtIsBoundedAndExact(t *testing.T) {
	data := bytes.Repeat([]byte("pandora-ca42-stream\n"), 400000)
	reader := &measuredReaderAt{data: data}
	hasher := sha256.New()
	if err := hashReaderAt(context.Background(), hasher, reader, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(data)
	if !bytes.Equal(hasher.Sum(nil), expected[:]) {
		t.Fatal("stream digest mismatch")
	}
	if reader.maxRequest > artifactHashBufferSize {
		t.Fatalf("read request exceeded fixed buffer: %d", reader.maxRequest)
	}
}

func TestHashReaderAtRejectsInvalidCancellationAndShortRead(t *testing.T) {
	if err := hashReaderAt(nil, sha256.New(), bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hashReaderAt(ctx, sha256.New(), bytes.NewReader([]byte("x")), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
	if err := hashReaderAt(context.Background(), sha256.New(), bytes.NewReader([]byte("x")), 2); err == nil {
		t.Fatal("short source accepted")
	}
}
