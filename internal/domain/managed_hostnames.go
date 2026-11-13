package domain

type ManagedHostname struct {
	Host           string                 `json:"host"`
	Organization   string                 `json:"-"`
	SuffixID       string                 `json:"suffix_id"`
	Revision       int                    `json:"revision"`
	GatewayManaged bool                   `json:"-"`
	Retired        bool                   `json:"-"`
	Routes         []ManagedHostnameRoute `json:"routes"`
}

type ManagedHostnameRoute struct {
	Match         string `json:"match"`
	Path          string `json:"path"`
	Worker        string `json:"worker_id"`
	WorkerLocator string `json:"worker_locator,omitempty"`
	Origin        string `json:"origin,omitempty"`
	CFPattern     string `json:"cf_pattern,omitempty"`
	CustomDomain  bool   `json:"custom_domain,omitempty"`
	Enabled       bool   `json:"enabled"`
}
