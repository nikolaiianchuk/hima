// Package token creates the secrets behind Invite links, Manage links, the
// Owner feed and admin sessions. Only hashes are ever stored.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// New returns a URL-safe random token carrying 128 bits of entropy.
func New() string {
	b := make([]byte, 16)
	rand.Read(b) // never returns an error; panics if the OS source fails
	return base64.RawURLEncoding.EncodeToString(b)
}

// Hash returns the value to store and look up in place of a token.
func Hash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}
