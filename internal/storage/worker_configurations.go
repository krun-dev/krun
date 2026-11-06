package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/storage/models"
)

func (s *Store) writeWorkerConfigurations(ctx context.Context, tx pgx.Tx, before, after *domain.State) error {
	for id, c := range after.Workers.Configurations {
		if old, exists := before.Workers.Configurations[id]; exists {
			sameTime := old.Created.Equal(c.Created)
			old.Created = c.Created
			if !sameTime || !reflect.DeepEqual(old, c) {
				return errors.New("Worker configurations are immutable")
			}
			continue
		}
		revision, guarded := after.PendingWorkerConfigurationGuards[id]
		if !guarded {
			return errors.New("Worker configuration revision guard is missing")
		}
		// Serialize just this environment against concurrent edits/deletion;
		// snapshot creation never advances its draft or deployment revision.
		tag, err := tx.Exec(ctx, `UPDATE krun_worker_environments SET config_revision=config_revision WHERE worker_id=$1 AND env_name=$2 AND config_revision=$3`, c.Worker, c.Environment, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return domain.Problem(409, "REVISION_CONFLICT", "Environment configuration changed")
		}
		variables, err := json.Marshal(c.Variables)
		if err != nil {
			return err
		}
		row := models.Configuration{
			ConfigurationID: id,
			OrgID:           c.Organization,
			WorkerID:        c.Worker,
			EnvName:         c.Environment,
			Name:            c.Name,
			Message:         c.Message,
			Variables:       variables,
			VersionMetadata: c.VersionMetadata,
			AssetsBinding:   c.AssetsBinding,
			CreatedAt:       c.Created,
		}
		if err := storageMutation(ctx, tx, s.orm.NewInsert().Model(&row).Returning("NULL"), true); err != nil {
			return err
		}
		for name, version := range c.SecretVersions {
			tag, err := tx.Exec(ctx, `INSERT INTO krun_worker_configuration_secrets(configuration_id,name,secret_version_id)
 SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM krun_worker_secret_versions WHERE secret_version_id=$3 AND org_id=$4 AND worker_id=$5 AND name=$2 AND (env_name=$6 OR ($6<>'production' AND env_name='@previews-base')))`, id, name, version, c.Organization, c.Worker, c.Environment)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return domain.Problem(409, "WORKER_SECRET_UNAVAILABLE", "Configuration secret is unavailable")
			}
		}
		for name, binding := range c.R2Bindings {
			if err := checkR2Bucket(ctx, tx, binding.BucketID, c.Organization); err != nil {
				return err
			}
			row := models.ConfigurationR2{ConfigurationID: id, Name: name, BucketID: binding.BucketID}
			if err := storageMutation(ctx, tx, s.orm.NewInsert().Model(&row).Returning("NULL"), true); err != nil {
				return err
			}
		}
	}
	for id := range before.Workers.Configurations {
		if _, exists := after.Workers.Configurations[id]; !exists {
			if err := deleteWorkerConfiguration(ctx, tx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteWorkerConfiguration(ctx context.Context, tx pgx.Tx, id string) error {
	for _, table := range []string{"krun_worker_configuration_secrets", "krun_worker_configuration_r2", "krun_worker_configurations"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE configuration_id=$1`, id); err != nil {
			return err
		}
	}
	return nil
}

func readWorkerConfigurations(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c domain.WorkerConfiguration
		var variables []byte
		if err := rows.Scan(&c.ID, &c.Organization, &c.Worker, &c.Environment, &c.Name, &c.Message, &variables, &c.VersionMetadata, &c.AssetsBinding, &c.Created); err != nil {
			return err
		}
		if err := json.Unmarshal(variables, &c.Variables); err != nil {
			return err
		}
		c.SecretVersions = map[string]string{}
		c.SecretNames = []string{}
		st.Workers.Configurations[c.ID] = c
	}
	return rows.Err()
}

func readWorkerConfigurationSecrets(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, version string
		if err := rows.Scan(&id, &name, &version); err != nil {
			return err
		}
		c, exists := st.Workers.Configurations[id]
		if !exists {
			continue
		}
		c.SecretVersions[name] = version
		c.SecretNames = domain.WorkerSecretNames(c.SecretVersions)
		st.Workers.Configurations[id] = c
	}
	return rows.Err()
}

func readWorkerConfigurationR2(results pgx.BatchResults, st *domain.State) error {
	rows, err := results.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, bucket string
		if err := rows.Scan(&id, &name, &bucket); err != nil {
			return err
		}
		c, exists := st.Workers.Configurations[id]
		if !exists {
			continue
		}
		if c.R2Bindings == nil {
			c.R2Bindings = map[string]domain.WorkerR2Binding{}
		}
		b := st.Workers.Buckets[bucket]
		c.R2Bindings[name] = domain.WorkerR2Binding{BucketID: bucket, BucketName: b.Name, StoreID: b.Store}
		st.Workers.Configurations[id] = c
	}
	return rows.Err()
}

// Configuration history is retained for 30 days. Current/confirmed and pending
// deployments protect their configurations; deleting an environment removes
// its snapshots and bindings together. Each background pass removes <=1000.
func (s *Store) retainWorkerConfigurations(ctx context.Context) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil || s.memory.Workers == nil {
			return nil
		}
		protected := map[string]bool{}
		for _, worker := range s.memory.Workers.Workers {
			for _, env := range worker.Environments {
				protected[s.memory.Workers.Versions[env.Version].Configuration] = true
				protected[s.memory.Workers.Versions[env.Confirmed].Configuration] = true
			}
		}
		for _, op := range s.memory.Workers.Operations {
			if op.State == "queued" || op.State == "validating" || op.State == "committed" {
				protected[s.memory.Workers.Versions[op.Version].Configuration] = true
			}
		}
		for id, c := range s.memory.Workers.Configurations {
			if !protected[id] && !c.Created.After(now().Add(-WorkerConfigurationRetain)) {
				delete(s.memory.Workers.Configurations, id)
			}
		}
		return nil
	}
	_, err := s.pool.Exec(ctx, `WITH victims AS MATERIALIZED(
 SELECT c.configuration_id FROM krun_worker_configurations c
 WHERE c.created_at<=$1
 AND NOT EXISTS(SELECT 1 FROM krun_worker_versions v JOIN krun_worker_environments e ON e.version_id=v.version_id OR e.confirmed_version_id=v.version_id WHERE v.configuration_id=c.configuration_id)
 AND NOT EXISTS(SELECT 1 FROM krun_worker_versions v JOIN krun_worker_operations o ON o.version_id=v.version_id WHERE v.configuration_id=c.configuration_id AND o.state NOT IN('succeeded','failed','available','superseded'))
 ORDER BY c.created_at LIMIT 1000
 ), secrets AS(DELETE FROM krun_worker_configuration_secrets WHERE configuration_id IN(SELECT configuration_id FROM victims)),
 r2 AS(DELETE FROM krun_worker_configuration_r2 WHERE configuration_id IN(SELECT configuration_id FROM victims))
 DELETE FROM krun_worker_configurations WHERE configuration_id IN(SELECT configuration_id FROM victims)`, now().Add(-WorkerConfigurationRetain))
	return err
}

// Delete after the environment CAS, so snapshots saved by a concurrent request
// before that CAS are removed as well, even if absent from our loaded preimage.
func deleteEnvironmentConfigurations(ctx context.Context, tx pgx.Tx, worker, environment string) error {
	_, err := tx.Exec(ctx, `WITH victims AS MATERIALIZED(SELECT configuration_id FROM krun_worker_configurations WHERE worker_id=$1 AND env_name=$2), secrets AS(DELETE FROM krun_worker_configuration_secrets WHERE configuration_id IN(SELECT configuration_id FROM victims)), r2 AS(DELETE FROM krun_worker_configuration_r2 WHERE configuration_id IN(SELECT configuration_id FROM victims)) DELETE FROM krun_worker_configurations WHERE configuration_id IN(SELECT configuration_id FROM victims)`, worker, environment)
	return err
}
