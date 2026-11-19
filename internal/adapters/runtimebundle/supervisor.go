package runtimebundle

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/krun-dev/krun/internal/config"
)

const ExecutionPort = 8082

func ServingPeerName(pool config.WorkerRuntimePool) string {
	digest := sha256.Sum256([]byte(pool.ID))
	return fmt.Sprintf("krun-execution-%x", digest[:10])
}

func ServingPeerSpec(app string) map[string]any {
	return map[string]any{
		"clusterIP": "None",
		"selector":  map[string]string{"app": app},
		"ports":     []any{map[string]any{"name": "execution", "port": ExecutionPort, "targetPort": ExecutionPort}},
	}
}

func ServingHashSpec(service string) map[string]any {
	return map[string]any{
		"targetRefs": []any{map[string]string{"group": "", "kind": "Service", "name": service}},
		"loadBalancer": map[string]any{
			"healthyPanicThreshold": 0,
			"ringHash":              map[string]any{"hashPolicies": []any{map[string]any{"header": map[string]string{"name": ":authority"}}}},
		},
	}
}

func ServingStreamSpec(service string) map[string]any {
	return map[string]any{
		"targetRefs":   []any{map[string]string{"group": "", "kind": "Service", "name": service}},
		"loadBalancer": map[string]any{"healthyPanicThreshold": 0, "leastRequest": map[string]int{"choiceCount": 2}},
	}
}

func StreamEnvironment(settings *config.WorkerStreamAdmission) []any {
	if settings == nil {
		return nil
	}
	return []any{
		map[string]string{"name": "POOL_STREAMS_ENABLED", "value": "1"},
		map[string]string{"name": "POOL_SSE_PATHS", "value": strings.Join(settings.SSEPaths, ",")},
		map[string]string{"name": "POOL_STREAM_SLOT_LIMIT", "value": strconv.Itoa(defaultQuota(settings.PerProcess, 300))},
		map[string]string{"name": "POOL_STREAM_POD_LIMIT", "value": strconv.Itoa(defaultQuota(settings.PerExecutionPod, 600))},
		map[string]string{"name": "POOL_STREAM_INGRESS_LIMIT", "value": strconv.Itoa(defaultQuota(settings.PerIngressPod, 600))},
		map[string]string{"name": "POOL_PEER_STREAM_LIMIT", "value": strconv.Itoa(defaultQuota(settings.PerPeer, 128))},
	}
}

// ServingCommand uses the same bundle as direct workerd. The supervisor owns
// the public sockets and passes per-process loopback sockets to the children.
func ServingCommand() []string {
	return []string{"/usr/local/bin/krun-runtime-supervisor", "--serving", "/app/node_modules/.bin/workerd", "/config/runtime.capnp"}
}

func ServingEnvironment(pool config.WorkerRuntimePool, cpuReserve int) []any {
	settings := pool.Execution
	compute := 2
	if settings.ComputeProcesses != nil {
		compute = *settings.ComputeProcesses
	}
	env := []any{
		map[string]string{"name": "POOL_PEER_DNS", "value": ServingPeerName(pool)},
		map[string]any{"name": "POOL_POD_IP", "valueFrom": map[string]any{"fieldRef": map[string]string{"fieldPath": "status.podIP"}}},
		map[string]string{"name": "POOL_COMPUTE", "value": strconv.Itoa(compute)},
		map[string]string{"name": "POOL_COMPUTE_HOSTS", "value": strings.Join(settings.ComputeHosts, ",")},
		map[string]string{"name": "POOL_ORDINARY_IN_FLIGHT", "value": strconv.Itoa(defaultQuota(settings.OrdinaryInFlight, 16))},
		map[string]string{"name": "POOL_COMPUTE_IN_FLIGHT", "value": strconv.Itoa(defaultQuota(settings.ComputeInFlight, 2))},
		map[string]string{"name": "POOL_PEER_ORDINARY_IN_FLIGHT", "value": strconv.Itoa(defaultQuota(settings.PeerOrdinaryInFlight, 16))},
		map[string]string{"name": "POOL_PEER_COMPUTE_IN_FLIGHT", "value": strconv.Itoa(defaultQuota(settings.PeerComputeInFlight, 4))},
		map[string]string{"name": "POOL_CPU_HIGH_MCPU", "value": strconv.Itoa(defaultQuota(settings.CPUHighMilli, 750))},
		map[string]string{"name": "POOL_CPU_RESERVE_MCPU", "value": strconv.Itoa(defaultQuota(settings.CPUReserveMilli, cpuReserve))},
		map[string]string{"name": "POOL_PEER_COOLDOWN_MS", "value": strconv.Itoa(defaultQuota(settings.PeerCooldownMS, 15))},
		map[string]string{"name": "POOL_MEMORY_RESERVE_MIB", "value": strconv.Itoa(defaultQuota(settings.MemoryReserveMiB, 1639))},
		map[string]string{"name": "POOL_MAX_LOADED", "value": strconv.Itoa(defaultQuota(settings.MaxLoadedPerProcess, 650))},
		map[string]string{"name": "POOL_MAX_COLD_IN_FLIGHT", "value": strconv.Itoa(defaultQuota(settings.MaxColdInFlight, 4))},
		map[string]string{"name": "POOL_PEER_SELECTION", "value": "capacity"},
		map[string]string{"name": "POOL_RETRY_CAPACITY_MS", "value": "100"},
	}
	return append(env, StreamEnvironment(settings.Streams)...)
}

func defaultQuota(configured, fallback int) int {
	if configured == 0 {
		return fallback
	}
	return configured
}

// StreamListenerPolicies attaches WebSocket upgrade support to the Gateway
// Gateways used by streaming Workers. kgateway 2.3 applies HTTP upgrade settings
// from Gateway-wide policies.
func StreamListenerPolicies(workers *config.WorkersConfig, poolID string) []any {
	policies := []any{}
	seen := map[string]bool{}
	for _, domain := range workers.Domains {
		pool, err := workers.DomainRuntimePool(domain.ID)
		if err != nil || pool.StreamService() == "" || (poolID != "" && pool.ID != poolID) {
			continue
		}
		gateway, ok := workers.Gateway(domain.Gateway)
		if !ok {
			continue
		}
		digest := sha256.Sum256([]byte(gateway.Namespace + "/" + gateway.Name))
		name := fmt.Sprintf("krun-stream-upgrade-%x", digest[:10])
		if seen[name] {
			continue
		}
		seen[name] = true
		target := map[string]string{"group": "gateway.networking.k8s.io", "kind": "Gateway", "name": gateway.Name}
		policies = append(policies, map[string]any{
			"apiVersion": "gateway.kgateway.dev/v1alpha1",
			"kind":       "ListenerPolicy",
			"metadata":   map[string]any{"name": name, "namespace": gateway.Namespace, "labels": map[string]string{"app.kubernetes.io/part-of": "krun"}},
			"spec": map[string]any{
				"targetRefs": []any{target},
				"default": map[string]any{"httpSettings": map[string]any{
					"upgradeConfig": map[string]any{"enabledUpgrades": []string{"websocket"}},
				}},
			},
		})
	}
	return policies
}
