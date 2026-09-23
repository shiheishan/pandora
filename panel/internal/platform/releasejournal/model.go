package releasejournal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	releaseJournalFormat        = "pandora-release-journal-v1"
	releaseJournalFormatV2      = "pandora-release-journal-v2"
	releaseContractCoreFormatV1 = "pandora-client-auth-00042-release-contract-core-v1"
	releaseControllerContractV1 = "pandora-production-release-controller-v1"
	maxRecordBytes              = 1 << 20
	maxRecordLineBytes          = 4096
)

type releaseState string

const (
	statePrepared              releaseState = "PREPARED"
	stateIsolationAttempted    releaseState = "ISOLATION_ATTEMPTED"
	stateIsolated              releaseState = "ISOLATED"
	stateBackupAttempted       releaseState = "BACKUP_ATTEMPTED"
	stateBackupVerified        releaseState = "BACKUP_VERIFIED"
	stateLayoutSwitchAttempted releaseState = "LAYOUT_SWITCH_ATTEMPTED"
	stateLayoutSwitched        releaseState = "LAYOUT_SWITCHED"
	stateAdmissionAttempted    releaseState = "ADMISSION_ATTEMPTED"
	stateAdmissionVerified     releaseState = "ADMISSION_VERIFIED"
	stateMigrationAttempted    releaseState = "MIGRATION_ATTEMPTED"
	stateMigrated              releaseState = "MIGRATED"
	stateWritersStartAttempted releaseState = "WRITERS_START_ATTEMPTED"
	stateWritersReady          releaseState = "WRITERS_READY"
	stateExposureAttempted     releaseState = "EXPOSURE_ATTEMPTED"
	stateCommitted             releaseState = "COMMITTED"
	stateRecoveryRequired      releaseState = "RECOVERY_REQUIRED"
)

var (
	safeTokenRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	sqlIDRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	hex64RE     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	journalRE   = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.-]{0,127})\.([0-9a-f]{64})\.release-journal$`)

	normalNext = map[releaseState]releaseState{
		statePrepared:              stateIsolationAttempted,
		stateIsolationAttempted:    stateIsolated,
		stateIsolated:              stateBackupAttempted,
		stateBackupAttempted:       stateBackupVerified,
		stateBackupVerified:        stateLayoutSwitchAttempted,
		stateLayoutSwitchAttempted: stateLayoutSwitched,
		stateLayoutSwitched:        stateAdmissionAttempted,
		stateAdmissionAttempted:    stateAdmissionVerified,
		stateAdmissionVerified:     stateMigrationAttempted,
		stateMigrationAttempted:    stateMigrated,
		stateMigrated:              stateWritersStartAttempted,
		stateWritersStartAttempted: stateWritersReady,
		stateWritersReady:          stateExposureAttempted,
		stateExposureAttempted:     stateCommitted,
	}

	stateSegment = map[releaseState]string{
		statePrepared:              "000.prepared.record",
		stateIsolationAttempted:    "010.isolation_attempted.record",
		stateIsolated:              "020.isolated.record",
		stateBackupAttempted:       "030.backup_attempted.record",
		stateBackupVerified:        "040.backup_verified.record",
		stateLayoutSwitchAttempted: "050.layout_switch_attempted.record",
		stateLayoutSwitched:        "060.layout_switched.record",
		stateAdmissionAttempted:    "070.admission_attempted.record",
		stateAdmissionVerified:     "080.admission_verified.record",
		stateMigrationAttempted:    "090.migration_attempted.record",
		stateMigrated:              "100.migrated.record",
		stateWritersStartAttempted: "110.writers_start_attempted.record",
		stateWritersReady:          "120.writers_ready.record",
		stateExposureAttempted:     "130.exposure_attempted.record",
		stateCommitted:             "140.committed.record",
		stateRecoveryRequired:      "900.recovery_required.record",
	}
)

type recordField struct {
	key   string
	value string
}

type preparedIdentity struct {
	JournalFormat             string
	JournalID                 string
	ReleaseAttemptID          string
	CreatedAtEpoch            string
	ReleaseID                 string
	ReleaseRunID              string
	Architecture              string
	ControllerSHA256          string
	ReleaseManifestSHA256     string
	ReleaseContractCoreFormat string
	ReleaseContractCoreSHA256 string
	ControllerContract        string
	ReleaseControllerSHA256   string
	TargetIdentitySHA256      string
	TargetRootDevice          string
	TargetRootInode           string
	StagedTreeManifestSHA256  string
	LiveTreeManifestSHA256    string
	EnvironmentFileSHA256     string
	BackupControllerSHA256    string
	MachineIdentitySHA256     string
	BootIDSHA256              string
	PostgresSystemIdentifier  string
	DatabaseName              string
	DatabaseOID               string
	SourceWaterline           string
	AuthorizedTargetWaterline string
	MigrationSetSHA256        string
	IngressUnitSHA256         string
	WriterUnitsSHA256         string
	HealthConfigSHA256        string
}

type transitionRecord struct {
	JournalFormat        string
	JournalID            string
	ReleaseAttemptID     string
	Sequence             string
	EventID              string
	OccurredAtEpoch      string
	PreviousState        releaseState
	State                releaseState
	PreviousRecordSHA256 string
	EvidenceSHA256       string
	EvidenceSize         string
	RecordSHA256         string
}

type releaseJournalSnapshot struct {
	JournalFormat string
	Prepared      preparedIdentity
	Transitions   []transitionRecord
	Names         []string
	RecordBytes   [][]byte
	SegmentSHA256 []string
	State         releaseState
	HeadSHA256    string
	ManifestSHA   string
}

func canonicalBody(fields []recordField) []byte {
	var builder strings.Builder
	for _, field := range fields {
		builder.WriteString(field.key)
		builder.WriteByte('=')
		builder.WriteString(field.value)
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func chainedRecordSHA(previous string, body []byte) string {
	hash := sha256.New()
	hash.Write([]byte(previous))
	hash.Write(body)
	return hex.EncodeToString(hash.Sum(nil))
}

func appendRecordSHA(body []byte, digest string) []byte {
	result := append([]byte(nil), body...)
	result = append(result, []byte("record_sha256="+digest+"\n")...)
	return result
}

func validatePreparedIdentity(identity preparedIdentity) error {
	format := identity.JournalFormat
	if format == "" {
		format = releaseJournalFormat
	}
	if format != releaseJournalFormat && format != releaseJournalFormatV2 {
		return errors.New("journal_format_invalid")
	}
	if !hex64RE.MatchString(identity.JournalID) || (format == releaseJournalFormatV2 && isZeroHex64(identity.JournalID)) {
		return errors.New("journal_id_invalid")
	}
	if !safeTokenRE.MatchString(identity.ReleaseAttemptID) || !safeTokenRE.MatchString(identity.ReleaseID) {
		return errors.New("release_identity_invalid")
	}
	if !canonicalPositive(identity.CreatedAtEpoch) {
		return errors.New("created_at_epoch_invalid")
	}
	if identity.Architecture != "amd64" && identity.Architecture != "arm64" {
		return errors.New("architecture_invalid")
	}
	for _, value := range []string{
		identity.TargetIdentitySHA256,
		identity.StagedTreeManifestSHA256, identity.LiveTreeManifestSHA256,
		identity.EnvironmentFileSHA256, identity.BackupControllerSHA256,
		identity.MachineIdentitySHA256, identity.BootIDSHA256, identity.MigrationSetSHA256,
		identity.IngressUnitSHA256, identity.WriterUnitsSHA256, identity.HealthConfigSHA256,
	} {
		if !hex64RE.MatchString(value) || (format == releaseJournalFormatV2 && isZeroHex64(value)) {
			return errors.New("prepared_sha256_invalid")
		}
	}
	if format == releaseJournalFormat {
		if !hex64RE.MatchString(identity.ControllerSHA256) || !hex64RE.MatchString(identity.ReleaseManifestSHA256) {
			return errors.New("prepared_sha256_invalid")
		}
		if identity.ReleaseRunID != "" || identity.ReleaseContractCoreFormat != "" || identity.ReleaseContractCoreSHA256 != "" ||
			identity.ControllerContract != "" || identity.ReleaseControllerSHA256 != "" {
			return errors.New("v1_prepared_contains_v2_fields")
		}
	} else {
		if !safeTokenRE.MatchString(identity.ReleaseRunID) ||
			identity.ReleaseContractCoreFormat != releaseContractCoreFormatV1 ||
			identity.ControllerContract != releaseControllerContractV1 ||
			!hex64RE.MatchString(identity.ReleaseContractCoreSHA256) || isZeroHex64(identity.ReleaseContractCoreSHA256) ||
			!hex64RE.MatchString(identity.ReleaseControllerSHA256) || isZeroHex64(identity.ReleaseControllerSHA256) ||
			identity.ControllerSHA256 != "" || identity.ReleaseManifestSHA256 != "" {
			return errors.New("v2_prepared_contract_invalid")
		}
	}
	for _, value := range []string{
		identity.TargetRootDevice, identity.TargetRootInode, identity.PostgresSystemIdentifier,
		identity.DatabaseOID,
	} {
		if !canonicalPositive(value) {
			return errors.New("prepared_positive_integer_invalid")
		}
	}
	if !sqlIDRE.MatchString(identity.DatabaseName) {
		return errors.New("database_name_invalid")
	}
	if !canonicalNonNegative(identity.SourceWaterline) || !canonicalNonNegative(identity.AuthorizedTargetWaterline) {
		return errors.New("waterline_invalid")
	}
	if compareCanonicalUint(identity.AuthorizedTargetWaterline, identity.SourceWaterline) <= 0 {
		return errors.New("target_waterline_not_after_source")
	}
	if format == releaseJournalFormatV2 {
		oid, err := strconv.ParseUint(identity.DatabaseOID, 10, 32)
		if err != nil || oid == 0 || identity.SourceWaterline != "41" || identity.AuthorizedTargetWaterline != "42" {
			return errors.New("v2_database_contract_invalid")
		}
	}
	return nil
}

func preparedRecordBytes(identity preparedIdentity) ([]byte, string, error) {
	if err := validatePreparedIdentity(identity); err != nil {
		return nil, "", err
	}
	format := identity.JournalFormat
	if format == "" {
		format = releaseJournalFormat
	}
	fields := []recordField{
		{"format", releaseJournalFormat},
		{"record", "prepared"},
		{"journal_id", identity.JournalID},
		{"release_attempt_id", identity.ReleaseAttemptID},
		{"created_at_epoch", identity.CreatedAtEpoch},
		{"release_id", identity.ReleaseID},
		{"architecture", identity.Architecture},
		{"controller_sha256", identity.ControllerSHA256},
		{"release_manifest_sha256", identity.ReleaseManifestSHA256},
		{"target_identity_sha256", identity.TargetIdentitySHA256},
		{"target_root_device", identity.TargetRootDevice},
		{"target_root_inode", identity.TargetRootInode},
		{"staged_tree_manifest_sha256", identity.StagedTreeManifestSHA256},
		{"live_tree_manifest_sha256", identity.LiveTreeManifestSHA256},
		{"environment_file_sha256", identity.EnvironmentFileSHA256},
		{"backup_controller_sha256", identity.BackupControllerSHA256},
		{"machine_identity_sha256", identity.MachineIdentitySHA256},
		{"boot_id_sha256", identity.BootIDSHA256},
		{"postgres_system_identifier", identity.PostgresSystemIdentifier},
		{"database_name", identity.DatabaseName},
		{"database_oid", identity.DatabaseOID},
		{"source_waterline", identity.SourceWaterline},
		{"authorized_target_waterline", identity.AuthorizedTargetWaterline},
		{"migration_set_sha256", identity.MigrationSetSHA256},
		{"ingress_unit_sha256", identity.IngressUnitSHA256},
		{"writer_units_sha256", identity.WriterUnitsSHA256},
		{"health_config_sha256", identity.HealthConfigSHA256},
		{"state", string(statePrepared)},
	}
	if format == releaseJournalFormatV2 {
		fields = []recordField{
			{"format", releaseJournalFormatV2}, {"record", "prepared"}, {"journal_id", identity.JournalID},
			{"release_attempt_id", identity.ReleaseAttemptID}, {"created_at_epoch", identity.CreatedAtEpoch},
			{"release_id", identity.ReleaseID}, {"release_run_id", identity.ReleaseRunID},
			{"architecture", identity.Architecture}, {"release_contract_core_format", identity.ReleaseContractCoreFormat},
			{"release_contract_core_sha256", identity.ReleaseContractCoreSHA256},
			{"controller_contract", identity.ControllerContract}, {"release_controller_sha256", identity.ReleaseControllerSHA256},
			{"target_identity_sha256", identity.TargetIdentitySHA256}, {"target_root_device", identity.TargetRootDevice},
			{"target_root_inode", identity.TargetRootInode}, {"staged_tree_manifest_sha256", identity.StagedTreeManifestSHA256},
			{"live_tree_manifest_sha256", identity.LiveTreeManifestSHA256}, {"environment_file_sha256", identity.EnvironmentFileSHA256},
			{"backup_controller_sha256", identity.BackupControllerSHA256}, {"machine_identity_sha256", identity.MachineIdentitySHA256},
			{"boot_id_sha256", identity.BootIDSHA256}, {"postgres_system_identifier", identity.PostgresSystemIdentifier},
			{"database_name", identity.DatabaseName}, {"database_oid", identity.DatabaseOID},
			{"source_waterline", identity.SourceWaterline}, {"authorized_target_waterline", identity.AuthorizedTargetWaterline},
			{"migration_set_sha256", identity.MigrationSetSHA256}, {"ingress_unit_sha256", identity.IngressUnitSHA256},
			{"writer_units_sha256", identity.WriterUnitsSHA256}, {"health_config_sha256", identity.HealthConfigSHA256},
			{"state", string(statePrepared)},
		}
	}
	body := canonicalBody(fields)
	digest := sha256Bytes(body)
	return appendRecordSHA(body, digest), digest, nil
}

func validateTransition(previous, next releaseState) error {
	if _, ok := stateSegment[previous]; !ok {
		return errors.New("previous_state_unknown")
	}
	if _, ok := stateSegment[next]; !ok {
		return errors.New("next_state_unknown")
	}
	if previous == stateCommitted || previous == stateRecoveryRequired {
		return errors.New("terminal_state")
	}
	if next == stateRecoveryRequired {
		return nil
	}
	if normalNext[previous] != next {
		return errors.New("transition_invalid")
	}
	return nil
}

func transitionRecordBytes(record transitionRecord) ([]byte, string, error) {
	format := record.JournalFormat
	if format == "" {
		format = releaseJournalFormat
	}
	if format != releaseJournalFormat && format != releaseJournalFormatV2 {
		return nil, "", errors.New("transition_format_invalid")
	}
	if !hex64RE.MatchString(record.JournalID) || !safeTokenRE.MatchString(record.ReleaseAttemptID) {
		return nil, "", errors.New("transition_identity_invalid")
	}
	if !canonicalPositive(record.Sequence) || !hex64RE.MatchString(record.EventID) ||
		!canonicalPositive(record.OccurredAtEpoch) || !hex64RE.MatchString(record.PreviousRecordSHA256) ||
		!hex64RE.MatchString(record.EvidenceSHA256) || !canonicalPositive(record.EvidenceSize) {
		return nil, "", errors.New("transition_field_invalid")
	}
	if format == releaseJournalFormatV2 && (isZeroHex64(record.JournalID) || isZeroHex64(record.EventID) ||
		isZeroHex64(record.PreviousRecordSHA256) || isZeroHex64(record.EvidenceSHA256)) {
		return nil, "", errors.New("transition_zero_identity_invalid")
	}
	EvidenceSize, err := strconv.ParseUint(record.EvidenceSize, 10, 64)
	if err != nil || EvidenceSize == 0 || EvidenceSize > maxRecordBytes {
		return nil, "", errors.New("transition_evidence_size_invalid")
	}
	if err := validateTransition(record.PreviousState, record.State); err != nil {
		return nil, "", err
	}
	body := canonicalBody([]recordField{
		{"format", format},
		{"record", "transition"},
		{"journal_id", record.JournalID},
		{"release_attempt_id", record.ReleaseAttemptID},
		{"sequence", record.Sequence},
		{"event_id", record.EventID},
		{"occurred_at_epoch", record.OccurredAtEpoch},
		{"previous_state", string(record.PreviousState)},
		{"state", string(record.State)},
		{"previous_record_sha256", record.PreviousRecordSHA256},
		{"evidence_sha256", record.EvidenceSHA256},
		{"evidence_size", record.EvidenceSize},
	})
	digest := chainedRecordSHA(record.PreviousRecordSHA256, body)
	return appendRecordSHA(body, digest), digest, nil
}

func parseReleaseJournal(segments map[string][]byte) (releaseJournalSnapshot, error) {
	var result releaseJournalSnapshot
	if len(segments) == 0 || len(segments) > len(stateSegment) {
		return result, errors.New("segment_count_invalid")
	}
	Names := make([]string, 0, len(segments))
	for name := range segments {
		Names = append(Names, name)
	}
	sort.Strings(Names)
	if Names[0] != stateSegment[statePrepared] {
		return result, errors.New("prepared_segment_missing")
	}

	Prepared, preparedSHA, err := parsePreparedRecord(segments[Names[0]])
	if err != nil {
		return result, err
	}
	result.Prepared = Prepared
	result.JournalFormat = Prepared.JournalFormat
	result.Names = append(result.Names, Names[0])
	result.RecordBytes = append(result.RecordBytes, append([]byte(nil), segments[Names[0]]...))
	result.SegmentSHA256 = append(result.SegmentSHA256, sha256Bytes(segments[Names[0]]))
	result.State = statePrepared
	result.HeadSHA256 = preparedSHA

	for index, name := range Names[1:] {
		if result.State == stateCommitted || result.State == stateRecoveryRequired {
			return releaseJournalSnapshot{}, errors.New("bytes_after_terminal_state")
		}
		record, digest, err := parseTransitionRecord(segments[name], result)
		if err != nil {
			return releaseJournalSnapshot{}, err
		}
		if name != stateSegment[record.State] {
			return releaseJournalSnapshot{}, errors.New("segment_name_state_mismatch")
		}
		if record.Sequence != strconv.Itoa(index+1) {
			return releaseJournalSnapshot{}, errors.New("sequence_invalid")
		}
		for _, previous := range result.Transitions {
			if previous.EventID == record.EventID {
				return releaseJournalSnapshot{}, errors.New("event_id_reused")
			}
		}
		result.Transitions = append(result.Transitions, record)
		result.Names = append(result.Names, name)
		result.RecordBytes = append(result.RecordBytes, append([]byte(nil), segments[name]...))
		result.SegmentSHA256 = append(result.SegmentSHA256, sha256Bytes(segments[name]))
		result.State = record.State
		result.HeadSHA256 = digest
	}
	result.ManifestSHA = journalManifestSHA(result.JournalFormat, result.Names, result.SegmentSHA256, result.HeadSHA256)
	return result, nil
}

func parsePreparedRecord(data []byte) (preparedIdentity, string, error) {
	if bytes.HasPrefix(data, []byte("format="+releaseJournalFormat+"\n")) {
		return parsePreparedRecordV1(data)
	}
	if bytes.HasPrefix(data, []byte("format="+releaseJournalFormatV2+"\n")) {
		return parsePreparedRecordV2(data)
	}
	return preparedIdentity{}, "", errors.New("prepared_format_unsupported")
}

func parsePreparedRecordV1(data []byte) (preparedIdentity, string, error) {
	keys := []string{
		"format", "record", "journal_id", "release_attempt_id", "created_at_epoch", "release_id",
		"architecture", "controller_sha256", "release_manifest_sha256", "target_identity_sha256",
		"target_root_device", "target_root_inode", "staged_tree_manifest_sha256",
		"live_tree_manifest_sha256", "environment_file_sha256", "backup_controller_sha256",
		"machine_identity_sha256", "boot_id_sha256", "postgres_system_identifier", "database_name",
		"database_oid", "source_waterline", "authorized_target_waterline", "migration_set_sha256",
		"ingress_unit_sha256", "writer_units_sha256", "health_config_sha256", "state", "record_sha256",
	}
	values, body, err := parseCanonicalRecord(data, keys)
	if err != nil {
		return preparedIdentity{}, "", err
	}
	if values["format"] != releaseJournalFormat || values["record"] != "prepared" ||
		values["state"] != string(statePrepared) {
		return preparedIdentity{}, "", errors.New("prepared_contract_invalid")
	}
	identity := preparedIdentity{
		JournalFormat: releaseJournalFormat,
		JournalID:     values["journal_id"], ReleaseAttemptID: values["release_attempt_id"],
		CreatedAtEpoch: values["created_at_epoch"], ReleaseID: values["release_id"],
		Architecture: values["architecture"], ControllerSHA256: values["controller_sha256"],
		ReleaseManifestSHA256: values["release_manifest_sha256"], TargetIdentitySHA256: values["target_identity_sha256"],
		TargetRootDevice: values["target_root_device"], TargetRootInode: values["target_root_inode"],
		StagedTreeManifestSHA256: values["staged_tree_manifest_sha256"], LiveTreeManifestSHA256: values["live_tree_manifest_sha256"],
		EnvironmentFileSHA256: values["environment_file_sha256"], BackupControllerSHA256: values["backup_controller_sha256"],
		MachineIdentitySHA256: values["machine_identity_sha256"], BootIDSHA256: values["boot_id_sha256"],
		PostgresSystemIdentifier: values["postgres_system_identifier"], DatabaseName: values["database_name"],
		DatabaseOID: values["database_oid"], SourceWaterline: values["source_waterline"],
		AuthorizedTargetWaterline: values["authorized_target_waterline"], MigrationSetSHA256: values["migration_set_sha256"],
		IngressUnitSHA256: values["ingress_unit_sha256"], WriterUnitsSHA256: values["writer_units_sha256"],
		HealthConfigSHA256: values["health_config_sha256"],
	}
	if err := validatePreparedIdentity(identity); err != nil {
		return preparedIdentity{}, "", err
	}
	expected := sha256Bytes(body)
	if values["record_sha256"] != expected {
		return preparedIdentity{}, "", errors.New("prepared_record_sha256_mismatch")
	}
	return identity, expected, nil
}

func parsePreparedRecordV2(data []byte) (preparedIdentity, string, error) {
	keys := []string{
		"format", "record", "journal_id", "release_attempt_id", "created_at_epoch", "release_id",
		"release_run_id", "architecture", "release_contract_core_format", "release_contract_core_sha256",
		"controller_contract", "release_controller_sha256", "target_identity_sha256", "target_root_device",
		"target_root_inode", "staged_tree_manifest_sha256", "live_tree_manifest_sha256",
		"environment_file_sha256", "backup_controller_sha256", "machine_identity_sha256", "boot_id_sha256",
		"postgres_system_identifier", "database_name", "database_oid", "source_waterline",
		"authorized_target_waterline", "migration_set_sha256", "ingress_unit_sha256",
		"writer_units_sha256", "health_config_sha256", "state", "record_sha256",
	}
	values, body, err := parseCanonicalRecord(data, keys)
	if err != nil {
		return preparedIdentity{}, "", err
	}
	if values["format"] != releaseJournalFormatV2 || values["record"] != "prepared" ||
		values["state"] != string(statePrepared) {
		return preparedIdentity{}, "", errors.New("prepared_v2_contract_invalid")
	}
	identity := preparedIdentity{
		JournalFormat: releaseJournalFormatV2, JournalID: values["journal_id"],
		ReleaseAttemptID: values["release_attempt_id"], CreatedAtEpoch: values["created_at_epoch"],
		ReleaseID: values["release_id"], ReleaseRunID: values["release_run_id"], Architecture: values["architecture"],
		ReleaseContractCoreFormat: values["release_contract_core_format"], ReleaseContractCoreSHA256: values["release_contract_core_sha256"],
		ControllerContract: values["controller_contract"], ReleaseControllerSHA256: values["release_controller_sha256"],
		TargetIdentitySHA256: values["target_identity_sha256"], TargetRootDevice: values["target_root_device"],
		TargetRootInode: values["target_root_inode"], StagedTreeManifestSHA256: values["staged_tree_manifest_sha256"],
		LiveTreeManifestSHA256: values["live_tree_manifest_sha256"], EnvironmentFileSHA256: values["environment_file_sha256"],
		BackupControllerSHA256: values["backup_controller_sha256"], MachineIdentitySHA256: values["machine_identity_sha256"],
		BootIDSHA256: values["boot_id_sha256"], PostgresSystemIdentifier: values["postgres_system_identifier"],
		DatabaseName: values["database_name"], DatabaseOID: values["database_oid"], SourceWaterline: values["source_waterline"],
		AuthorizedTargetWaterline: values["authorized_target_waterline"], MigrationSetSHA256: values["migration_set_sha256"],
		IngressUnitSHA256: values["ingress_unit_sha256"], WriterUnitsSHA256: values["writer_units_sha256"],
		HealthConfigSHA256: values["health_config_sha256"],
	}
	if err := validatePreparedIdentity(identity); err != nil {
		return preparedIdentity{}, "", err
	}
	expected := sha256Bytes(body)
	if values["record_sha256"] != expected {
		return preparedIdentity{}, "", errors.New("prepared_v2_record_sha256_mismatch")
	}
	return identity, expected, nil
}

func parseTransitionRecord(data []byte, prior releaseJournalSnapshot) (transitionRecord, string, error) {
	keys := []string{
		"format", "record", "journal_id", "release_attempt_id", "sequence", "event_id",
		"occurred_at_epoch", "previous_state", "state", "previous_record_sha256",
		"evidence_sha256", "evidence_size", "record_sha256",
	}
	values, body, err := parseCanonicalRecord(data, keys)
	if err != nil {
		return transitionRecord{}, "", err
	}
	if values["format"] != prior.JournalFormat ||
		(values["format"] != releaseJournalFormat && values["format"] != releaseJournalFormatV2) ||
		values["record"] != "transition" {
		return transitionRecord{}, "", errors.New("transition_contract_invalid")
	}
	record := transitionRecord{
		JournalFormat: values["format"],
		JournalID:     values["journal_id"], ReleaseAttemptID: values["release_attempt_id"],
		Sequence: values["sequence"], EventID: values["event_id"], OccurredAtEpoch: values["occurred_at_epoch"],
		PreviousState: releaseState(values["previous_state"]), State: releaseState(values["state"]),
		PreviousRecordSHA256: values["previous_record_sha256"], EvidenceSHA256: values["evidence_sha256"],
		EvidenceSize: values["evidence_size"], RecordSHA256: values["record_sha256"],
	}
	if record.JournalID != prior.Prepared.JournalID || record.ReleaseAttemptID != prior.Prepared.ReleaseAttemptID ||
		record.PreviousState != prior.State || record.PreviousRecordSHA256 != prior.HeadSHA256 {
		return transitionRecord{}, "", errors.New("transition_prior_binding_mismatch")
	}
	priorEpoch := prior.Prepared.CreatedAtEpoch
	if len(prior.Transitions) > 0 {
		priorEpoch = prior.Transitions[len(prior.Transitions)-1].OccurredAtEpoch
	}
	if compareCanonicalUint(record.OccurredAtEpoch, priorEpoch) < 0 {
		return transitionRecord{}, "", errors.New("transition_time_not_monotonic")
	}
	if !hex64RE.MatchString(record.EventID) || !canonicalPositive(record.OccurredAtEpoch) ||
		!hex64RE.MatchString(record.EvidenceSHA256) || !canonicalPositive(record.EvidenceSize) {
		return transitionRecord{}, "", errors.New("transition_value_invalid")
	}
	if record.JournalFormat == releaseJournalFormatV2 && (isZeroHex64(record.EventID) ||
		isZeroHex64(record.EvidenceSHA256) || isZeroHex64(record.PreviousRecordSHA256)) {
		return transitionRecord{}, "", errors.New("transition_zero_identity_invalid")
	}
	EvidenceSize, err := strconv.ParseUint(record.EvidenceSize, 10, 64)
	if err != nil || EvidenceSize == 0 || EvidenceSize > maxRecordBytes {
		return transitionRecord{}, "", errors.New("transition_evidence_size_invalid")
	}
	if err := validateTransition(record.PreviousState, record.State); err != nil {
		return transitionRecord{}, "", err
	}
	expected := chainedRecordSHA(prior.HeadSHA256, body)
	if record.RecordSHA256 != expected {
		return transitionRecord{}, "", errors.New("transition_record_sha256_mismatch")
	}
	return record, expected, nil
}

func parseCanonicalRecord(data []byte, keys []string) (map[string]string, []byte, error) {
	if len(data) == 0 || len(data) > maxRecordBytes || !utf8.Valid(data) ||
		bytes.IndexByte(data, 0) >= 0 || bytes.IndexByte(data, '\r') >= 0 || data[len(data)-1] != '\n' {
		return nil, nil, errors.New("record_encoding_invalid")
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	if len(lines) != len(keys) {
		return nil, nil, errors.New("record_line_count_invalid")
	}
	values := make(map[string]string, len(keys))
	var body strings.Builder
	for index, key := range keys {
		line := lines[index]
		if line == "" || len(line) > maxRecordLineBytes {
			return nil, nil, errors.New("record_line_invalid")
		}
		prefix := key + "="
		if !strings.HasPrefix(line, prefix) {
			return nil, nil, fmt.Errorf("record_key_order_invalid:%s", key)
		}
		value := strings.TrimPrefix(line, prefix)
		if value == "" || strings.Contains(value, "=") {
			return nil, nil, fmt.Errorf("record_value_invalid:%s", key)
		}
		values[key] = value
		if key != "record_sha256" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	return values, []byte(body.String()), nil
}

func journalManifestSHA(format string, Names, segmentHashes []string, head string) string {
	var builder strings.Builder
	manifestFormat := "pandora-release-journal-manifest-v1"
	if format == releaseJournalFormatV2 {
		manifestFormat = "pandora-release-journal-manifest-v2"
	}
	builder.WriteString("format=" + manifestFormat + "\n")
	for index, name := range Names {
		builder.WriteString("segment=")
		builder.WriteString(name)
		builder.WriteByte('\n')
		builder.WriteString("sha256=")
		builder.WriteString(segmentHashes[index])
		builder.WriteByte('\n')
	}
	builder.WriteString("head_sha256=")
	builder.WriteString(head)
	builder.WriteByte('\n')
	return sha256Bytes([]byte(builder.String()))
}

func isZeroHex64(value string) bool { return value == strings.Repeat("0", 64) }

func snapshotPrefix(snapshot releaseJournalSnapshot, count int) (releaseJournalSnapshot, error) {
	if count < 1 || count > len(snapshot.Names) {
		return releaseJournalSnapshot{}, errors.New("prefix_count_invalid")
	}
	segments := make(map[string][]byte, count)
	for index := 0; index < count; index++ {
		segments[snapshot.Names[index]] = snapshot.RecordBytes[index]
	}
	return parseReleaseJournal(segments)
}

func canonicalPositive(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func canonicalNonNegative(value string) bool {
	if value == "0" {
		return true
	}
	return canonicalPositive(value)
}

func compareCanonicalUint(left, right string) int {
	left = strings.TrimLeft(left, "0")
	right = strings.TrimLeft(right, "0")
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return strings.Compare(left, right)
}
