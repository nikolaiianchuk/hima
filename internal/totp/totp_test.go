package totp

import (
	"testing"
	"time"
)

// RFC 6238 appendix B test vectors (SHA1, 8-digit codes truncated to 6).
func TestRFCVectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1234567890: "005924", 2000000000: "279037"} {
		if got := Code(key, uint64(unix/30)); got != want {
			t.Errorf("Code(%d) = %s, want %s", unix, got, want)
		}
	}
}

func TestVerifier(t *testing.T) {
	secret := NewSecret()
	key, err := ParseSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	v := &Verifier{Key: key, Now: func() time.Time { return now }}
	cur := uint64(now.Unix() / 30)
	if !v.Verify(Code(key, cur-1)) {
		t.Fatal("previous step (clock drift) refused")
	}
	if !v.Verify(Code(key, cur)) {
		t.Fatal("current code refused")
	}
	if v.Verify(Code(key, cur)) {
		t.Fatal("replayed code accepted")
	}
	if v.Verify(Code(key, cur-1)) {
		t.Fatal("older code accepted after a newer one")
	}
	if v.Verify("12345") || v.Verify(Code(key, cur+5)) {
		t.Fatal("bad code accepted")
	}
	if _, err := ParseSecret("not base32!"); err == nil {
		t.Error("bad secret accepted")
	}
}
