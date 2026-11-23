package runtimeconfig

import (
	"container/list"
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"golang.org/x/sync/singleflight"
)

type ResourceStore interface {
	RuntimeResources(context.Context, string, []domain.ResourceRef) (string, []domain.RuntimeResource, error)
	RuntimeGenerations(context.Context, []domain.ResourceRef) (string, map[string]domain.ResourceRef, error)
}

type cachedResource struct {
	pool     string
	resource domain.RuntimeResource
	epoch    string
	checked  time.Time
	token    uint64
	bytes    int64
}

type ResourceHub struct {
	store            ResourceStore
	config           config.WorkerRuntimeConfig
	mu               sync.Mutex
	lru              *list.List
	cache            map[string]*list.Element
	index            map[string]map[string]bool
	watchers         map[string]map[chan struct{}]bool
	bytes            int64
	token            uint64
	flights          singleflight.Group
	reads            chan struct{}
	connections      atomic.Int64
	hits             atomic.Uint64
	misses           atomic.Uint64
	checks           atomic.Uint64
	loads            atomic.Uint64
	now              func() time.Time
	epoch            string
	authorityChecked time.Time
}

func NewResourceHub(store ResourceStore, limits config.WorkerRuntimeConfig) *ResourceHub {
	_ = limits.Validate()
	return &ResourceHub{store: store, config: limits, lru: list.New(), cache: map[string]*list.Element{}, index: map[string]map[string]bool{}, watchers: map[string]map[chan struct{}]bool{}, reads: make(chan struct{}, limits.ReadConcurrency), now: time.Now}
}

func resourceCacheKey(pool string, ref domain.ResourceRef) string { return pool + "/" + ref.Key() }

func sameResourceVersion(a, b domain.ResourceRef) bool {
	return a.Kind == b.Kind && a.ID == b.ID && a.Incarnation == b.Incarnation && a.Generation == b.Generation
}

func (h *ResourceHub) TryConnect() bool {
	n := h.connections.Add(1)
	if n > int64(h.config.MaxConnections) {
		h.connections.Add(-1)
		return false
	}
	return true
}

func (h *ResourceHub) Disconnect() { h.connections.Add(-1) }

func (h *ResourceHub) Invalidate(ref domain.ResourceRef) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key := range h.index[ref.Key()] {
		e := h.cache[key]
		entry := e.Value.(*cachedResource)
		if entry.resource.Kind == ref.Kind && entry.resource.ID == ref.ID {
			h.token++
			entry.token = h.token
			entry.checked = time.Time{}
			h.wakeLocked(key)
		}
	}
}

func (h *ResourceHub) wakeLocked(key string) {
	for ch := range h.watchers[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Watch registers before the caller reads resources. Checks repair notifications
// received before dependency discovery; callers re-register expanded graphs.
func (h *ResourceHub) Watch(pool string, refs []domain.ResourceRef, wake chan struct{}) func() {
	keys := make([]string, 0, len(refs))
	h.mu.Lock()
	for _, ref := range refs {
		key := resourceCacheKey(pool, ref)
		keys = append(keys, key)
		if h.watchers[key] == nil {
			h.watchers[key] = map[chan struct{}]bool{}
		}
		h.watchers[key][wake] = true
	}
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, key := range keys {
			delete(h.watchers[key], wake)
			if len(h.watchers[key]) == 0 {
				delete(h.watchers, key)
			}
		}
	}
}

func (h *ResourceHub) graphLocked(pool string, refs []domain.ResourceRef) (string, []domain.RuntimeResource, bool) {
	seen := map[string]bool{}
	out := []domain.RuntimeResource{}
	epoch := ""
	for len(refs) > 0 {
		ref := refs[0]
		refs = refs[1:]
		key := resourceCacheKey(pool, ref)
		if seen[key] {
			continue
		}
		seen[key] = true
		e, ok := h.cache[key]
		if !ok {
			return "", nil, false
		}
		v := e.Value.(*cachedResource)
		ttl := time.Duration(h.config.VerifyMillis) * time.Millisecond
		if v.resource.Deleted {
			ttl = min(ttl, time.Second)
		}
		if v.checked.IsZero() || h.now().Sub(v.checked) >= ttl || v.resource.Generation < ref.Generation || ref.Incarnation != "" && ref.Incarnation != v.resource.Incarnation {
			return "", nil, false
		}
		if epoch != "" && epoch != v.epoch {
			return "", nil, false
		}
		epoch = v.epoch
		h.lru.MoveToFront(e)
		out = append(out, v.resource)
		if !v.resource.Deleted {
			refs = append(refs, v.resource.Dependencies...)
		}
		if len(seen) > h.config.MaxSubscriptions {
			return "", nil, false
		}
	}
	return epoch, out, true
}

func (h *ResourceHub) Resources(ctx context.Context, pool string, refs []domain.ResourceRef) (string, []domain.RuntimeResource, error) {
	if len(refs) == 0 || len(refs) > 256 {
		return "", nil, domain.Invalid("CONFIG_RESOURCES_INVALID", "Between 1 and 256 resources required")
	}
	h.mu.Lock()
	epoch, resources, ok := h.graphLocked(pool, append([]domain.ResourceRef(nil), refs...))
	h.mu.Unlock()
	if ok {
		h.hits.Add(1)
		return epoch, resources, nil
	}
	h.misses.Add(1)
	keys := make([]string, 0, len(refs))
	for _, r := range refs {
		keys = append(keys, r.Key())
	}
	sort.Strings(keys)
	flightKey := pool
	for _, key := range keys {
		flightKey += "\x00" + key
	}
	// A waiter's cancellation never cancels the shared query for other waiters.
	result := h.flights.DoChan(flightKey, func() (any, error) {
		queryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		select {
		case h.reads <- struct{}{}:
			defer func() { <-h.reads }()
		case <-queryCtx.Done():
			return nil, queryCtx.Err()
		}
		for attempt := 0; attempt < 3; attempt++ {
			h.mu.Lock()
			startToken := h.token
			h.mu.Unlock()
			started := h.now()
			h.loads.Add(1)
			epoch, resources, err := h.store.RuntimeResources(queryCtx, pool, refs)
			if err != nil {
				return nil, err
			}
			h.mu.Lock()
			valid := h.now().Sub(started) < time.Duration(h.config.VerifyMillis)*time.Millisecond
			for _, r := range resources {
				if e := h.cache[resourceCacheKey(pool, r.ResourceRef)]; e != nil {
					cached := e.Value.(*cachedResource)
					if cached.token > startToken && (cached.checked.IsZero() || cached.epoch != epoch || !sameResourceVersion(cached.resource.ResourceRef, r.ResourceRef)) {
						valid = false
					}
				}
			}
			if valid {
				incoming := make(map[string]int64, len(resources))
				projectedBytes, projectedCount := h.bytes, len(h.cache)
				for _, r := range resources {
					key := resourceCacheKey(pool, r.ResourceRef)
					size := int64(len(r.Payload) + len(key) + 256 + len(r.Dependencies)*128)
					if size > int64(h.config.ResourceBytes) {
						h.mu.Unlock()
						return nil, domain.Invalid("CONFIG_RESOURCE_TOO_LARGE", "Resource exceeds configured limit")
					}
					incoming[key] = size
					projectedBytes += size
					if old := h.cache[key]; old != nil {
						projectedBytes -= old.Value.(*cachedResource).bytes
					} else {
						projectedCount++
					}
				}
				entryLimit, byteLimit := h.config.CacheEntries, h.config.CacheBytes
				for _, r := range resources {
					if r.Kind == "route" && h.cache[resourceCacheKey(pool, r.ResourceRef)] == nil {
						// Reserve replacement space before admitting a new host.
						// Existing subscriptions may consume this headroom while
						// their old dependency watches are being replaced.
						entryLimit = max(1, entryLimit*3/4)
						byteLimit = byteLimit * 3 / 4
						break
					}
				}
				victims := []*list.Element{}
				for e := h.lru.Back(); e != nil && (projectedCount > entryLimit || projectedBytes > byteLimit); e = e.Prev() {
					v := e.Value.(*cachedResource)
					key := resourceCacheKey(v.pool, v.resource.ResourceRef)
					if _, replacing := incoming[key]; replacing || len(h.watchers[key]) > 0 {
						continue
					}
					victims = append(victims, e)
					projectedBytes -= v.bytes
					projectedCount--
				}
				if projectedCount > entryLimit || projectedBytes > byteLimit {
					h.mu.Unlock()
					return nil, domain.Unavailable("CONFIG_CACHE_CAPACITY", "Configuration working set exceeds server budget")
				}
				for _, e := range victims {
					v := e.Value.(*cachedResource)
					key := resourceCacheKey(v.pool, v.resource.ResourceRef)
					h.bytes -= v.bytes
					delete(h.cache, key)
					delete(h.index[v.resource.Key()], key)
					if len(h.index[v.resource.Key()]) == 0 {
						delete(h.index, v.resource.Key())
					}
					h.lru.Remove(e)
				}
				h.token++
				installToken := h.token
				for _, r := range resources {
					key := resourceCacheKey(pool, r.ResourceRef)
					if e := h.cache[key]; e != nil {
						h.bytes -= e.Value.(*cachedResource).bytes
						h.lru.Remove(e)
					}
					size := incoming[key]
					h.cache[key] = h.lru.PushFront(&cachedResource{pool: pool, resource: r, epoch: epoch, checked: started, token: installToken, bytes: size})
					if h.index[r.Key()] == nil {
						h.index[r.Key()] = map[string]bool{}
					}
					h.index[r.Key()][key] = true
					h.bytes += size
				}
				h.mu.Unlock()
				return resourceResult{epoch, resources}, nil
			}
			h.mu.Unlock()
		}
		return nil, errors.New("configuration changed during resource read")
	})
	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case v := <-result:
		if v.Err != nil {
			return "", nil, v.Err
		}
		loaded := v.Val.(resourceResult)
		// A different waiter can require a higher minimum than the shared reader.
		for _, minimum := range refs {
			for _, r := range loaded.resources {
				if r.Key() == minimum.Key() && (r.Generation < minimum.Generation || minimum.Incarnation != "" && r.Incarnation != minimum.Incarnation) {
					return "", nil, domain.Unavailable("CONFIG_VERSION_PENDING", "Requested generation is not available")
				}
			}
		}
		return loaded.epoch, loaded.resources, nil
	}
}

type resourceResult struct {
	epoch     string
	resources []domain.RuntimeResource
}

// Verify deduplicates the subscribed working set across all connections. Hits
// never slide the check time. Source payloads are reloaded only when changed.
func (h *ResourceHub) Verify(ctx context.Context) error {
	h.mu.Lock()
	entries := []cachedResource{}
	for key, e := range h.cache {
		v := e.Value.(*cachedResource)
		if len(h.watchers[key]) > 0 && !v.checked.IsZero() && h.now().Sub(v.checked) >= time.Duration(h.config.VerifyMillis)*time.Millisecond/2 {
			entries = append(entries, *v)
		}
	}
	h.mu.Unlock()
	if len(entries) == 0 {
		h.mu.Lock()
		due := h.authorityChecked.IsZero() || h.now().Sub(h.authorityChecked) >= time.Duration(h.config.VerifyMillis)*time.Millisecond/2
		h.mu.Unlock()
		if due {
			checked := h.now()
			epoch, _, err := h.store.RuntimeGenerations(ctx, nil)
			if err != nil {
				return err
			}
			h.mu.Lock()
			h.epoch = epoch
			h.authorityChecked = checked
			h.mu.Unlock()
		}
	}
	for start := 0; start < len(entries); start += 256 {
		end := min(start+256, len(entries))
		batch := entries[start:end]
		refs := make([]domain.ResourceRef, 0, len(batch))
		for _, v := range batch {
			refs = append(refs, v.resource.ResourceRef)
		}
		select {
		case h.reads <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		checked := h.now()
		epoch, versions, err := h.store.RuntimeGenerations(ctx, refs)
		<-h.reads
		if err != nil {
			return err
		}
		h.checks.Add(uint64(len(refs)))
		h.mu.Lock()
		h.epoch = epoch
		h.authorityChecked = checked
		for _, old := range batch {
			key := resourceCacheKey(old.pool, old.resource.ResourceRef)
			e := h.cache[key]
			if e == nil {
				continue
			}
			v := e.Value.(*cachedResource)
			if v.token != old.token {
				continue
			}
			if epoch == old.epoch && sameResourceVersion(versions[v.resource.Key()], v.resource.ResourceRef) {
				v.checked = checked
			} else {
				h.token++
				v.token = h.token
				v.checked = time.Time{}
				h.wakeLocked(key)
			}
		}
		h.mu.Unlock()
	}
	return nil
}

func (h *ResourceHub) Authority() (string, time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.epoch, h.authorityChecked, !h.authorityChecked.IsZero() && h.now().Sub(h.authorityChecked) < time.Duration(h.config.VerifyMillis)*time.Millisecond
}

func (h *ResourceHub) Confirmed(pool string, refs []domain.ResourceRef) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var oldest time.Time
	for _, r := range refs {
		e := h.cache[resourceCacheKey(pool, r)]
		if e == nil {
			return time.Time{}, false
		}
		v := e.Value.(*cachedResource)
		if v.checked.IsZero() || h.now().Sub(v.checked) >= time.Duration(h.config.VerifyMillis)*time.Millisecond || !sameResourceVersion(r, v.resource.ResourceRef) {
			return time.Time{}, false
		}
		if oldest.IsZero() || v.checked.Before(oldest) {
			oldest = v.checked
		}
	}
	return oldest, !oldest.IsZero()
}

func (h *ResourceHub) Stale(pool string, refs []domain.ResourceRef) []domain.ResourceRef {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []domain.ResourceRef{}
	for _, ref := range refs {
		e := h.cache[resourceCacheKey(pool, ref)]
		if e == nil || e.Value.(*cachedResource).checked.IsZero() || !sameResourceVersion(e.Value.(*cachedResource).resource.ResourceRef, ref) {
			out = append(out, domain.ResourceRef{Kind: ref.Kind, ID: ref.ID})
		}
	}
	return out
}

func (h *ResourceHub) Stats() map[string]uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]uint64{"connections": uint64(h.connections.Load()), "cache_entries": uint64(len(h.cache)), "cache_bytes": uint64(h.bytes), "cache_hits": h.hits.Load(), "cache_misses": h.misses.Load(), "source_loads": h.loads.Load(), "version_checks": h.checks.Load()}
}

// Recheck closes the dependency-discovery race. The stream installs watches for
// the expanded graph before checking its generations against the primary.
func (h *ResourceHub) Recheck(ctx context.Context, epoch string, resources []domain.RuntimeResource) (bool, error) {
	refs := make([]domain.ResourceRef, 0, len(resources))
	for _, resource := range resources {
		refs = append(refs, resource.ResourceRef)
	}
	valid := true
	for start := 0; start < len(refs); start += 256 {
		select {
		case h.reads <- struct{}{}:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		current, versions, err := h.store.RuntimeGenerations(ctx, refs[start:min(start+256, len(refs))])
		<-h.reads
		if err != nil {
			return false, err
		}
		for _, ref := range refs[start:min(start+256, len(refs))] {
			if current != epoch || !sameResourceVersion(ref, versions[ref.Key()]) {
				valid = false
				h.Invalidate(ref)
			}
		}
	}
	return valid, nil
}
