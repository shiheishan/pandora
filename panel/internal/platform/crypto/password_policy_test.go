package crypto

import "testing"

func TestValidatePassword(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "minimum alphanumeric", password: "abc12345"},
		{name: "symbols optional", password: "Abc123!@"},
		{name: "unicode letter", password: "密码abc12345"},
		{name: "too short", password: "abc1234", wantErr: true},
		{name: "letters only", password: "abcdefgh", wantErr: true},
		{name: "digits only", password: "12345678", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if gotErr := ValidatePassword(tt.password) != nil; gotErr != tt.wantErr {
				t.Fatalf("ValidatePassword() error = %v, wantErr %v", gotErr, tt.wantErr)
			}
		})
	}
}
