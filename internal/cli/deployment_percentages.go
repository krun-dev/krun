package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"regexp"
	"strings"

	"github.com/krun-dev/krun/internal/domain"
)

type versionPercentage struct {
	ID         string  `json:"version_id"`
	Percentage float64 `json:"percentage"`
}

type deploymentView struct {
	domain.WorkerDeployment
	State    string              `json:"state"`
	Hosts    []string            `json:"hosts"`
	Versions []versionPercentage `json:"versions"`
}

func (d *deploymentView) setPercentages() {
	d.Versions = releasePercentages(d.Version, d.WorkerGradualRelease)
}

func (c *Client) readDeployment(ctx context.Context, path string) (deploymentView, error) {
	var deployment deploymentView
	err := c.request(ctx, "GET", path, nil, &deployment, false)
	deployment.setPercentages()
	return deployment, err
}

var decimalPercentage = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)?|\.[0-9]+)$`)

// versionDeployArgs allows the version specs and options to be interleaved.
func versionDeployArgs(f *flag.FlagSet, args []string) ([]string, error) {
	var specs, options []string
	for len(args) > 0 {
		arg := args[0]
		args = args[1:]
		if arg == "--" {
			specs = append(specs, args...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			specs = append(specs, arg)
			continue
		}
		options = append(options, arg)
		name, _, inline := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		option := f.Lookup(name)
		if inline || option == nil || len(args) == 0 {
			continue
		}
		if boolean, ok := option.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		options = append(options, args[0])
		args = args[1:]
	}
	return specs, parseFlags(f, options)
}

func versionDeployPercentages(specs []string) ([]versionPercentage, error) {
	if len(specs) < 1 || len(specs) > 2 {
		return nil, errors.New("provide one or two version IDs as VERSION_ID[@PERCENTAGE%]")
	}
	ids := make([]string, len(specs))
	weights := make([]int, len(specs))
	remaining := domain.WorkerFullWeight
	omitted := 0
	for i, spec := range specs {
		id, percentage, _ := strings.Cut(spec, "@")
		if id == "" {
			return nil, errors.New("version ID is required before @")
		}
		if i > 0 && ids[0] == id {
			return nil, errors.New("provide distinct version IDs")
		}
		ids[i] = id
		if percentage == "" {
			weights[i] = -1
			omitted++
			continue
		}
		decimal := strings.TrimSuffix(percentage, "%")
		weight, ok := domain.ParseWorkerPercentage(decimal)
		if !decimalPercentage.MatchString(decimal) || !ok {
			return nil, fmt.Errorf("invalid percentage %q for %s; use 0–100 with at most three decimal places", percentage, id)
		}
		weights[i] = weight
		remaining -= weight
	}
	if remaining < 0 || omitted == 0 && remaining != 0 {
		return nil, errors.New("traffic percentages must total 100%")
	}

	out := make([]versionPercentage, len(specs))
	for i, weight := range weights {
		if weight < 0 {
			weight = remaining / omitted
		}
		if weight > 0 && weight < domain.WorkerMinPositiveWeight {
			return nil, fmt.Errorf("percentage for %s must be 0%% or at least 0.01%%", ids[i])
		}
		out[i] = versionPercentage{ID: ids[i], Percentage: float64(weight) / 1000}
	}
	return out, nil
}

func releasePercentages(version string, release domain.WorkerGradualRelease) []versionPercentage {
	versions := make([]versionPercentage, 0, 2)
	if version != "" {
		versions = append(versions, versionPercentage{ID: version, Percentage: float64(domain.WorkerFullWeight-release.CandidateWeight) / 1000})
	}
	if release.Candidate != "" {
		versions = append(versions, versionPercentage{ID: release.Candidate, Percentage: float64(release.CandidateWeight) / 1000})
	}
	return versions
}
