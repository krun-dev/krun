package localize

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLanguageNegotiation(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"", Chinese}, {"en-US,en;q=0.9", English}, {"zh-CN,en;q=0.8", Chinese},
		{"zh-CN;q=0.1,en;q=0.9", English}, {"de", Chinese}, {"not valid", Chinese},
	} {
		if got := RequestLanguage(tc.header); got != tc.want {
			t.Fatalf("%q: got %s, want %s", tc.header, got, tc.want)
		}
	}
}

func TestOutputPreservesValuesAndErrorIdentity(t *testing.T) {
	if got := Text(Chinese, `unknown command "测试"; use krun help`); got != `未知命令 "测试"；请运行 krun help` {
		t.Fatal(got)
	}
	if got := Format(Chinese, "environment variable %s is unset or empty", "KRUN_DB"); got != "环境变量 KRUN_DB 未设置或为空" {
		t.Fatal(got)
	}
	if Text(English, "没有访问权限") != "Permission denied" || Text(English, "AUTH: 没有访问权限") != "AUTH: Permission denied" {
		t.Fatal("reverse translation failed")
	}
	original := fmt.Errorf("Permission denied")
	translated := Error(Chinese, original)
	if !errors.Is(translated, original) || translated.Error() != "没有访问权限" {
		t.Fatal("error identity or translation changed")
	}
	if Error(Chinese, nil) != nil {
		t.Fatal("nil error changed")
	}
	var out bytes.Buffer
	raw := []byte("Usage of keys:\n")
	n, err := Writer(Chinese, &out).Write(raw)
	if err != nil || n != len(raw) || out.String() != "keys 用法：\n" {
		t.Fatal("writer translation failed")
	}
	for english, chinese := range catalog {
		if len(placeholder.FindAllString(english, -1)) != len(placeholder.FindAllString(chinese, -1)) {
			t.Fatalf("placeholder mismatch: %q", english)
		}
		if strings.Contains(chinese, "{{") {
			t.Fatal("wrong interpolation syntax")
		}
	}
}

func TestLanguageFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("KRUN_LANG", "en")
	lang, args, err := Options([]string{"keys", "--lang=zh-CN", "list", "--org", "org_1"})
	if err != nil || lang != Chinese || strings.Join(args, " ") != "keys list --org org_1" {
		t.Fatal(lang, args, err)
	}
	if _, _, err = Options([]string{"--lang", "invalid"}); err == nil {
		t.Fatal("invalid language accepted")
	}
	if _, _, err = Options([]string{"--lang"}); err == nil {
		t.Fatal("missing language accepted")
	}
}
