//go:build linux

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca44runner"
)

const (
	exitOK       = 0
	exitUsage    = 64
	exitInternal = 70
	exitIO       = 74
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signalChannel)
	var receivedSignal atomic.Int32
	go func() {
		select {
		case value := <-signalChannel:
			if unixSignal, ok := value.(syscall.Signal); ok {
				receivedSignal.Store(int32(unixSignal))
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	os.Exit(runCLI(ctx, os.Args[1:], os.Stdout, os.Stderr, func() int {
		return int(receivedSignal.Load())
	}))
}

func runCLI(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	receivedSignal func() int,
) (code int) {
	defer func() {
		if recover() != nil {
			_, _ = io.WriteString(stderr,
				"root_runner=DENY stage=bootstrap reason=internal_failure\n")
			code = exitInternal
		}
	}()
	cfg, err := ca44runner.ParseCLI(args)
	if err != nil {
		_, _ = io.WriteString(stderr,
			"root_runner=DENY stage=bootstrap reason=invalid_arguments\n")
		return exitUsage
	}
	result, failure := ca44runner.Run(ctx, cfg)
	if failure != nil {
		if failure.Kind == ca44runner.FailureInterrupted && receivedSignal != nil {
			failure.Signal = receivedSignal()
		}
		_, _ = io.WriteString(stderr, failure.SanitizedLine())
		return failure.ExitCode()
	}
	line := "root_runner=OK status=published authorization=NONE\n"
	if result.AlreadyPublished {
		line = "root_runner=OK status=already_published authorization=NONE\n"
	}
	if err := writeFull(stdout, []byte(line)); err != nil {
		_, _ = io.WriteString(stderr,
			"root_runner=DENY stage=publish reason=io_failure\n")
		return exitIO
	}
	return exitOK
}

func writeFull(writer io.Writer, data []byte) error {
	written, err := writer.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
