package clientauth

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	canonicalDomain = "AEGIS-DEVICE-POP-V1"
	nonceDomain     = "AEGIS-NONCE-REQUEST-V1"
	refreshDomain   = "AEGIS-REFRESH-REPLAY-REQUEST-V1"
	issuanceDomain  = "AEGIS-DEVICE-ISSUANCE-REPLAY-V1"
)

type CredentialKind string

const (
	CredentialNone         CredentialKind = "none"
	CredentialDeviceCode   CredentialKind = "device_code"
	CredentialAccessToken  CredentialKind = "access_token"
	CredentialRefreshToken CredentialKind = "refresh_token"
)

type CanonicalRequestV1 struct {
	Origin         string
	Method         string
	Target         string
	Timestamp      string
	NonceB64U      string
	Body           []byte
	CredentialKind CredentialKind
	Credential     []byte
}

// CanonicalBytesV1 is an opaque value that can only be produced by the strict
// canonical request builder. Bytes returns a defensive copy for signing.
type CanonicalBytesV1 struct {
	value []byte
}

func (canonical CanonicalBytesV1) Bytes() []byte {
	return append([]byte(nil), canonical.value...)
}

func BuildCanonicalRequestV1(request CanonicalRequestV1) (CanonicalBytesV1, error) {
	if err := validateOrigin(request.Origin); err != nil {
		return CanonicalBytesV1{}, err
	}
	if !validMethod(request.Method) {
		return CanonicalBytesV1{}, fmt.Errorf("%w: method must be uppercase ASCII token", ErrMalformed)
	}
	if err := validateTarget(request.Target); err != nil {
		return CanonicalBytesV1{}, err
	}
	if _, err := ParseUnixSeconds(request.Timestamp); err != nil {
		return CanonicalBytesV1{}, err
	}
	if _, err := DecodeBase64URLNoPad(request.NonceB64U, 16); err != nil {
		return CanonicalBytesV1{}, fmt.Errorf("%w: nonce: %v", ErrMalformed, err)
	}
	if err := validateCredential(request.CredentialKind, request.Credential); err != nil {
		return CanonicalBytesV1{}, err
	}
	bodyHash := sha256.Sum256(request.Body)
	ath := ""
	if request.CredentialKind != CredentialNone {
		credentialHash := sha256.Sum256(request.Credential)
		ath = EncodeBase64URLNoPad(credentialHash[:])
	}
	canonical := canonicalDomain + "\n" +
		"origin:" + request.Origin + "\n" +
		"method:" + request.Method + "\n" +
		"target:" + request.Target + "\n" +
		"timestamp:" + request.Timestamp + "\n" +
		"nonce:" + request.NonceB64U + "\n" +
		"body-sha256:" + EncodeBase64URLNoPad(bodyHash[:]) + "\n" +
		"credential-kind:" + string(request.CredentialKind) + "\n" +
		"ath:" + ath + "\n"
	return CanonicalBytesV1{value: []byte(canonical)}, nil
}

func NonceRequestHash(canonical CanonicalBytesV1) ([32]byte, error) {
	if len(canonical.value) == 0 {
		return [32]byte{}, fmt.Errorf("%w: invalid canonical request bytes", ErrMalformed)
	}
	input := make([]byte, 0, len(nonceDomain)+1+len(canonical.value))
	input = append(input, nonceDomain...)
	input = append(input, 0)
	input = append(input, canonical.value...)
	return sha256.Sum256(input), nil
}

type RefreshReplayRecord struct {
	Origin           string
	Method           string
	Target           string
	BodySHA256B64U   string
	RefreshATHB64U   string
	KeyIDB64U        string
	RefreshRequestID string
}

func BuildRefreshReplayRecord(record RefreshReplayRecord) ([]byte, error) {
	if err := validateOrigin(record.Origin); err != nil {
		return nil, err
	}
	if record.Method != "POST" || record.Target != "/v1/auth/refresh" {
		return nil, fmt.Errorf("%w: frozen refresh method or target mismatch", ErrMalformed)
	}
	for name, value := range map[string]string{
		"body-sha256": record.BodySHA256B64U,
		"ath":         record.RefreshATHB64U,
		"key-id":      record.KeyIDB64U,
	} {
		if _, err := DecodeBase64URLNoPad(value, 32); err != nil {
			return nil, fmt.Errorf("%w: %s", err, name)
		}
	}
	if _, err := ParseCanonicalUUIDv7(record.RefreshRequestID); err != nil {
		return nil, fmt.Errorf("%w: refresh-request-id", err)
	}
	value := refreshDomain + "\n" +
		"origin:" + record.Origin + "\n" +
		"method:POST\n" +
		"target:/v1/auth/refresh\n" +
		"body-sha256:" + record.BodySHA256B64U + "\n" +
		"credential-kind:refresh_token\n" +
		"ath:" + record.RefreshATHB64U + "\n" +
		"key-id:" + record.KeyIDB64U + "\n" +
		"refresh-request-id:" + record.RefreshRequestID + "\n"
	return []byte(value), nil
}

func RefreshReplayRequestHash(record RefreshReplayRecord) ([32]byte, error) {
	encoded, err := BuildRefreshReplayRecord(record)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

type IssuanceStableFields struct {
	Origin                   string
	Method                   string
	Target                   string
	RequestBodySHA256B64U    string
	DeviceCodeATHB64U        string
	KeyFingerprintSHA256B64U string
}

func BuildIssuanceStableRecord(fields IssuanceStableFields) ([]byte, error) {
	if err := validateOrigin(fields.Origin); err != nil {
		return nil, err
	}
	if fields.Method != "POST" || fields.Target != "/v1/device/token" {
		return nil, fmt.Errorf("%w: frozen issuance method or target mismatch", ErrMalformed)
	}
	values := []string{fields.Origin, fields.Method, fields.Target, fields.RequestBodySHA256B64U, fields.DeviceCodeATHB64U, fields.KeyFingerprintSHA256B64U}
	for index := 3; index < len(values); index++ {
		if _, err := DecodeBase64URLNoPad(values[index], 32); err != nil {
			return nil, fmt.Errorf("%w: issuance hash field %d", err, index)
		}
	}
	out := make([]byte, 0, len(issuanceDomain)+1+6*4+256)
	out = append(out, issuanceDomain...)
	out = append(out, 0)
	for _, value := range values {
		bytes := []byte(value)
		if uint64(len(bytes)) > math.MaxUint32 {
			return nil, fmt.Errorf("%w: issuance field too long", ErrMalformed)
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(bytes)))
		out = append(out, length[:]...)
		out = append(out, bytes...)
	}
	return out, nil
}

func IssuanceStableRequestHash(fields IssuanceStableFields) ([32]byte, error) {
	encoded, err := BuildIssuanceStableRecord(fields)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func validateCredential(kind CredentialKind, credential []byte) error {
	switch kind {
	case CredentialNone:
		if len(credential) != 0 {
			return fmt.Errorf("%w: none credential must be empty", ErrMalformed)
		}
	case CredentialDeviceCode, CredentialRefreshToken:
		if len(credential) == 0 || !isASCII(string(credential)) {
			return fmt.Errorf("%w: credential must be non-empty ASCII", ErrMalformed)
		}
	case CredentialAccessToken:
		if !isJWTCompact(string(credential)) {
			return fmt.Errorf("%w: access token", ErrMalformed)
		}
	default:
		return fmt.Errorf("%w: credential kind", ErrUnsupported)
	}
	return nil
}

func validateOrigin(origin string) error {
	if origin == "" || !isASCII(origin) || containsASCIIControlOrSpace(origin) {
		return fmt.Errorf("%w: origin bytes", ErrMalformed)
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return fmt.Errorf("%w: canonical HTTPS origin", ErrMalformed)
	}
	hostname := parsed.Hostname()
	if hostname == "" || origin != strings.ToLower(origin) || strings.HasSuffix(hostname, ".") || net.ParseIP(hostname) != nil || !validDNSName(hostname) {
		return fmt.Errorf("%w: non-canonical origin", ErrMalformed)
	}
	if strings.HasSuffix(parsed.Host, ":") || strings.Contains(parsed.Host, "[") || strings.Contains(parsed.Host, "]") {
		return fmt.Errorf("%w: empty port or IP-literal authority", ErrMalformed)
	}
	port := parsed.Port()
	if port != "" {
		if port == "443" || len(port) > 1 && port[0] == '0' {
			return fmt.Errorf("%w: default or non-canonical port", ErrMalformed)
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return fmt.Errorf("%w: invalid port", ErrMalformed)
		}
	}
	canonicalAuthority := hostname
	if port != "" {
		canonicalAuthority += ":" + port
	}
	if parsed.Host != canonicalAuthority || parsed.String() != origin || origin != "https://"+canonicalAuthority {
		return fmt.Errorf("%w: origin did not round-trip exactly", ErrMalformed)
	}
	return nil
}

func validateTarget(target string) error {
	if target == "" || target[0] != '/' || !isASCII(target) || containsASCIIControlOrSpace(target) || strings.Contains(target, "#") {
		return fmt.Errorf("%w: target", ErrMalformed)
	}
	for index := 0; index < len(target); index++ {
		if target[index] == '%' {
			if index+2 >= len(target) || !isHex(target[index+1]) || !isHex(target[index+2]) {
				return fmt.Errorf("%w: malformed percent escape", ErrMalformed)
			}
			index += 2
		}
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed.IsAbs() || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery || parsed.RequestURI() != target {
		return fmt.Errorf("%w: target is not exact origin-form", ErrMalformed)
	}
	expected := parsed.EscapedPath()
	if parsed.RawQuery != "" {
		expected += "?" + parsed.RawQuery
	}
	if expected != target {
		return fmt.Errorf("%w: target escaped-path did not round-trip", ErrMalformed)
	}
	return nil
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'F' || value >= 'a' && value <= 'f'
}

func containsASCIIControlOrSpace(value string) bool {
	for index := range len(value) {
		if value[index] <= 0x20 || value[index] == 0x7f {
			return true
		}
	}
	return false
}

func validDNSName(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	labels := strings.Split(value, ".")
	hasLetter := false
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := range len(label) {
			character := label[index]
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
			if character >= 'a' && character <= 'z' {
				hasLetter = true
			}
		}
	}
	return hasLetter
}

func validMethod(method string) bool {
	if method == "" || !isASCII(method) {
		return false
	}
	for i := range len(method) {
		if method[i] < 'A' || method[i] > 'Z' {
			return false
		}
	}
	return true
}
