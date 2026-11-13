package workers

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"

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
		raw := string(item.Percentage)
		var decimal big.Rat
		if len(raw) == 0 || len(raw) > 24 {
			return nil, invalid
		}
		if i := strings.IndexAny(raw, "eE"); i >= 0 {
			exponent, err := strconv.Atoi(raw[i+1:])
			if err != nil || exponent < -12 || exponent > 12 {
				return nil, invalid
			}
		}
		if _, ok := decimal.SetString(raw); !ok {
			return nil, invalid
		}
		decimal.Mul(&decimal, big.NewRat(1000, 1))
		if !decimal.IsInt() || !decimal.Num().IsInt64() {
			return nil, invalid
		}
		weight := decimal.Num().Int64()
		if weight < 0 || weight > domain.WorkerFullWeight || weight > 0 && weight < 10 {
			return nil, invalid
		}
		total += int(weight)
		out = append(out, domain.WorkerVersionWeight{Version: item.ID, Weight: int(weight)})
	}
	if total != domain.WorkerFullWeight {
		return nil, invalid
	}
	return out, nil
}
