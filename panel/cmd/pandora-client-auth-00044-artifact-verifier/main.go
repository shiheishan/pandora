package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

const (
	exitOK       = 0
	exitUsage    = 64
	exitInternal = 70
	exitIO       = 74
	exitVerify   = 78
)

type cliConfig struct {
	Argv           []string
	SourceFormat   string
	SourceFD       int
	ArtifactFD     int
	DetachedFD     int
	ExpectationsFD int
	ArtifactKeyID  string
	ArtifactKeyFD  int
	EvidenceKeyID  string
	EvidenceKeyFD  int
}

type failureKind uint8

const (
	failureIO failureKind = iota + 1
	failureVerify
	failureInternal
)

type verifierFailure struct {
	kind failureKind
	err  error
}

func (e *verifierFailure) Error() string { return e.err.Error() }
func (e *verifierFailure) Unwrap() error { return e.err }

func ioFailure(err error) error       { return &verifierFailure{failureIO, err} }
func verifyFailure(err error) error   { return &verifierFailure{failureVerify, err} }
func internalFailure(err error) error { return &verifierFailure{failureInternal, err} }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) (code int) {
	defer func() {
		if recover() != nil {
			writeDeny(stderr, "internal_failure")
			code = exitInternal
		}
	}()
	config, err := parseCLI(args)
	if err != nil {
		writeDeny(stderr, "invalid_arguments")
		return exitUsage
	}
	receipt, err := executePlatform(config)
	if err != nil {
		var failure *verifierFailure
		if !errors.As(err, &failure) {
			writeDeny(stderr, "internal_failure")
			return exitInternal
		}
		switch failure.kind {
		case failureIO:
			writeDeny(stderr, "io_failure")
			return exitIO
		case failureVerify:
			writeDeny(stderr, "verification_denied")
			return exitVerify
		default:
			writeDeny(stderr, "internal_failure")
			return exitInternal
		}
	}
	if err := writeFull(stdout, receipt); err != nil {
		writeDeny(stderr, "io_failure")
		return exitIO
	}
	return exitOK
}

func parseCLI(args []string) (cliConfig, error) {
	var config cliConfig
	if len(args) != 18 {
		return config, errors.New("exact flags required")
	}
	expected := [...]string{
		"-source-format", "-source-fd", "-artifact-fd",
		"-detached-manifest-fd", "-release-expectations-fd",
		"-artifact-hmac-key-id", "-artifact-hmac-key-fd",
		"-evidence-hmac-key-id", "-evidence-hmac-key-fd",
	}
	values := make([]string, len(expected))
	for i, name := range expected {
		if args[i*2] != name || args[i*2+1] == "" {
			return config, errors.New("invalid ordered flag grammar")
		}
		values[i] = args[i*2+1]
	}
	if values[0] != "tsv" && values[0] != "ndjson" {
		return config, errors.New("invalid source format")
	}
	config.SourceFormat = values[0]
	config.Argv = append([]string(nil), args...)
	config.ArtifactKeyID = values[5]
	config.EvidenceKeyID = values[7]
	canonicalFD := regexp.MustCompile(`^(?:[3-9]|[1-9][0-9]+)$`)
	parseFD := func(value string) (int, error) {
		if !canonicalFD.MatchString(value) {
			return 0, errors.New("noninteger fd")
		}
		parsed, err := strconv.ParseUint(value, 10, strconv.IntSize)
		if err != nil || parsed < 3 || parsed > uint64(^uint(0)>>1) {
			return 0, errors.New("invalid fd")
		}
		return int(parsed), nil
	}
	assignments := []struct {
		value string
		dest  *int
	}{
		{values[1], &config.SourceFD}, {values[2], &config.ArtifactFD},
		{values[3], &config.DetachedFD}, {values[4], &config.ExpectationsFD},
		{values[6], &config.ArtifactKeyFD}, {values[8], &config.EvidenceKeyFD},
	}
	seen := map[int]bool{}
	for _, assignment := range assignments {
		fd, err := parseFD(assignment.value)
		if err != nil || seen[fd] {
			return cliConfig{}, errors.New("fd alias")
		}
		seen[fd] = true
		*assignment.dest = fd
	}
	return config, nil
}

func writeFull(w io.Writer, data []byte) error {
	written, err := w.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func writeDeny(w io.Writer, reason string) {
	_, _ = fmt.Fprintf(w, "verifier=DENY reason=%s\n", reason)
}
