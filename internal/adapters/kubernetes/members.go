package kubernetes

import (
	"context"
	"net/url"
	"sort"
	"time"
)

type WorkerPod struct {
	Name string
	UID  string
	IP   string
}

type WorkerPodList struct {
	Items []struct {
		Metadata struct {
			Name              string     `json:"name"`
			UID               string     `json:"uid"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp"`
		} `json:"metadata"`
		Status struct {
			PodIP      string `json:"podIP"`
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

func (k *Client) Members(ctx context.Context, role, pool, service string) ([]WorkerPod, error) {
	var pods WorkerPodList
	if e := k.Call(ctx, "GET", k.Core("pods")+"?labelSelector="+url.QueryEscape("role="+role+",pool="+pool), nil, &pods); e != nil {
		return nil, e
	}
	var slices struct {
		Items []struct {
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready *bool `json:"ready"`
				} `json:"conditions"`
				TargetRef *struct {
					UID string `json:"uid"`
				} `json:"targetRef"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if e := k.Call(ctx, "GET", "/apis/discovery.k8s.io/v1/namespaces/"+k.Namespace+"/endpointslices?labelSelector="+url.QueryEscape("kubernetes.io/service-name="+service), nil, &slices); e != nil {
		return nil, e
	}
	ready := map[string]bool{}
	for _, sl := range slices.Items {
		for _, ep := range sl.Endpoints {
			if ep.TargetRef != nil && ep.Conditions.Ready != nil && *ep.Conditions.Ready {
				ready[ep.TargetRef.UID] = true
			}
		}
	}
	out := []WorkerPod{}
	for _, p := range pods.Items {
		if p.Metadata.DeletionTimestamp != nil || !ready[p.Metadata.UID] || p.Status.PodIP == "" {
			continue
		}
		healthy := false
		for _, c := range p.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				healthy = true
			}
		}
		if healthy {
			out = append(out, WorkerPod{p.Metadata.Name, p.Metadata.UID, p.Status.PodIP})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out, nil
}

func (k *Client) RuntimeMembers(ctx context.Context, pool, service string) ([]WorkerPod, error) {
	return k.Members(ctx, "krun-runtime", pool, service)
}

func (k *Client) ValidatorMembers(ctx context.Context, pool, service string) ([]WorkerPod, error) {
	return k.Members(ctx, "krun-validator", pool, service)
}
