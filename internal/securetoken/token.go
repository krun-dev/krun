package securetoken

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

func Random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

const idBytes = 12

func NewID(prefix string) string {
	var random [idBytes]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(err)
	}
	return prefix + "-" + hex.EncodeToString(random[:])
}

// IsID reports whether value has the generated ID format for prefix.
func IsID(value, prefix string) bool {
	suffix, ok := strings.CutPrefix(value, prefix+"-")
	if !ok || len(suffix) != hex.EncodedLen(idBytes) {
		return false
	}
	return strings.Trim(suffix, "0123456789abcdef") == ""
}

func Digest(value string) string { return DigestBytes([]byte(value)) }

func DigestBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// UUID is the public identifier shared with Cloudflare-compatible version APIs.
func UUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	encoded := hex.EncodeToString(b)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
