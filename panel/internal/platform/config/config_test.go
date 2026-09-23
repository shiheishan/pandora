package config

import (
	"testing"
	"time"
)

func TestValidateTokenTTLs(t *testing.T) {
	for _, tt := range []struct {
		name            string
		access, refresh time.Duration
		wantErr         bool
	}{
		{name: "default thirty days", access: 30 * 24 * time.Hour, refresh: 30 * 24 * time.Hour},
		{name: "short access long refresh", access: time.Hour, refresh: 30 * 24 * time.Hour},
		{name: "zero access", access: 0, refresh: time.Hour, wantErr: true},
		{name: "negative refresh", access: time.Hour, refresh: -time.Hour, wantErr: true},
		{name: "access exceeds refresh", access: 2 * time.Hour, refresh: time.Hour, wantErr: true},
		{name: "access too large", access: 366 * 24 * time.Hour, refresh: 366 * 24 * time.Hour, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTokenTTLs(tt.access, tt.refresh)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTokenTTLs(%s, %s) error=%v wantErr=%v", tt.access, tt.refresh, err, tt.wantErr)
			}
		})
	}
}

func TestStrictEnvDurationRejectsInvalidValue(t *testing.T) {
	t.Setenv("AEGIS_ACCESS_TOKEN_TTL", "thirty-days")
	if _, err := strictEnvDuration("AEGIS_ACCESS_TOKEN_TTL", 30*24*time.Hour); err == nil {
		t.Fatal("invalid duration was silently replaced by the default")
	}
}
