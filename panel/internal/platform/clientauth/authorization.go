package clientauth

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

const authorizationPrefix = "Aegis-PoP "

// ParseAegisPoPAuthorization consumes the original HTTP field lines. Passing a
// pre-joined header value would erase cardinality and is intentionally not an
// accepted API shape.
func ParseAegisPoPAuthorization(fieldLines []string) ([]byte, error) {
	if len(fieldLines) != 1 {
		return nil, fmt.Errorf("%w: Authorization must have exactly one field line", ErrMalformed)
	}
	value := fieldLines[0]
	if !isASCII(value) || len(value) <= len(authorizationPrefix) || value[:len(authorizationPrefix)] != authorizationPrefix {
		return nil, fmt.Errorf("%w: invalid Authorization scheme", ErrMalformed)
	}
	token := value[len(authorizationPrefix):]
	if !isJWTCompact(token) {
		return nil, fmt.Errorf("%w: invalid JWT compact serialization", ErrMalformed)
	}
	return []byte(token), nil
}

func RejectAuthorization(fieldLines []string) error {
	if len(fieldLines) != 0 {
		return fmt.Errorf("%w: Authorization is forbidden", ErrMalformed)
	}
	return nil
}

func AuthorizationTokenHash(token []byte) ([32]byte, error) {
	if !isJWTCompact(string(token)) {
		return [32]byte{}, fmt.Errorf("%w: invalid JWT compact serialization", ErrMalformed)
	}
	return sha256.Sum256(token), nil
}

func isJWTCompact(token string) bool {
	if token == "" || !isASCII(token) {
		return false
	}
	dots := 0
	segmentStart := 0
	for i := range len(token) {
		if token[i] == '.' {
			if i == segmentStart || !validBase64URLSegment(token[segmentStart:i]) {
				return false
			}
			dots++
			segmentStart = i + 1
			continue
		}
		if !isBase64URLChar(token[i]) {
			return false
		}
	}
	return dots == 2 && segmentStart < len(token) && validBase64URLSegment(token[segmentStart:]) && !strings.Contains(token, "..")
}

func validBase64URLSegment(value string) bool {
	_, err := DecodeBase64URLNoPad(value, -1)
	return err == nil
}
