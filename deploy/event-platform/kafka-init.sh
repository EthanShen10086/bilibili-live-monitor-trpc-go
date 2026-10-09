#!/bin/sh
set -eu
/opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --create --if-not-exists \
  --topic live.events.v1 --partitions 12 --replication-factor 1
# Creation flags do not update an existing topic. Apply retention on every init.
/opt/kafka/bin/kafka-configs.sh --bootstrap-server kafka:9092 --alter \
  --entity-type topics --entity-name live.events.v1 \
  --add-config "retention.ms=${KAFKA_RETENTION_MS:-604800000}"
