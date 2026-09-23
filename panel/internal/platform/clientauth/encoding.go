// Package clientauth implements the byte-exact, side-effect-free primitives
// frozen by CLIENT-AUTH-01. It deliberately contains no HTTP, database, or
// production-secret integration.
package clientauth

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

var (
	ErrMalformed          = errors.New("clientauth: malformed input")
	ErrUnsupported        = errors.New("clientauth: unsupported value")
	ErrVerificationFailed = errors.New("clientauth: verification failed")
)

func EncodeBase64URLNoPad(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

// DecodeBase64URLNoPad rejects padding, non-URL alphabets and non-canonical
// encodings. expectedLen < 0 means that the decoded length is unconstrained.
func DecodeBase64URLNoPad(value string, expectedLen int) ([]byte, error) {
	if value == "" || !isASCII(value) {
		return nil, fmt.Errorf("%w: empty or non-ASCII base64url", ErrMalformed)
	}
	for i := range len(value) {
		c := value[i]
		if !isBase64URLChar(c) {
			return nil, fmt.Errorf("%w: invalid base64url character", ErrMalformed)
		}
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, fmt.Errorf("%w: non-canonical base64url", ErrMalformed)
	}
	if expectedLen >= 0 && len(decoded) != expectedLen {
		return nil, fmt.Errorf("%w: decoded length %d, want %d", ErrMalformed, len(decoded), expectedLen)
	}
	return decoded, nil
}

func ParseUnixSeconds(value string) (int64, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, fmt.Errorf("%w: non-canonical unix seconds", ErrMalformed)
	}
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return 0, fmt.Errorf("%w: invalid unix seconds", ErrMalformed)
		}
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("%w: invalid unix seconds", ErrMalformed)
	}
	return seconds, nil
}

const microsecondUTCLayout = "2006-01-02T15:04:05.000000Z"

// ParseMicrosecondUTC requires RFC3339 UTC with exactly six fractional digits.
func ParseMicrosecondUTC(value string) (time.Time, error) {
	if len(value) != len("2006-01-02T15:04:05.000000Z") {
		return time.Time{}, fmt.Errorf("%w: timestamp width", ErrMalformed)
	}
	parsed, err := time.Parse(microsecondUTCLayout, value)
	if err != nil || parsed.Format(microsecondUTCLayout) != value {
		return time.Time{}, fmt.Errorf("%w: timestamp must be UTC with six fractional digits", ErrMalformed)
	}
	return parsed, nil
}

func FormatMicrosecondUTC(value time.Time) (string, error) {
	if value.Location() != time.UTC || value.Nanosecond()%1000 != 0 {
		return "", fmt.Errorf("%w: timestamp must be UTC at microsecond precision", ErrMalformed)
	}
	return value.Format(microsecondUTCLayout), nil
}

// ParseCanonicalUUID returns the 16 UUID bytes and rejects uppercase, braces,
// URNs, missing hyphens and non-canonical text.
func ParseCanonicalUUID(value string) ([16]byte, error) {
	var out [16]byte
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return out, fmt.Errorf("%w: UUID shape", ErrMalformed)
	}
	j := 0
	for i := 0; i < len(value); {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			i++
			continue
		}
		hi := fromLowerHex(value[i])
		i++
		if i >= len(value) {
			return [16]byte{}, fmt.Errorf("%w: UUID width", ErrMalformed)
		}
		lo := fromLowerHex(value[i])
		i++
		if hi < 0 || lo < 0 {
			return [16]byte{}, fmt.Errorf("%w: UUID must use lowercase hexadecimal", ErrMalformed)
		}
		out[j] = byte(hi<<4 | lo)
		j++
	}
	if out[8]>>6 != 2 {
		return [16]byte{}, fmt.Errorf("%w: UUID must use RFC 9562 variant", ErrMalformed)
	}
	return out, nil
}

func ParseCanonicalUUIDv7(value string) ([16]byte, error) {
	id, err := ParseCanonicalUUID(value)
	if err != nil {
		return [16]byte{}, err
	}
	if id[6]>>4 != 7 || id[8]>>6 != 2 {
		return [16]byte{}, fmt.Errorf("%w: UUID must have version 7 and RFC 9562 variant", ErrMalformed)
	}
	return id, nil
}

func fromLowerHex(value byte) int {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0')
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10
	default:
		return -1
	}
}

func isASCII(value string) bool {
	for i := range len(value) {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

func isBase64URLChar(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' ||
		value >= '0' && value <= '9' || value == '_' || value == '-'
}
