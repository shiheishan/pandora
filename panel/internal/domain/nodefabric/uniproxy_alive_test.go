package nodefabric

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestAliveRowsDeduplicatesAndSorts(t *testing.T) {
	uids, hashes := aliveRows(map[string][]string{
		"2":    {"10.0.0.2", "10.0.0.1"},
		"01":   {"10.0.0.9"},
		"1":    {"10.0.0.9", "10.0.0.9"},
		"oops": {"10.0.0.3"},
	})
	if len(uids) != 3 || len(hashes) != 3 {
		t.Fatalf("rows = %v / %d hashes; want 3 distinct (uid, ip) pairs", uids, len(hashes))
	}
	if uids[0] != 1 || uids[1] != 2 || uids[2] != 2 {
		t.Fatalf("uids not sorted: %v", uids)
	}
	if string(hashes[1]) >= string(hashes[2]) {
		t.Fatal("hashes for one uid are not sorted")
	}
	want := sha256.Sum256([]byte("10.0.0.9"))
	if string(hashes[0]) != string(want[:]) {
		t.Fatal("alive rows must store only the SHA-256 of the IP")
	}
}

func TestReportAliveIsOneStatement(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("Service.ReportAlive")
	if strings.Count(src, "tx.Exec(") != 1 || strings.Contains(src, "tx.QueryRow(") || strings.Contains(src, "for ") {
		t.Fatal("ReportAlive must write the whole report with one batched statement")
	}
	if !strings.Contains(src, "unnest($3::bigint[], $4::bytea[])") {
		t.Fatal("ReportAlive no longer batches through unnest")
	}
}
