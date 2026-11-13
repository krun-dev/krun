package domain

import (
	"math/big"
	"strconv"
	"strings"
)

// WorkerMinPositiveWeight represents 0.01 percent.
const WorkerMinPositiveWeight = 10

// ParseWorkerPercentage converts a decimal percentage to the exact routing
// weight without rounding or accepting fractions outside 0–100.
func ParseWorkerPercentage(raw string) (int, bool) {
	if len(raw) == 0 || len(raw) > 24 || strings.Contains(raw, "/") {
		return 0, false
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(raw[i+1:])
		if err != nil || exponent < -12 || exponent > 12 {
			return 0, false
		}
	}

	var decimal big.Rat
	if _, ok := decimal.SetString(raw); !ok {
		return 0, false
	}
	decimal.Mul(&decimal, big.NewRat(1000, 1))
	if !decimal.IsInt() || !decimal.Num().IsInt64() {
		return 0, false
	}
	weight := decimal.Num().Int64()
	return int(weight), weight >= 0 && weight <= WorkerFullWeight
}
