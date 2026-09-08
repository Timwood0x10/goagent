package ares_security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// JWT implementation (HS256) built on the standard library only. A third-party
// JWT library is deliberately avoided (prefer stdlib).
// HS256 is HMAC-SHA256 over base64url(header).base64url(payload); only the
// signed form is accepted on verify — the payload is never trusted as-is.
//
// Tokens carry three claims that the middleware consumes:
//   - sub:  the authenticated subject (user or service name);
//   - role: one of RoleAdmin / RoleOperator / RoleAgent;
//   - exp:  Unix seconds; the middleware rejects expired tokens.
//
// The secret is the only shared state; rotate it to invalidate all tokens.

// jwtHeader is the fixed JOSE header for HS256 tokens.
var jwtHeader = map[string]string{
	"alg": "HS256",
	"typ": "JWT",
}

// ErrInvalidToken is returned when a token is malformed, wrongly signed, or
// missing required claims. It is the catch-all sentinel; callers that need to
// distinguish expiry should use errors.Is(err, ErrTokenExpired) first.
var (
	ErrInvalidToken  = errors.New("invalid token")
	ErrTokenExpired  = errors.New("token expired")
	ErrTokenTooEarly = errors.New("token not yet valid")
	// ErrUnconfiguredSecret means the verifier was handed an empty HMAC key.
	// It is a server misconfiguration, not a client error: callers must fail
	// closed with 5xx and a loud log, never accept the token.
	ErrUnconfiguredSecret = errors.New("jwt: unconfigured secret")
)

// jwtClaims is the wire format of a signed token. NumericDate claims are Unix
// seconds per RFC 7519; RegisteredClaims exists so Verify can decode without
// caring about custom claims the caller did not sign.
type jwtClaims struct {
	Subject string `json:"sub,omitempty"`
	Role    string `json:"role,omitempty"`
	Expires int64  `json:"exp,omitempty"`
	Issued  int64  `json:"iat,omitempty"`
}

// SignJWT issues a signed HS256 token for the given subject and role. The
// token is valid from now until now+ttl. ttl must be positive; an empty role
// or subject is rejected up front so no unsigned-looking token ever exists.
func SignJWT(secret []byte, subject, role string, ttl time.Duration, now time.Time) (string, error) {
	if len(secret) == 0 {
		return "", ErrUnconfiguredSecret
	}
	if subject == "" || role == "" {
		return "", errors.New("jwt: subject and role are required")
	}
	if ttl <= 0 {
		return "", errors.New("jwt: ttl must be positive")
	}
	claims := jwtClaims{
		Subject: subject,
		Role:    role,
		Expires: now.Add(ttl).Unix(),
		Issued:  now.Unix(),
	}
	return encodeSigned(secret, claims)
}

// VerifyJWT validates the token signature, expiry and required claims, and
// returns the extracted subject and role. It returns ErrTokenExpired for an
// otherwise-well-formed token whose exp has passed, and ErrInvalidToken for
// anything else (bad signature, malformed base64, missing claims).
func VerifyJWT(secret []byte, token string, now time.Time) (subject, role string, err error) {
	claims, err := decodeSigned(secret, token)
	if err != nil {
		return "", "", err
	}
	if claims.Expires == 0 {
		return "", "", fmt.Errorf("%w: missing exp", ErrInvalidToken)
	}
	if now.Unix() > claims.Expires {
		return "", "", fmt.Errorf("%w: expired at %d", ErrTokenExpired, claims.Expires)
	}
	if claims.Issued > 0 && now.Unix() < claims.Issued {
		return "", "", fmt.Errorf("%w: issued in the future", ErrTokenTooEarly)
	}
	if claims.Subject == "" || claims.Role == "" {
		return "", "", fmt.Errorf("%w: missing sub or role", ErrInvalidToken)
	}
	return claims.Subject, claims.Role, nil
}

// encodeSigned builds and signs a token from claims. It is the single place
// that constructs the three-part wire form.
func encodeSigned(secret []byte, claims jwtClaims) (string, error) {
	headerJSON, err := json.Marshal(jwtHeader)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal header: %w", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("jwt: marshal claims: %w", err)
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(headerJSON) + "." + enc.EncodeToString(payloadJSON)
	sig := signHS256(secret, []byte(unsigned))
	return unsigned + "." + enc.EncodeToString(sig), nil
}

// decodeSigned verifies the signature and parses the claims. The signature is
// checked before any payload field is trusted (constant-time compare).
//
// An empty secret is refused here rather than only in SignJWT: HMAC with a
// zero-length key is still a well-defined signature, so without this guard any
// attacker could mint a token signed with the empty key and pass verification.
// Verification is the security boundary, so the guard belongs on this choke
// point and not on the caller.
func decodeSigned(secret []byte, token string) (jwtClaims, error) {
	if len(secret) == 0 {
		return jwtClaims{}, ErrUnconfiguredSecret
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, fmt.Errorf("%w: wrong part count", ErrInvalidToken)
	}
	enc := base64.RawURLEncoding
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("%w: bad signature encoding", ErrInvalidToken)
	}
	unsigned := parts[0] + "." + parts[1]
	expected := signHS256(secret, []byte(unsigned))
	if !hmac.Equal(sig, expected) {
		return jwtClaims{}, fmt.Errorf("%w: bad signature", ErrInvalidToken)
	}
	payload, err := enc.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, fmt.Errorf("%w: bad payload encoding", ErrInvalidToken)
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, fmt.Errorf("%w: bad claims json", ErrInvalidToken)
	}
	return claims, nil
}

// signHS256 computes the HMAC-SHA256 of data with the given secret.
func signHS256(secret []byte, data []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(data)
	return mac.Sum(nil)
}
