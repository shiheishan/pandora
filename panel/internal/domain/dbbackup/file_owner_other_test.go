//go:build !linux

package dbbackup

import "testing"

func trustCurrentUserAsSecureOwner(*testing.T) {}
