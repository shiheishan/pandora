package ca42runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func TestParseMigrationInventoryExact00001Through00042(t *testing.T) {
	data, setHash, migration42Hash := migrationInventoryFixture()
	entries, err := parseMigrationInventory(data, setHash, migration42Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 42 || entries[0].name != "00001_foundation.sql" || entries[41].name != "00042_client_auth_expand.sql" ||
		hex.EncodeToString(entries[41].digest[:]) != migration42Hash {
		t.Fatalf("unexpected inventory projection: count=%d first=%q last=%q", len(entries), entries[0].name, entries[41].name)
	}
}

func TestParseMigrationInventoryRejectsDrift(t *testing.T) {
	data, _, migration42Hash := migrationInventoryFixture()
	cases := map[string]func([]byte) []byte{
		"set_hash": func(value []byte) []byte { return value },
		"missing": func(value []byte) []byte {
			last := strings.LastIndex(string(value[:len(value)-1]), "\n")
			return append([]byte(nil), value[:last+1]...)
		},
		"extra": func(value []byte) []byte {
			return append(append([]byte(nil), value...), []byte(strings.Repeat("a", 64)+"  00043_extra.sql\n")...)
		},
		"reordered": func(value []byte) []byte {
			lines := strings.Split(string(value), "\n")
			lines[0], lines[1] = lines[1], lines[0]
			return []byte(strings.Join(lines, "\n"))
		},
		"duplicate": func(value []byte) []byte {
			lines := strings.Split(string(value), "\n")
			lines[1] = lines[0]
			return []byte(strings.Join(lines, "\n"))
		},
		"renamed_00042": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "00042_client_auth_expand.sql", "00042_wrong.sql", 1))
		},
		"single_space": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "  00001_", " 00001_", 1))
		},
		"three_spaces": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "  00001_", "   00001_", 1))
		},
		"tab_separator": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "  00001_", "\t00001_", 1))
		},
		"uppercase_digest": func(value []byte) []byte {
			candidate := append([]byte(nil), value...)
			candidate[0] = 'A'
			return candidate
		},
		"non_hex_digest": func(value []byte) []byte {
			candidate := append([]byte(nil), value...)
			candidate[0] = 'g'
			return candidate
		},
		"zero_digest": func(value []byte) []byte {
			candidate := append([]byte(nil), value...)
			copy(candidate[:64], strings.Repeat("0", 64))
			return candidate
		},
		"crlf": func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"nul": func(value []byte) []byte {
			candidate := append([]byte(nil), value...)
			candidate[70] = 0
			return candidate
		},
		"bom": func(value []byte) []byte { return append([]byte{0xef, 0xbb, 0xbf}, value...) },
		"invalid_utf8": func(value []byte) []byte {
			candidate := append([]byte(nil), value...)
			candidate[70] = 0xff
			return candidate
		},
		"no_final_lf": func(value []byte) []byte { return append([]byte(nil), value[:len(value)-1]...) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), data...))
			candidateDigest := sha256.Sum256(candidate)
			candidateSet := hex.EncodeToString(candidateDigest[:])
			if name == "set_hash" {
				candidateSet = strings.Repeat("f", 64)
			}
			if _, err := parseMigrationInventory(candidate, candidateSet, migration42Hash); err == nil {
				t.Fatal("migration inventory drift accepted")
			}
		})
	}
}

func migrationInventoryFixture() ([]byte, string, string) {
	var builder strings.Builder
	var migration42Hash string
	for version := 1; version <= 42; version++ {
		body := []byte(fmt.Sprintf("-- migration %05d\n", version))
		digest := sha256.Sum256(body)
		name := ca42MigrationNames[version-1]
		if version == 42 {
			migration42Hash = hex.EncodeToString(digest[:])
		}
		fmt.Fprintf(&builder, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	data := []byte(builder.String())
	setDigest := sha256.Sum256(data)
	return data, hex.EncodeToString(setDigest[:]), migration42Hash
}
