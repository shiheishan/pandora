package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"golang.org/x/crypto/argon2"
)

func TestValidatePassword(t *testing.T) {
	ok := "abc12345"
	if err := validatePassword(ok, false); err != nil {
		t.Fatalf("期望通过: %v", err)
	}

	bad := []string{
		"abc1234",
		"abcdefgh",
		"12345678",
		"abc\x0012345",
		"abc1234\xff",
		strings.Repeat("a1", 129),
	}
	for _, pw := range bad {
		err := validatePassword(pw, false)
		if err == nil {
			t.Fatalf("%q 应失败", pw)
		}
		e := errs.As(err)
		if e.Code != errs.ValidationFailed {
			t.Fatalf("%q: code=%v want validation_failed", pw, e.Code)
		}
		if e.Fields == nil || e.Fields["password"] == "" {
			t.Fatalf("%q: 缺少 password 字段错误: %v", pw, e.Fields)
		}
	}
}

func TestValidatePasswordStaff(t *testing.T) {
	if err := validatePassword("abcdefgh123", true); err == nil {
		t.Fatal("11 位应失败")
	}
	if err := validatePassword("abcdefgh1234", true); err != nil {
		t.Fatalf("12 位应通过: %v", err)
	}
}

func TestHashPHCVerifyPHC(t *testing.T) {
	pw := "Secret1x"
	h1, err := hashPHC(pw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h1, "$argon2id$") {
		t.Fatalf("PHC 前缀: %q", h1)
	}
	h2, err := hashPHC(pw)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("两次哈希应因盐不同而不同")
	}

	ok, rehash, err := verifyPHC(pw, h1)
	if err != nil || !ok || rehash {
		t.Fatalf("正确口令: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, rehash, err = verifyPHC("wrong", h1)
	if err != nil || ok || rehash {
		t.Fatalf("错误口令: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestVerifyPHCNeedsRehashLowParams(t *testing.T) {
	pw := "lowparam12"
	const m, it, par = uint32(8192), uint32(1), uint8(1)
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	key := argon2.IDKey([]byte(pw), salt, it, m, par, 32)
	phc := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, m, it, par,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))

	ok, rehash, err := verifyPHC(pw, phc)
	if err != nil || !ok || !rehash {
		t.Fatalf("低参数: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestVerifyPHCBadFormat(t *testing.T) {
	cases := []string{"garbage", "$argon2id$v=19$m=x"}
	for _, phc := range cases {
		ok, _, err := verifyPHC("any", phc)
		if ok {
			t.Fatalf("%q 不应 ok=true", phc)
		}
		if err == nil && !ok {
			continue
		}
		if err == nil {
			t.Fatalf("%q 应 err!=nil 或 ok=false", phc)
		}
	}
}

func TestPasswordHasherAcquire(t *testing.T) {
	h := newPasswordHasher(config.PasswordHash{
		PublicConcurrency: 1,
		AdminConcurrency:  1,
		QueueTimeout:      50 * time.Millisecond,
	})
	ctx := context.Background()

	rel, err := h.acquire(ctx, authn.AudiencePublic)
	if err != nil {
		t.Fatalf("首次 public acquire: %v", err)
	}

	start := time.Now()
	_, err = h.acquire(ctx, authn.AudiencePublic)
	if !errors.Is(err, ErrPasswordHashBusy) {
		t.Fatalf("第二次 public: %v want ErrPasswordHashBusy", err)
	}
	if d := time.Since(start); d < 40*time.Millisecond {
		t.Fatalf("排队超时过短: %v", d)
	}

	relAdmin, err := h.acquire(ctx, authn.AudienceAdmin)
	if err != nil {
		t.Fatalf("public 占满时 admin 仍应成功: %v", err)
	}
	relAdmin()
	rel()

	if _, err = h.acquire(ctx, authn.AudiencePublic); err != nil {
		t.Fatalf("release 后再 acquire: %v", err)
	}
}

func TestPasswordHasherZeroConfigDefaults(t *testing.T) {
	h := newPasswordHasher(config.PasswordHash{})
	rel, err := h.acquire(context.Background(), authn.AudiencePublic)
	if err != nil {
		t.Fatalf("零值配置应能 acquire: %v", err)
	}
	rel()
}
