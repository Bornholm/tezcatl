package model

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns a random 128-bit identifier encoded as hexadecimal.
func NewID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}

	return hex.EncodeToString(buf[:])
}
