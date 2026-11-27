package config

import (
	"strings"
	"testing"
)

func TestGatewayRequestTimeoutValidation(t *testing.T) {
	for _, tc := range []struct {
		input int
		want  int
		bad   bool
	}{
		{input: 0, want: 15},
		{input: 300, want: 300},
		{input: -1, bad: true},
		{input: 3601, bad: true},
	} {
		cfg := WorkersConfig{GatewayRequestTimeoutSeconds: tc.input}
		err := cfg.Validate()
		if tc.bad {
			if err == nil || !strings.Contains(err.Error(), "gateway request timeout") {
				t.Fatalf("timeout %d: expected validation error, got %v", tc.input, err)
			}
		} else if cfg.GatewayRequestTimeoutSeconds != tc.want {
			t.Fatalf("timeout %d: got %d, want %d", tc.input, cfg.GatewayRequestTimeoutSeconds, tc.want)
		}
	}
}
