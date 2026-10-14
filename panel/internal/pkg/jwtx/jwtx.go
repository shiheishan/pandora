// Package jwtx 提供面板访问令牌（at+jwt）的签发与校验。
package jwtx

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalid 表示令牌无法通过校验。
var ErrInvalid = errors.New("invalid token")

// Domain 区分门户与后台受众。
type Domain string

const (
	Public Domain = "public"
	Admin  Domain = "admin"
)

// Claims 是访问令牌载荷。
type Claims struct {
	jwt.RegisteredClaims
	SessionID string   `json:"sid"`
	AMR       []string `json:"amr,omitempty"`
}

// Issuer 持有域、密钥表与 TTL。
type Issuer struct {
	domain Domain
	ttl    time.Duration
	sign   []byte
	signID string
	keys   map[string][]byte
}

// NewIssuer 构造签发器；current 至少 32 字节，previous 可选且不得与 current 相同。
func NewIssuer(domain Domain, current, previous []byte, ttl time.Duration) (*Issuer, error) {
	if len(current) < 32 {
		return nil, errors.New("current signing key shorter than 32 bytes")
	}
	if previous != nil {
		if len(previous) < 32 {
			return nil, errors.New("previous signing key shorter than 32 bytes")
		}
		if bytes.Equal(current, previous) {
			return nil, errors.New("previous key must differ from current")
		}
	}
	keys := make(map[string][]byte)
	curID := keyID(current)
	keys[curID] = current
	if previous != nil {
		keys[keyID(previous)] = previous
	}
	return &Issuer{
		domain: domain,
		ttl:    ttl,
		sign:   current,
		signID: curID,
		keys:   keys,
	}, nil
}

func keyID(secret []byte) string {
	h := sha256.Sum256(secret)
	return hex.EncodeToString(h[:])[:16]
}

// TTL 返回令牌有效期。
func (i *Issuer) TTL() time.Duration {
	return i.ttl
}

// Sign 签发 HS256 访问令牌。
func (i *Issuer) Sign(userID, sessionID string, amr []string, now time.Time) (token string, expiresAt time.Time, err error) {
	expiresAt = now.Add(i.ttl)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			Audience:  jwt.ClaimStrings{string(i.domain)},
			Issuer:    "pandora-" + string(i.domain),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		SessionID: sessionID,
		AMR:       amr,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t.Header["kid"] = i.signID
	t.Header["typ"] = "at+jwt"
	token, err = t.SignedString(i.sign)
	return token, expiresAt, err
}

// Verify 校验令牌并解析 Claims。
func (i *Issuer) Verify(token string, now time.Time) (*Claims, error) {
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(string(i.domain)),
		jwt.WithIssuer("pandora-"+string(i.domain)),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	claims := &Claims{}
	parsed, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		typ, _ := t.Header["typ"].(string)
		if typ != "at+jwt" {
			return nil, fmt.Errorf("unexpected typ %q", typ)
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := i.keys[kid]
		if !ok || kid == "" {
			return nil, fmt.Errorf("unknown kid %q", kid)
		}
		return key, nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !parsed.Valid {
		return nil, fmt.Errorf("%w: token not valid", ErrInvalid)
	}
	if claims.Subject == "" || claims.SessionID == "" {
		return nil, fmt.Errorf("%w: empty sub or sid", ErrInvalid)
	}
	return claims, nil
}
