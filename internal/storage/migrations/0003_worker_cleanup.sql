CREATE TABLE krun_worker_cleanup (
 worker_id text PRIMARY KEY,
 org_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 next_attempt_at timestamptz NOT NULL DEFAULT now() + interval '90 seconds',
 completed_at timestamptz,
 attempts integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT ''
);
CREATE INDEX krun_worker_cleanup_pending ON krun_worker_cleanup(next_attempt_at);
INSERT INTO krun_worker_cleanup(worker_id,org_id)
 SELECT b.worker_id,min(b.org_id) FROM krun_worker_builds b
 WHERE NOT EXISTS (SELECT 1 FROM krun_resources r WHERE r.resource_id=b.worker_id AND r.kind='worker')
 GROUP BY b.worker_id ON CONFLICT DO NOTHING;
