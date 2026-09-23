//go:build linux

package ca42storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxSourceIdentityDetectsReopensAndHardlinks(t *testing.T) {
	directory := t.TempDir()
	originalPath := filepath.Join(directory, "original")
	if err := os.WriteFile(originalPath, []byte("identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardlinkPath := filepath.Join(directory, "hardlink")
	if err := os.Link(originalPath, hardlinkPath); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	reopened, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	hardlink, err := os.Open(hardlinkPath)
	if err != nil {
		t.Fatal(err)
	}
	defer hardlink.Close()
	first, err := linuxSourceIdentity(original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := linuxSourceIdentity(reopened)
	if err != nil {
		t.Fatal(err)
	}
	third, err := linuxSourceIdentity(hardlink)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first != third {
		t.Fatalf("same inode produced different identities: first=%+v second=%+v hardlink=%+v", first, second, third)
	}
	otherPath := filepath.Join(directory, "other")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	other, err := os.Open(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherIdentity, err := linuxSourceIdentity(other)
	if err != nil {
		t.Fatal(err)
	}
	if otherIdentity == first {
		t.Fatal("distinct inode produced duplicate identity")
	}
}
