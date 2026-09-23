package evidencecodec

func pgType(name string) TypeIdentity { return TypeIdentity{Schema: "pg_catalog", Name: name} }

func scalar(att uint16, name, typ string, tag TypeTag, nullable bool, typmod int32) ColumnSpec {
	id := pgType(typ)
	return ColumnSpec{Attnum: att, Name: name, Declared: id, Base: id, Tag: tag, Nullable: nullable, Typmod: typmod, ArrayElementTypmod: -1}
}

func array(att uint16, name, typ string, element TypeTag, nullable bool) ColumnSpec {
	id := pgType(typ)
	return ColumnSpec{Attnum: att, Name: name, Declared: id, Base: id, Tag: TypeArray, Nullable: nullable, Typmod: -1, ArrayElementTag: element, ArrayElementTypmod: -1}
}

// Schema00042 is the exact positive-attnum manifest after migration 00042,
// including columns added by 00018 and 00019. The returned slice is a deep
// copy and is already in canonical schema/relation byte order.
func Schema00042() []RelationSpec {
	relations := []RelationSpec{
		{Schema: "public", Name: "api_tokens", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "user_id", "uuid", TypeUUID, true, -1), scalar(4, "name", "text", TypeText, false, -1),
			scalar(5, "token_hash", "bytea", TypeBytea, false, -1), scalar(6, "token_prefix", "text", TypeText, false, -1),
			scalar(7, "audience", "text", TypeText, false, -1), array(8, "scopes", "_text", TypeText, false),
			array(9, "allowed_cidrs", "_inet", TypeInet, true), scalar(10, "last_used_at", "timestamptz", TypeTimestamptz, true, -1),
			scalar(11, "expires_at", "timestamptz", TypeTimestamptz, true, -1), scalar(12, "revoked_at", "timestamptz", TypeTimestamptz, true, -1),
			scalar(13, "created_at", "timestamptz", TypeTimestamptz, false, -1),
		}},
		{Schema: "public", Name: "config_bundle_nodes", SourceKeyKind: SourceKeyBundleNode, Columns: []ColumnSpec{
			scalar(1, "tenant_id", "uuid", TypeUUID, false, -1), scalar(2, "bundle_id", "uuid", TypeUUID, false, -1),
			scalar(3, "node_id", "uuid", TypeUUID, false, -1), scalar(4, "created_at", "timestamptz", TypeTimestamptz, false, 6),
		}},
		{Schema: "public", Name: "config_bundles", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "subscription_id", "uuid", TypeUUID, false, -1), scalar(4, "credential_id", "uuid", TypeUUID, true, -1),
			scalar(5, "device_id", "uuid", TypeUUID, true, -1), scalar(6, "adapter", "text", TypeText, false, -1),
			scalar(7, "schema_version", "text", TypeText, false, -1), scalar(8, "version", "int4", TypeInt4, false, -1),
			scalar(9, "payload", "bytea", TypeBytea, false, -1), scalar(10, "content_hash", "bytea", TypeBytea, false, -1),
			scalar(11, "signature", "bytea", TypeBytea, false, -1), scalar(12, "signing_key_id", "text", TypeText, false, -1),
			scalar(13, "etag", "text", TypeText, false, -1), scalar(14, "min_client_version", "text", TypeText, true, -1),
			scalar(15, "decision_input", "jsonb", TypeJSONB, false, -1), scalar(16, "policy_version", "text", TypeText, true, -1),
			array(17, "node_ids", "_uuid", TypeUUID, false), scalar(18, "status", "text", TypeText, false, -1),
			scalar(19, "issued_at", "timestamptz", TypeTimestamptz, false, -1), scalar(20, "expires_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(21, "user_id", "uuid", TypeUUID, true, -1), scalar(22, "revoked_at", "timestamptz", TypeTimestamptz, true, 6),
		}},
		{Schema: "public", Name: "credential_access_log", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "credential_id", "uuid", TypeUUID, false, -1), scalar(4, "ip_hash", "bytea", TypeBytea, true, -1),
			scalar(5, "ip_asn", "int4", TypeInt4, true, -1), scalar(6, "user_agent_hash", "bytea", TypeBytea, true, -1),
			scalar(7, "client_kind", "text", TypeText, true, -1), scalar(8, "outcome", "text", TypeText, false, -1),
			scalar(9, "config_bundle_id", "uuid", TypeUUID, true, -1), scalar(10, "occurred_at", "timestamptz", TypeTimestamptz, false, -1),
		}},
		{Schema: "public", Name: "device_authorizations", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "user_code_hash", "bytea", TypeBytea, false, -1), scalar(4, "device_code_hash", "bytea", TypeBytea, false, -1),
			scalar(5, "device_public_key", "bytea", TypeBytea, false, -1), scalar(6, "platform", "text", TypeText, false, -1),
			scalar(7, "user_id", "uuid", TypeUUID, true, -1), scalar(8, "device_id", "uuid", TypeUUID, true, -1),
			scalar(9, "status", "text", TypeText, false, -1), scalar(10, "poll_interval_seconds", "int2", TypeInt2, false, -1),
			scalar(11, "last_polled_at", "timestamptz", TypeTimestamptz, true, -1), scalar(12, "expires_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(13, "approved_at", "timestamptz", TypeTimestamptz, true, -1), scalar(14, "created_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(15, "key_algorithm", "text", TypeText, true, -1), scalar(16, "public_key_spki", "bytea", TypeBytea, true, -1),
			scalar(17, "key_fingerprint", "bytea", TypeBytea, true, -1), scalar(18, "user_code_mac", "bytea", TypeBytea, true, -1),
			scalar(19, "denied_at", "timestamptz", TypeTimestamptz, true, 6), scalar(20, "consumed_at", "timestamptz", TypeTimestamptz, true, 6),
			scalar(21, "poll_count", "int4", TypeInt4, true, -1),
		}},
		{Schema: "public", Name: "device_tokens", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "device_id", "uuid", TypeUUID, false, -1), scalar(4, "user_id", "uuid", TypeUUID, false, -1),
			scalar(5, "token_hash", "bytea", TypeBytea, false, -1), scalar(6, "kind", "text", TypeText, false, -1),
			scalar(7, "status", "text", TypeText, false, -1), scalar(8, "replaced_by", "uuid", TypeUUID, true, -1),
			scalar(9, "expires_at", "timestamptz", TypeTimestamptz, false, -1), scalar(10, "created_at", "timestamptz", TypeTimestamptz, false, -1),
		}},
		{Schema: "public", Name: "devices", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "user_id", "uuid", TypeUUID, false, -1), scalar(4, "public_key", "bytea", TypeBytea, false, -1),
			scalar(5, "key_algorithm", "text", TypeText, false, -1), scalar(6, "platform", "text", TypeText, false, -1),
			scalar(7, "arch", "text", TypeText, true, -1), scalar(8, "os_version", "text", TypeText, true, -1),
			scalar(9, "app_version", "text", TypeText, true, -1), array(10, "config_schemas", "_text", TypeText, false),
			scalar(11, "update_channel", "text", TypeText, false, -1), scalar(12, "friendly_name", "text", TypeText, true, -1),
			scalar(13, "status", "text", TypeText, false, -1), scalar(14, "released_at", "timestamptz", TypeTimestamptz, true, -1),
			scalar(15, "revoked_at", "timestamptz", TypeTimestamptz, true, -1), scalar(16, "revoked_reason", "text", TypeText, true, -1),
			scalar(17, "last_seen_at", "timestamptz", TypeTimestamptz, true, -1), scalar(18, "first_seen_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(19, "created_at", "timestamptz", TypeTimestamptz, false, -1), scalar(20, "updated_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(21, "public_key_spki", "bytea", TypeBytea, true, -1), scalar(22, "key_fingerprint", "bytea", TypeBytea, true, -1),
		}},
		{Schema: "public", Name: "refresh_tokens", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "session_id", "uuid", TypeUUID, false, -1), scalar(4, "user_id", "uuid", TypeUUID, false, -1),
			scalar(5, "token_hash", "bytea", TypeBytea, false, -1), scalar(6, "status", "text", TypeText, false, -1),
			scalar(7, "replaced_by", "uuid", TypeUUID, true, -1), scalar(8, "expires_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(9, "used_at", "timestamptz", TypeTimestamptz, true, -1), scalar(10, "created_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(11, "authority", "text", TypeText, true, -1), scalar(12, "family_id", "uuid", TypeUUID, true, -1),
			scalar(13, "device_id", "uuid", TypeUUID, true, -1), scalar(14, "generation", "int4", TypeInt4, true, -1),
			scalar(15, "parent_id", "uuid", TypeUUID, true, -1), scalar(16, "key_fingerprint", "bytea", TypeBytea, true, -1),
		}},
		{Schema: "public", Name: "sessions", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "user_id", "uuid", TypeUUID, false, -1), scalar(4, "audience", "text", TypeText, false, -1),
			scalar(5, "device_id", "uuid", TypeUUID, true, -1), scalar(6, "user_agent", "text", TypeText, true, -1),
			scalar(7, "ip_hash", "bytea", TypeBytea, true, -1), scalar(8, "ip_asn", "int4", TypeInt4, true, -1),
			scalar(9, "ip_country", "bpchar", TypeBPChar, true, 6), array(10, "auth_methods", "_text", TypeText, false),
			scalar(11, "last_reauth_at", "timestamptz", TypeTimestamptz, true, -1), scalar(12, "risk_score", "int2", TypeInt2, false, -1),
			scalar(13, "revoked_at", "timestamptz", TypeTimestamptz, true, -1), scalar(14, "revoked_reason", "text", TypeText, true, -1),
			scalar(15, "expires_at", "timestamptz", TypeTimestamptz, false, -1), scalar(16, "last_seen_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(17, "created_at", "timestamptz", TypeTimestamptz, false, -1),
		}},
		{Schema: "public", Name: "subscription_credentials", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
			scalar(1, "id", "uuid", TypeUUID, false, -1), scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
			scalar(3, "subscription_id", "uuid", TypeUUID, false, -1), scalar(4, "user_id", "uuid", TypeUUID, false, -1),
			scalar(5, "token_hash", "bytea", TypeBytea, false, -1), scalar(6, "token_prefix", "text", TypeText, false, -1),
			scalar(7, "scope", "text", TypeText, false, -1), scalar(8, "device_id", "uuid", TypeUUID, true, -1),
			scalar(9, "rate_limit_per_hour", "int4", TypeInt4, false, -1), scalar(10, "fetch_count", "int8", TypeInt8, false, -1),
			scalar(11, "last_fetched_at", "timestamptz", TypeTimestamptz, true, -1), scalar(12, "last_fetch_ip_hash", "bytea", TypeBytea, true, -1),
			scalar(13, "status", "text", TypeText, false, -1), scalar(14, "revoked_at", "timestamptz", TypeTimestamptz, true, -1),
			scalar(15, "revoked_reason", "text", TypeText, true, -1), scalar(16, "grace_until", "timestamptz", TypeTimestamptz, true, -1),
			scalar(17, "expires_at", "timestamptz", TypeTimestamptz, true, -1), scalar(18, "created_at", "timestamptz", TypeTimestamptz, false, -1),
			scalar(19, "rotated_count", "int4", TypeInt4, false, -1), scalar(20, "rotated_at", "timestamptz", TypeTimestamptz, true, -1),
			scalar(21, "token_encrypted", "bytea", TypeBytea, true, -1),
		}},
	}
	out := make([]RelationSpec, len(relations))
	for i := range relations {
		out[i] = relations[i]
		out[i].Columns = append([]ColumnSpec(nil), relations[i].Columns...)
	}
	return out
}
