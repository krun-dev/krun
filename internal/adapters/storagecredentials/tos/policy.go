package tos

import (
	"encoding/json"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/domain"
)

type statement struct {
	Effect    string                         `json:"Effect"`
	Action    []string                       `json:"Action"`
	Resource  []string                       `json:"Resource"`
	Condition map[string]map[string][]string `json:"Condition,omitempty"`
}

func sessionPolicy(scope domain.StorageCredentialScope) (string, error) {
	if err := provider.ValidateScope(scope); err != nil {
		return "", err
	}
	actions := []string{}
	if scope.Access&domain.StorageRead != 0 {
		actions = append(actions, "tos:GetObject")
	}
	if scope.Access&domain.StorageWrite != 0 {
		actions = append(actions, "tos:PutObject")
	}
	if scope.Access&domain.StorageDelete != 0 {
		actions = append(actions, "tos:DeleteObject")
	}
	if scope.Access&domain.StorageMultipart != 0 {
		if scope.Access&domain.StorageWrite == 0 {
			return "", &provider.Error{Kind: provider.InvalidScope}
		}
		actions = append(actions, "tos:AbortMultipartUpload", "tos:ListMultipartUploadParts")
	}
	statements := []statement{}
	if len(actions) != 0 {
		statements = append(statements, statement{
			Effect: "Allow", Action: actions,
			Resource: []string{"trn:tos:::" + scope.Bucket + "/" + scope.Prefix + "*"},
		})
	}
	if scope.Access&domain.StorageList != 0 {
		statements = append(statements, statement{
			Effect: "Allow", Action: []string{"tos:ListBucket"},
			Resource:  []string{"trn:tos:::" + scope.Bucket},
			Condition: map[string]map[string][]string{"StringLike": {"tos:prefix": {scope.Prefix + "*"}}},
		})
	}
	raw, err := json.Marshal(struct {
		Statement []statement `json:"Statement"`
	}{statements})
	if err != nil || len(raw) > 2048 {
		return "", &provider.Error{Kind: provider.InvalidScope}
	}
	return string(raw), nil
}
