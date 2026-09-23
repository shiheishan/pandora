package ca42authorityprod

const (
	// RootPath is the sole production authority-ledger root. The production
	// opener never accepts a caller-provided path, directory descriptor or
	// expected ledger snapshot.
	RootPath = "/var/lib/pandora/ca42"
	// RecordName is the canonical durable authority-ledger record.
	RecordName = "authority-ledger.v1"
)
