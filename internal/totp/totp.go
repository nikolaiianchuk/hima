// Package totp implements time-based one-time passwords (RFC 6238) as used
// by authenticator apps: 6 digits, 30-second steps, HMAC-SHA1.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const step = 30 * time.Second

var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random 160-bit secret in base32.
func NewSecret() string {
	b := make([]byte, 20)
	rand.Read(b)
	return encoding.EncodeToString(b)
}

// URL is the otpauth:// link authenticator apps import (often as a QR code).
func URL(secret, account string) string {
	v := url.Values{"secret": {secret}, "issuer": {"hima"}}
	return "otpauth://totp/hima:" + url.PathEscape(account) + "?" + v.Encode()
}

// ParseSecret validates and normalises a base32 secret.
func ParseSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	key, err := encoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(key) < 10 {
		return nil, fmt.Errorf("not a valid base32 TOTP secret")
	}
	return key, nil
}

// Code is the code for counter.
func Code(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", n%1_000_000)
}

// Verifier checks codes, allowing one step of clock drift either way, and
// refuses a code (time step) that was already used.
type Verifier struct {
	Key []byte
	Now func() time.Time

	mu   sync.Mutex
	last uint64
}

func (v *Verifier) Verify(code string) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return false
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	cur := uint64(now().Unix()) / uint64(step.Seconds())
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, c := range []uint64{cur - 1, cur, cur + 1} {
		if c <= v.last {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(Code(v.Key, c)), []byte(code)) == 1 {
			v.last = c
			return true
		}
	}
	return false
}
