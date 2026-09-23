#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

name=pandora-pg18-enrollment-20260810
port=55459
password=pandora_test_only
database=pandora_enrollment
migrations=${PANDORA_MIGRATIONS_DIR:-/root/pandora-fullgate-20260810-1240/panel/migrations}
goose=${GOOSE_BIN:-/root/go/bin/goose}

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

if docker inspect "$name" >/dev/null 2>&1; then
  echo "exact test container already exists: $name" >&2
  exit 78
fi

docker run -d --name "$name" \
  --tmpfs /var/lib/postgresql:rw,noexec,nosuid,size=512m \
  -p "127.0.0.1:${port}:5432" \
  -e "POSTGRES_PASSWORD=$password" -e "POSTGRES_DB=$database" \
  postgres:18-alpine >/dev/null

for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -U postgres -d "$database" >/dev/null 2>&1 && break
  if [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" != true ]; then
    docker logs "$name" >&2 || true
    exit 71
  fi
  sleep 1
done
if ! docker exec "$name" pg_isready -U postgres -d "$database" >/dev/null; then
  docker logs "$name" >&2 || true
  exit 72
fi
sleep 2

dsn="postgres://postgres:${password}@127.0.0.1:${port}/${database}?sslmode=disable"
PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes' \
  "$goose" -dir "$migrations" postgres "$dsn" up

major=$(docker exec "$name" psql -U postgres -d "$database" -Atqc "show server_version_num")
oid=$(docker exec "$name" psql -U postgres -d "$database" -Atqc "select oid from pg_database where datname=current_database()")
system_id=$(docker exec "$name" sh -c "pg_controldata \"\$PGDATA\"" | awk -F: '/Database system identifier/{gsub(/ /,"",$2);print $2}')
[ "${major%%????}" = 18 ] || { echo "expected PostgreSQL 18, got $major" >&2; exit 70; }

docker exec "$name" psql -v ON_ERROR_STOP=1 -U postgres -d "$database" <<'SQL'
DO $$
BEGIN
  IF has_table_privilege('aegis_app','node_enrollments','UPDATE') THEN
    RAISE EXCEPTION 'aegis_app unexpectedly has table UPDATE';
  END IF;
  IF NOT has_column_privilege('aegis_app','node_enrollments','state','UPDATE') THEN
    RAISE EXCEPTION 'aegis_app lacks state UPDATE';
  END IF;
  IF has_column_privilege('aegis_app','node_enrollments','id','UPDATE') THEN
    RAISE EXCEPTION 'aegis_app unexpectedly has id UPDATE';
  END IF;
END
$$;
SQL

docker exec "$name" psql -v ON_ERROR_STOP=1 -U postgres -d "$database" \
  -c "ALTER ROLE aegis_app LOGIN PASSWORD 'pandora_app_test_only'; ALTER ROLE aegis_app SET search_path=pg_catalog,public,pg_temp; REVOKE TEMPORARY ON DATABASE ${database} FROM PUBLIC, aegis_app" >/dev/null
cd /root/pandora-fullgate-20260810-1240/panel
AEGIS_ENROLLMENT_PG18_FIXTURE=disposable-v1 \
AEGIS_ENROLLMENT_PG18_DATABASE="$database" \
AEGIS_ENROLLMENT_PG18_ADMIN_DSN="$dsn" \
AEGIS_ENROLLMENT_PG18_DSN="postgres://aegis_app:pandora_app_test_only@127.0.0.1:${port}/${database}?sslmode=disable" \
GOTMPDIR=/root/pandora-go-tmp GOMAXPROCS=1 \
  go test -mod=readonly -race -p 1 -count=1 -run '^TestNodeEnrollmentPG18$' ./internal/domain/nodefabric

printf 'PANDORA_NODE_ENROLLMENT_PG18_PASS major=%s database_oid=%s system_identifier=%s\n' \
  "$major" "$oid" "$system_id"
