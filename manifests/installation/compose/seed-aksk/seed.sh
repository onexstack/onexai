#!/bin/sh
#
# Wait for oai-agent-apiserver's AutoMigrate to create agent.secret, then write
# the two caller rows used by the secure profile.
#
# Runs as the entrypoint of the postgres:16-alpine seed container, so it must not
# start a database. PGPASSWORD, POSTGRES_USER, POSTGRES_DB, CHAT_SECRET and
# JOB_SECRET come from the service environment.

set -eu

for _ in $(seq 1 60); do
  if psql -h postgres \
      -U "${POSTGRES_USER:-onexai}" \
      -d "${POSTGRES_DB:-onexai}" \
      -v chat_secret="$CHAT_SECRET" \
      -v job_secret="$JOB_SECRET" \
      -f /seed/aksk.sql; then
    echo "caller secrets are seeded"
    exit 0
  fi
  sleep 2
done

echo "timed out waiting for agent.secret; is oai-agent-apiserver healthy?" >&2
exit 1
