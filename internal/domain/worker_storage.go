package domain

import "time"

// WorkerBucket is a logical organization resource. Its ID and data namespace
// survive Worker deployments, rollbacks and environment deletion.
type WorkerBucket struct {
	ResourcePath string    `json:"resource_path,omitempty"`
	ID           string    `json:"id"`
	Organization string    `json:"organization_id"`
	Name         string    `json:"name"`
	Store        string    `json:"-"`
	Enabled      bool      `json:"enabled"`
	Revision     int64     `json:"revision"`
	Created      time.Time `json:"created_at"`
}

type WorkerR2Binding struct {
	StoreID    string `json:"store_id"`
	BucketID   string `json:"bucket_id"`
	BucketName string `json:"bucket_name"`
}

func CloneR2Bindings(source map[string]WorkerR2Binding) map[string]WorkerR2Binding {
	out := make(map[string]WorkerR2Binding, len(source))
	for name, binding := range source {
		out[name] = binding
	}
	return out
}
