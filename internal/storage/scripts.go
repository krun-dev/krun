package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func scriptKey(org, name string) string { return org + "/" + name }
func (s *Store) WorkerScript(ctx context.Context, org, name string) (domain.WorkerScript, bool, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		v, ok := s.scripts[scriptKey(org, name)]
		return v, ok, nil
	}
	var row models.Script
	err := s.orm.NewSelect().Model(&row).Where("org_id = ?", org).Where("script_name = ?", name).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkerScript{}, false, nil
	}
	return domain.WorkerScript{Organization: row.OrgID, Name: row.ScriptName, Service: row.ServiceID, WorkersDev: row.WorkersDev, PreviewsEnabled: row.PreviewsEnabled}, err == nil, err
}
func (s *Store) BindWorkerScript(ctx context.Context, script domain.WorkerScript) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.scripts == nil {
			s.scripts = map[string]domain.WorkerScript{}
		}
		key := scriptKey(script.Organization, script.Name)
		if old, ok := s.scripts[key]; ok && old.Service != script.Service {
			return domain.Problem(409, "REVISION_CONFLICT", "Worker name already bound")
		}
		s.scripts[key] = script
		return nil
	}
	row := models.Script{OrgID: script.Organization, ScriptName: script.Name, ServiceID: script.Service, WorkersDev: script.WorkersDev, PreviewsEnabled: script.PreviewsEnabled}
	result, err := s.orm.NewInsert().Model(&row).Column("org_id", "script_name", "service_id", "workers_dev", "previews_enabled").On("CONFLICT (org_id, script_name) DO UPDATE").Set("workers_dev = EXCLUDED.workers_dev").Set("previews_enabled = EXCLUDED.previews_enabled").Where("krun_worker_scripts.service_id = EXCLUDED.service_id").Returning("NULL").Exec(ctx)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.Problem(409, "REVISION_CONFLICT", "Worker name already bound")
	}
	return nil
}
func (s *Store) DeleteWorkerScript(ctx context.Context, org, name string) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.scripts, scriptKey(org, name))
		return nil
	}
	_, err := s.orm.NewDelete().Model((*models.Script)(nil)).Where("org_id = ?", org).Where("script_name = ?", name).Exec(ctx)
	return err
}
func (s *Store) WorkerScripts(ctx context.Context, org string) ([]domain.WorkerScript, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := []domain.WorkerScript{}
		for _, v := range s.scripts {
			if v.Organization == org {
				out = append(out, v)
			}
		}
		return out, nil
	}
	rows := []models.Script{}
	err := s.orm.NewSelect().Model(&rows).Where("org_id = ?", org).Order("script_name").Limit(200).Scan(ctx)
	out := make([]domain.WorkerScript, 0, len(rows))
	for _, v := range rows {
		out = append(out, domain.WorkerScript{Organization: v.OrgID, Name: v.ScriptName, Service: v.ServiceID, WorkersDev: v.WorkersDev, PreviewsEnabled: v.PreviewsEnabled})
	}
	return out, err
}

// ReserveWorkerScript atomically chooses one identity for concurrent first
// uploads. Recreating a deleted Script always receives a fresh service ID.
func (s *Store) ReserveWorkerScript(ctx context.Context, script domain.WorkerScript) (domain.WorkerScript, error) {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.scripts == nil {
			s.scripts = map[string]domain.WorkerScript{}
		}
		key := scriptKey(script.Organization, script.Name)
		if old, ok := s.scripts[key]; ok {
			return old, nil
		}
		s.scripts[key] = script
		return script, nil
	}
	row := models.Script{OrgID: script.Organization, ScriptName: script.Name, ServiceID: script.Service, WorkersDev: script.WorkersDev, PreviewsEnabled: script.PreviewsEnabled}
	if _, err := s.orm.NewInsert().Model(&row).Column("org_id", "script_name", "service_id", "workers_dev", "previews_enabled").On("CONFLICT (org_id,script_name) DO NOTHING").Returning("NULL").Exec(ctx); err != nil {
		return script, err
	}
	got, ok, err := s.WorkerScript(ctx, script.Organization, script.Name)
	if err == nil && !ok {
		err = domain.Problem(409, "REVISION_CONFLICT", "Worker identity changed")
	}
	return got, err
}
