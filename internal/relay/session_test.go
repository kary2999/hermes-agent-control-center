package relay

import (
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifySessionValueRoundTrip(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	value := signSessionValue(key, now.Add(time.Hour))

	if !verifySessionValue(key, value, now) {
		t.Fatal("verifySessionValue() = false, want true for a freshly signed, unexpired value")
	}
}

func TestSignSessionValueDoesNotContainSharedToken(t *testing.T) {
	token := "super-secret-shared-token-xyz"
	key := deriveSessionKey(token)
	value := signSessionValue(key, time.Now().Add(time.Hour))

	if strings.Contains(value, token) {
		t.Fatalf("signSessionValue() = %q, must not contain the raw shared token %q", value, token)
	}
}

func TestVerifySessionValueRejectsExpired(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	value := signSessionValue(key, now.Add(-time.Second)) // already expired

	if verifySessionValue(key, value, now) {
		t.Fatal("verifySessionValue() = true, want false for an expired value")
	}
}

func TestVerifySessionValueRejectsTamperedSignature(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	value := signSessionValue(key, now.Add(time.Hour))

	tampered := value[:len(value)-1] + flipHexChar(value[len(value)-1])
	if verifySessionValue(key, tampered, now) {
		t.Fatal("verifySessionValue() = true, want false for a tampered signature")
	}
}

func TestVerifySessionValueRejectsTamperedPayload(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	value := signSessionValue(key, now.Add(time.Hour))

	parts := strings.SplitN(value, ".", 2)
	forged := parts[0] + "9999999999." + parts[1] // extend the expiry, keep the old signature

	if verifySessionValue(key, forged, now) {
		t.Fatal("verifySessionValue() = true, want false for a forged payload with a stale signature")
	}
}

func TestVerifySessionValueRejectsMalformedValue(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []string{"", "no-dot-here", ".", "123.", ".abc", "abc.def.ghi"}
	for _, v := range cases {
		if verifySessionValue(key, v, now) {
			t.Errorf("verifySessionValue(%q) = true, want false for malformed input", v)
		}
	}
}

func TestVerifySessionValueRejectsWrongKey(t *testing.T) {
	key := deriveSessionKey("shared-secret-token")
	otherKey := deriveSessionKey("a-different-token")
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	value := signSessionValue(key, now.Add(time.Hour))

	if verifySessionValue(otherKey, value, now) {
		t.Fatal("verifySessionValue() = true, want false when verified against a key derived from a different token")
	}
}

func flipHexChar(c byte) string {
	if c == '0' {
		return "1"
	}
	return "0"
}
