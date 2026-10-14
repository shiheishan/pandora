package main

import (
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)

func TestParseInfraArgs(t *testing.T) {
	args := []string{
		"--admin-url", "https://panel.example.com/",
		"--admin-email", "admin@example.com",
		"--admin-password-file", "/tmp/admin.pw",
		"--out-dir", "/tmp/out",
	}
	o, err := parseInfraArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if o.AdminURL != "https://panel.example.com" || o.Servers != 200 {
		t.Fatalf("defaults: %+v", o)
	}
	args = append(args, "--servers", "50")
	o, err = parseInfraArgs(args)
	if err != nil || o.Servers != 50 {
		t.Fatalf("servers: %+v err=%v", o, err)
	}
	_, err = parseInfraArgs([]string{"--admin-url", "ftp://x", "--admin-email", "a@b", "--admin-password-file", "f", "--out-dir", "d"})
	if err == nil {
		t.Fatal("expected scheme error")
	}
}

func TestParseActivateArgs(t *testing.T) {
	args := []string{
		"--admin-url", "http://127.0.0.1:9001",
		"--admin-email", "admin@example.com",
		"--admin-password-file", "/tmp/admin.pw",
		"--out-dir", "/tmp/out",
	}
	o, err := parseActivateArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	if o.AdminURL != "http://127.0.0.1:9001" {
		t.Fatalf("url: %s", o.AdminURL)
	}
	_, err = parseActivateArgs([]string{"--admin-email", "a@b", "--admin-password-file", "f", "--out-dir", "d"})
	if err == nil {
		t.Fatal("expected missing admin-url")
	}
}

func TestParseUsersArgs(t *testing.T) {
	base := []string{
		"--dsn-file", "/tmp/dsn",
		"--out-dir", "/tmp/out",
		"--user-password-file", "/tmp/user.pw",
	}
	o, err := parseUsersArgs(base, fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if o.Users != 5000 || o.HistoryDays != 30 || o.ExpiredPercent != 10 || o.Seed != 1 {
		t.Fatalf("defaults: %+v", o)
	}
	if o.SecretsEnv != "/etc/pandora/secrets.env" {
		t.Fatalf("secrets: %s", o.SecretsEnv)
	}
	expireAt := fixedNow.Add(2 * time.Hour).Format(time.RFC3339)
	withExpire := append(base, "--expire-at", expireAt, "--expire-count", "10", "--users", "100", "--expired-percent", "10")
	o, err = parseUsersArgs(withExpire, fixedNow)
	if err != nil || o.ExpireCount != 10 || o.ExpireAt == nil {
		t.Fatalf("expire: %+v err=%v", o, err)
	}
	_, err = parseUsersArgs(append(base, "--expire-at", expireAt), fixedNow)
	if err == nil {
		t.Fatal("expire-at without count")
	}
	_, err = parseUsersArgs(append(base, "--expire-count", "5"), fixedNow)
	if err == nil {
		t.Fatal("expire-count without at")
	}
	high := append(base, "--expire-at", expireAt, "--expire-count", "5000")
	_, err = parseUsersArgs(high, fixedNow)
	if err == nil {
		t.Fatal("expire-count over active users")
	}
}
