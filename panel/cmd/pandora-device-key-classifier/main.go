package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func main() {
	os.Exit(runWithPanicBoundary(func() int {
		return run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	}, os.Stderr))
}

func runWithPanicBoundary(execute func() int, stderr io.Writer) (code int) {
	defer func() {
		if recover() != nil {
			fmt.Fprintln(stderr, "classifier=DENY reason=internal_failure")
			code = 70
		}
	}()
	return execute()
}

type classifierCLI struct {
	Format            string
	ArtifactDirFD     int
	ArtifactName      string
	ArtifactHMACKeyID string
	ArtifactHMACKeyFD int
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cli, ok := parseClassifierCLI(args)
	if !ok {
		fmt.Fprintln(stderr, "classifier=DENY reason=invalid_arguments")
		return 64
	}

	// Sensitive source rows are accepted only on inherited stdin.  Opening a
	// caller-provided path here would reintroduce symlink/ancestor trust and
	// leak-prone file lifecycle decisions that belong to the root runner.
	source, err := io.ReadAll(io.LimitReader(stdin, maxSourceBytes+1))
	if err != nil {
		fmt.Fprintln(stderr, "classifier=DENY reason=input_read_failed")
		return 74
	}
	artifactKey, err := readRootOwnedSecretFD(cli.ArtifactHMACKeyFD)
	if err != nil {
		fmt.Fprintln(stderr, "classifier=DENY reason=artifact_hmac_key_read_failed")
		return 78
	}
	defer zeroBytes(artifactKey)

	artifact, summary, err := classifySource(source, cli.Format)
	if err != nil {
		fmt.Fprintf(stderr, "classifier=DENY reason=%s\n", err)
		return 78
	}
	detached, err := buildDetachedManifest(
		source,
		cli.Format,
		artifact,
		summary,
		cli.ArtifactHMACKeyID,
		artifactKey,
	)
	if err != nil {
		fmt.Fprintf(stderr, "classifier=DENY reason=%s\n", err)
		return 78
	}
	if err := writeRootOwnedArtifact(cli.ArtifactDirFD, cli.ArtifactName, artifact); err != nil {
		fmt.Fprintln(stderr, "classifier=DENY reason=artifact_write_failed")
		return 74
	}
	if err := writeExact(stdout, detached); err != nil {
		fmt.Fprintln(stderr, "classifier=DENY reason=detached_write_failed")
		return 74
	}
	return 0
}

func parseClassifierCLI(args []string) (classifierCLI, bool) {
	var out classifierCLI
	expected := [...]string{
		"-format",
		"-artifact-dir-fd",
		"-artifact-name",
		"-artifact-hmac-key-id",
		"-artifact-hmac-key-fd",
	}
	if len(args) != len(expected)*2 {
		return out, false
	}
	values := make([]string, len(expected))
	for i, name := range expected {
		if args[i*2] != name || args[i*2+1] == "" {
			return out, false
		}
		values[i] = args[i*2+1]
	}
	if values[0] != "tsv" && values[0] != "ndjson" {
		return out, false
	}
	artifactDirFD, ok := parseCanonicalFD(values[1])
	if !ok || !validArtifactName(values[2]) {
		return out, false
	}
	artifactKeyFD, ok := parseCanonicalFD(values[4])
	if !ok || artifactKeyFD == artifactDirFD {
		return out, false
	}
	out = classifierCLI{
		Format:            values[0],
		ArtifactDirFD:     artifactDirFD,
		ArtifactName:      values[2],
		ArtifactHMACKeyID: values[3],
		ArtifactHMACKeyFD: artifactKeyFD,
	}
	return out, true
}

func parseCanonicalFD(value string) (int, bool) {
	if value == "" || value[0] == '0' {
		return 0, false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed < 3 || strconv.FormatUint(parsed, 10) != value || parsed > uint64(^uint(0)>>1) {
		return 0, false
	}
	return int(parsed), true
}

func validArtifactName(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, `/\\`) && !strings.ContainsRune(value, 0)
}

func writeExact(writer io.Writer, payload []byte) error {
	written, err := writer.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}
	return nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
