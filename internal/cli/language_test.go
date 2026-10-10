package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIAlwaysUsesEnglish(t *testing.T) {
	for _, language := range []string{"", "zh-CN", "en", "invalid"} {
		t.Run(language, func(t *testing.T) {
			t.Setenv("KRUN_LANG", language)
			base := []string{"--config", filepath.Join(t.TempDir(), "config.yaml")}
			var out, errOut bytes.Buffer
			if err := Run(context.Background(), append(base, "help"), strings.NewReader(""), &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "control-plane CLI") || strings.Contains(out.String(), "--lang") || strings.Contains(out.String(), "KRUN_LANG") {
				t.Fatal(out.String())
			}
			if err := Run(context.Background(), append(base, "check", "--help"), strings.NewReader(""), &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(errOut.String(), "Action") {
				t.Fatal(errOut.String())
			}
			if err := Run(context.Background(), append(base, "unknown"), strings.NewReader(""), &out, &errOut); err == nil || err.Error() != `unknown command "unknown"; use krun help` {
				t.Fatal(err)
			}
		})
	}
}

func TestCLIRequestsEnglishErrors(t *testing.T) {
	t.Setenv("KRUN_LANG", "zh-CN")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Language"); got != "en" {
			t.Errorf("Accept-Language = %q, want en", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"authentication required"}}`))
	}))
	defer server.Close()
	var out bytes.Buffer
	err := Run(context.Background(), []string{"--config", filepath.Join(t.TempDir(), "config.yaml"), "--server", server.URL, "whoami"}, strings.NewReader(""), &out, &out)
	if err == nil || err.Error() != "UNAUTHENTICATED: authentication required" {
		t.Fatal(err)
	}
}
