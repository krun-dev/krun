package control

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAPIErrorLanguageKeepsProtocol(t *testing.T) {
	s, store := testServer(t)
	defer store.Close()
	for _, tc := range []struct{ language, want, tag string }{
		{"", "请先登录", "zh-CN"},
		{"en-US", "Authentication required", "en"},
		{"zh-CN", "请先登录", "zh-CN"},
	} {
		r := httptest.NewRequest("GET", "/auth/me", nil)
		r.Header.Set("Accept-Language", tc.language)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var envelope struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if w.Code != 401 || envelope.Error.Code != "UNAUTHENTICATED" || envelope.Error.Message != tc.want || w.Header().Get("Content-Language") != tc.tag {
			t.Fatal(w.Code, envelope, w.Header())
		}
	}
}
