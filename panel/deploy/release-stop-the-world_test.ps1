$ErrorActionPreference = 'Stop'

$deploy = Split-Path -Parent $MyInvocation.MyCommand.Path
$scriptPath = Join-Path $deploy 'release-stop-the-world.sh'
$buildPath = Join-Path $deploy 'build-release.sh'
$migratePath = Join-Path $deploy 'migrate.sh'

foreach ($path in @($scriptPath, $buildPath, $migratePath)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "missing $path" }
}

$release = Get-Content -Raw -LiteralPath $scriptPath
$build = Get-Content -Raw -LiteralPath $buildPath
$migrate = Get-Content -Raw -LiteralPath $migratePath

function Assert-Contains([string]$Text, [string]$Needle, [string]$Message) {
    if (-not $Text.Contains($Needle)) { throw $Message }
}

function Assert-Order([string]$Text, [string[]]$Needles) {
    $last = -1
    foreach ($needle in $Needles) {
        $next = $Text.IndexOf($needle, $last + 1, [StringComparison]::Ordinal)
        if ($next -lt 0) { throw "missing ordered marker: $needle" }
        if ($next -le $last) { throw "incorrect order at: $needle" }
        $last = $next
    }
}

Assert-Contains $release 'flock -n 9' 'release must use a non-blocking exclusive lock'
Assert-Contains $release 'verify_release_manifest "$RELEASE_DIR"' 'release must verify its manifest before stopping services'
Assert-Contains $release 'wait_unit_inactive "$unit"' 'release must prove units inactive'
Assert-Contains $release 'wait_pid_gone "$OLD_ADMIN_PID"' 'release must prove old admin PID exited'
Assert-Contains $release 'wait_port_closed "$ADMIN_PORT"' 'release must prove admin port closed'
Assert-Contains $release 'PHASE=migration_attempted' 'migration fail-closed phase is missing'
Assert-Contains $release 'PHASE=isolating' 'partial-stop rollback phase is missing'
Assert-Contains $release 'PHASE=layout_switching' 'partial-layout rollback phase is missing'
Assert-Contains $release 'find bin migrations deploy -type f -print' 'manifest exact-file comparison must retain path prefixes'
Assert-Contains $release 'wait_pid_gone "$OLD_NODE_PID"' 'release must stop and prove the node DB writer exited'
Assert-Contains $release 'wait_port_closed "$NODE_PORT"' 'release must prove node port closed'
Assert-Contains $release 'rollback manual_required' 'post-migration failure must fail closed'
Assert-Contains $release 'PHASE=exposure_attempted' 'exposure boundary is missing'
Assert-Contains $release 'rollback prohibited_after_exposure' 'post-exposure rollback must be prohibited'
Assert-Contains $release '/readyz' 'dependency readiness must be checked'
Assert-Contains $release 'kill -TERM -- "-${MIGRATION_PGID:-$MIGRATION_PID}"' 'signal cleanup must stop the migration process group'
Assert-Contains $release 'verify_release_manifest "$RELEASE_DIR"' 'root-owned staged release must be reverified'
Assert-Contains $release 'BACKUP_OUTPUT="$($APP_DIR/deploy/backup-postgres.sh)"' 'an encrypted database backup must precede migration'
Assert-Contains $release 'wait_running_binary "$NODE_UNIT" aegis-node' 'service identity checks must tolerate bounded startup delay'
Assert-Contains $release 'PANDORA_STOPPED_WRITER_UPGRADE_APPROVED=yes' 'release must explicitly bridge stopped-writer proof into guarded migrations'
Assert-Contains $release 'PANDORA_LOCAL_MIGRATION_APPROVED=yes' 'release must explicitly approve the root-only loopback migration fallback'
Assert-Contains $release 'read_env_value AEGIS_PUBLIC_ADDR' 'health ports must derive from the live environment'
Assert-Contains $release 'PANDORA_APP_DIR cannot be /' 'live application root must be protected'
Assert-Contains $release 'release directory must be outside the live application directory' 'release source must not be nested under the live tree'
Assert-Contains $release 'exec 9>>"$LOCK_FILE"' 'lock acquisition must not truncate an existing file'
Assert-Contains $release 'release-stop-the-world.sh renewal-cutover.md' 'live deploy layout must retain the renewal cutover procedure'
Assert-Contains $release '[ "${file##*.}" != md ] || mode=0644' 'release documentation must be installed non-executable'
Assert-Contains $release 'release renewal cutover procedure is missing' 'release manifest preflight must require the cutover procedure'

Assert-Order $release @(
    'verify_release_manifest "$RELEASE_DIR"',
    'stop_unit_and_prove "$INGRESS_UNIT"',
    'stop_unit_and_prove "$ADMIN_UNIT"',
    'wait_pid_gone "$OLD_ADMIN_PID"',
    'wait_port_closed "$ADMIN_PORT"',
    'BACKUP_OUTPUT="$($APP_DIR/deploy/backup-postgres.sh)"',
    '"$RELEASE_DIR/deploy/check-migrations.sh" &',
    'PHASE=migration_attempted',
    'setsid env -i PATH="$PATH"',
    'systemctl start "${WRITER_UNITS[@]}"',
    'prove_all_ready || die "new writer binary identity or readiness check failed"',
    'PHASE=exposure_attempted',
    'systemctl start "$INGRESS_UNIT"'
)

Assert-Contains $build 'cp "$ROOT"/migrations/*.sql' 'release archive must contain migrations'
Assert-Contains $build 'find bin migrations deploy -type f' 'manifest must cover binaries, migrations and deployment controller'
Assert-Contains $build 'PANDORA_VERSION must match [A-Za-z0-9._-]+' 'release version must be path-safe'
Assert-Contains $build 'release output directory cannot be /' 'release output must protect filesystem root'
Assert-Contains $migrate '"$GOOSE" "$COMMAND" "$@"' 'migration wrapper must forward goose arguments'
Assert-Contains $migrate 'mktemp "${TMPDIR:-/tmp}/pandora-precheck.XXXXXX"' 'precheck log must use a private temporary file'
Assert-Contains $migrate "MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes" 'migration wrapper must use fixed guarded-migration GUCs'
Assert-Contains $migrate 'AEGIS_MIGRATION_DATABASE_URL="host=127.0.0.1 port=$POSTGRES_PORT user=$POSTGRES_USER dbname=$POSTGRES_DB sslmode=disable"' 'fallback migration DSN must be fixed to loopback and omit the password'
Assert-Contains $migrate 'GOOSE_ENV+=(PGPASSWORD="$MIGRATION_PGPASSWORD")' 'fallback password must be passed only through the scrubbed child environment'

$check = Get-Content -Raw -LiteralPath (Join-Path $deploy 'check-migrations.sh')
Assert-Contains $check 'set -Eeuo pipefail' 'migration precheck must enable strict pipeline handling'
Assert-Contains $check 'migration_files=(' 'empty migration sets must be detected'
Assert-Contains $check 'docker exec -i -e PGPASSWORD' 'database password value must not appear in docker argv'
Assert-Contains $check 'exec -c /bin/bash --noprofile --norc -p -c' 'host database tooling must receive a clean environment'
Assert-Contains $check 'exec /usr/bin/env -i PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' 'container database tooling must receive a clean environment'
Assert-Contains $check 'mktemp -d "$TEMP_ROOT/pandora-migration-check.XXXXXX"' 'migration errors must use the validated private temp root'
Assert-Contains $check "MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes" 'disposable precheck must support guarded migrations after stop proof'
Assert-Contains $check 'renewal cutover active_legacy=0' 'release precheck must prove no unlinked active renewal survives cutover'
Assert-Contains $check "status IN ('draft','pending_payment','processing','paid')" 'renewal cutover must cover every active renewal state'
Assert-Contains $check 'business_request_id IS DISTINCT FROM idempotency_key_id' 'renewal cutover must require exact claim linkage'
if ($check.Contains('${PGOPTIONS}')) { throw 'migration precheck must not trust caller-supplied PGOPTIONS' }

if ($release -match '(?m)^\s*set\s+-[^\r\n]*x') { throw 'release must not enable shell tracing' }
if ($release -match '(?i)(POSTGRES_PASSWORD|AEGIS_MASTER_KEY|JWT_.*SECRET).*echo') {
    throw 'release appears to print a secret variable'
}
if ($release -match 'systemctl\s+restart\s+("?\$?ADMIN_UNIT|"?\$?PUBLIC_UNIT|aegis-admin|aegis-public)') {
    throw 'rolling restart of writer services is forbidden'
}
if ($release.Contains('migration_command down-to')) { throw 'automatic database down-to is forbidden after migration starts' }
if ($check.Contains('-e "PGPASSWORD=${POSTGRES_PASSWORD}"')) { throw 'database password must not be placed in argv' }

Write-Output 'release-stop-the-world static gate: PASS'
