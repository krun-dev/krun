package domain

import (
	"bytes"
	"encoding/json"
)

// Preserve JSON number text across storage reads and working-copy clones.
type WorkerVariables map[string]any

func (v *WorkerVariables) UnmarshalJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return err
	}
	*v = out
	return nil
}

// CloneWorkerVariables keeps nested JSON values separate from later edits to
// desired environment configuration and from other immutable versions.
func CloneWorkerVariables(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = cloneWorkerValue(value)
	}
	return out
}

func cloneWorkerValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return CloneWorkerVariables(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneWorkerValue(item)
		}
		return out
	default:
		return value
	}
}
