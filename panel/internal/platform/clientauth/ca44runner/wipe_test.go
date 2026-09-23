package ca44runner

import "testing"

func TestWipeBytesClearsEntireBackingSlice(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	wipeBytes(secret)
	for index, value := range secret {
		if value != 0 {
			t.Fatalf("secret byte %d was not cleared", index)
		}
	}
}
