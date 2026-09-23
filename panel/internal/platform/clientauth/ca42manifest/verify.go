package ca42manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	Format                = "client-auth-00042-external-manifest-v1"
	ObjectFormat          = "client-auth-00042-object-manifest-v1"
	CatalogFormat         = "client-auth-00042-catalog-v1"
	FrozenMigrationSHA256 = "ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5"
	ContractSHA256        = "4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E"
	Barrier               = "exclusive-shared-and-local-catalog-lock-v1"
	MaxManifestBytes      = 4 << 20
	maxJSONDepth          = 32
)

var (
	lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	runID      = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
	imageID    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Receipt struct {
	ReceiptFormat             string `json:"receipt_format"`
	Decision                  string `json:"decision"`
	Authorization             string `json:"authorization"`
	ExternalManifestSHA256    string `json:"external_manifest_sha256"`
	ExactObjectManifestSHA256 string `json:"exact_object_manifest_sha256"`
	ContractSHA256            string `json:"contract_sha256"`
	IsolatedSystemIdentifier  string `json:"isolated_system_identifier"`
	IsolatedContainerID       string `json:"isolated_container_id"`
	IsolatedNetworkID         string `json:"isolated_network_id"`
	IsolatedDatabase          string `json:"isolated_database"`
	IsolatedDatabaseOID       string `json:"isolated_database_oid"`
	IsolatedImageID           string `json:"isolated_image_id"`
	IsolatedRunID             string `json:"isolated_run_id"`
}

type source struct {
	SystemIdentifier string `json:"system_identifier"`
	ContainerID      string `json:"container_id"`
	NetworkID        string `json:"network_id"`
	Database         string `json:"database"`
	DatabaseOID      string `json:"database_oid"`
	ImageID          string `json:"image_id"`
	RunID            string `json:"run_id"`
}

type envelope struct {
	Format                    string          `json:"format"`
	Status                    string          `json:"status"`
	FrozenMigrationSHA256     string          `json:"frozen_migration_sha256"`
	PostgreSQLMajor           int             `json:"postgresql_major"`
	Barrier                   string          `json:"barrier"`
	ExactObjectManifestSHA256 string          `json:"exact_object_manifest_sha256"`
	Source                    source          `json:"source"`
	ObjectManifest            json.RawMessage `json:"object_manifest"`
}

type objectManifest struct {
	Format                  string          `json:"format"`
	ContractSHA256          string          `json:"contract_sha256"`
	RoleCreationMask        int             `json:"role_creation_mask"`
	RoleOIDManifest         json.RawMessage `json:"role_oid_manifest"`
	PortableCatalogManifest json.RawMessage `json:"portable_catalog_manifest"`
	SourceObjectProvenance  json.RawMessage `json:"source_object_provenance"`
}

type catalog struct {
	Format    string            `json:"format"`
	Relations []json.RawMessage `json:"relations"`
}

type relation struct {
	Schema          string          `json:"schema"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Persistence     string          `json:"persistence"`
	Owner           string          `json:"owner"`
	RLS             bool            `json:"rls"`
	ForceRLS        bool            `json:"force_rls"`
	ReplicaIdentity string          `json:"replica_identity"`
	Comment         *string         `json:"comment"`
	Columns         json.RawMessage `json:"columns"`
	Constraints     json.RawMessage `json:"constraints"`
	Indexes         json.RawMessage `json:"indexes"`
	Policies        json.RawMessage `json:"policies"`
	Triggers        json.RawMessage `json:"triggers"`
	ACL             json.RawMessage `json:"acl"`
	Sequence        json.RawMessage `json:"sequence"`
}

var expectedRelations = map[string]struct{}{
	"app.client_auth_00042_meta": {},
	"public.devices":             {}, "public.device_authorizations": {},
	"public.config_bundles": {}, "public.refresh_tokens": {},
	"public.config_bundle_nodes": {}, "public.refresh_families": {},
	"public.device_proof_nonces": {}, "public.client_access_token_jtis": {},
	"public.device_issuance_response_replays": {}, "public.client_refresh_response_replays": {},
	"public.device_issuance_replay_uses": {}, "public.client_refresh_replay_uses": {},
	"public.device_proof_nonces_id_seq": {}, "public.client_access_token_jtis_id_seq": {},
}

func Verify(data []byte) (Receipt, error) {
	var zero Receipt
	if len(data) == 0 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' || bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return zero, errors.New("invalid manifest byte envelope")
	}
	body := data[:len(data)-1]
	if err := rejectDuplicateKeys(body); err != nil {
		return zero, err
	}
	outer, err := exactObject(body, []string{"format", "status", "frozen_migration_sha256", "postgresql_major", "barrier", "exact_object_manifest_sha256", "source", "object_manifest"})
	if err != nil {
		return zero, fmt.Errorf("external manifest schema: %w", err)
	}
	var env envelope
	if err := strictDecode(body, &env); err != nil {
		return zero, fmt.Errorf("external manifest: %w", err)
	}
	if env.Format != Format || env.Status != "READY" || env.FrozenMigrationSHA256 != FrozenMigrationSHA256 || env.PostgreSQLMajor != 18 || env.Barrier != Barrier || !lowerHex64.MatchString(env.ExactObjectManifestSHA256) {
		return zero, errors.New("external manifest constants mismatch")
	}
	if _, err := exactObject(outer["source"], []string{"system_identifier", "container_id", "network_id", "database", "database_oid", "image_id", "run_id"}); err != nil {
		return zero, fmt.Errorf("isolated source schema: %w", err)
	}
	if !canonicalPositiveDecimal(env.Source.SystemIdentifier) || !canonicalOID(env.Source.DatabaseOID) || !lowerHex64.MatchString(env.Source.ContainerID) || !lowerHex64.MatchString(env.Source.NetworkID) || !identifier.MatchString(env.Source.Database) || !imageID.MatchString(env.Source.ImageID) || !runID.MatchString(env.Source.RunID) {
		return zero, errors.New("isolated source identity invalid")
	}
	objectRaw := bytes.TrimSpace(outer["object_manifest"])
	objectDigest := sha256.Sum256(objectRaw)
	objectHex := hex.EncodeToString(objectDigest[:])
	if objectHex != env.ExactObjectManifestSHA256 {
		return zero, errors.New("exact object manifest digest mismatch")
	}
	objectMap, err := exactObject(objectRaw, []string{"format", "contract_sha256", "role_creation_mask", "role_oid_manifest", "portable_catalog_manifest", "source_object_provenance"})
	if err != nil {
		return zero, fmt.Errorf("object manifest schema: %w", err)
	}
	var object objectManifest
	if err := strictDecode(objectRaw, &object); err != nil {
		return zero, err
	}
	if object.Format != ObjectFormat || object.ContractSHA256 != ContractSHA256 || object.RoleCreationMask < 0 || object.RoleCreationMask > 15 {
		return zero, errors.New("object manifest constants mismatch")
	}
	if err := validateRoleOIDs(objectMap["role_oid_manifest"]); err != nil {
		return zero, err
	}
	if err := validateProvenance(objectMap["source_object_provenance"]); err != nil {
		return zero, err
	}
	if err := validateCatalog(objectMap["portable_catalog_manifest"]); err != nil {
		return zero, err
	}
	manifestDigest := sha256.Sum256(data)
	return Receipt{
		ReceiptFormat: "client-auth-00042-external-manifest-structural-receipt-v1", Decision: "STRUCTURALLY_VALID", Authorization: "NONE",
		ExternalManifestSHA256: hex.EncodeToString(manifestDigest[:]), ExactObjectManifestSHA256: objectHex,
		ContractSHA256: ContractSHA256, IsolatedSystemIdentifier: env.Source.SystemIdentifier,
		IsolatedContainerID: env.Source.ContainerID, IsolatedNetworkID: env.Source.NetworkID,
		IsolatedDatabase: env.Source.Database, IsolatedDatabaseOID: env.Source.DatabaseOID,
		IsolatedImageID: env.Source.ImageID, IsolatedRunID: env.Source.RunID,
	}, nil
}

func validateRoleOIDs(raw []byte) error {
	keys := []string{"aegis_client_auth_owner", "aegis_client_auth_gc_owner", "aegis_client_auth_gc", "aegis_client_keyring_preflight_owner"}
	values, err := exactObject(raw, keys)
	if err != nil {
		return fmt.Errorf("role OID manifest: %w", err)
	}
	seen := map[string]bool{}
	for _, key := range keys {
		var number json.Number
		if err := strictDecode(values[key], &number); err != nil || !canonicalOID(string(number)) || seen[string(number)] {
			return errors.New("role OID manifest invalid")
		}
		seen[string(number)] = true
	}
	return nil
}

func validateProvenance(raw []byte) error {
	keys := []string{"namespaces", "relations", "attributes", "constraints", "indexes", "policies", "triggers", "roles", "role_descriptions"}
	values, err := exactObject(raw, keys)
	if err != nil {
		return fmt.Errorf("source provenance: %w", err)
	}
	limits := map[string]int{"namespaces": 2, "relations": 15, "attributes": 1024, "constraints": 256, "indexes": 256, "policies": 128, "triggers": 128, "roles": 4, "role_descriptions": 4}
	widths := map[string]int{"namespaces": 5, "relations": 9, "attributes": 5, "constraints": 4, "indexes": 4, "policies": 4, "triggers": 4, "roles": 13, "role_descriptions": 4}
	for _, key := range keys {
		var array []json.RawMessage
		if err := strictDecode(values[key], &array); err != nil || array == nil || len(array) > limits[key] {
			return fmt.Errorf("source provenance %s is not an array", key)
		}
		if (key == "namespaces" || key == "relations" || key == "roles") && len(array) != limits[key] {
			return fmt.Errorf("source provenance %s cardinality invalid", key)
		}
		if err := validateTupleArray(array, widths[key]); err != nil {
			return fmt.Errorf("source provenance %s: %w", key, err)
		}
	}
	return nil
}

func validateCatalog(raw []byte) error {
	values, err := exactObject(raw, []string{"format", "relations"})
	if err != nil {
		return fmt.Errorf("portable catalog: %w", err)
	}
	var value catalog
	if err := strictDecode(raw, &value); err != nil || value.Format != CatalogFormat || len(value.Relations) != len(expectedRelations) {
		return errors.New("portable catalog envelope invalid")
	}
	seen := map[string]bool{}
	relationKeys := []string{"schema", "name", "kind", "persistence", "owner", "rls", "force_rls", "replica_identity", "comment", "columns", "constraints", "indexes", "policies", "triggers", "acl", "sequence"}
	for _, item := range value.Relations {
		fields, err := exactObject(item, relationKeys)
		if err != nil {
			return fmt.Errorf("portable catalog relation: %w", err)
		}
		var rel relation
		if err := strictDecode(item, &rel); err != nil {
			return err
		}
		identity := rel.Schema + "." + rel.Name
		if _, ok := expectedRelations[identity]; !ok || seen[identity] || !identifier.MatchString(rel.Owner) || rel.Kind == "" || rel.Persistence == "" || rel.ReplicaIdentity == "" {
			return errors.New("portable catalog relation identity invalid")
		}
		seen[identity] = true
		relationLimits := map[string]int{"columns": 256, "constraints": 128, "indexes": 128, "policies": 64, "triggers": 64, "acl": 256}
		relationWidths := map[string]int{"columns": 11, "constraints": 7, "indexes": 9, "policies": 6, "triggers": 4, "acl": 4}
		for _, arrayKey := range []string{"columns", "constraints", "indexes", "policies", "triggers", "acl"} {
			var array []json.RawMessage
			if err := strictDecode(fields[arrayKey], &array); err != nil || array == nil || len(array) > relationLimits[arrayKey] {
				return fmt.Errorf("portable catalog %s invalid", arrayKey)
			}
			if err := validateTupleArray(array, relationWidths[arrayKey]); err != nil {
				return fmt.Errorf("portable catalog %s: %w", arrayKey, err)
			}
		}
		trimmedSequence := bytes.TrimSpace(fields["sequence"])
		if !bytes.Equal(trimmedSequence, []byte("null")) {
			var sequence []json.RawMessage
			if err := strictDecode(trimmedSequence, &sequence); err != nil || len(sequence) != 7 {
				return errors.New("portable catalog sequence invalid")
			}
			var dependencies []json.RawMessage
			if err := strictDecode(sequence[6], &dependencies); err != nil || len(dependencies) > 16 || validateTupleArray(dependencies, 4) != nil {
				return errors.New("portable catalog sequence dependencies invalid")
			}
		}
	}
	_ = values
	return nil
}

func exactObject(raw []byte, expected []string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := strictDecode(raw, &value); err != nil || value == nil {
		return nil, errors.New("expected JSON object")
	}
	if len(value) != len(expected) {
		return nil, errors.New("object field count mismatch")
	}
	want := append([]string(nil), expected...)
	sort.Strings(want)
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			return nil, errors.New("unknown or missing object field")
		}
	}
	return value, nil
}

func strictDecode(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errors.New("invalid JSON schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0
}

func canonicalOID(value string) bool {
	if !canonicalPositiveDecimal(value) {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	return err == nil && parsed > 0
}

func validateTupleArray(values []json.RawMessage, width int) error {
	for _, raw := range values {
		var tuple []json.RawMessage
		if err := strictDecode(raw, &tuple); err != nil || len(tuple) != width {
			return errors.New("tuple width invalid")
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > maxJSONDepth {
			return errors.New("JSON nesting too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid JSON")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return errors.New("invalid JSON key")
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON key")
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}
