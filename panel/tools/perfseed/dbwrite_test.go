package main

import (
	"testing"
	"time"
)

func TestBuildInsertSQL(t *testing.T) {
	want := "INSERT INTO t (c1, c2) SELECT u.c1::uuid, u.c2::timestamptz FROM unnest($1::text[], $2::text[]) AS u(c1, c2)"
	got := buildInsertSQL("t", []string{"c1", "c2"}, []string{"uuid", "timestamptz"})
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestByteap(t *testing.T) {
	p := byteap([]byte{0xab, 0x01})
	if p == nil || *p != "\\xab01" {
		t.Fatalf("byteap = %v", p)
	}
}

func TestTsp(t *testing.T) {
	tm := time.Date(2024, 6, 1, 12, 30, 0, 123456789, time.FixedZone("X", 8*3600))
	s := tsp(tm)
	if s == nil {
		t.Fatal("nil")
	}
	parsed, err := time.Parse(time.RFC3339Nano, *s)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(tm.UTC()) {
		t.Fatalf("%v vs %v", parsed, tm.UTC())
	}
}

func TestDatep(t *testing.T) {
	tm := time.Date(2024, 3, 5, 15, 0, 0, 0, time.UTC)
	p := datep(tm)
	if p == nil || *p != "2024-03-05" {
		t.Fatalf("datep = %v", p)
	}
}

func TestI64PtrNil(t *testing.T) {
	if i64Ptr(nil) != nil {
		t.Fatal("i64Ptr(nil) 应为 nil")
	}
	var n int64 = 7
	p := i64Ptr(&n)
	if p == nil || *p != "7" {
		t.Fatalf("i64Ptr = %v", p)
	}
}
