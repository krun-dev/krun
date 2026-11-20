package kubernetes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
)

var cleanupLabelValue = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)

// CleanupBuildResources removes Worker-owned builder resources, but never shared
// serving resources or routes. It returns the number observed, so callers wait
// for Kubernetes garbage collection and retry until a subsequent scan is empty.
func (k *Client) CleanupBuildResources(ctx context.Context, org, worker string, legacyJobs []string) (int, error) {
	if !cleanupLabelValue.MatchString(org) || !cleanupLabelValue.MatchString(worker) {
		return 0, fmt.Errorf("invalid Worker ownership labels")
	}
	selector := url.Values{"labelSelector": {"app=krun-builder,krun.dev/organization-id=" + org + ",krun.dev/worker-id=" + worker}, "limit": {"100"}}
	count := 0
	for _, base := range []string{k.Jobs(), k.Core("pods"), k.Core("secrets")} {
		continuation := ""
		for {
			if continuation != "" {
				selector.Set("continue", continuation)
			} else {
				selector.Del("continue")
			}
			var list struct {
				Metadata struct {
					Continue string `json:"continue"`
				} `json:"metadata"`
				Items []struct {
					Metadata cleanupMetadata `json:"metadata"`
				} `json:"items"`
			}
			if err := k.Call(ctx, http.MethodGet, base+"?"+selector.Encode(), nil, &list); err != nil {
				return count, err
			}
			for _, item := range list.Items {
				count++
				if err := k.deleteOwnedBuildObject(ctx, base, item.Metadata, org, worker, false); err != nil {
					return count, err
				}
			}
			if list.Metadata.Continue == "" {
				break
			}
			if list.Metadata.Continue == continuation {
				return count, fmt.Errorf("Kubernetes resource pagination did not advance")
			}
			continuation = list.Metadata.Continue
		}
	}
	// Older builder objects predate ownership labels. Their recorded Job name is
	// read from existing build metadata; do not persist a per-resource inventory.
	for _, name := range legacyJobs {
		for _, base := range []string{k.Jobs(), k.Core("secrets")} {
			var object struct {
				Metadata cleanupMetadata `json:"metadata"`
			}
			err := k.Call(ctx, http.MethodGet, base+"/"+url.PathEscape(name), nil, &object)
			if KubeMissing(err) {
				continue
			}
			if err != nil {
				return count, err
			}
			count++
			if err := k.deleteOwnedBuildObject(ctx, base, object.Metadata, org, worker, true); err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

type cleanupMetadata struct {
	Name   string            `json:"name"`
	UID    string            `json:"uid"`
	Labels map[string]string `json:"labels"`
}

func (k *Client) deleteOwnedBuildObject(ctx context.Context, base string, metadata cleanupMetadata, org, worker string, legacy bool) error {
	labels := metadata.Labels
	if metadata.Name == "" || metadata.UID == "" || labels["app"] != "krun-builder" {
		return fmt.Errorf("refusing to delete unverified builder resource")
	}
	if labels["krun.dev/organization-id"] != "" && labels["krun.dev/organization-id"] != org || labels["krun.dev/worker-id"] != "" && labels["krun.dev/worker-id"] != worker {
		return fmt.Errorf("builder resource ownership does not match")
	}
	if !legacy && (labels["krun.dev/organization-id"] != org || labels["krun.dev/worker-id"] != worker) {
		return fmt.Errorf("builder resource ownership labels are missing")
	}
	options := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Background", "preconditions": map[string]string{"uid": metadata.UID}}
	if err := k.Call(ctx, http.MethodDelete, base+"/"+url.PathEscape(metadata.Name), options, nil); err != nil && !KubeMissing(err) {
		return err
	}
	return nil
}
