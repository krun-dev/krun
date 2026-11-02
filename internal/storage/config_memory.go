package storage

import (
	"context"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
)

// Synthetic feed fixtures; production publishes only normalized deltas.
func (s *Store) AppendConfigRevision(ctx context.Context, digest string, payload, ops []byte, retain int) (int64, int64, error) {
	latest, _, err := s.ConfigState(ctx)
	if err != nil {
		return 0, 0, err
	}
	for {
		revision, floor, appended, err := s.AppendConfigRevisionCAS(ctx, latest, digest, payload, ops, retain)
		if err != nil || appended {
			return revision, floor, err
		}
		latest = revision
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
	}
}

func (s *Store) AppendConfigRevisionCAS(ctx context.Context, expected int64, digest string, payload, ops []byte, retain int) (revision int64, floor int64, appended bool, err error) {
	if s.pool != nil {
		return 0, 0, false, errors.New("snapshot config revisions require the memory backend")
	}
	if retain < 1 {
		retain = 1
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.configSeq != expected {
		floor := int64(0)
		if len(s.configRevs) > 0 {
			floor = s.configRevs[0].Revision
		}
		return s.configSeq, floor, false, nil
	}
	s.configSeq = expected + 1
	revision = s.configSeq
	s.configRevs = append(s.configRevs, domain.WorkerConfigRevision{Revision: revision, Digest: digest, Payload: payload, Ops: ops, Created: now()})
	floor = revision - int64(retain) + 1
	if floor < 1 {
		floor = 1
	}
	cut := 0
	for cut < len(s.configRevs) && s.configRevs[cut].Revision < floor {
		cut++
	}
	if cut > 0 {
		s.configRevs = append([]domain.WorkerConfigRevision(nil), s.configRevs[cut:]...)
	}
	return revision, floor, true, nil
}

func (s *Store) LatestConfigRevision(ctx context.Context) (domain.WorkerConfigRevision, bool, error) {
	if s.pool != nil {
		return domain.WorkerConfigRevision{}, false, errors.New("snapshot config revisions require the memory backend")
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	if len(s.configRevs) == 0 {
		return domain.WorkerConfigRevision{}, false, nil
	}
	last := s.configRevs[len(s.configRevs)-1]
	return last, true, nil
}
