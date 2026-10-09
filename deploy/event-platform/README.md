# Event platform deployment

This is an opt-in **single-host learning deployment**, not a production HA topology.
The existing monitor CLI, SQLite and `deploy/cloud` profiles remain independent.
Never start both senders for a subscription during migration.

1. Copy `.env.example` to `.env`; provide random database/bootstrap/OAuth secrets and a
   JSON map of base64-encoded 32-byte AES keys. Keep previous key IDs during rotation.
2. Provide `TLS_DIR` with `tls.crt`, `tls.key`, `ca.crt` for three separate DNS names.
   CA must be readable by API/Grafana containers. Do not use development certificates
   in production. External and container DNS must resolve the same HTTPS issuer.
3. Export deployment variables before `./manage.sh render` (Compose reads `.env`,
   Python does not). Render does not start processes or change the database.
4. `GATEWAY=apisix ./manage.sh config`, then `./manage.sh up` starts migrations before
   workers. Set the Keycloak subject allowlist before granting platform-admin access.
   Keycloak realm import only initializes an empty realm; later identity changes are
   managed through its private administration tooling, never a public admin route.
5. `GATEWAY=nginx ./manage.sh up` stops APISIX and replaces only the entry process.
   Both listen on host port 443; enabling both profiles directly causes a port conflict.

Both entries share `gateway/policy.json`: Bearer auth, 64KiB bodies, IP limits,
12-second upstream budgets, no upstream retries, no credential-response cache and
removed client identity headers. The API independently validates JWTs and membership.
Keycloak/Grafana cookies do not authorize management API requests.
APISIX uses one worker within this single-host memory budget and Docker's private
DNS resolver with a five-second positive TTL. It fails closed while backend DNS or
authentication is unavailable; initial negative DNS answers can persist for 30 seconds.
Wait for authenticated API readiness before sending management writes. Larger gateway
deployments should size workers and service discovery separately from this profile.

The `live-api` public client uses Keycloak device authorization for CLI users.
POST form fields `client_id=live-api&scope=openid` to the issuer's
`/protocol/openid-connect/auth/device` endpoint. Open the returned verification URI,
sign in and approve the displayed user code. Poll `/protocol/openid-connect/token`
with `grant_type=urn:ietf:params:oauth:grant-type:device_code`, `client_id=live-api`
and the returned `device_code`, respecting `interval`, `slow_down` and expiry.
Use the resulting access token as Bearer; keep tokens out of command history/logs.
See [Keycloak device authorization](https://www.keycloak.org/docs/latest/server_admin/#_oidc-auth-flows).
Provision users privately and add their `sub` through tenant membership APIs.
Unattended integrations should have separately provisioned confidential service
accounts; no default production service-account credentials are shipped.

Statistics report the observed start/end span, rather than an exact upstream live
duration. List queries and projection replay are bounded; one replay handles up to
10,000 events and reports `range_too_large` for larger ranges. Split the requested
time range when that occurs. Shared polling currently groups identical configured
room IDs; configure one canonical ID consistently when subscriptions share a room.

SMTP adapters require STARTTLS and an administrator-maintained hostname allowlist;
port 465 implicit TLS is not configured. SMTP DATA acceptance and Feishu API acceptance
are durable outcomes; recipient inbox/user delivery requires separate acceptance.

Grafana accepts only Keycloak `operations` group members. No anonymous/login-form
access is enabled. Configure an actual Alertmanager receiver before operational
acceptance; the default empty receiver is intentionally not a delivery claim.
Alloy reads Docker JSON logs from a read-only path, without a Docker control socket.
Use the Docker `json-file` driver; rootless/alternative logging needs an adjusted source.
Service/role/level are labels; room, tenant, event, task and trace IDs stay in log bodies.

Default retention: broker 7 days, domain data 90 days, Loki 14 days, Tempo 7 days,
Prometheus 30 days. Kafka, Prometheus, Loki and Tempo retention is set in deployment variables.
Never prune active session deduplication or unpublished events to satisfy retention.

Persisted volumes are separate for business PostgreSQL, Keycloak PostgreSQL, Kafka,
Grafana, Loki, Tempo, Alloy positions and Prometheus. Back up both databases and the
credential keyring together; PostgreSQL backup alone cannot decrypt targets. Preserve
Keycloak issuer/client IDs and source offsets on restore. Backups/restores and service
recovery still require a disposable-environment drill before production deployment.

Use a release revision matching an exact main SHA, not a moving feature branch.
A server deployment, real notification and production restoration are separate gates.

Rendered gateway/realm files are readable by their container workers; the host generated
directory is forced to mode 0700. Mount only each required file into its service. Do not
make the generated directory publicly readable or include it in a web root or archive.
