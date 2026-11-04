package objectdelivery

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

func TestSignedPrivateCDNProtocol(t *testing.T) {
	const signingKey = "fixture-key-for-cdn-signing-32-bytes"
	cfg := &config.WorkerObjectDeliveryConfig{Type: "volc_cdn_a", BaseURL: "https://files.example.com", SigningKey: signingKey, TTLSeconds: 300}
	now := time.Unix(1793750400, 0).UTC()
	bucket := domain.WorkerBucket{ID: "bkt-example", Organization: "org-example"}
	access, err := New(cfg).Access(bucket, "目录/a +?#%.txt", now)
	if err != nil || access.Method != "GET" || !access.ExpiresAt.Equal(now.Add(300*time.Second)) {
		t.Fatal("CDN capability was not issued")
	}
	parsed, err := url.Parse(access.URL)
	if err != nil || parsed.Host != "files.example.com" || parsed.Path != "/org/org-example/buckets/bkt-example/目录/a +?#%.txt" || strings.Contains(access.URL, signingKey) {
		t.Fatal("invalid logical CDN URL")
	}
	// A small independent CDN verifier checks the on-the-wire protocol. It
	// verifies before a cache hit and rewrites only the authenticated URI.
	var originPath string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signature := strings.Split(r.URL.Query().Get("sign"), "-")
		if len(signature) != 4 || signature[2] != "0" {
			w.WriteHeader(403)
			return
		}
		message := strings.Join([]string{r.URL.EscapedPath(), signature[0], signature[1], "0", signingKey}, "-")
		digest := sha256.Sum256([]byte(message))
		if hex.EncodeToString(digest[:]) != signature[3] {
			w.WriteHeader(403)
			return
		}
		originPath = "/worker-data/v1" + r.URL.Path
		w.WriteHeader(204)
	}))
	defer cdn.Close()
	for _, test := range []struct {
		request string
		status  int
	}{
		{cdn.URL + parsed.RequestURI(), 204},
		{cdn.URL + strings.Replace(parsed.RequestURI(), "bkt-example", "bkt-other", 1), 403},
		{cdn.URL + parsed.EscapedPath(), 403},
	} {
		response, err := http.Get(test.request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != test.status {
			t.Fatal("CDN accepted a changed object or rejected the signed URI")
		}
	}
	if originPath != "/worker-data/v1/org/org-example/buckets/bkt-example/目录/a +?#%.txt" {
		t.Fatal("CDN origin rewrite did not preserve the logical scope")
	}
	if _, err := New(cfg).Access(bucket, "folder//object", now); err == nil {
		t.Fatal("ambiguous CDN path was accepted")
	}
}
