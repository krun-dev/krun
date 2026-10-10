// Package localize translates human-readable output while preserving protocol identifiers.
package localize

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/text/language"
)

const Chinese = "zh-CN"
const English = "en"

//go:embed zh-CN.json
var rawCatalog []byte

var catalog map[string]string
var reverse map[string]string
var patterns []pattern
var matcher = language.NewMatcher([]language.Tag{language.SimplifiedChinese, language.English})
var placeholder = regexp.MustCompile(`%[sdq]`)

type pattern struct {
	english, chinese string
	match            *regexp.Regexp
}

func init() {
	if err := json.Unmarshal(rawCatalog, &catalog); err != nil {
		panic(err)
	}
	reverse = map[string]string{}
	keys := make([]string, 0, len(catalog))
	for key := range catalog {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := catalog[key]
		reverse[value] = key
		if placeholder.MatchString(key) {
			parts := placeholder.Split(key, -1)
			for i := range parts {
				parts[i] = regexp.QuoteMeta(parts[i])
			}
			patterns = append(patterns, pattern{key, value, regexp.MustCompile(`(?s)^` + strings.Join(parts, `(.*?)`) + `$`)})
		}
	}
}

func Normalize(value string) (string, error) {
	switch strings.ToLower(value) {
	case "", "zh", "zh-cn":
		return Chinese, nil
	case "en", "en-us", "en-gb":
		return English, nil
	default:
		return Chinese, fmt.Errorf("invalid language; use zh-CN or en")
	}
}

// RequestLanguage honors quality weights and defaults to Chinese for unsupported languages.
func RequestLanguage(header string) string {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil || len(tags) == 0 {
		return Chinese
	}
	supported := tags[:0]
	for _, tag := range tags {
		base, _ := tag.Base()
		if base.String() == "zh" || base.String() == "en" {
			supported = append(supported, tag)
		}
	}
	if len(supported) == 0 {
		return Chinese
	}
	_, index, _ := matcher.Match(supported...)
	if index == 1 {
		return English
	}
	return Chinese
}

func Text(lang, message string) string {
	if lang == English {
		if value, ok := reverse[message]; ok {
			return value
		}
	} else if value, ok := catalog[message]; ok {
		return value
	}
	if lang != English {
		for _, p := range patterns {
			if values := p.match.FindStringSubmatch(message); values != nil {
				i := 0
				return placeholder.ReplaceAllStringFunc(p.chinese, func(string) string {
					i++
					return values[i]
				})
			}
		}
	}
	// Preserve the machine-readable code in a CLI API error.
	if prefix, body, ok := strings.Cut(message, ": "); ok {
		if translated := textPart(lang, body); translated != body {
			return prefix + ": " + translated
		}
	}
	return message
}

func textPart(lang, message string) string {
	if lang == English {
		if value, ok := reverse[message]; ok {
			return value
		}
	} else if value, ok := catalog[message]; ok {
		return value
	}
	return message
}

func Format(lang, message string, values ...any) string {
	return fmt.Sprintf(Text(lang, message), values...)
}

type contextKey struct{}

func WithLanguage(ctx context.Context, lang string) context.Context {
	return context.WithValue(ctx, contextKey{}, lang)
}
func Language(ctx context.Context) string {
	if lang, ok := ctx.Value(contextKey{}).(string); ok {
		return lang
	}
	return Chinese
}

type localizedError struct {
	err  error
	lang string
}

func (e *localizedError) Error() string {
	var pathErr *os.PathError
	if errors.As(e.err, &pathErr) {
		return Format(e.lang, "file operation failed: %s", pathErr.Path)
	}
	var urlErr *url.Error
	if errors.As(e.err, &urlErr) {
		return Text(e.lang, "network request failed; check the connection and retry")
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(e.err, &typeErr) {
		return Format(e.lang, "invalid configuration value for field %s", typeErr.Field)
	}
	if strings.HasPrefix(e.err.Error(), "yaml:") {
		return Text(e.lang, "invalid YAML format; check indentation and value syntax")
	}
	return Text(e.lang, e.err.Error())
}
func (e *localizedError) Unwrap() error { return e.err }
func Error(lang string, err error) error {
	if err == nil {
		return nil
	}
	return &localizedError{err, lang}
}

type translatedWriter struct {
	io.Writer
	lang string
}

func (w translatedWriter) Write(raw []byte) (int, error) {
	text := Text(w.lang, string(raw))
	if w.lang != English {
		text = strings.ReplaceAll(text, "(default ", "("+Text(w.lang, "default")+" ")
	}
	_, err := io.WriteString(w.Writer, text)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}
func Writer(lang string, out io.Writer) io.Writer { return translatedWriter{out, lang} }

// Options extracts language flags without changing command-specific arguments.
func Options(args []string) (string, []string, error) {
	value := os.Getenv("KRUN_LANG")
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--lang" {
			if i+1 >= len(args) {
				return Chinese, nil, fmt.Errorf("missing global flag value")
			}
			i++
			value = args[i]
		} else if strings.HasPrefix(args[i], "--lang=") {
			value = strings.TrimPrefix(args[i], "--lang=")
		} else {
			filtered = append(filtered, args[i])
		}
	}
	lang, err := Normalize(value)
	return lang, filtered, err
}
