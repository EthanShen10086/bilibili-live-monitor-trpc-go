# Event platform implementation

Implementation baseline: main `58ffbe8`. The legacy CLI/configuration is preserved.
The platform is opt-in; existing deployed processes have not been restarted.

Implemented feature gates:

- Instance-scoped legacy credential resolution and original behavior regression.
- Tenant/membership/quotas/audit, encrypted targets and key rotation, version checks,
  OIDC/JWKS rotation and consumer-owned management repository interfaces.
- Shared room leases, transactional state/events/Outbox, acknowledged Kafka relay,
  separate notification/analytics groups and poison-event isolation.
- Independent Feishu group/private and STARTTLS SMTP tasks, durable TTL/retry/leases,
  duplicate suppression and graceful accepted-task drain.
- Statistics and projection-only replay; stopped SQLite snapshot import with
  source protection, historical/sent/pending preservation and disabled activation.
- Standalone APISIX and interchangeable Nginx, private backends, identity-header
  removal, JWT revalidation, DNS refresh and the same ingress contract suite.
- Alloy/Loki/Prometheus/Tempo/Grafana/Alertmanager profiles with bounded retention,
  log/request/trace/task correlation and operations-only Grafana identity.
- Fixed quality tools/hooks and real PostgreSQL, Kafka, Keycloak, mock Feishu and
  test SMTP CI. Contracts include actual broker/API outages and database timeouts.

Integration evidence and current delivery status are attached to
[PR #2](https://github.com/EthanShen10086/bilibili-live-monitor-trpc-go/pull/2).
Only a successful required `contracts` run confirms both real gateways and telemetry;
source configuration or unit tests alone do not confirm external acceptance.

Local quality checks include lint, race/unit tests, migration/import contracts against
disposable PostgreSQL, vulnerability reachability scanning and Git upload-manifest checks.
GitHub runs disposable Linux Docker infrastructure because Docker is unavailable on
the editing host. Feature commits are retained when merging to main.

Production boundaries: single-host Compose is not HA; SMTP DATA/Feishu API acceptance
does not prove a human recipient received the notification. Production server deployment,
real notification receipt and restore rehearsal remain independent operator acceptance.
The current importer supports SQLite polling snapshots; existing PostgreSQL/Redis legacy
deployments continue using their CLI and need a separate migration adapter to import.
Room sharing requires consistent configured IDs, and projection replays are bounded.
