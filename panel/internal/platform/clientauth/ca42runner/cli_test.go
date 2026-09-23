package ca42runner

import "testing"

func TestParseCLIAllowsOnlyFixedAttemptCommand(t *testing.T) {
	command, err := ParseCLI([]string{"execute", "--attempt-id", "attempt-ca42-1"})
	if err != nil || command.AttemptID != "attempt-ca42-1" {
		t.Fatalf("fixed command rejected: command=%#v err=%v", command, err)
	}
	for _, args := range [][]string{
		nil,
		{"execute", "--attempt-id", "UPPER"},
		{"execute", "--attempt-id", "../escape"},
		{"execute", "--authority-path", "/tmp/self-signed"},
		{"execute", "--attempt-id", "attempt-1", "--docker", "evil"},
		{"execute", "--attempt-id", "attempt-1", "--journal-root", "/tmp/journal"},
		{"execute", "--attempt-id", "attempt-1", "--expected-root-device", "1"},
		{"execute", "--attempt-id", "attempt-1", "--allow-devices", "1,2"},
		{"execute", "--attempt-id", "attempt-1", "--journal-format", "pandora-release-journal-v1"},
		{"execute", "--attempt-id", "attempt-1", "--expect-journal-sha256", "deadbeef"},
		{"execute", "--attempt-id", "attempt-1", "--evidence-fd", "3"},
		{"execute", "--attempt-id", "attempt-1", "--from", "PREPARED"},
		{"execute", "--attempt-id", "attempt-1", "--to", "COMMITTED"},
		{"execute", "--attempt-id", "attempt-1", "--release-contract-core-sha256", "deadbeef"},
		{"verify", "--attempt-id", "attempt-1"},
	} {
		if result, err := ParseCLI(args); err == nil || result.AttemptID != "" {
			t.Fatalf("accepted caller policy override: args=%q result=%#v err=%v", args, result, err)
		}
	}
}
