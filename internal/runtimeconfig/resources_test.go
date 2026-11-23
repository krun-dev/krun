package runtimeconfig

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
)

type resourceSource struct {
	common     bool
	mu         sync.Mutex
	generation int64
	loads      int
	fail       bool
	entered    chan struct{}
	release    chan struct{}
}

func (s *resourceSource) RuntimeResources(ctx context.Context, pool string, refs []domain.ResourceRef) (string, []domain.RuntimeResource, error) {
	s.mu.Lock()
	s.loads++
	generation, fail, entered, release, common := s.generation, s.fail, s.entered, s.release, s.common
	s.entered = nil
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	if fail {
		return "", nil, errors.New("database offline")
	}
	out := make([]domain.RuntimeResource, 0, len(refs))
	for _, ref := range refs {
		ref.Generation = generation
		ref.Incarnation = "inc"
		out = append(out, domain.RuntimeResource{ResourceRef: ref, Payload: []byte(`{"enabled":true}`)})
	}
	if common {
		out = append(out, domain.RuntimeResource{ResourceRef: domain.ResourceRef{Kind: "organization", ID: "shared", Incarnation: "inc", Generation: 1}, Payload: []byte(`{"allowed":true}`)})
	}
	return "epoch", out, nil
}
func (s *resourceSource) RuntimeGenerations(ctx context.Context, refs []domain.ResourceRef) (string, map[string]domain.ResourceRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", nil, errors.New("database offline")
	}
	out := map[string]domain.ResourceRef{}
	for _, ref := range refs {
		ref.Generation = s.generation
		ref.Incarnation = "inc"
		out[ref.Key()] = ref
	}
	return "epoch", out, nil
}
func resourceHubFixture() (*ResourceHub, *resourceSource, []domain.ResourceRef) {
	source := &resourceSource{generation: 1}
	return NewResourceHub(source, config.WorkerRuntimeConfig{}), source, []domain.ResourceRef{{Kind: "deployment", ID: "worker/prod"}}
}
func TestResourceCacheHitsDoNotRenewAuthority(t *testing.T) {
	h, s, refs := resourceHubFixture()
	now := time.Now()
	h.now = func() time.Time { return now }
	_, loaded, err := h.Resources(context.Background(), "pool", refs)
	if err != nil {
		t.Fatal(err)
	}
	stop := h.Watch("pool", refs, make(chan struct{}, 1))
	defer stop()
	now = now.Add(4 * time.Second)
	if _, _, err = h.Resources(context.Background(), "pool", refs); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.fail = true
	s.mu.Unlock()
	now = now.Add(2 * time.Second)
	if _, fresh := h.Confirmed("pool", []domain.ResourceRef{loaded[0].ResourceRef}); fresh {
		t.Fatal("cache hit renewed authority")
	}
	if err = h.Verify(context.Background()); err == nil {
		t.Fatal("offline verification succeeded")
	}
	if _, _, err = h.Resources(context.Background(), "pool", refs); err == nil {
		t.Fatal("expired cache served as authoritative")
	}
}
func TestResourceCacheRepairsMissedHint(t *testing.T) {
	h, s, refs := resourceHubFixture()
	now := time.Now()
	h.now = func() time.Time { return now }
	_, _, err := h.Resources(context.Background(), "pool", refs)
	if err != nil {
		t.Fatal(err)
	}
	stop := h.Watch("pool", refs, make(chan struct{}, 1))
	defer stop()
	s.mu.Lock()
	s.generation = 2
	s.mu.Unlock()
	now = now.Add(3 * time.Second)
	if err = h.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, got, err := h.Resources(context.Background(), "pool", refs)
	if err != nil || got[0].Generation != 2 {
		t.Fatalf("generation not repaired: %+v %v", got, err)
	}
}
func TestResourceCacheRejectsColdLoadBeforeEvictingSubscribedEntries(t *testing.T) {
	h, _, refs := resourceHubFixture()
	h.config.CacheEntries = 1
	if _, _, err := h.Resources(context.Background(), "pool", refs); err != nil {
		t.Fatal(err)
	}
	stop := h.Watch("pool", refs, make(chan struct{}, 1))
	defer stop()
	if _, _, err := h.Resources(context.Background(), "pool", []domain.ResourceRef{{Kind: "deployment", ID: "other/prod"}}); err == nil {
		t.Fatal("cold load exceeded protected capacity")
	}
	if _, _, ok := h.graphLocked("pool", refs); !ok {
		t.Fatal("hot entry was evicted")
	}
}
func TestResourceCacheMinimumGenerationAndSharedCancellation(t *testing.T) {
	h, s, refs := resourceHubFixture()
	s.entered = make(chan struct{})
	s.release = make(chan struct{})
	entered := s.entered
	first, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, _, err := h.Resources(first, "pool", refs); finished <- err }()
	<-entered
	second := make(chan error, 1)
	go func() { _, _, err := h.Resources(context.Background(), "pool", refs); second <- err }()
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter cancellation: %v", err)
	}
	close(s.release)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.release = nil
	s.generation = 2
	s.mu.Unlock()
	refs[0].Generation = 2
	_, got, err := h.Resources(context.Background(), "pool", refs)
	if err != nil || got[0].Generation != 2 {
		t.Fatalf("minimum ignored: %+v %v", got, err)
	}
}

func TestConcurrentIdenticalDependencyFillsDoNotInvalidateEachOther(t *testing.T) {
	h, s, _ := resourceHubFixture()
	s.common = true
	s.release = make(chan struct{})
	release := s.release
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, _, err := h.Resources(context.Background(), "pool", []domain.ResourceRef{{Kind: "deployment", ID: fmt.Sprintf("worker-%d/prod", i)}})
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		loads := s.loads
		s.mu.Unlock()
		if loads == 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent reads did not start")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	for i := 0; i < 8; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loads != 8 {
		t.Fatalf("identical dependencies caused retries: %d", s.loads)
	}
}
func TestLateFillCannotOverwriteANewerGeneration(t *testing.T) {
	h, s, refs := resourceHubFixture()
	if _, _, err := h.Resources(context.Background(), "pool", refs); err != nil {
		t.Fatal(err)
	}
	h.Invalidate(refs[0])
	s.entered = make(chan struct{})
	s.release = make(chan struct{})
	entered, release := s.entered, s.release
	result := make(chan []domain.RuntimeResource, 1)
	failed := make(chan error, 1)
	go func() {
		_, resources, err := h.Resources(context.Background(), "pool", refs)
		if err != nil {
			failed <- err
			return
		}
		result <- resources
	}()
	<-entered
	s.mu.Lock()
	s.generation = 2
	s.release = nil
	s.mu.Unlock()
	if _, _, err := h.Resources(context.Background(), "pool", append([]domain.ResourceRef{refs[0]}, domain.ResourceRef{Kind: "deployment", ID: "second/prod"})); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-failed:
		t.Fatal(err)
	case resources := <-result:
		if resources[0].Generation != 2 {
			t.Fatal("late fill resurrected old configuration")
		}
	}
}

func TestColdHostsLeaveReplacementHeadroom(t *testing.T) {
	h, s, _ := resourceHubFixture()
	h.config.CacheEntries = 2
	first := []domain.ResourceRef{{Kind: "route", ID: "pool/first.test"}}
	if _, _, err := h.Resources(context.Background(), "pool", first); err != nil {
		t.Fatal(err)
	}
	stop := h.Watch("pool", first, make(chan struct{}, 1))
	defer stop()
	if _, _, err := h.Resources(context.Background(), "pool", []domain.ResourceRef{{Kind: "route", ID: "pool/second.test"}}); err == nil {
		t.Fatal("cold admission consumed replacement reserve")
	}
	s.mu.Lock()
	s.generation = 2
	s.mu.Unlock()
	first[0].Generation = 2
	if _, resources, err := h.Resources(context.Background(), "pool", first); err != nil || resources[0].Generation != 2 {
		t.Fatalf("existing host could not update: %+v %v", resources, err)
	}
}
