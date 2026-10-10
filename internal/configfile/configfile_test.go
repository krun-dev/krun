package configfile

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestYAMLTypesAndNumericPrecision(t *testing.T) {
	var value struct {
		Name string         `json:"name"`
		Body map[string]any `json:"body"`
	}
	err := Decode("config.yml", []byte(`# comments are supported
name: test
body:
  id: 123456789012345678901234567890
  status: 0
  decimal: 1.0
  enabled: true
  wildcard: '*'
  items: [read, null]
`), &value)
	if err != nil {
		t.Fatal(err)
	}
	if value.Body["id"] != json.Number("123456789012345678901234567890") || value.Body["status"] != json.Number("0") || value.Body["decimal"] != json.Number("1.0") || value.Body["enabled"] != true || value.Body["wildcard"] != "*" {
		t.Fatalf("YAML types changed: %#v", value.Body)
	}
}

func TestRejectAmbiguousOrInvalidConfiguration(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":             "unknown: true",
		"wrong type":                "name: 12",
		"duplicate":                 "name: one\nname: two",
		"nested duplicate":          "body: {a: 1, a: 2}",
		"non-string key":            "body: {1: value}",
		"multiple documents":        "name: one\n---\nname: two",
		"invalid trailing document": "name: one\n---\n[",
		"alias":                     "body: {a: &v hello, b: *v}",
		"merge":                     "body: {<<: {a: 1}}",
		"infinity":                  "body: {number: .inf}",
		"nan":                       "body: {number: .nan}",
		"empty":                     "",
	} {
		t.Run(name, func(t *testing.T) {
			var value struct {
				Name string         `json:"name"`
				Body map[string]any `json:"body"`
			}
			if Decode("config.yaml", []byte(raw), &value) == nil {
				t.Fatal("invalid YAML accepted")
			}
		})
	}
	var value any
	if Decode("config.json", []byte(`{"name":"test"}`), &value) == nil {
		t.Fatal("JSON config accepted")
	}
	if _, err := Marshal("config.json", map[string]string{"name": "test"}); err == nil {
		t.Fatal("JSON config written")
	}
}

func TestMarshalYAMLRoundTrip(t *testing.T) {
	want := map[string]any{"wildcard": "*", "numeric_text": "123", "boolean_text": "true", "token": "secret:# value\nnext", "number": json.Number("12345678901234567890"), "enabled": true}
	raw, err := Marshal("config.yaml", want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(raw), "{") {
		t.Fatal("YAML output uses JSON formatting")
	}
	var got any
	if err = Decode("config.yaml", raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("roundtrip changed values: %#v", got)
	}
}

func TestEnvironmentExpansionPreservesYAMLStructure(t *testing.T) {
	t.Setenv("CONFIG_TEST_VALUE", "quoted: value\nother: injected\n# comment $literal ${NO_RECURSION}")
	t.Setenv("CONFIG_TEST_HOST", "db.example.com")
	t.Setenv("CONFIG_TEST_EMPTY", "")
	var got struct {
		Name string         `json:"name"`
		Body map[string]any `json:"body"`
	}
	raw := []byte(`name: ${CONFIG_TEST_VALUE}
body:
  url: "postgres://${CONFIG_TEST_HOST}:5432/app"
  fallback: ${CONFIG_TEST_EMPTY:-local}
  unset_fallback: ${CONFIG_TEST_UNSET:-fallback}
  empty_fallback: ${CONFIG_TEST_UNSET:-}
  literal: $${CONFIG_TEST_VALUE}
  plain_dollar: $literal
  items: ["${CONFIG_TEST_HOST}", direct]
  headers: {X-App: "${CONFIG_TEST_HOST}"}
`)
	t.Setenv("CONFIG_TEST_UNSET", "")
	if err := DecodeWithEnv("server.yaml", raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "quoted: value\nother: injected\n# comment $literal ${NO_RECURSION}" {
		t.Fatal("environment value changed YAML structure or expanded twice")
	}
	if got.Body["url"] != "postgres://db.example.com:5432/app" || got.Body["fallback"] != "local" || got.Body["unset_fallback"] != "fallback" || got.Body["empty_fallback"] != "" || got.Body["literal"] != "${CONFIG_TEST_VALUE}" || got.Body["plain_dollar"] != "$literal" {
		t.Fatal("environment expansion semantics changed")
	}
	if got.Body["items"].([]any)[0] != "db.example.com" || got.Body["headers"].(map[string]any)["X-App"] != "db.example.com" {
		t.Fatal("nested references not resolved")
	}
	var literal struct {
		Name string `json:"name"`
	}
	if err := Decode("profile.yaml", []byte("name: ${CONFIG_TEST_VALUE}"), &literal); err != nil || literal.Name != "${CONFIG_TEST_VALUE}" {
		t.Fatal("literal YAML decoding unexpectedly expands environment")
	}
}

func TestEnvironmentExpansionRejectsMissingOrMalformedReferences(t *testing.T) {
	t.Setenv("CONFIG_TEST_EMPTY", "")
	for _, raw := range []string{"name: ${CONFIG_TEST_EMPTY}", "name: ${CONFIG_TEST_EMPTY", "name: ${}", "name: ${bad-name}", "name: ${CONFIG_TEST_EMPTY:?unsupported}", "name: ${CONFIG_TEST_EMPTY:-${OTHER}}"} {
		var got struct {
			Name string `json:"name"`
		}
		if err := DecodeWithEnv("server.yaml", []byte(raw), &got); err == nil {
			t.Fatal("invalid reference accepted")
		}
	}
}
