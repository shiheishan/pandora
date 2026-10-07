package kernel

import (
	"bytes"
	"testing"
)

func TestDecodeGunHunk(t *testing.T) {
	multi := append(encodeGunHunk([]byte("ab")), encodeGunHunk([]byte("cde"))...)
	cases := []struct {
		name    string
		message []byte
		want    []byte
		wantErr bool
	}{
		{"single", encodeGunHunk([]byte("hello")), []byte("hello"), false},
		{"empty message", nil, nil, false},
		{"empty hunk", encodeGunHunk(nil), []byte{}, false},
		{"multi hunk", multi, []byte("abcde"), false},
		{"large", encodeGunHunk(bytes.Repeat([]byte{7}, 300)), bytes.Repeat([]byte{7}, 300), false},
		{"raw payload is rejected", []byte{0x00, 0x01, 0x02}, nil, true},
		{"truncated", []byte{gunHunkTag, 0x05, 'a'}, nil, true},
		{"bad varint", []byte{gunHunkTag, 0xff}, nil, true},
	}
	for _, c := range cases {
		got, err := decodeGunHunk(c.message)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", c.name, err, c.wantErr)
		}
		if !c.wantErr && !bytes.Equal(got, c.want) {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
