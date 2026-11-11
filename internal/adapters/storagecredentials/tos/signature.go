package tos

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/krun-dev/krun/internal/securetoken"
	"net/http"
	"strings"
	"time"
)

func hash(value string) string { return securetoken.Digest(value) }

func hmacSHA256(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(value))
	return h.Sum(nil)
}

// Volcengine OpenAPI uses HMAC-SHA256 with the raw SK and a /request
// terminator. This is intentionally separate from S3's AWS4 derivation.
func sign(req *http.Request, body string, options Options, now time.Time) {
	stamp := now.Format("20060102T150405Z")
	day := stamp[:8]
	payload := hash(body)
	signedHeaders := "content-type;host;x-content-sha256;x-date"
	headers := "content-type:" + req.Header.Get("Content-Type") + "\nhost:" + req.URL.Host + "\nx-content-sha256:" + payload + "\nx-date:" + stamp + "\n"
	query := strings.ReplaceAll(req.URL.Query().Encode(), "+", "%20")
	canonical := strings.Join([]string{req.Method, "/", query, headers, signedHeaders, payload}, "\n")
	scope := day + "/" + options.Region + "/sts/request"
	key := []byte(options.SecretAccessKey)
	for _, component := range []string{day, options.Region, "sts", "request"} {
		key = hmacSHA256(key, component)
	}
	signature := hex.EncodeToString(hmacSHA256(key, "HMAC-SHA256\n"+stamp+"\n"+scope+"\n"+hash(canonical)))
	req.Header.Set("X-Date", stamp)
	req.Header.Set("X-Content-Sha256", payload)
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+options.AccessKeyID+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
}
