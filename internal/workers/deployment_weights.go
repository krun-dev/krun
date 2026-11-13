package workers

import (
	"encoding/json"

	"github.com/krun-dev/krun/internal/domain"
)

// DeploymentPercentage is the version target shared by the native and
// Cloudflare deployment APIs.
type DeploymentPercentage struct {
	ID         string      `json:"version_id"`
	Percentage json.Number `json:"percentage"`
}

// DeploymentWeights validates decimal percentages and converts them to the
// fixed-point representation used by release routing.
func DeploymentWeights(input []DeploymentPercentage) ([]domain.WorkerVersionWeight, error) {
	invalid := domain.Invalid("UNSUPPORTED_DEPLOYMENT", "Specify one or two distinct versions with percentages totaling 100")
	if len(input) < 1 || len(input) > 2 {
		return nil, invalid
	}
	out := make([]domain.WorkerVersionWeight, 0, len(input))
	total := 0
	for _, item := range input {
		if item.ID == "" || len(out) > 0 && item.ID == out[0].Version {
			return nil, invalid
		}
		weight, ok := domain.ParseWorkerPercentage(string(item.Percentage))
		if !ok {
			return nil, invalid
		}
		if weight > 0 && weight < domain.WorkerMinPositiveWeight {
			return nil, invalid
		}
		total += weight
		out = append(out, domain.WorkerVersionWeight{Version: item.ID, Weight: weight})
	}
	if total != domain.WorkerFullWeight {
		return nil, invalid
	}
	return out, nil
}
