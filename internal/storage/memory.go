package storage

import (
	"github.com/krun-dev/krun/internal/domain"
)

// MemoryOptions configures an isolated in-memory backend for tests. State is
// owned by the store after construction; callers use View/Update thereafter.
type MemoryOptions struct {
	State     *domain.State
	Audits    []domain.Audit
	OnPersist func()
}

func NewMemory(options MemoryOptions) *Store {
	state := options.State
	if state == nil {
		state = domain.NewState()
	}
	return &Store{memory: state, memoryAudits: append([]domain.Audit(nil), options.Audits...), onPersist: options.OnPersist}
}
