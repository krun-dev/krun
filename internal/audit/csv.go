package audit

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/krun-dev/krun/internal/authorization"
)

func ExportCSV(ctx context.Context, store Reader, target authorization.Target, current string, at time.Time, redact bool) ([]byte, error) {
	_, entries, err := readSnapshot(ctx, store, target, current, at)
	if err != nil {
		return nil, err
	}

	var output bytes.Buffer
	// Excel uses the BOM to recognize UTF-8 when opening a CSV directly.
	output.WriteString("\uFEFF")
	writer := csv.NewWriter(&output)
	if err := writer.Write([]string{
		"id", "time", "actor", "credential", "key_id", "action", "resource",
		"organization_id", "allowed", "request_id", "revision",
	}); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := writer.Write([]string{
			identifierCell(entry.ID, redact),
			entry.Time.UTC().Format(time.RFC3339Nano),
			identifierCell(entry.Actor, redact),
			safeCell(entry.Credential),
			identifierCell(entry.KeyID, redact),
			safeCell(entry.Action),
			identifierCell(entry.Resource, redact),
			identifierCell(entry.Organization, redact),
			strconv.FormatBool(entry.Allowed),
			identifierCell(entry.RequestID, redact),
			strconv.Itoa(entry.Revision),
		}); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return output.Bytes(), writer.Error()
}

func identifierCell(value string, redact bool) string {
	if redact && value != "" {
		return "[REDACTED]"
	}
	return safeCell(value)
}

func safeCell(value string) string {
	// CSV quoting alone does not stop spreadsheet apps from running formulas.
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || r == '\uFEFF'
	})
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	if strings.ContainsRune("=+-@＝＋－＠", first) {
		return "'" + value
	}
	return value
}
