// Package randtoken generates cryptographically random hexadecimal tokens.
package randtoken

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns 16 random bytes encoded as 32 lowercase hexadecimal characters.
func New() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
