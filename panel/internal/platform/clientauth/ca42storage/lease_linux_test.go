//go:build linux

package ca42storage

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxDuplicateCloexecOwnsIndependentDescriptor(t *testing.T) {
	created, err := os.CreateTemp(t.TempDir(), "ca42-linux-dup-*")
	if err != nil {
		t.Fatal(err)
	}
	name := created.Name()
	if _, err := created.Write([]byte("linux duplicate fixture")); err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := duplicateCloexec(source)
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	var sourceStat, duplicateStat unix.Stat_t
	if err := unix.Fstat(int(source.Fd()), &sourceStat); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(int(duplicate.Fd()), &duplicateStat); err != nil {
		t.Fatal(err)
	}
	if sourceStat.Dev != duplicateStat.Dev || sourceStat.Ino != duplicateStat.Ino {
		t.Fatal("duplicate does not retain the exact source file identity")
	}
	flags, err := unix.FcntlInt(duplicate.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("duplicate is not CLOEXEC: flags=%d err=%v", flags, err)
	}
	openFlags, err := unix.FcntlInt(duplicate.Fd(), unix.F_GETFL, 0)
	if err != nil || openFlags&unix.O_ACCMODE != unix.O_RDONLY {
		t.Fatalf("duplicate is not read-only: flags=%d err=%v", openFlags, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err := duplicate.ReadAt(buffer, 0); err != nil {
		t.Fatalf("duplicate did not survive caller source close: %v", err)
	}
}
