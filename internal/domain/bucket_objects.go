package domain

import "time"

// Object references are always relative to a logical bucket.
type BucketObject struct {
	Key            string            `json:"key"`
	Size           int64             `json:"size"`
	ETag           string            `json:"etag"`
	Uploaded       time.Time         `json:"uploaded"`
	Version        string            `json:"version,omitempty"`
	R2Compatible   *bool             `json:"r2_compatible,omitempty"`
	HTTPMetadata   map[string]string `json:"http_metadata,omitempty"`
	CustomMetadata map[string]string `json:"custom_metadata,omitempty"`
}

type BucketObjectPage struct {
	Objects           []BucketObject `json:"objects"`
	Prefixes          []string       `json:"delimited_prefixes"`
	Cursor            string         `json:"cursor,omitempty"`
	Truncated         bool           `json:"truncated"`
	DeliveryAvailable bool           `json:"delivery_available"`
	PerPage           int            `json:"per_page"`
}

type BucketObjectList struct {
	Prefix     string
	Cursor     string
	Limit      int
	Delimiter  string
	StartAfter string
}

type BucketObjectAccessInput struct {
	Key            string            `json:"key"`
	Operation      string            `json:"operation"`
	Size           int64             `json:"size,omitempty"`
	HTTPMetadata   map[string]string `json:"http_metadata,omitempty"`
	CustomMetadata map[string]string `json:"custom_metadata,omitempty"`
}

// A capability URL is short lived. It is never a persistent object identity.
type BucketObjectAccess struct {
	Key       string            `json:"key"`
	URL       string            `json:"url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	Version   string            `json:"version,omitempty"`
}
