package ca42runner

const (
	TrustRootPath            = "/etc/pandora/ca42"
	AuthorityPath            = "authority.v1"
	HostIdentityPath         = "host.identity"
	LedgerRootPath           = "/var/lib/pandora/ca42"
	LedgerPath               = "authority-ledger.v1"
	ReleaseJournalRootPath   = "/var/lib/pandora/release-journal"
	IncomingRootPath         = "/var/lib/pandora/releases/incoming"
	ReleaseManifestName      = "release.manifest"
	ExecutionPlanName        = "execution.plan"
	TrustCapsuleName         = "trust.capsule"
	ExternalManifestName     = "external.manifest"
	PathtrustBinaryName      = "pandora-pathtrust"
	AttestationCoreName      = "client-auth-attestation-v2.sh"
	AttestationName          = "attestation.v2"
	AttestationExpectedName  = "attestation.expected"
	AttestationPublicKeyName = "attestation-public-key.pem"
	ManifestVerifierName     = "pandora-client-auth-00042-manifest-verifier"
	PreflightRunnerName      = "check-migrations-isolated-pg18.sh"
	MigrationRunnerName      = "pandora-client-auth-00042-migration-runner"
	GooseBinaryName          = "goose"
	MigrationManifestName    = "migrations.sha256"
	MigrationsDirectoryName  = "migrations"
	GlobalsDumpName          = "globals.sql"
	DatabaseDumpName         = "database.dump"
)

const (
	maxHostIdentityBytes = 4096
	maxExternalBytes     = 4 << 20
)
