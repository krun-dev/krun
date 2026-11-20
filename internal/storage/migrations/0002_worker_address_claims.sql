CREATE TABLE krun_worker_address_claims (
 label text PRIMARY KEY,
 owner_kind text NOT NULL,
 owner_id text NOT NULL,
 expires_at timestamptz
);
CREATE INDEX krun_worker_address_claims_owner ON krun_worker_address_claims(owner_kind, owner_id);
CREATE INDEX krun_worker_address_claims_expiry ON krun_worker_address_claims(expires_at) WHERE expires_at IS NOT NULL;
INSERT INTO krun_worker_address_claims(label, owner_kind, owner_id)
 SELECT locator, 'worker', resource_id FROM krun_resources WHERE kind='worker' AND locator<>'';
