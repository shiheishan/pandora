package ca42gooseinfo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestProjectFromRetainedReaderAtRejectsNonGoELF(t *testing.T) {
	content := bytes.Repeat([]byte{'x'}, 4096)
	digest := sha256.Sum256(content)
	if _, err := ProjectFromRetainedReaderAt(context.Background(), bytes.NewReader(content), uint64(len(content)), fmt.Sprintf("%x", digest), "amd64"); err == nil {
		t.Fatal("non-Go ELF accepted")
	}
}

func TestProjectApprovedGooseBinary(t *testing.T) {
	path := os.Getenv("PANDORA_GOOSE_TEST_BINARY")
	if path == "" {
		t.Skip("dedicated retained Goose binary not configured")
	}
	architecture := os.Getenv("PANDORA_GOOSE_TEST_ARCH")
	if architecture == "" {
		architecture = "amd64"
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 {
		t.Fatalf("invalid Goose binary: info=%v err=%v", info, err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, 0, info.Size())); err != nil {
		t.Fatal(err)
	}
	binarySHA := hex.EncodeToString(hasher.Sum(nil))
	projection, err := ProjectFromRetainedReaderAt(context.Background(), file, uint64(info.Size()), binarySHA, architecture)
	if err != nil {
		t.Fatal(err)
	}
	projectionSHA := sha256.Sum256(projection)
	parsed, err := Parse(projection, projectionSHA, binarySHA, architecture)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.IsParsed() || parsed.MainModuleSum != RequiredMainModuleSum || parsed.VCSRevision != RequiredVCSRevision {
		t.Fatalf("unexpected approved Goose projection: %+v", parsed)
	}
}

func TestProjectFromRetainedReaderAtRejectsBadBounds(t *testing.T) {
	if _, err := ProjectFromRetainedReaderAt(context.Background(), nil, 1, string(make([]byte, 64)), "amd64"); err == nil {
		t.Fatal("nil retained reader accepted")
	}
	reader := bytes.NewReader([]byte("short"))
	digest := sha256.Sum256([]byte("short"))
	if _, err := ProjectFromRetainedReaderAt(context.Background(), reader, 6, fmt.Sprintf("%x", digest), "mips64"); err == nil {
		t.Fatal("unsupported architecture accepted")
	}
}
