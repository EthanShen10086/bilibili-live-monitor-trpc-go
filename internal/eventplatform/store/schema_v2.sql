ALTER TABLE ep_jobs ADD COLUMN traceparent text NOT NULL DEFAULT '';
ALTER TABLE ep_jobs ADD COLUMN tracestate text NOT NULL DEFAULT '';
CREATE INDEX ep_events_retention ON ep_events(at);
CREATE INDEX ep_jobs_retention ON ep_jobs(expires) WHERE state IN ('sent','failed','expired','disabled');
CREATE INDEX ep_audit_retention ON ep_audit(at);
