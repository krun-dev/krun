// Package storagecredentials provides bounded shared caching for independently
// configured cloud issuers. It never authorizes a runtime or transfers objects.
package storagecredentials

import (
	"container/list"
	"context"
	"errors"
	"hash/fnv"
	"sync"
	"time"

	"github.com/krun-dev/krun/internal/adapters/storagecredentials/provider"
	"github.com/krun-dev/krun/internal/domain"
)

type Options struct {
	TTL              time.Duration
	RefreshBefore    time.Duration
	Timeout          time.Duration
	CacheEntries     int
	MaxConcurrent    int
	RequireTemporary bool
}

type cacheEntry struct {
	scope       domain.StorageCredentialScope
	credentials domain.StorageCredentials
	refreshAt   time.Time
	retryAt     time.Time
	lastError   error
}

type flight struct {
	done        chan struct{}
	credentials domain.StorageCredentials
	err         error
}

type Manager struct {
	provider provider.Provider
	options  Options
	mu       sync.Mutex
	entries  map[domain.StorageCredentialScope]*list.Element
	lru      *list.List
	flights  map[domain.StorageCredentialScope]*flight
	permits  chan struct{}
	now      func() time.Time
}

func New(p provider.Provider, options Options) (*Manager, error) {
	if p == nil || options.TTL < 900*time.Second || options.TTL > 12*time.Hour ||
		options.RefreshBefore < 0 || options.RefreshBefore >= options.TTL ||
		options.CacheEntries < 1 || options.CacheEntries > 65536 ||
		options.MaxConcurrent < 1 || options.MaxConcurrent > 128 ||
		options.Timeout <= 0 || options.Timeout > time.Minute {
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	capabilities := p.Capabilities()
	if options.RequireTemporary && (!capabilities.CanIssueTemporary || !capabilities.CanRestrictScope) {
		return nil, &provider.Error{Kind: provider.InvalidConfiguration}
	}
	return &Manager{
		provider: p,
		options:  options,
		entries:  make(map[domain.StorageCredentialScope]*list.Element),
		lru:      list.New(),
		flights:  make(map[domain.StorageCredentialScope]*flight),
		permits:  make(chan struct{}, options.MaxConcurrent),
		now:      time.Now,
	}, nil
}

func (m *Manager) Capabilities() provider.Capabilities { return m.provider.Capabilities() }

func usable(c domain.StorageCredentials, now time.Time, minimum time.Duration) bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != "" && (c.ExpiresAt.IsZero() || c.ExpiresAt.After(now.Add(minimum)))
}

func (m *Manager) Credentials(ctx context.Context, scope domain.StorageCredentialScope, minimum time.Duration) (domain.StorageCredentials, error) {
	if err := provider.ValidateScope(scope); err != nil {
		return domain.StorageCredentials{}, err
	}
	if minimum < 0 || minimum >= m.options.TTL {
		return domain.StorageCredentials{}, &provider.Error{Kind: provider.InsufficientValidity}
	}
	if err := ctx.Err(); err != nil {
		return domain.StorageCredentials{}, err
	}
	m.mu.Lock()
	now := m.now()
	if element := m.entries[scope]; element != nil {
		entry := element.Value.(*cacheEntry)
		if now.Before(entry.retryAt) && entry.lastError != nil && !usable(entry.credentials, now, minimum) {
			err := entry.lastError
			m.mu.Unlock()
			return domain.StorageCredentials{}, err
		}
		if usable(entry.credentials, now, minimum) && (entry.credentials.ExpiresAt.IsZero() || now.Before(entry.refreshAt) || now.Before(entry.retryAt)) {
			m.lru.MoveToFront(element)
			credentials := entry.credentials
			m.mu.Unlock()
			return credentials, nil
		}
	}
	f := m.flights[scope]
	if f == nil {
		select {
		case m.permits <- struct{}{}:
		default:
			m.mu.Unlock()
			return domain.StorageCredentials{}, &provider.Error{Kind: provider.Throttled}
		}
		f = &flight{done: make(chan struct{})}
		m.flights[scope] = f
		// A cancelled request does not cancel another request's shared refresh.
		refreshContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.options.Timeout)
		go m.refresh(refreshContext, cancel, scope, f)
	}
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return domain.StorageCredentials{}, ctx.Err()
	case <-f.done:
	}
	if f.err == nil && usable(f.credentials, m.now(), minimum) {
		return f.credentials, nil
	}
	var issue *provider.Error
	if errors.As(f.err, &issue) && issue.Retryable() {
		m.mu.Lock()
		if element := m.entries[scope]; element != nil {
			entry := element.Value.(*cacheEntry)
			if usable(entry.credentials, m.now(), minimum) {
				credentials := entry.credentials
				m.mu.Unlock()
				return credentials, nil
			}
		}
		m.mu.Unlock()
	}
	if f.err != nil {
		return domain.StorageCredentials{}, f.err
	}
	return domain.StorageCredentials{}, &provider.Error{Kind: provider.InsufficientValidity}
}

func (m *Manager) refresh(ctx context.Context, cancel context.CancelFunc, scope domain.StorageCredentialScope, f *flight) {
	defer cancel()
	credentials, err := m.provider.Retrieve(ctx, scope, m.options.TTL)
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		err = &provider.Error{Kind: provider.Unavailable}
	}
	now := m.now()
	invalidTemporary := m.Capabilities().CanIssueTemporary &&
		(credentials.SessionToken == "" || credentials.ExpiresAt.IsZero())
	if err == nil && (!usable(credentials, now, 0) || invalidTemporary) {
		err = &provider.Error{Kind: provider.Unavailable}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		if old := m.entries[scope]; old != nil {
			m.lru.Remove(old)
		}
		// Deterministic per-scope jitter avoids synchronized refresh across buckets.
		jitterHash := fnv.New32a()
		_, _ = jitterHash.Write([]byte(scope.Organization + "/" + scope.BucketID + "/" + scope.Prefix))
		jitter := time.Duration(jitterHash.Sum32()%31) * time.Second
		entry := &cacheEntry{
			scope:       scope,
			credentials: credentials,
			refreshAt:   credentials.ExpiresAt.Add(-m.options.RefreshBefore - jitter),
		}
		m.entries[scope] = m.lru.PushFront(entry)
	} else {
		var issue *provider.Error
		if errors.As(err, &issue) && issue.Retryable() {
			old := m.entries[scope]
			if old == nil {
				old = m.lru.PushFront(&cacheEntry{scope: scope})
				m.entries[scope] = old
			}
			entry := old.Value.(*cacheEntry)
			entry.retryAt = now.Add(5 * time.Second)
			entry.lastError = err
		} else if old := m.entries[scope]; old != nil {
			delete(m.entries, scope)
			m.lru.Remove(old)
		}
	}
	for m.lru.Len() > m.options.CacheEntries {
		oldest := m.lru.Back()
		delete(m.entries, oldest.Value.(*cacheEntry).scope)
		m.lru.Remove(oldest)
	}
	f.credentials, f.err = credentials, err
	delete(m.flights, scope)
	<-m.permits
	close(f.done)
}
