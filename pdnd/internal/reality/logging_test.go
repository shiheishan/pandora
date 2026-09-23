package reality

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
)

var realityStdoutMu sync.Mutex

// TestServerShowIsSilent protects the compatibility field from regressing
// into a per-handshake trace switch.  Native production must not emit derived
// REALITY authentication material or an unbounded flight trace to stdout.
func TestServerShowIsSilent(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realityStdoutMu.Lock()
	oldStdout := os.Stdout
	os.Stdout = writePipe
	_, serverErr := Server(context.Background(), server, &Config{
		Show: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("intentional test dial failure")
		},
	})
	_ = writePipe.Close()
	os.Stdout = oldStdout
	realityStdoutMu.Unlock()

	output, readErr := io.ReadAll(readPipe)
	_ = readPipe.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if serverErr == nil {
		t.Fatal("expected the intentional dial failure")
	}
	if len(output) != 0 {
		t.Fatalf("Show emitted %q; native REALITY must stay stdout-silent", output)
	}
}
