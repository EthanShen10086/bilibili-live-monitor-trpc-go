CREATE TABLE IF NOT EXISTS lm_scopes (
 scope text PRIMARY KEY, room bigint NOT NULL, binding text NOT NULL, owner text NOT NULL DEFAULT '',
 lease_until timestamptz NOT NULL DEFAULT '-infinity', version bigint NOT NULL DEFAULT 0,
 last_cleanup bigint NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS lm_observations (
 scope text NOT NULL REFERENCES lm_scopes(scope), room bigint NOT NULL,
 live integer NOT NULL, start text, key text, PRIMARY KEY(scope,room)
);
CREATE TABLE IF NOT EXISTS lm_jobs (
 scope text NOT NULL REFERENCES lm_scopes(scope), key text NOT NULL,
 payload text NOT NULL,status text NOT NULL DEFAULT 'pending',attempts integer NOT NULL DEFAULT 0,
 next bigint NOT NULL,expires bigint NOT NULL,last_error text,
 claim text NOT NULL DEFAULT '',lease_until timestamptz NOT NULL DEFAULT '-infinity',
 published boolean NOT NULL DEFAULT false,PRIMARY KEY(scope,key)
);
CREATE INDEX IF NOT EXISTS lm_pending_next ON lm_jobs(scope,next) WHERE status='pending';
CREATE INDEX IF NOT EXISTS lm_pending_expires ON lm_jobs(scope,expires) WHERE status='pending';
CREATE INDEX IF NOT EXISTS lm_outbox ON lm_jobs(scope,next) WHERE status='pending' AND NOT published;
