//go:build linux

package ca42storage

import (
	"os"
	"testing"
)

func TestMeasureVerityRejectsOrdinaryUnsealedFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "unsealed-*")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write([]byte("ordinary mutable bytes")); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := MeasureVerity(file); err == nil {
		t.Fatal("ordinary unsealed file produced an fs-verity measurement")
	}
}
