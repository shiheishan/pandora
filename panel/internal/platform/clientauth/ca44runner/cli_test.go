package ca44runner

import (
	"reflect"
	"strings"
	"testing"
)

func validCLIArgs() []string {
	values := []string{
		"/opt/pandora", "release/manifest.txt", strings.Repeat("1", 64),
		"release/signer.pub", strings.Repeat("2", 64), "release/contract.md",
		"bin/classifier", "bin/verifier", "inputs/source.ndjson",
		"keys/artifact.key", strings.Repeat("3", 64), "keys/evidence.key",
		strings.Repeat("4", 64), "runtime/staging", "runtime/published", "30s", "1s",
	}
	args := make([]string, 0, len(rootRunnerCLIFlags)*2)
	for index, flag := range rootRunnerCLIFlags {
		args = append(args, flag, values[index])
	}
	return args
}

func TestParseCLIExactContract(t *testing.T) {
	args := validCLIArgs()
	original := append([]string(nil), args...)
	cfg, err := ParseCLI(args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, original) {
		t.Fatal("CLI parser mutated caller arguments")
	}
	if cfg.TrustRoot != "/opt/pandora" || cfg.ChildTimeout.String() != "30s" ||
		cfg.WaitDelay.String() != "1s" || cfg.ArtifactKeySHA256 != strings.Repeat("3", 64) {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
	if err := cfg.Validate("linux", "amd64"); err != nil {
		t.Fatalf("parsed config failed validation: %v", err)
	}
}

func TestParseCLIRejectsGrammarAndDurationDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]string) []string
	}{
		{"short", func(value []string) []string { return value[:len(value)-1] }},
		{"extra", func(value []string) []string { return append(value, "-extra", "x") }},
		{"reordered", func(value []string) []string {
			value[0], value[2] = value[2], value[0]
			return value
		}},
		{"empty", func(value []string) []string { value[1] = ""; return value }},
		{"duration alias", func(value []string) []string { value[31] = "60s"; return value }},
		{"zero duration", func(value []string) []string { value[31] = "0s"; return value }},
		{"negative delay", func(value []string) []string { value[33] = "-1s"; return value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseCLI(test.mutate(validCLIArgs())); err == nil {
				t.Fatal("invalid CLI grammar accepted")
			}
		})
	}
}
