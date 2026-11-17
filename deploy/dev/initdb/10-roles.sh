#!/bin/bash
# Development only: creates the two database roles on first start of the dev
# database. The passwords come from the container environment (docker-compose.yml).
set -euo pipefail
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v migrator_password="$KAS_MIGRATOR_PASSWORD" -v runtime_password="$KAS_RUNTIME_PASSWORD" -v db="$POSTGRES_DB" <<'SQL'
CREATE ROLE kas_migrator LOGIN PASSWORD :'migrator_password';
CREATE ROLE kas_runtime LOGIN PASSWORD :'runtime_password';
ALTER DATABASE :"db" OWNER TO kas_migrator;
SQL
