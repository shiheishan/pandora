package evidencecodec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

type expectedVectors struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	EmptyHMAC      string `json:"empty_rowset_hmac"`
	BPChar         string `json:"bpchar_hex"`
	IPv4           string `json:"ipv4_hex"`
	IPv6           string `json:"ipv6_hex"`
	EmptyArray     string `json:"empty_array_hex"`
	TextArray      string `json:"text_array_hex"`
	JSONB          string `json:"jsonb_hex"`
	Row            string `json:"row_hex"`
	RowSHA         string `json:"row_sha256"`
	RowsetHMAC     string `json:"rowset_hmac"`
}

func vectorSpec() RelationSpec {
	return RelationSpec{Schema: "public", Name: "vector_evidence", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{
		scalar(1, "id", "uuid", TypeUUID, false, -1),
		scalar(2, "tenant_id", "uuid", TypeUUID, false, -1),
		scalar(3, "nullable_text", "text", TypeText, true, -1),
		scalar(4, "observed_at", "timestamptz", TypeTimestamptz, false, 6),
		scalar(5, "amount", "numeric", TypeNumeric, false, -1),
		scalar(6, "document", "jsonb", TypeJSONB, false, -1),
		scalar(7, "country", "bpchar", TypeBPChar, false, 6),
		array(8, "networks", "_inet", TypeInet, false),
		array(9, "labels", "_text", TypeText, false),
	}}
}

func vectorValues() (UUID, UUID, [32]byte, []Value) {
	var tenant, id UUID
	copy(tenant[:], mustHex("00112233445566778899aabbccddeeff"))
	copy(id[:], mustHex("102132435465768798a9babbdcddedef"))
	var key [32]byte
	copy(key[:], mustHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	document := JSONNode{Kind: JSONObject, Members: []JSONMember{
		{Key: "a", Value: JSONNode{Kind: JSONNumber, Number: "12.34"}},
		{Key: "z", Value: JSONNode{Kind: JSONArray, Elements: []JSONNode{{Kind: JSONNull}, {Kind: JSONTrue}, {Kind: JSONString, String: "中"}}}},
	}}
	values := []Value{
		{Tag: TypeUUID, UUID: id},
		{Tag: TypeUUID, UUID: tenant},
		{Tag: TypeText, Null: true},
		{Tag: TypeTimestamptz, Microseconds: -123456789},
		{Tag: TypeNumeric, Numeric: "12.34"},
		{Tag: TypeJSONB, JSON: document},
		{Tag: TypeBPChar, Text: "U "},
		{Tag: TypeArray, Array: &ArrayValue{ElementTag: TypeInet, ElementTypmod: -1, Dimensions: []Dimension{{Length: 2, LowerBound: 1}}, Elements: []Value{
			{Tag: TypeInet, Inet: InetValue{Family: 4, Prefix: 25, Address: mustHex("c0000281")}},
			{Tag: TypeInet, Inet: InetValue{Family: 6, Prefix: 65, Address: mustHex("20010db8000000008000000000000001")}},
		}}},
		{Tag: TypeArray, Array: &ArrayValue{ElementTag: TypeText, ElementTypmod: -1, Dimensions: []Dimension{{Length: 2, LowerBound: -2}}, Elements: []Value{{Tag: TypeText, Text: "alpha"}, {Tag: TypeText, Null: true}}}},
	}
	return tenant, id, key, values
}

func actualVectors(t *testing.T) expectedVectors {
	t.Helper()
	tenant, id, key, values := vectorValues()
	spec := vectorSpec()
	_, mh, err := EncodeRelationManifest([]RelationSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	bp, _ := EncodeScalar(spec.Columns[6], values[6])
	ip4, _ := EncodeScalar(ColumnSpec{Tag: TypeInet, Typmod: -1}, values[7].Array.Elements[0])
	ip6, _ := EncodeScalar(ColumnSpec{Tag: TypeInet, Typmod: -1}, values[7].Array.Elements[1])
	empty, err := EncodeArray(ArrayValue{ElementTag: TypeText, ElementTypmod: -1})
	if err != nil {
		t.Fatal(err)
	}
	ta, err := EncodeArray(*values[8].Array)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := EncodeJSONB(values[5].JSON)
	if err != nil {
		t.Fatal(err)
	}
	sk := SourceKeyForID(id)
	row, err := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values})
	if err != nil {
		t.Fatal(err)
	}
	rh := sha256.Sum256(row.Bytes)
	emptySet := NewRowSetBuilder(key, 0)
	_, eh, err := emptySet.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	set := NewRowSetBuilder(key, 1)
	if err = set.Add(row); err != nil {
		t.Fatal(err)
	}
	_, sh, err := set.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	return expectedVectors{hex.EncodeToString(mh[:]), hex.EncodeToString(eh[:]), hex.EncodeToString(bp), hex.EncodeToString(ip4), hex.EncodeToString(ip6), hex.EncodeToString(empty), hex.EncodeToString(ta), hex.EncodeToString(jb), hex.EncodeToString(row.Bytes), hex.EncodeToString(rh[:]), hex.EncodeToString(sh[:])}
}

func TestCommittedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/evidence_v1/expected_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var want expectedVectors
	if err = json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	got := actualVectors(t)
	if want.ManifestSHA256 == "" {
		b, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("expected vectors not pinned; actual=%s", b)
	}
	if got != want {
		t.Fatalf("vector drift\n got: %+v\nwant: %+v", got, want)
	}
}

func TestSchema00042AndFailures(t *testing.T) {
	relations := Schema00042()
	if len(relations) != 10 {
		t.Fatalf("relations=%d", len(relations))
	}
	wantCounts := []int{13, 4, 22, 10, 21, 10, 22, 16, 17, 21}
	for i, r := range relations {
		if len(r.Columns) != wantCounts[i] {
			t.Fatalf("%s columns=%d", r.Name, len(r.Columns))
		}
	}
	_, schemaHash, err := EncodeRelationManifest(relations)
	if err != nil {
		t.Fatal(err)
	}
	const expectedSchemaHash = "8b7d0fa9f055c329798e6af360fc3b47a345892257ded6070204f8f39dbc563c"
	if hex.EncodeToString(schemaHash[:]) != expectedSchemaHash {
		t.Fatalf("Schema00042 manifest drift: %x", schemaHash)
	}
	bad := append([]RelationSpec(nil), relations...)
	bad[0], bad[1] = bad[1], bad[0]
	if _, _, err := EncodeRelationManifest(bad); !errors.Is(err, ErrOrder) {
		t.Fatalf("order err=%v", err)
	}
}

func TestArrayAndRowsetRefusals(t *testing.T) {
	_, err := EncodeArray(ArrayValue{ElementTag: TypeText, ElementTypmod: -1, Dimensions: []Dimension{{Length: 2, LowerBound: 1}}, Elements: []Value{{Tag: TypeText, Text: "x"}}})
	if !errors.Is(err, ErrCountMismatch) {
		t.Fatalf("product err=%v", err)
	}
	_, err = EncodeArray(ArrayValue{ElementTag: TypeText, ElementTypmod: -1, Dimensions: []Dimension{{Length: 1, LowerBound: 1}}, Elements: []Value{{Tag: TypeInet}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("type err=%v", err)
	}
	tenant, id, key, values := vectorValues()
	spec := vectorSpec()
	sk := SourceKeyForID(id)
	row, err := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values})
	if err != nil {
		t.Fatal(err)
	}
	b := NewRowSetBuilder(key, 2)
	if err = b.Add(row); err != nil {
		t.Fatal(err)
	}
	if err = b.Add(row); !errors.Is(err, ErrOrder) {
		t.Fatalf("duplicate err=%v", err)
	}
	if _, _, err = b.Finalize(); !errors.Is(err, ErrCountMismatch) {
		t.Fatalf("count err=%v", err)
	}
	badKey := append([]byte(nil), sk[:15]...)
	if _, err = EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: badKey, Values: values}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source key err=%v", err)
	}
	bundleKey := SourceKeyForBundleNode(id, tenant)
	if len(bundleKey) != 32 || string(bundleKey[:16]) != string(id[:]) || string(bundleKey[16:]) != string(tenant[:]) {
		t.Fatal("bundle-node source key composition")
	}
	var mutated [32]byte
	_, original, err := NewRowSetBuilder(key, 0).Finalize()
	if err != nil {
		t.Fatal(err)
	}
	mutated = original
	mutated[0] ^= 1
	if VerifyDigest(original, mutated) {
		t.Fatal("mutated digest accepted")
	}
}

func TestNumericJSONAndInetRefusals(t *testing.T) {
	for _, v := range []string{".5", "1.", "+1", "01", "1.0", "NaN", "Infinity", "-0"} {
		if _, err := EncodeScalar(ColumnSpec{Tag: TypeNumeric, Typmod: -1}, Value{Tag: TypeNumeric, Numeric: v}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q accepted", v)
		}
	}
	// raw numeric typmod = 4 + (precision<<16) + scale.
	numeric42 := ColumnSpec{Tag: TypeNumeric, Typmod: 4 + (4 << 16) + 2}
	if _, err := EncodeScalar(numeric42, Value{Tag: TypeNumeric, Numeric: "12.34"}); err != nil {
		t.Fatalf("numeric(4,2) valid: %v", err)
	}
	if _, err := EncodeScalar(numeric42, Value{Tag: TypeNumeric, Numeric: "123.45"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("numeric(4,2) overflow err=%v", err)
	}
	tooLarge := "1" + strings.Repeat("0", 131072)
	if _, err := EncodeScalar(ColumnSpec{Tag: TypeNumeric, Typmod: -1}, Value{Tag: TypeNumeric, Numeric: tooLarge}); !errors.Is(err, ErrInvalid) {
		t.Fatal("PostgreSQL numeric integer limit not enforced")
	}
	if _, err := EncodeJSONB(JSONNode{Kind: JSONNumber, Number: tooLarge}); !errors.Is(err, ErrInvalid) {
		t.Fatal("JSONB numeric limit not enforced")
	}
	_, err := EncodeJSONB(JSONNode{Kind: JSONObject, Members: []JSONMember{{Key: "b", Value: JSONNode{Kind: JSONNull}}, {Key: "a", Value: JSONNode{Kind: JSONNull}}}})
	if !errors.Is(err, ErrOrder) {
		t.Fatalf("JSON order err=%v", err)
	}
	_, err = EncodeScalar(ColumnSpec{Tag: TypeInet, Typmod: -1}, Value{Tag: TypeInet, Inet: InetValue{Family: 4, Prefix: 33, Address: make([]byte, 4)}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("inet err=%v", err)
	}
	tooLong := vectorSpec()
	tooLong.Columns[0].Name = string(make([]byte, 65536))
	if _, _, err := EncodeRelationManifest([]RelationSpec{tooLong}); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("manifest overflow err=%v", err)
	}
	drift := vectorSpec()
	drift.Columns[3].Typmod = 3
	if _, _, err := EncodeRelationManifest([]RelationSpec{drift}); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("timestamp drift err=%v", err)
	}
}

func TestRemediationSecurityBoundaries(t *testing.T) {
	for _, v := range []Value{{Tag: TypeTimestamptz, Microseconds: math.MinInt64}, {Tag: TypeTimestamp, Microseconds: math.MaxInt64}} {
		if _, err := EncodeScalar(ColumnSpec{Tag: v.Tag, Typmod: -1}, v); !errors.Is(err, ErrInvalid) {
			t.Fatalf("infinity accepted: %v", err)
		}
	}
	if _, err := EncodeScalar(ColumnSpec{Tag: TypeDate, Typmod: -1}, Value{Tag: TypeDate, Days: math.MaxInt32}); !errors.Is(err, ErrInvalid) {
		t.Fatal("date infinity accepted")
	}
	if _, err := EncodeArray(ArrayValue{ElementTag: TypeText, ElementTypmod: -1, Dimensions: []Dimension{{Length: 0, LowerBound: 1}}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero dimension accepted")
	}

	tenant, id, key, values := vectorValues()
	spec := vectorSpec()
	sk := SourceKeyForID(id)
	row, err := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values})
	if err != nil {
		t.Fatal(err)
	}
	forged := row
	forged.Relation = "forged"
	b := NewRowSetBuilder(key, 1)
	if err = b.Add(forged); !errors.Is(err, ErrInvalid) {
		t.Fatalf("forged row accepted: %v", err)
	}
	badValues := append([]Value(nil), values...)
	badValues[1].UUID[0] ^= 1
	if _, err := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: badValues}); !errors.Is(err, ErrInvalid) {
		t.Fatal("tenant mismatch accepted")
	}
	badSpec := spec
	badSpec.Columns = append([]ColumnSpec(nil), spec.Columns...)
	badSpec.Columns[0].Base = TypeIdentity{"pg_catalog", "text"}
	if _, _, err := EncodeRelationManifest([]RelationSpec{badSpec}); !errors.Is(err, ErrSchemaDrift) {
		t.Fatal("base identity drift accepted")
	}

	_, mh, err := EncodeRelationManifest([]RelationSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, rh, err := BuildRowSnapshot(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values}, mh)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRowSnapshot(snapshot, spec, mh, rh); err != nil {
		t.Fatal(err)
	}
	dup := bytes.Replace(snapshot, []byte(`"codec_version":`), []byte(`"codec_version":"x","codec_version":`), 1)
	if err := VerifyRowSnapshot(dup, spec, mh, rh); !errors.Is(err, ErrInvalid) {
		t.Fatalf("snapshot duplicate accepted: %v", err)
	}
	badHex := bytes.Replace(snapshot, []byte(`"value_hex":"5520"`), []byte(`"value_hex":"ff"`), 1)
	if err := VerifyRowSnapshot(badHex, spec, mh, rh); !errors.Is(err, ErrInvalid) {
		t.Fatalf("noncanonical snapshot bytes accepted: %v", err)
	}
	if err := VerifyRowSnapshot([]byte(`{"x":`), spec, mh, rh); err == nil {
		t.Fatal("malformed snapshot accepted")
	}
	var typed RowSnapshot
	if err := json.Unmarshal(snapshot, &typed); err != nil {
		t.Fatal(err)
	}
	for i := range typed.Columns {
		if typed.Columns[i].Name == "networks" {
			h := *typed.Columns[i].ValueHex
			h = "05" + h[2:]
			typed.Columns[i].ValueHex = &h
		}
	}
	wrongArrayTag, _ := json.Marshal(typed)
	if err := VerifyRowSnapshot(wrongArrayTag, spec, mh, rh); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("_text/_uuid element tag drift accepted: %v", err)
	}

	relations := Schema00042()
	apiRow := makeSchemaRow(t, relations[0], tenant, id)
	tenant2 := tenant
	tenant2[0] = 1
	id3 := id
	id3[0] = 0x30
	deviceRow := makeSchemaRow(t, relations[6], tenant2, id3)
	sets := map[string][]EncodedRow{"before": {apiRow, deviceRow}, "retained": {apiRow}, "archived": {deviceRow}, "after": {apiRow}}
	cm, err := BuildClassificationManifest(key, "evidence-2026-01", relations, sets)
	if err != nil {
		t.Fatal(err)
	}
	cmJSON, _ := json.Marshal(cm)
	cmHash := sha256.Sum256(cmJSON)
	const twoRowClassificationSHA256 = "03bd63db897e1ea8d2c06281207972aac040ff45b6560fd407faecab28d3c158"
	if hex.EncodeToString(cmHash[:]) != twoRowClassificationSHA256 {
		t.Fatalf("two-row golden drift: %x", cmHash)
	}
	if err := VerifyClassificationManifest(cmJSON, key, "evidence-2026-01", relations, sets); err != nil {
		t.Fatal(err)
	}
	sets["before"] = []EncodedRow{deviceRow, apiRow}
	if _, err := BuildClassificationManifest(key, "evidence-2026-01", relations, sets); !errors.Is(err, ErrOrder) {
		t.Fatalf("full order not enforced: %v", err)
	}
	id4 := id
	id4[0] = 0x40
	apiWrongTenant := makeSchemaRow(t, relations[0], tenant2, id4)
	swapSets := map[string][]EncodedRow{"before": {apiRow, deviceRow}, "retained": {apiRow}, "archived": {deviceRow}, "after": {apiWrongTenant}}
	if _, err := BuildClassificationManifest(key, "evidence-2026-01", relations, swapSets); !errors.Is(err, ErrCountMismatch) {
		t.Fatalf("cross-tenant conservation swap accepted: %v", err)
	}
	// Balanced 2x2 cross-swap: all global, relation, and tenant marginals
	// remain equal, but the (relation, tenant) joint cells are wrong.
	id5, id6 := id, id
	id5[0], id6[0] = 0x50, 0x60
	apiTenant2 := makeSchemaRow(t, relations[0], tenant2, id5)
	deviceTenant1 := makeSchemaRow(t, relations[6], tenant, id6)
	balancedSwap := map[string][]EncodedRow{
		"before":   {apiRow, apiTenant2, deviceTenant1, deviceRow},
		"retained": {apiRow, deviceRow},
		"archived": {apiTenant2, deviceTenant1},
		"after":    {apiTenant2, deviceTenant1},
	}
	if _, err := BuildClassificationManifest(key, "evidence-2026-01", relations, balancedSwap); !errors.Is(err, ErrCountMismatch) {
		t.Fatalf("balanced relation-tenant cross-swap accepted: %v", err)
	}
	delete(sets, "archived")
	if _, err := BuildClassificationManifest(key, "evidence-2026-01", relations, sets); !errors.Is(err, ErrSchemaDrift) {
		t.Fatal("missing set accepted")
	}

	var artifactKey, evidenceKey [32]byte
	copy(artifactKey[:], mustHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"))
	for i := range evidenceKey {
		evidenceKey[i] = 0xa5
	}
	ad, err := ComputeSeparatedArtifactDigest("artifact-2026-01", artifactKey[:], "evidence-2026-01", evidenceKey[:], ArtifactHMACVersion, ArtifactFormat, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(ad.HMAC[:]) != "1a8a9a2c4ee75fd679467f61edd8fd00918dcb2548af0fd6ec8c85bae2bf559f" {
		t.Fatalf("artifact empty vector %x", ad.HMAC)
	}
	ad, err = ComputeSeparatedArtifactDigest("artifact-2026-01", artifactKey[:], "evidence-2026-01", evidenceKey[:], ArtifactHMACVersion, ArtifactFormat, []byte("{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(ad.HMAC[:]) != "dedbfc2835f734833e5cfc83a7bb190df0e18a35f96c4e81bf786383bffd6eba" {
		t.Fatalf("artifact LF vector %x", ad.HMAC)
	}
	if _, err := ComputeSeparatedArtifactDigest("same", artifactKey[:], "same", evidenceKey[:], ArtifactHMACVersion, ArtifactFormat, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("same key id accepted")
	}
	if _, err := ComputeSeparatedArtifactDigest("artifact-2026-01", artifactKey[:31], "evidence-2026-01", evidenceKey[:], ArtifactHMACVersion, ArtifactFormat, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("short artifact key accepted")
	}
	msg, _ := ArtifactHMACMessage("artifact-2026-01", ArtifactHMACVersion, ArtifactFormat, []byte("{}\n"))
	const objectMessage = "70616e646f72612d636c69656e742d617574682d30303034342d636c617373696669636174696f6e2d61727469666163742d686d61632d763100001061727469666163742d323032362d3031002470616e646f72612d6465766963652d6b65792d636c617373696669636174696f6e2d763100000000000000037b7d0a"
	if hex.EncodeToString(msg) != objectMessage {
		t.Fatal("artifact message vector drift")
	}
	enumDomain := ColumnSpec{Attnum: 1, Name: "e", Declared: TypeIdentity{"app", "d"}, Base: TypeIdentity{"app", "e"}, Tag: TypeEnum, Typmod: -1, ArrayElementTypmod: -1}
	if err := ValidateRelation(RelationSpec{Schema: "public", Name: "d", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{enumDomain}}); err != nil {
		t.Fatalf("domain over enum denied: %v", err)
	}
	arrayDomain := array(1, "a", "_text", TypeText, false)
	arrayDomain.Declared = TypeIdentity{"app", "d_text_array"}
	if err := ValidateRelation(RelationSpec{Schema: "public", Name: "d", SourceKeyKind: SourceKeyID, Columns: []ColumnSpec{arrayDomain}}); err != nil {
		t.Fatalf("domain over array denied: %v", err)
	}
}

func TestRowSetTupleOrderBranches(t *testing.T) {
	_, _, key, _ := vectorValues()
	var tenant1, tenant2, id1, id2 UUID
	tenant1[15] = 1
	tenant2[15] = 2
	id1[15] = 1
	id2[15] = 2
	base := vectorSpec()
	cases := []struct {
		name string
		a, b EncodedRow
	}{
		{"schema", makeVectorRow(t, withIdentity(base, "aaa", "r"), tenant1, id1), makeVectorRow(t, withIdentity(base, "bbb", "r"), tenant1, id1)},
		{"relation", makeVectorRow(t, withIdentity(base, "public", "a"), tenant1, id1), makeVectorRow(t, withIdentity(base, "public", "b"), tenant1, id1)},
		{"tenant", makeVectorRow(t, base, tenant1, id1), makeVectorRow(t, base, tenant2, id1)},
		{"source", makeVectorRow(t, base, tenant1, id1), makeVectorRow(t, base, tenant1, id2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok := NewRowSetBuilder(key, 2)
			if err := ok.Add(tc.a); err != nil {
				t.Fatal(err)
			}
			if err := ok.Add(tc.b); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ok.Finalize(); err != nil {
				t.Fatal(err)
			}
			bad := NewRowSetBuilder(key, 2)
			if err := bad.Add(tc.b); err != nil {
				t.Fatal(err)
			}
			if err := bad.Add(tc.a); !errors.Is(err, ErrOrder) {
				t.Fatalf("reverse accepted: %v", err)
			}
		})
	}
}
func withIdentity(spec RelationSpec, schema, name string) RelationSpec {
	spec.Schema = schema
	spec.Name = name
	return spec
}
func makeVectorRow(t *testing.T, spec RelationSpec, tenant, id UUID) EncodedRow {
	t.Helper()
	_, _, _, values := vectorValues()
	values[0].UUID = id
	values[1].UUID = tenant
	sk := SourceKeyForID(id)
	r, e := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func makeSchemaRow(t *testing.T, spec RelationSpec, tenant, id UUID) EncodedRow {
	t.Helper()
	values := make([]Value, len(spec.Columns))
	for i, c := range spec.Columns {
		v := Value{Tag: c.Tag}
		switch c.Tag {
		case TypeUUID:
			v.UUID = id
		case TypeText, TypeEnum, TypeBPChar:
			v.Text = ""
		case TypeBytea:
			v.Bytes = []byte{}
		case TypeJSONB:
			v.JSON = JSONNode{Kind: JSONNull}
		case TypeArray:
			v.Array = &ArrayValue{ElementTag: c.ArrayElementTag, ElementTypmod: c.ArrayElementTypmod}
		case TypeInet:
			v.Inet = InetValue{Family: 4, Prefix: 32, Address: []byte{127, 0, 0, 1}}
		}
		if c.Name == "tenant_id" {
			v.UUID = tenant
		}
		if c.Name == "id" {
			v.UUID = id
		}
		values[i] = v
	}
	sk := SourceKeyForID(id)
	row, err := EncodeRow(Row{Spec: spec, Tenant: tenant, SourceKey: sk[:], Values: values})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func mustHex(value string) []byte {
	out, err := hex.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return out
}
