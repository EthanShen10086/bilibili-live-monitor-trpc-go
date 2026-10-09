# Event platform implementation

Implementation baseline: main 58ffbe8. Legacy CLI/configuration and deployed services remain unchanged.

Delivery gates: compatibility; tenant API/OIDC; room events/outbox; Kafka/router; channels; projections/replay; APISIX/Nginx; observability; migration/end-to-end CI; PR/main integration and cleanup.

Each gate requires feature-scoped commits, the shared quality entrypoint and behavioral evidence. Production delivery and cloud deployment are separate from integration tests.
