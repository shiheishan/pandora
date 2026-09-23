package reality

import "testing"

func TestRealitySessionTicketRecordFits(t *testing.T) {
	const overhead = 16
	const minimum = recordHeaderLen + 1 + 1 + overhead
	for _, test := range []struct {
		name string
		len  int
		want bool
	}{
		{name: "zero target", len: 0, want: false},
		{name: "one byte short", len: minimum - 1, want: false},
		{name: "minimum", len: minimum, want: true},
		{name: "larger target", len: minimum + 144, want: true},
		{name: "negative target", len: -1, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := realitySessionTicketRecordFits(test.len, overhead); got != test.want {
				t.Fatalf("recordLen=%d got %v, want %v", test.len, got, test.want)
			}
		})
	}
}

func TestRealitySessionTicketRecordRejectsInvalidOverhead(t *testing.T) {
	if realitySessionTicketRecordFits(recordHeaderLen+2, -1) {
		t.Fatal("negative AEAD overhead unexpectedly accepted")
	}
}
