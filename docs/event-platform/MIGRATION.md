# SQLite transition and recovery

1. Back up `config.yaml`, `.env`, SQLite, and any existing PostgreSQL/Redis state.
   This importer supports the lightweight SQLite polling mode only. PostgreSQL/Redis
   deployments remain compatible in the old CLI and require their own migration adapter.
2. Stop the old sending process. Confirm no launchd/systemd/container supervisor can
   restart it. Copy the stopped SQLite file to a separate migration snapshot.
3. Migrate the destination schema with `event-platform --role migrate`.
4. Run `event-platform --role import --legacy-root /absolute/config-directory
   --legacy-db /absolute/stopped-snapshot.sqlite --owner-subject KEYCLOAK_SUBJECT
   --confirm-old-sender-stopped`. Destination/keyring comes from platform environment.
   The source is opened immutable/read-only; it is never overwritten. Keep this command
   and its source digest report as private migration evidence.
5. Verify default tenant `legacy-default`, subscription `legacy-room`, target metadata,
   `ep_rooms.session_key` and `ep_jobs` states/counts/TTL/attempts against the backup.
   Import creates **disabled** subscription, published historical events and original
   sent/pending keys. Retry of identical snapshot is idempotent; changed snapshot or
   conflicting default tenant is rejected. No provider or Kafka publication occurs.
6. With sender stopped, enable the subscription using its current version, run detector,
   relay/router/analytics in shadow mode, verify the current room key and pending queue.
   Keep every sender stopped while reconciling. Then start the new sender once.
7. Retain old source and keyring until independent notification/restore acceptance.
   To roll back, stop the new sender first and reconcile accepted notifications back to
   the old durable queue. Restoring the old DB without reconciliation can duplicate sends.

Source digest changes when the SQLite snapshot changes. Do not import a changing live
file, WAL sidecar, or a backup taken while sending; immutable mode assumes a complete
closed snapshot. The explicit stopped-sender flag is an operator declaration, not proof
that unrelated supervisors were stopped.

Restore drill: restore business DB and old+active encryption keys, restore Keycloak DB
with unchanged issuer/client configuration, recreate Kafka topic and retained offsets,
start API/detector/relay/router/analytics with sender off, compare jobs/events/projections,
then enable exactly one sender per subscription. A broker outage leaves Outbox durable;
a publication crash may duplicate an event, and persisted routing prevents duplicate jobs.
External provider acceptance followed by process death still permits resend; there is no
claim of absolute exactly-once delivery to human recipients.
