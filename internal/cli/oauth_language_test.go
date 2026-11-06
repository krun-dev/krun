package cli

import (
	"strings"
	"testing"
)

func TestLoginCallbackPageLanguage(t *testing.T) {
	tests := []struct {
		preferred string
		browser   string
		want      string
		lang      string
	}{
		{"zh-CN", "en-US", "返回终端", `lang="zh-CN"`},
		{"en", "zh-CN", "Return to your terminal", `lang="en"`},
		{"", "en-US,en;q=0.9", "Return to your terminal", `lang="en"`},
		{"invalid", "zh-CN", "返回终端", `lang="zh-CN"`},
	}
	for _, test := range tests {
		page := loginCallbackPage(test.preferred, test.browser)
		if !strings.Contains(page, test.want) || !strings.Contains(page, test.lang) {
			t.Errorf("preferred=%q browser=%q: expected %q and %q", test.preferred, test.browser, test.want, test.lang)
		}
	}
}
