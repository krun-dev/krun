package workers

import (
	"encoding/json"
	"testing"
)

func TestDeploymentPercentages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		valid  bool
		weight int
	}{
		{"single", `[{"version_id":"a","percentage":100}]`, true, 0},
		{"stage", `[{"version_id":"a","percentage":100},{"version_id":"b","percentage":0}]`, true, 0},
		{"decimal", `[{"version_id":"a","percentage":99.975},{"version_id":"b","percentage":0.025}]`, true, 25},
		{"minimum", `[{"version_id":"a","percentage":99.99},{"version_id":"b","percentage":0.01}]`, true, 10},
		{"too small", `[{"version_id":"a","percentage":99.999},{"version_id":"b","percentage":0.001}]`, false, 0},
		{"sum", `[{"version_id":"a","percentage":80},{"version_id":"b","percentage":30}]`, false, 0},
		{"duplicates", `[{"version_id":"a","percentage":50},{"version_id":"a","percentage":50}]`, false, 0},
		{"three", `[{"version_id":"a","percentage":50},{"version_id":"b","percentage":50},{"version_id":"c","percentage":0}]`, false, 0},
		{"missing", `[{"version_id":"a","percentage":100},{"version_id":"b"}]`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var versions []DeploymentPercentage
			if err := json.Unmarshal([]byte(tc.raw), &versions); err != nil {
				t.Fatal(err)
			}
			got, err := DeploymentWeights(versions)
			if (err == nil) != tc.valid {
				t.Fatalf("unexpected validation result: %v", err)
			}
			if tc.valid && len(got) == 2 && got[1].Weight != tc.weight {
				t.Fatalf("candidate weight = %d, want %d", got[1].Weight, tc.weight)
			}
		})
	}
}
