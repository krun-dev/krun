// Package configfile reads strict YAML configuration files.
package configfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

func isYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

func ValidatePath(path string) error {
	if !isYAML(path) {
		return errors.New("configuration file must use .yaml or .yml")
	}
	return nil
}

func Decode(path string, raw []byte, value any) error {
	return decode(path, raw, value, false)
}

// DecodeWithEnv expands environment references in parsed YAML string values.
// Parsing first keeps environment contents from becoming YAML structure.
func DecodeWithEnv(path string, raw []byte, value any) error {
	return decode(path, raw, value, true)
}

func decode(path string, raw []byte, value any, withEnv bool) error {
	if err := ValidatePath(path); err != nil {
		return err
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := d.Decode(&document); err != nil {
		return err
	}
	if d.Decode(new(yaml.Node)) != io.EOF {
		return errors.New("one YAML document required")
	}
	data, err := yamlValue(&document)
	if err != nil {
		return err
	}
	if withEnv {
		data, err = expandValues(data)
		if err != nil {
			return err
		}
	}
	raw, err = json.Marshal(data)
	if err != nil {
		return errors.New("YAML values must be JSON-compatible")
	}
	return DecodeJSON(raw, value)
}

// Convert through JSON so existing JSON field names and json.Number semantics
// remain identical for HTTP DTOs, OAuth business-status checks and config files.
func yamlValue(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) != 1 {
			return nil, errors.New("one YAML value required")
		}
		return yamlValue(n.Content[0])
	case yaml.MappingNode:
		result := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return nil, fmt.Errorf("line %d: YAML keys must be strings", key.Line)
			}
			if _, exists := result[key.Value]; exists {
				return nil, fmt.Errorf("line %d: duplicate YAML key", key.Line)
			}
			value, err := yamlValue(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			result[key.Value] = value
		}
		return result, nil
	case yaml.SequenceNode:
		result := make([]any, len(n.Content))
		for i, child := range n.Content {
			value, err := yamlValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = value
		}
		return result, nil
	case yaml.ScalarNode:
		if n.Tag == "!!float" {
			literal := strings.ReplaceAll(n.Value, "_", "")
			// Preserve decimal spelling for business-status equality checks.
			if json.Valid([]byte(literal)) {
				return json.Number(literal), nil
			}
		}
		if n.Tag == "!!int" {
			// Preserve integers beyond uint64 without converting them to float64.
			number, ok := new(big.Int).SetString(strings.ReplaceAll(n.Value, "_", ""), 0)
			if !ok {
				return nil, fmt.Errorf("line %d: invalid integer", n.Line)
			}
			return json.Number(number.String()), nil
		}
		if n.Tag != "!!str" && n.Tag != "!!bool" && n.Tag != "!!null" && n.Tag != "!!float" {
			return nil, fmt.Errorf("line %d: unsupported YAML scalar; quote text values", n.Line)
		}
		var value any
		if err := n.Decode(&value); err != nil {
			return nil, err
		}
		return value, nil
	default:
		return nil, errors.New("YAML aliases and merge keys are not supported")
	}
}

func DecodeJSON(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := RejectDuplicateKeys(raw); err != nil {
		return err
	}
	if err := d.Decode(value); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("one JSON value required")
	}
	return nil
}

func RejectDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate JSON key")
				}
				seen[name] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON")
		}
		_, err = d.Token()
		return err
	}
	return walk()
}

func Marshal(path string, value any) ([]byte, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return raw, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return nil, err
	}
	var blockStyle func(*yaml.Node)
	blockStyle = func(n *yaml.Node) {
		n.Style = 0
		for _, child := range n.Content {
			blockStyle(child)
		}
	}
	blockStyle(&document)
	var out bytes.Buffer
	e := yaml.NewEncoder(&out)
	e.SetIndent(2)
	if err := e.Encode(&document); err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func expandValues(value any) (any, error) {
	switch v := value.(type) {
	case string:
		return expandString(v)
	case map[string]any:
		for name, item := range v {
			resolved, err := expandValues(item)
			if err != nil {
				return nil, err
			}
			v[name] = resolved
		}
	case []any:
		for i, item := range v {
			resolved, err := expandValues(item)
			if err != nil {
				return nil, err
			}
			v[i] = resolved
		}
	}
	return value, nil
}

func expandString(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] != '$' {
			out.WriteByte(value[i])
			i++
			continue
		}
		if i+1 < len(value) && value[i+1] == '$' {
			out.WriteByte('$')
			i += 2
			continue
		}
		if i+1 >= len(value) || value[i+1] != '{' {
			out.WriteByte('$')
			i++
			continue
		}
		end := strings.IndexByte(value[i+2:], '}')
		if end < 0 {
			return "", errors.New("unclosed environment variable reference")
		}
		end += i + 2
		parts := strings.SplitN(value[i+2:end], ":-", 2)
		if len(parts) == 2 && strings.Contains(parts[1], "${") {
			return "", errors.New("nested environment references are not supported")
		}
		name := parts[0]
		if !environmentName.MatchString(name) {
			return "", errors.New("invalid environment variable reference")
		}
		resolved, ok := os.LookupEnv(name)
		if !ok || resolved == "" {
			if len(parts) != 2 {
				return "", fmt.Errorf("environment variable %s is unset or empty", name)
			}
			resolved = parts[1]
		}
		// Values are inserted once; dollar characters in credentials remain literal.
		out.WriteString(resolved)
		i = end + 1
	}
	return out.String(), nil
}
