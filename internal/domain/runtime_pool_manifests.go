package domain

// RuntimePoolManifests is the generated Kubernetes YAML for a serving pool.
type RuntimePoolManifests struct {
	Deployment          string `json:"deployment"`
	ConfigMap           string `json:"config_map"`
	SupportingResources string `json:"supporting_resources"`
}

func (m RuntimePoolManifests) YAML() string {
	return m.ConfigMap + "---\n" + m.Deployment + "---\n" + m.SupportingResources
}
