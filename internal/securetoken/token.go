package securetoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

func Random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func NewID(prefix string) string { return prefix + "_" + Random()[:16] }
func Digest(value string) string { b := sha256.Sum256([]byte(value)); return hex.EncodeToString(b[:]) }
