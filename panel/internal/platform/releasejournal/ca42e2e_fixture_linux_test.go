//go:build ca42e2e && linux && (amd64 || arm64)

package releasejournal

import "testing"

func TestCA42E2ELayoutSwitchedRecordsAreCanonical(t *testing.T) {
	input := CA42E2EJournalInput{
		JournalID: SHA256Bytes([]byte("journal")), AttemptID: "attempt-ca42-e2e",
		ReleaseContractCoreSHA256: SHA256Bytes([]byte("core")),
		ControllerSHA256:          SHA256Bytes([]byte("controller")), CreatedAtEpoch: 1700000000,
	}
	records, snapshot, err := ca42E2ELayoutSwitchedRecords(input)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State() != V3LayoutSwitched || snapshot.Sequence() != 6 || len(records) != 7 ||
		snapshot.AttemptID() != input.AttemptID || snapshot.JournalID() != input.JournalID {
		t.Fatalf("unexpected CA42 E2E journal: snapshot=%+v records=%d", snapshot, len(records))
	}
	parsed, err := ParseV3(records)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.HeadSHA256() != snapshot.HeadSHA256() || parsed.ManifestSHA256() != snapshot.ManifestSHA256() {
		t.Fatal("CA42 E2E journal did not round-trip canonically")
	}
}
