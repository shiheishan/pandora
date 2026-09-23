package dbbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func retentionWebDAVFixture(server *memoryWebDAV) RetentionDeleteTriplet {
	backupID := "20260701T120000Z"
	prefix := "aegis-postgres-" + backupID
	ref := func(name, value string) ArtifactRef {
		digest := sha256.Sum256([]byte(value))
		server.objects["/pandora/"+name] = []byte(value)
		return ArtifactRef{Name: name, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(value))}
	}
	manifest := ref(prefix+".manifest.json", "signed-manifest")
	checksum := ref(prefix+".dump.age.sha256", "archive-digest")
	archive := ref(prefix+".dump.age", "age-encrypted-archive")
	return RetentionDeleteTriplet{
		BackupID: backupID, Sequence: 1,
		Bytes:    manifest.Bytes + checksum.Bytes + archive.Bytes,
		Manifest: manifest, Checksum: checksum, Archive: archive,
	}
}

func TestInspectRetentionTripletReturnsExactDigestStates(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	triplet := retentionWebDAVFixture(server)
	state, err := testWebDAVClient(server).inspectRetentionTriplet(context.Background(), triplet)
	if err != nil {
		t.Fatal(err)
	}
	if state != (RetentionRemoteState{
		Manifest: RetentionRemoteExact,
		Checksum: RetentionRemoteExact,
		Archive:  RetentionRemoteExact,
	}) {
		t.Fatalf("state=%#v", state)
	}
	if got := strings.Join(server.methods, ","); got != "GET,GET,GET" {
		t.Fatalf("methods=%s, want exact reads only", got)
	}
}

func TestDeleteRetentionObjectRequiresExactDigestAndConfirmsMissing(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	triplet := retentionWebDAVFixture(server)
	client := testWebDAVClient(server)
	if err := client.deleteRetentionObjectVerifiedMissing(context.Background(), triplet.Manifest); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(server.methods, ","); got != "GET,DELETE,GET" {
		t.Fatalf("methods=%s, want precheck-delete-confirm", got)
	}
	server.methods = nil
	if err := client.deleteRetentionObjectVerifiedMissing(context.Background(), triplet.Manifest); err != nil {
		t.Fatalf("missing object must be idempotent: %v", err)
	}
	if got := strings.Join(server.methods, ","); got != "GET" {
		t.Fatalf("missing retry methods=%s", got)
	}
}

func TestDeleteRetentionObjectRefusesMismatchWithoutDelete(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}}
	triplet := retentionWebDAVFixture(server)
	server.objects["/pandora/"+triplet.Manifest.Name] = []byte("replaced-object")
	err := testWebDAVClient(server).deleteRetentionObjectVerifiedMissing(context.Background(), triplet.Manifest)
	if !errors.Is(err, ErrRetentionRemoteMismatch) {
		t.Fatalf("err=%v, want mismatch", err)
	}
	if got := strings.Join(server.methods, ","); got != "GET" {
		t.Fatalf("mismatch must not delete: %s", got)
	}
}

func TestDeleteRetentionObjectFailsWhenSuccessDoesNotRemoveObject(t *testing.T) {
	server := &memoryWebDAV{objects: map[string][]byte{}, ignoreDelete: true}
	triplet := retentionWebDAVFixture(server)
	err := testWebDAVClient(server).deleteRetentionObjectVerifiedMissing(context.Background(), triplet.Manifest)
	if !errors.Is(err, ErrRetentionDeleteUnconfirmed) {
		t.Fatalf("err=%v, want unconfirmed deletion", err)
	}
	if got := strings.Join(server.methods, ","); got != "GET,DELETE,GET" {
		t.Fatalf("methods=%s", got)
	}
}
