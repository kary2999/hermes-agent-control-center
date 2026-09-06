package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	// [skill: go-team-standards · HTTP handler · 安全认证] relay session scopes
	// sessionCookieName is the cookie set by POST /api/v1/session and
	// required by GET /dashboard.
	sessionCookieName = "hermes_session"
	// sessionTTL bounds how long an issued session cookie stays valid.
	sessionTTL = 12 * time.Hour
	// sessionKeyContext domain-separates the session signing key from any
	// other HMAC derived from the shared token.
	sessionKeyContext   = "hermes-relay-session-v1"
	sessionScopeRead    = "read"
	sessionScopeHandoff = "read+handoff"
)

// deriveSessionKey derives a session-signing key from the shared Connector
// token via HMAC-SHA256. The shared token itself is never placed in a
// cookie; only values signed with this derived key are.
func deriveSessionKey(token string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(sessionKeyContext))
	return mac.Sum(nil)
}

// signSessionValue produces an opaque "<expiry>.<hmac>" cookie value: the
// session's Unix expiry timestamp plus its HMAC-SHA256 signature under key.
func signSessionValue(key []byte, expiresAt time.Time) string {
	return signSessionValueScope(key, expiresAt, sessionScopeRead)
}

func signSessionValueScope(key []byte, expiresAt time.Time, scope string) string {
	payload := strconv.FormatInt(expiresAt.Unix(), 10) + "." + scope
	return payload + "." + sessionSignature(key, payload)
}

// verifySessionValue reports whether value is a well-formed, correctly
// signed, not-yet-expired session value for key. Signature comparison is
// constant-time to avoid leaking timing information to an attacker
// submitting forged cookies.
func verifySessionValue(key []byte, value string, now time.Time) bool {
	_, ok := verifySessionValueScope(key, value, now)
	return ok
}

func verifySessionValueScope(key []byte, value string, now time.Time) (string, bool) {
	lastDot := strings.LastIndex(value, ".")
	if lastDot < 0 {
		return "", false
	}
	payload, sig := value[:lastDot], value[lastDot+1:]
	if payload == "" || sig == "" {
		return "", false
	}
	expected := sessionSignature(key, payload)
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expected)) != 1 {
		return "", false
	}
	expiresText, scope, ok := strings.Cut(payload, ".")
	if !ok {
		expiresText = payload
		scope = sessionScopeRead
	}
	expiresAtUnix, err := strconv.ParseInt(expiresText, 10, 64)
	if err != nil {
		return "", false
	}
	if !now.Before(time.Unix(expiresAtUnix, 0)) {
		return "", false
	}
	if scope != sessionScopeRead && scope != sessionScopeHandoff {
		return "", false
	}
	return scope, true
}

func sessionSignature(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
