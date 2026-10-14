package main

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashPasswordPHC(t *testing.T) {
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]+\$[A-Za-z0-9+/]+$`)
	pwd := "testpass1"

	a, err := hashPasswordPHC(pwd)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(a) {
		t.Fatalf("格式不匹配: %s", a)
	}

	parts := strings.Split(a, "$")
	if len(parts) != 6 {
		t.Fatalf("段数 %d", len(parts))
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		t.Fatal(err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		t.Fatal(err)
	}
	got := argon2.IDKey([]byte(pwd), salt, 2, 19*1024, 1, uint32(len(want)))
	if len(got) != len(want) {
		t.Fatalf("哈希长度 %d vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal("重算 argon2 与 PHC 哈希不一致")
		}
	}

	b, err := hashPasswordPHC(pwd)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次哈希应因盐随机而不同")
	}
}
