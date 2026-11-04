package storagecredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/adapters/storagecredentials/staticcred"
	"github.com/krun-dev/krun/internal/domain"
)

type issuerFunc func(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error)

func (f issuerFunc) Retrieve(ctx context.Context, scope domain.StorageCredentialScope, ttl time.Duration) (domain.StorageCredentials, error) {
	return f(ctx, scope, ttl)
}

func (issuerFunc) Capabilities() provider.Capabilities {
	return provider.Capabilities{CanIssueTemporary: true, CanRestrictScope: true}
}

func testScope() domain.StorageCredentialScope {
	return domain.StorageCredentialScope{
		Organization: "org-test", BucketID: "bucket-test", StoreID: "primary", Bucket: "example-bucket",
		Prefix: "worker-data/v1/org/org-test/buckets/bucket-test/", Access: domain.StorageRead | domain.StorageList, AuthorizationRevision: 1,
	}
}

func testOptions() Options {
	return Options{TTL: time.Hour, RefreshBefore: 5 * time.Minute, Timeout: time.Second, CacheEntries: 4, MaxConcurrent: 2, RequireTemporary: true}
}

func credential(now time.Time) domain.StorageCredentials {
	return domain.StorageCredentials{AccessKeyID: "test-access", SecretAccessKey: "test-secret", SessionToken: "test-token", ExpiresAt: now.Add(time.Hour)}
}

func TestManagerCoalescesAndCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	var calls atomic.Int64
	entered := make(chan struct{})
	release := make(chan struct{})
	manager, err := New(issuerFunc(func(ctx context.Context, _ domain.StorageCredentialScope, _ time.Duration) (domain.StorageCredentials, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return domain.StorageCredentials{}, ctx.Err()
		case <-release:
			return credential(time.Now()), nil
		}
	}), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := manager.Credentials(ctx, testScope(), time.Minute); first <- err }()
	<-entered
	cancel()
	if !errors.Is(<-first, context.Canceled) {
		t.Fatal("cancelled waiter did not return")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err != nil {
				t.Error(err)
			}
		})
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("issuer calls: %d", calls.Load())
	}
}

func TestCacheIdentityIncludesEntireAuthorizationScope(t *testing.T) {
	var calls int
	manager, err := New(issuerFunc(func(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error) {
		calls++
		return credential(time.Now()), nil
	}), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	scope := testScope()
	variants := []domain.StorageCredentialScope{scope}
	for _, mutate := range []func(*domain.StorageCredentialScope){
		func(s *domain.StorageCredentialScope) { s.Organization = "another-org" },
		func(s *domain.StorageCredentialScope) { s.BucketID = "another-bucket-id" },
		func(s *domain.StorageCredentialScope) { s.StoreID = "another-store" },
		func(s *domain.StorageCredentialScope) { s.Bucket = "another-bucket" },
		func(s *domain.StorageCredentialScope) { s.Prefix += "child/" },
		func(s *domain.StorageCredentialScope) { s.Access |= domain.StorageWrite },
		func(s *domain.StorageCredentialScope) { s.AuthorizationRevision++ },
	} {
		copy := scope
		mutate(&copy)
		variants = append(variants, copy)
	}
	for _, variant := range variants {
		for range 2 {
			if _, err := manager.Credentials(context.Background(), variant, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != len(variants) {
		t.Fatalf("expected independent scopes; calls=%d", calls)
	}
	if len(manager.entries) != testOptions().CacheEntries {
		t.Fatal("cache is not bounded")
	}
}

func TestRefreshFallbackNeverExtendsExpiryAndDenialDiscardsCache(t *testing.T) {
	var clock atomic.Int64
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock.Store(start.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	var failure error
	var calls int
	manager, err := New(issuerFunc(func(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error) {
		calls++
		return credential(start), failure
	}), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	manager.now = now
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err != nil {
		t.Fatal(err)
	}
	clock.Store(start.Add(56 * time.Minute).Unix())
	failure = &provider.Error{Kind: provider.Unavailable}
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err != nil {
		t.Fatal("valid fallback rejected:", err)
	}
	if _, err := manager.Credentials(context.Background(), testScope(), 5*time.Minute); err == nil {
		t.Fatal("returned insufficient validity")
	}
	if calls != 2 {
		t.Fatal("failure backoff did not suppress issuer call")
	}
	clock.Store(start.Add(57 * time.Minute).Unix())
	failure = &provider.Error{Kind: provider.PermissionDenied}
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err == nil {
		t.Fatal("permission denial reused credentials")
	}
	if len(manager.entries) != 0 {
		t.Fatal("denied credentials retained")
	}
}

func TestColdFailureBackoffAndConcurrencyAreBounded(t *testing.T) {
	var calls atomic.Int64
	manager, err := New(issuerFunc(func(context.Context, domain.StorageCredentialScope, time.Duration) (domain.StorageCredentials, error) {
		calls.Add(1)
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.Throttled}
	}), testOptions())
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err == nil {
			t.Fatal("expected error")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("cold failure retried without backoff")
	}
	manager.permits <- struct{}{}
	manager.permits <- struct{}{}
	scope := testScope()
	scope.BucketID = "new-bucket"
	_, err = manager.Credentials(context.Background(), scope, time.Minute)
	var issue *provider.Error
	if !errors.As(err, &issue) || issue.Kind != provider.Throttled {
		t.Fatal("missing admission limit")
	}
}

func TestStaticIsExplicitAndCredentialsAreRedacted(t *testing.T) {
	p, err := staticcred.New("example-access", "example-secret")
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	if _, err := New(p, options); err == nil {
		t.Fatal("static satisfies temporary-required policy")
	}
	options.RequireTemporary = false
	manager, err := New(p, options)
	if err != nil {
		t.Fatal(err)
	}
	c, err := manager.Credentials(context.Background(), testScope(), time.Minute)
	if err != nil || !c.ExpiresAt.IsZero() {
		t.Fatal("static credentials failed")
	}
	raw, _ := json.Marshal(c)
	for _, value := range []string{string(raw), fmt.Sprint(c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(value, "example-access") || strings.Contains(value, "example-secret") {
			t.Fatal("credentials leaked")
		}
	}
}

func TestRefreshTimeoutUsesOnlySufficientlyValidCachedCredentials(t *testing.T) {
	start := time.Now()
	var clock atomic.Int64
	clock.Store(start.Unix())
	var calls atomic.Int64
	p := issuerFunc(func(ctx context.Context, _ domain.StorageCredentialScope, _ time.Duration) (domain.StorageCredentials, error) {
		if calls.Add(1) == 1 {
			return credential(start), nil
		}
		<-ctx.Done()
		return domain.StorageCredentials{}, ctx.Err()
	})
	options := testOptions()
	options.Timeout = 10 * time.Millisecond
	manager, err := New(p, options)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err != nil {
		t.Fatal(err)
	}
	clock.Store(start.Add(56 * time.Minute).Unix())
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err != nil {
		t.Fatal("timeout lost a valid cache entry:", err)
	}
	clock.Store(start.Add(time.Hour).Unix())
	if _, err := manager.Credentials(context.Background(), testScope(), time.Minute); err == nil {
		t.Fatal("expired credentials reused")
	}
}
