package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

// acmeStore 在 kvStore 上补 Delete，供 ACME HMAC 清除。
type acmeStore struct {
	kvStore
}

func (k *acmeStore) Delete(_ context.Context, keys ...string) error {
	for _, key := range keys {
		delete(k.kv, key)
	}
	return nil
}

func newACMEService(st *acmeStore, al *auditLog) *Service {
	sealer, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		panic(err)
	}
	return newService(st, fakeTx{}, sealer, nil, al)
}

func TestACMEDefaults(t *testing.T) {
	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	s := newACMEService(st, &auditLog{})
	ctx := context.Background()

	a, err := s.ACME(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if a.ContactEmail != "" || a.UseStaging || a.ZeroSSLEnabled || a.ZeroSSLEABKID != "" || a.ZeroSSLEABHMACSet {
		t.Fatalf("defaults: %+v", a)
	}
}

func TestSetACMEPersistsAndReadBack(t *testing.T) {
	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	al := &auditLog{}
	s := newACMEService(st, al)
	ctx := context.Background()

	hmac := "YWJj"
	got, err := s.SetACME(ctx, ACMEUpdate{
		ContactEmail:   "ops@example.com",
		UseStaging:     true,
		ZeroSSLEnabled: true,
		ZeroSSLEABKID:  "kid-1",
		ZeroSSLEABHMAC: &hmac,
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got.ContactEmail != "ops@example.com" || !got.UseStaging || !got.ZeroSSLEnabled || got.ZeroSSLEABKID != "kid-1" || !got.ZeroSSLEABHMACSet {
		t.Fatalf("returned: %+v", got)
	}

	a, err := s.ACME(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if a.ContactEmail != "ops@example.com" || !a.UseStaging || !a.ZeroSSLEnabled || a.ZeroSSLEABKID != "kid-1" || !a.ZeroSSLEABHMACSet {
		t.Fatalf("read back: %+v", a)
	}
	plain, err := s.ACMEEABHMAC(ctx)
	if err != nil || plain != "YWJj" {
		t.Fatalf("hmac plain: %q err=%v", plain, err)
	}
}

func TestSetACMEHMACPreserveClearReplace(t *testing.T) {
	ctx := context.Background()
	hmac1 := "YWJj"
	hmac2 := "ZGVm"

	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	s := newACMEService(st, &auditLog{})
	if _, err := s.SetACME(ctx, ACMEUpdate{
		ZeroSSLEnabled: true,
		ZeroSSLEABKID:  "k",
		ZeroSSLEABHMAC: &hmac1,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := s.SetACME(ctx, ACMEUpdate{
		ZeroSSLEnabled: true,
		ZeroSSLEABKID:  "k",
	})
	if err != nil {
		t.Fatalf("preserve: %v", err)
	}
	plain, err := s.ACMEEABHMAC(ctx)
	if err != nil || plain != "YWJj" {
		t.Fatalf("after preserve: %q err=%v", plain, err)
	}

	empty := ""
	_, err = s.SetACME(ctx, ACMEUpdate{
		ZeroSSLEnabled: false,
		ZeroSSLEABHMAC: &empty,
	})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	a, _ := s.ACME(ctx)
	if a.ZeroSSLEABHMACSet {
		t.Fatal("hmac should be cleared")
	}
	plain, err = s.ACMEEABHMAC(ctx)
	if err != nil || plain != "" {
		t.Fatalf("after clear: %q err=%v", plain, err)
	}
	if _, ok := st.kv[keyACMEZeroSSLEABHMAC]; ok {
		t.Fatal("hmac key should be deleted")
	}

	_, err = s.SetACME(ctx, ACMEUpdate{
		ZeroSSLEnabled: true,
		ZeroSSLEABKID:  "k2",
		ZeroSSLEABHMAC: &hmac2,
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	plain, err = s.ACMEEABHMAC(ctx)
	if err != nil || plain != "ZGVm" {
		t.Fatalf("after replace: %q err=%v", plain, err)
	}
}

func TestSetACMEZeroSSLRequiresKIDAndHMAC(t *testing.T) {
	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	s := newACMEService(st, &auditLog{})
	ctx := context.Background()

	_, err := s.SetACME(ctx, ACMEUpdate{ZeroSSLEnabled: true})
	assertFieldValidation(t, err, "zerossl_eab_kid")
	assertFieldValidation(t, err, "zerossl_eab_hmac")

	hmac := "YWJj"
	_, err = s.SetACME(ctx, ACMEUpdate{ZeroSSLEnabled: true, ZeroSSLEABHMAC: &hmac})
	assertFieldValidation(t, err, "zerossl_eab_kid")

	_, err = s.SetACME(ctx, ACMEUpdate{ZeroSSLEnabled: true, ZeroSSLEABKID: "kid"})
	assertFieldValidation(t, err, "zerossl_eab_hmac")
}

func TestSetACMEInvalidEmail(t *testing.T) {
	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	s := newACMEService(st, &auditLog{})
	_, err := s.SetACME(context.Background(), ACMEUpdate{ContactEmail: "not-an-email"})
	assertFieldValidation(t, err, "contact_email")
}

func TestSetACMEAuditNoHMACPlaintext(t *testing.T) {
	st := &acmeStore{kvStore: kvStore{kv: map[string]json.RawMessage{}}}
	al := &auditLog{}
	s := newACMEService(st, al)
	ctx := context.Background()

	if _, err := s.SetACME(ctx, ACMEUpdate{ContactEmail: "a@b.co"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count after first: %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditACMEChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit meta: %s %s", e.action, e.resourceType)
	}
	if e.data["group"] != "acme" {
		t.Fatalf("group: %v", e.data["group"])
	}
	if auditContainsHMACSecret(t, e.data) {
		t.Fatal("audit must not contain hmac plaintext")
	}

	if _, err := s.SetACME(ctx, ACMEUpdate{ContactEmail: "a@b.co"}); err != nil {
		t.Fatalf("noop: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop: %d", len(al.entries))
	}

	hmac := "YWJj"
	if _, err := s.SetACME(ctx, ACMEUpdate{
		ContactEmail:   "a@b.co",
		ZeroSSLEnabled: true,
		ZeroSSLEABKID:  "kid",
		ZeroSSLEABHMAC: &hmac,
	}); err != nil {
		t.Fatalf("hmac set: %v", err)
	}
	if len(al.entries) != 2 {
		t.Fatalf("audit after hmac: %d", len(al.entries))
	}
	if auditContainsHMACSecret(t, al.entries[1].data) {
		t.Fatal("audit after hmac change must not contain plaintext")
	}
	if al.entries[1].data["zerossl_eab_hmac_changed"] != true {
		t.Fatalf("flag: %v", al.entries[1].data["zerossl_eab_hmac_changed"])
	}
}

func auditContainsHMACSecret(t *testing.T, data map[string]any) bool {
	t.Helper()
	var b strings.Builder
	walkAuditAny(data, &b)
	s := b.String()
	return strings.Contains(s, "abc") || strings.Contains(s, "YWJj")
}

func walkAuditAny(v any, b *strings.Builder) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			b.WriteString(k)
			walkAuditAny(val, b)
		}
	case []any:
		for _, val := range x {
			walkAuditAny(val, b)
		}
	case string:
		b.WriteString(x)
	}
}
