package control

import (
	"net/http"
	"slices"
)

type systemConfigView struct {
	Workers    *workersConfigView    `json:"workers"`
	Deployment *deploymentConfigView `json:"deployment,omitempty"`
}

type workersConfigView struct {
	RuntimePools       []runtimePoolConfigView `json:"runtime_pools"`
	ValidatorPool      string                  `json:"validator_pool,omitempty"`
	RuntimeAPIOverride bool                    `json:"runtime_api_override"`
	BuilderMode        string                  `json:"builder_mode"`
	PreviewLimit       int                     `json:"preview_limit"`
}

type runtimePoolConfigView struct {
	ID                  string   `json:"id"`
	Default             bool     `json:"default"`
	Access              []string `json:"access"`
	PrivateEgress       string   `json:"private_egress"`
	PublicEgress        string   `json:"public_egress"`
	ValidatorConfigured bool     `json:"validator_configured"`
	NetworkProfiles     []string `json:"network_profiles"`
}

type deploymentConfigView struct {
	RuntimeReplicas   int `json:"runtime_replicas"`
	ValidatorReplicas int `json:"validator_replicas"`
}

func (s *Server) systemConfig(st *State, p Principal, r *http.Request) (any, int, error) {
	if err := s.require(st, p, r, "system.config.read", systemTarget()); err != nil {
		return nil, 0, err
	}
	return describeSystemConfig(s.Config), http.StatusOK, nil
}

// Project approved fields from the effective configuration. Do not serialize
// config structs or arbitrary label/selector values across this boundary.
func describeSystemConfig(c Config) systemConfigView {
	var view systemConfigView
	w := c.Workers
	if w == nil {
		return view
	}

	profilesByPool := make(map[string][]string)
	for _, profile := range w.ConfiguredNetworkProfiles() {
		for _, pool := range w.PoolsForProfile(profile.ID) {
			profilesByPool[pool.ID] = append(profilesByPool[pool.ID], profile.ID)
		}
	}

	workers := &workersConfigView{
		RuntimePools:       make([]runtimePoolConfigView, 0, len(w.ConfiguredRuntimePools())),
		ValidatorPool:      w.ValidatorPool,
		RuntimeAPIOverride: w.RuntimeAPIURL != "",
		BuilderMode:        w.BuilderMode,
		PreviewLimit:       w.PreviewLimit,
	}
	for _, pool := range w.ConfiguredRuntimePools() {
		profiles := profilesByPool[pool.ID]
		slices.Sort(profiles)
		workers.RuntimePools = append(workers.RuntimePools, runtimePoolConfigView{
			ID:                  pool.ID,
			Default:             pool.Default,
			Access:              pool.Access,
			PrivateEgress:       pool.PrivateEgress,
			PublicEgress:        pool.PublicEgress,
			ValidatorConfigured: pool.ValidatorService != "",
			NetworkProfiles:     append([]string{}, profiles...),
		})
	}
	view.Workers = workers
	if d := c.Deployment; d != nil {
		view.Deployment = &deploymentConfigView{
			RuntimeReplicas:   d.RuntimeReplicas,
			ValidatorReplicas: d.ValidatorReplicas,
		}
	}
	return view
}
