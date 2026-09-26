//go:build linux && (amd64 || arm64)

// [INPUT]: 依赖 main_linux.go 的 parsedJournal、field 与 chainedDigest
// [OUTPUT]: 包内提供 recordSchemas、parseJournal、parseRecord、validateRecordValues、validateJournalName
// [POS]: pandora-cic-journal 的日志解析与校验：从 main_linux.go 拆出。只收 UTF-8、拒绝 CR 与 NUL，键必须齐全、不重复且按 schema 顺序，哈希链逐条复算，intent → catalog → drop → close 的状态迁移与互相绑定在这里判定，close 之后不许有尾随字节
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

var recordSchemas = map[string][]string{
	"intent": {
		"format", "record", "journal_id", "created_at_epoch", "run_id",
		"release_manifest_sha256", "runner_sha256", "migration_sha256",
		"source_system_identifier", "database_name", "database_oid", "candidate",
		"table", "index_name", "expected_indexdef_sha256",
		"expected_predicate_sha256", "expected_dependency_sha256", "attempt",
		"status", "record_sha256",
	},
	"catalog": {
		"record", "observed_at_epoch", "journal_id", "run_id", "candidate",
		"table_oid", "index_oid", "constraint_oid", "catalog_sha256",
		"indexdef_sha256", "predicate_sha256", "dependency_sha256", "classifier",
		"status", "record_sha256",
	},
	"drop": {
		"record", "dropped_at_epoch", "journal_id", "run_id", "candidate",
		"index_oid", "catalog_sha256", "classifier", "status", "record_sha256",
	},
	"close": {
		"record", "closed_at_epoch", "journal_id", "run_id", "candidate",
		"former_index_oid", "classifier", "status", "record_sha256",
	},
}

func parseJournal(data []byte) (parsedJournal, error) {
	var result parsedJournal
	if len(data) == 0 || len(data) > maxJournalBytes {
		return result, deny("journal_size_invalid")
	}
	if !utf8.Valid(data) {
		return result, deny("journal_non_utf8")
	}
	if bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 {
		return result, deny("journal_cr_or_nul")
	}
	if data[len(data)-1] != '\n' {
		return result, deny("journal_missing_final_lf")
	}
	rawLines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(rawLines) == 0 {
		return result, deny("journal_empty")
	}
	lines := make([]string, len(rawLines))
	for index, raw := range rawLines {
		if len(raw) == 0 || len(raw) > maxLineBytes {
			return result, deny("journal_blank_or_overlong_line")
		}
		lines[index] = string(raw)
	}

	offset := 0
	previousHash := ""
	expectedNext := "intent"
	for offset < len(lines) {
		recordType := expectedNext
		if expectedNext == "catalog-or-terminal" {
			key, value, ok := splitCanonicalLine(lines[offset])
			if !ok || key != "record" {
				return result, deny("journal_record_boundary_invalid")
			}
			recordType = value
			if recordType != "catalog" && recordType != "drop" && recordType != "close" {
				return result, deny("journal_unknown_record")
			}
		}
		schema, ok := recordSchemas[recordType]
		if !ok || offset+len(schema) > len(lines) {
			return result, deny("journal_record_truncated")
		}
		recordLines := lines[offset : offset+len(schema)]
		values, body, hashValue, err := parseRecord(recordType, recordLines, schema, previousHash)
		if err != nil {
			return result, err
		}
		if err := validateRecordValues(recordType, values, result); err != nil {
			return result, err
		}
		_ = body
		result.records = append(result.records, values)
		result.lastType = recordType
		result.lastHash = hashValue
		previousHash = hashValue
		offset += len(schema)

		switch recordType {
		case "intent":
			result.journalID = values["journal_id"]
			result.runID = values["run_id"]
			result.candidate = values["candidate"]
			result.table = values["table"]
			result.indexName = values["index_name"]
			result.indexdefSHA = values["expected_indexdef_sha256"]
			result.predicateSHA = values["expected_predicate_sha256"]
			result.dependencySHA = values["expected_dependency_sha256"]
			expectedNext = "catalog-or-terminal"
		case "catalog":
			result.indexOID = values["index_oid"]
			result.catalogSHA = values["catalog_sha256"]
			if offset < len(lines) {
				key, value, ok := splitCanonicalLine(lines[offset])
				if !ok || key != "record" || (value != "drop" && value != "close") {
					return result, deny("journal_transition_after_catalog_invalid")
				}
			}
			expectedNext = "catalog-or-terminal"
		case "drop":
			if offset < len(lines) {
				key, value, ok := splitCanonicalLine(lines[offset])
				if !ok || key != "record" || value != "close" {
					return result, deny("journal_transition_after_drop_invalid")
				}
			}
			expectedNext = "catalog-or-terminal"
		case "close":
			if offset != len(lines) {
				return result, deny("journal_trailing_bytes_after_closed")
			}
		}
	}
	if len(result.records) == 0 || result.records[0]["record"] != "intent" {
		return result, deny("journal_intent_missing")
	}
	if len(result.records) > 4 {
		return result, deny("journal_too_many_records")
	}
	seen := make(map[string]struct{})
	for _, record := range result.records {
		kind := record["record"]
		if _, ok := seen[kind]; ok {
			return result, deny("journal_duplicate_phase")
		}
		seen[kind] = struct{}{}
	}
	if len(result.records) >= 2 && result.records[1]["record"] != "catalog" {
		return result, deny("journal_catalog_phase_missing")
	}
	result.fullSHA256 = digestBytes(data)
	return result, nil
}

func parseRecord(recordType string, lines, schema []string, previousHash string) (map[string]string, []byte, string, error) {
	values := make(map[string]string, len(schema))
	fields := make([]field, 0, len(schema)-1)
	for index, expectedKey := range schema {
		key, value, ok := splitCanonicalLine(lines[index])
		if !ok {
			return nil, nil, "", deny("journal_key_value_line_invalid")
		}
		if key != expectedKey {
			return nil, nil, "", deny("journal_key_missing_duplicate_or_reordered")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, nil, "", deny("journal_duplicate_key")
		}
		values[key] = value
		if key != "record_sha256" {
			fields = append(fields, field{key, value})
		}
	}
	if values["record"] != recordType {
		return nil, nil, "", deny("journal_record_type_mismatch")
	}
	body := canonicalBody(fields)
	expectedHash := digestBytes(body)
	if previousHash != "" {
		expectedHash = chainedDigest(previousHash, body)
	}
	if values["record_sha256"] != expectedHash {
		return nil, nil, "", deny("journal_record_sha256_mismatch")
	}
	return values, body, expectedHash, nil
}

func splitCanonicalLine(line string) (string, string, bool) {
	if strings.Count(line, "=") != 1 {
		return "", "", false
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok || key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func validateRecordValues(recordType string, values map[string]string, prior parsedJournal) error {
	switch recordType {
	case "intent":
		if values["format"] != "client-auth-00043-cleanup-journal-v1" ||
			!hex64RE.MatchString(values["journal_id"]) ||
			requireEpoch(values["created_at_epoch"]) != nil ||
			!runIDRE.MatchString(values["run_id"]) ||
			!hex64RE.MatchString(values["release_manifest_sha256"]) ||
			!hex64RE.MatchString(values["runner_sha256"]) ||
			!hex64RE.MatchString(values["migration_sha256"]) ||
			!sqlIDRE.MatchString(values["database_name"]) ||
			!candidateRE.MatchString(values["candidate"]) ||
			!sqlIDRE.MatchString(values["table"]) ||
			!sqlIDRE.MatchString(values["index_name"]) ||
			!hex64RE.MatchString(values["expected_indexdef_sha256"]) ||
			!hex64RE.MatchString(values["expected_predicate_sha256"]) ||
			!hex64RE.MatchString(values["expected_dependency_sha256"]) ||
			values["attempt"] != "1" || values["status"] != "intent_fsynced" {
			return deny("journal_intent_value_invalid")
		}
		if _, err := parsePositiveUint(values["source_system_identifier"]); err != nil {
			return deny("journal_intent_source_invalid")
		}
		if _, err := parsePositiveUint(values["database_oid"]); err != nil {
			return deny("journal_intent_database_oid_invalid")
		}
	case "catalog":
		if prior.lastType != "intent" ||
			requireEpoch(values["observed_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["constraint_oid"] != "0" ||
			!hex64RE.MatchString(values["catalog_sha256"]) ||
			values["indexdef_sha256"] != prior.indexdefSHA ||
			values["predicate_sha256"] != prior.predicateSHA ||
			values["dependency_sha256"] != prior.dependencySHA ||
			values["classifier"] != "INVALID_EXACT" ||
			values["status"] != "catalog_fsynced" {
			return deny("journal_catalog_value_or_binding_invalid")
		}
		if _, err := parsePositiveUint(values["table_oid"]); err != nil {
			return deny("journal_catalog_table_oid_invalid")
		}
		if _, err := parsePositiveUint(values["index_oid"]); err != nil {
			return deny("journal_catalog_index_oid_invalid")
		}
	case "drop":
		if prior.lastType != "catalog" ||
			requireEpoch(values["dropped_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["index_oid"] != prior.indexOID ||
			values["catalog_sha256"] != prior.catalogSHA ||
			values["classifier"] != "REMOVED_EXACT" ||
			values["status"] != "drop_fsynced" {
			return deny("journal_drop_value_or_binding_invalid")
		}
	case "close":
		if (prior.lastType != "catalog" && prior.lastType != "drop") ||
			requireEpoch(values["closed_at_epoch"]) != nil ||
			values["journal_id"] != prior.journalID ||
			values["run_id"] != prior.runID ||
			values["candidate"] != prior.candidate ||
			values["former_index_oid"] != prior.indexOID ||
			values["classifier"] != "REMOVED_EXACT" ||
			values["status"] != "closed" {
			return deny("journal_close_value_or_binding_invalid")
		}
	default:
		return deny("journal_unknown_record")
	}
	return nil
}

func validateJournalName(basename string, parsed parsedJournal) error {
	match := journalRE.FindStringSubmatch(basename)
	if match == nil || match[1] != parsed.candidate || match[3] != parsed.journalID {
		return deny("journal_basename_identity_mismatch")
	}
	return nil
}
