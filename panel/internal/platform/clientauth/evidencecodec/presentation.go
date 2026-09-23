package evidencecodec

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"unicode/utf8"
)

const (
	ClassificationRuleVersion = "pandora-client-auth-00044-classification-v1"
	ArtifactHMACVersion       = "pandora-client-auth-00044-classification-artifact-hmac-v1"
	ArtifactFormat            = "pandora-device-key-classification-v1"
	artifactDomain            = ArtifactHMACVersion + "\x00"
	tenantRefDomain           = "pandora-client-auth-00044-tenant-ref-v1\x00"
)

var keyIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type SnapshotColumn struct {
	Attnum   uint16  `json:"attnum"`
	Name     string  `json:"name"`
	TypeTag  TypeTag `json:"type_tag"`
	Typmod   int32   `json:"typmod"`
	Nullable bool    `json:"nullable"`
	IsNull   bool    `json:"is_null"`
	ValueHex *string `json:"value_hex"`
}
type RowSnapshot struct {
	CodecVersion           string           `json:"codec_version"`
	RelationManifestSHA256 string           `json:"relation_manifest_sha256"`
	Schema                 string           `json:"schema"`
	Relation               string           `json:"relation"`
	Columns                []SnapshotColumn `json:"columns"`
}

func BuildRowSnapshot(row Row, manifestHash [32]byte) ([]byte, [32]byte, error) {
	encoded, err := EncodeRow(row)
	if err != nil {
		return nil, [32]byte{}, err
	}
	s := RowSnapshot{CodecVersion: CodecVersion, RelationManifestSHA256: hex.EncodeToString(manifestHash[:]), Schema: row.Spec.Schema, Relation: row.Spec.Name}
	for i, c := range row.Spec.Columns {
		sc := SnapshotColumn{Attnum: c.Attnum, Name: c.Name, TypeTag: c.Tag, Typmod: c.Typmod, Nullable: c.Nullable, IsNull: row.Values[i].Null}
		if !sc.IsNull {
			var raw []byte
			switch c.Tag {
			case TypeJSONB:
				raw, err = EncodeJSONB(row.Values[i].JSON)
			case TypeArray:
				raw, err = EncodeArray(*row.Values[i].Array)
			default:
				raw, err = EncodeScalar(c, row.Values[i])
			}
			if err != nil {
				return nil, [32]byte{}, err
			}
			h := hex.EncodeToString(raw)
			sc.ValueHex = &h
		}
		s.Columns = append(s.Columns, sc)
	}
	out, err := json.Marshal(s)
	return out, sha256.Sum256(encoded.Bytes), err
}

func VerifyRowSnapshot(data []byte, spec RelationSpec, manifestHash, rowHash [32]byte) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	var s RowSnapshot
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return fmt.Errorf("%w: snapshot JSON", ErrInvalid)
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("%w: trailing snapshot JSON", ErrInvalid)
	}
	if s.CodecVersion != CodecVersion || s.RelationManifestSHA256 != hex.EncodeToString(manifestHash[:]) || s.Schema != spec.Schema || s.Relation != spec.Name || len(s.Columns) != len(spec.Columns) {
		return fmt.Errorf("%w: snapshot header", ErrSchemaDrift)
	}
	var b bytes.Buffer
	b.WriteString(rowDomain)
	writeU16String(&b, spec.Schema)
	writeU16String(&b, spec.Name)
	writeU32(&b, uint32(len(spec.Columns)))
	for i, c := range spec.Columns {
		x := s.Columns[i]
		if x.Attnum != c.Attnum || x.Name != c.Name || x.TypeTag != c.Tag || x.Typmod != c.Typmod || x.Nullable != c.Nullable {
			return fmt.Errorf("%w: snapshot column", ErrSchemaDrift)
		}
		writeU16String(&b, c.Name)
		b.WriteByte(byte(c.Tag))
		if x.IsNull {
			if !c.Nullable || x.ValueHex != nil {
				return fmt.Errorf("%w: snapshot NULL", ErrInvalid)
			}
			b.WriteByte(0)
			continue
		}
		if x.ValueHex == nil || *x.ValueHex != "" && !isLowerHex(*x.ValueHex) {
			return fmt.Errorf("%w: snapshot hex", ErrInvalid)
		}
		raw, err := hex.DecodeString(*x.ValueHex)
		if err != nil {
			return fmt.Errorf("%w: snapshot hex", ErrInvalid)
		}
		if err := validateCanonicalBytes(c, raw); err != nil {
			return err
		}
		b.WriteByte(1)
		writeU64Bytes(&b, raw)
	}
	actual := sha256.Sum256(b.Bytes())
	if !hmac.Equal(actual[:], rowHash[:]) {
		return fmt.Errorf("%w: snapshot row hash", ErrInvalid)
	}
	return nil
}

func validateCanonicalBytes(c ColumnSpec, raw []byte) error {
	switch c.Tag {
	case TypeBool:
		if len(raw) != 1 || (raw[0] != 0 && raw[0] != 1) {
			return fmt.Errorf("%w: bool bytes", ErrInvalid)
		}
	case TypeInt2:
		if len(raw) != 2 {
			return fmt.Errorf("%w: int2 bytes", ErrInvalid)
		}
	case TypeInt4, TypeDate:
		if len(raw) != 4 {
			return fmt.Errorf("%w: int4/date bytes", ErrInvalid)
		}
		if c.Tag == TypeDate {
			v := int32(binary.BigEndian.Uint32(raw))
			if v == math.MinInt32 || v == math.MaxInt32 {
				return fmt.Errorf("%w: date infinity", ErrInvalid)
			}
		}
	case TypeInt8, TypeTimestamptz, TypeTimestamp:
		if len(raw) != 8 {
			return fmt.Errorf("%w: int8/time bytes", ErrInvalid)
		}
		if c.Tag != TypeInt8 {
			v := int64(binary.BigEndian.Uint64(raw))
			if v == math.MinInt64 || v == math.MaxInt64 {
				return fmt.Errorf("%w: timestamp infinity", ErrInvalid)
			}
		}
	case TypeUUID:
		if len(raw) != 16 {
			return fmt.Errorf("%w: uuid bytes", ErrInvalid)
		}
	case TypeText, TypeEnum, TypeBPChar:
		if !utf8.Valid(raw) {
			return fmt.Errorf("%w: text bytes", ErrInvalid)
		}
	case TypeBytea:
	case TypeNumeric:
		if !canonicalNumeric.Match(raw) || !numericFitsTypmod(string(raw), c.Typmod) {
			return fmt.Errorf("%w: numeric bytes", ErrInvalid)
		}
	case TypeInet:
		if len(raw) < 2 {
			return fmt.Errorf("%w: inet bytes", ErrInvalid)
		}
		f, p := raw[0], raw[1]
		if f == 4 && (len(raw) != 6 || p > 32) || f == 6 && (len(raw) != 18 || p > 128) || (f != 4 && f != 6) {
			return fmt.Errorf("%w: inet bytes", ErrInvalid)
		}
	case TypeJSONB:
		r := bytes.NewReader(raw)
		n, err := decodeJSONNode(r)
		if err != nil || r.Len() != 0 {
			return fmt.Errorf("%w: JSONB bytes", ErrInvalid)
		}
		out, _ := EncodeJSONB(n)
		if !bytes.Equal(out, raw) {
			return fmt.Errorf("%w: noncanonical JSONB", ErrInvalid)
		}
	case TypeArray:
		r := bytes.NewReader(raw)
		a, err := decodeArray(r, c)
		if err != nil {
			return err
		}
		if r.Len() != 0 {
			return fmt.Errorf("%w: array bytes", ErrInvalid)
		}
		out, _ := EncodeArray(a)
		if !bytes.Equal(out, raw) {
			return fmt.Errorf("%w: noncanonical array", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown value tag", ErrInvalid)
	}
	return nil
}
func readN(r *bytes.Reader, n uint64) ([]byte, error) {
	if n > uint64(r.Len()) {
		return nil, io.ErrUnexpectedEOF
	}
	b := make([]byte, int(n))
	_, e := io.ReadFull(r, b)
	return b, e
}
func readU64(r *bytes.Reader) (uint64, error) {
	var b [8]byte
	_, e := io.ReadFull(r, b[:])
	return binary.BigEndian.Uint64(b[:]), e
}
func decodeJSONNode(r *bytes.Reader) (JSONNode, error) {
	tag, e := r.ReadByte()
	if e != nil {
		return JSONNode{}, e
	}
	n := JSONNode{Kind: JSONKind(tag)}
	switch n.Kind {
	case JSONNull, JSONFalse, JSONTrue:
		return n, nil
	case JSONNumber, JSONString:
		l, e := readU64(r)
		if e != nil {
			return n, e
		}
		b, e := readN(r, l)
		if e != nil {
			return n, e
		}
		if n.Kind == JSONNumber {
			n.Number = string(b)
		} else {
			n.String = string(b)
		}
		return n, nil
	case JSONArray:
		l, e := readU64(r)
		if e != nil {
			return n, e
		}
		for i := uint64(0); i < l; i++ {
			z, e := readU64(r)
			if e != nil {
				return n, e
			}
			b, e := readN(r, z)
			if e != nil {
				return n, e
			}
			c, e := decodeJSONNode(bytes.NewReader(b))
			if e != nil {
				return n, e
			}
			n.Elements = append(n.Elements, c)
		}
		return n, nil
	case JSONObject:
		l, e := readU64(r)
		if e != nil {
			return n, e
		}
		for i := uint64(0); i < l; i++ {
			kl, e := readU64(r)
			if e != nil {
				return n, e
			}
			k, e := readN(r, kl)
			if e != nil {
				return n, e
			}
			vl, e := readU64(r)
			if e != nil {
				return n, e
			}
			v, e := readN(r, vl)
			if e != nil {
				return n, e
			}
			c, e := decodeJSONNode(bytes.NewReader(v))
			if e != nil {
				return n, e
			}
			n.Members = append(n.Members, JSONMember{string(k), c})
		}
		return n, nil
	}
	return n, ErrInvalid
}
func decodeArray(r *bytes.Reader, c ColumnSpec) (ArrayValue, error) {
	tag, e := r.ReadByte()
	if e != nil {
		return ArrayValue{}, e
	}
	if TypeTag(tag) != c.ArrayElementTag {
		return ArrayValue{}, fmt.Errorf("%w: array element tag drift", ErrSchemaDrift)
	}
	var nb [4]byte
	if _, e = io.ReadFull(r, nb[:]); e != nil {
		return ArrayValue{}, e
	}
	nd := binary.BigEndian.Uint32(nb[:])
	a := ArrayValue{ElementTag: TypeTag(tag), ElementTypmod: c.ArrayElementTypmod}
	for i := uint32(0); i < nd; i++ {
		var b [8]byte
		if _, e = io.ReadFull(r, b[:]); e != nil {
			return a, e
		}
		a.Dimensions = append(a.Dimensions, Dimension{int32(binary.BigEndian.Uint32(b[:4])), int32(binary.BigEndian.Uint32(b[4:]))})
	}
	count, e := readU64(r)
	if e != nil {
		return a, e
	}
	for i := uint64(0); i < count; i++ {
		nt, e := r.ReadByte()
		if e != nil {
			return a, e
		}
		v := Value{Tag: a.ElementTag, Null: nt == 0}
		if nt > 1 {
			return a, ErrInvalid
		}
		if nt == 1 {
			l, e := readU64(r)
			if e != nil {
				return a, e
			}
			b, e := readN(r, l)
			if e != nil {
				return a, e
			}
			if e = validateCanonicalBytes(ColumnSpec{Tag: a.ElementTag, Typmod: a.ElementTypmod, ArrayElementTypmod: -1}, b); e != nil {
				return a, e
			}
			v = rawValue(a.ElementTag, b)
		}
		a.Elements = append(a.Elements, v)
	}
	return a, nil
}
func rawValue(tag TypeTag, b []byte) Value {
	v := Value{Tag: tag}
	switch tag {
	case TypeBool:
		v.Bool = b[0] == 1
	case TypeInt2:
		v.Int = int64(int16(binary.BigEndian.Uint16(b)))
	case TypeInt4:
		v.Int = int64(int32(binary.BigEndian.Uint32(b)))
	case TypeInt8:
		v.Int = int64(binary.BigEndian.Uint64(b))
	case TypeUUID:
		copy(v.UUID[:], b)
	case TypeBytea:
		v.Bytes = b
	case TypeText, TypeEnum, TypeBPChar:
		v.Text = string(b)
	case TypeTimestamptz, TypeTimestamp:
		v.Microseconds = int64(binary.BigEndian.Uint64(b))
	case TypeDate:
		v.Days = int32(binary.BigEndian.Uint32(b))
	case TypeNumeric:
		v.Numeric = string(b)
	case TypeInet:
		v.Inet = InetValue{b[0], b[1], append([]byte(nil), b[2:]...)}
	}
	return v
}

type TenantSummary struct {
	TenantRefHMACHex string `json:"tenant_ref_hmac_hex"`
	RecordCount      uint64 `json:"record_count"`
	RowsetHMACHex    string `json:"rowset_hmac_hex"`
}
type RelationSummary struct {
	Schema        string          `json:"schema"`
	Relation      string          `json:"relation"`
	RecordCount   uint64          `json:"record_count"`
	RowsetHMACHex string          `json:"rowset_hmac_hex"`
	Tenants       []TenantSummary `json:"tenants"`
}
type SetSummary struct {
	Name          string            `json:"name"`
	RecordCount   uint64            `json:"record_count"`
	RowsetHMACHex string            `json:"rowset_hmac_hex"`
	Relations     []RelationSummary `json:"relations"`
}
type ClassificationManifest struct {
	CodecVersion      string       `json:"codec_version"`
	RuleVersion       string       `json:"rule_version"`
	EvidenceHMACKeyID string       `json:"evidence_hmac_key_id"`
	Sets              []SetSummary `json:"sets"`
}

func BuildClassificationManifest(key [32]byte, keyID string, relations []RelationSpec, sets map[string][]EncodedRow) (ClassificationManifest, error) {
	if !keyIDPattern.MatchString(keyID) {
		return ClassificationManifest{}, fmt.Errorf("%w: key id", ErrInvalid)
	}
	if _, _, err := EncodeRelationManifest(relations); err != nil {
		return ClassificationManifest{}, err
	}
	if !reflect.DeepEqual(relations, Schema00042()) {
		return ClassificationManifest{}, fmt.Errorf("%w: classification must use exact Schema00042", ErrSchemaDrift)
	}
	if len(sets) != 4 {
		return ClassificationManifest{}, fmt.Errorf("%w: exact four sets required", ErrSchemaDrift)
	}
	for name := range sets {
		if name != "before" && name != "retained" && name != "archived" && name != "after" {
			return ClassificationManifest{}, fmt.Errorf("%w: unknown set", ErrSchemaDrift)
		}
	}
	covered := map[string]bool{}
	for _, r := range relations {
		covered[r.Schema+"\x00"+r.Name] = true
	}
	for _, rows := range sets {
		for _, r := range rows {
			if !covered[r.Schema+"\x00"+r.Relation] {
				return ClassificationManifest{}, fmt.Errorf("%w: uncovered row", ErrSchemaDrift)
			}
		}
	}
	m := ClassificationManifest{CodecVersion: CodecVersion, RuleVersion: ClassificationRuleVersion, EvidenceHMACKeyID: keyID}
	for _, name := range []string{"before", "retained", "archived", "after"} {
		rows := sets[name]
		count, digest, err := digestRows(key, rows)
		if err != nil {
			return m, err
		}
		ss := SetSummary{Name: name, RecordCount: count, RowsetHMACHex: hex.EncodeToString(digest[:])}
		for _, rel := range relations {
			var rr []EncodedRow
			for _, r := range rows {
				if r.Schema == rel.Schema && r.Relation == rel.Name {
					rr = append(rr, r)
				}
			}
			rc, rd, err := digestRows(key, rr)
			if err != nil {
				return m, err
			}
			rs := RelationSummary{Schema: rel.Schema, Relation: rel.Name, RecordCount: rc, RowsetHMACHex: hex.EncodeToString(rd[:]), Tenants: []TenantSummary{}}
			groups := map[[32]byte][]EncodedRow{}
			for _, r := range rr {
				ref := tenantRef(key, r.Tenant)
				groups[ref] = append(groups[ref], r)
			}
			refs := make([][32]byte, 0, len(groups))
			for ref := range groups {
				refs = append(refs, ref)
			}
			sort.Slice(refs, func(i, j int) bool { return bytes.Compare(refs[i][:], refs[j][:]) < 0 })
			for _, ref := range refs {
				tc, td, e := digestRows(key, groups[ref])
				if e != nil {
					return m, e
				}
				rs.Tenants = append(rs.Tenants, TenantSummary{hex.EncodeToString(ref[:]), tc, hex.EncodeToString(td[:])})
			}
			ss.Relations = append(ss.Relations, rs)
		}
		m.Sets = append(m.Sets, ss)
	}
	if m.Sets[0].RecordCount != m.Sets[1].RecordCount+m.Sets[2].RecordCount || m.Sets[3].RecordCount != m.Sets[1].RecordCount {
		return m, fmt.Errorf("%w: set conservation", ErrCountMismatch)
	}
	if err := validateSubsetConservation(sets); err != nil {
		return m, err
	}
	return m, nil
}

func validateSubsetConservation(sets map[string][]EncodedRow) error {
	type tenantKey [16]byte
	type jointKey struct {
		relation string
		tenant   tenantKey
	}
	relationCounts := map[string]map[string]uint64{}
	tenantCounts := map[string]map[tenantKey]uint64{}
	jointCounts := map[string]map[jointKey]uint64{}
	for _, name := range []string{"before", "retained", "archived", "after"} {
		relationCounts[name] = map[string]uint64{}
		tenantCounts[name] = map[tenantKey]uint64{}
		jointCounts[name] = map[jointKey]uint64{}
		for _, r := range sets[name] {
			relation := r.Schema + "\x00" + r.Relation
			tenant := tenantKey(r.Tenant)
			relationCounts[name][relation]++
			tenantCounts[name][tenant]++
			jointCounts[name][jointKey{relation: relation, tenant: tenant}]++
		}
	}
	check := func(before, retained, archived, after uint64) bool {
		return before == retained+archived && after == retained
	}
	relations := map[string]bool{}
	for _, m := range relationCounts {
		for k := range m {
			relations[k] = true
		}
	}
	for k := range relations {
		if !check(relationCounts["before"][k], relationCounts["retained"][k], relationCounts["archived"][k], relationCounts["after"][k]) {
			return fmt.Errorf("%w: relation conservation", ErrCountMismatch)
		}
	}
	tenants := map[tenantKey]bool{}
	for _, m := range tenantCounts {
		for k := range m {
			tenants[k] = true
		}
	}
	for k := range tenants {
		if !check(tenantCounts["before"][k], tenantCounts["retained"][k], tenantCounts["archived"][k], tenantCounts["after"][k]) {
			return fmt.Errorf("%w: tenant conservation", ErrCountMismatch)
		}
	}
	joints := map[jointKey]bool{}
	for _, counts := range jointCounts {
		for key := range counts {
			joints[key] = true
		}
	}
	for key := range joints {
		if !check(jointCounts["before"][key], jointCounts["retained"][key], jointCounts["archived"][key], jointCounts["after"][key]) {
			return fmt.Errorf("%w: relation-tenant joint conservation", ErrCountMismatch)
		}
	}
	return nil
}
func VerifyClassificationManifest(data []byte, key [32]byte, keyID string, relations []RelationSpec, sets map[string][]EncodedRow) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	var got ClassificationManifest
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&got) != nil {
		return fmt.Errorf("%w: classification JSON", ErrInvalid)
	}
	want, err := BuildClassificationManifest(key, keyID, relations, sets)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: classification mismatch", ErrInvalid)
	}
	return nil
}
func digestRows(key [32]byte, rows []EncodedRow) (uint64, [32]byte, error) {
	b := NewRowSetBuilder(key, uint64(len(rows)))
	for _, r := range rows {
		if err := b.Add(r); err != nil {
			return 0, [32]byte{}, err
		}
	}
	return b.Finalize()
}
func tenantRef(key [32]byte, tenant UUID) [32]byte {
	m := hmac.New(sha256.New, key[:])
	m.Write([]byte(tenantRefDomain))
	m.Write(tenant[:])
	var o [32]byte
	copy(o[:], m.Sum(nil))
	return o
}

type ArtifactDigest struct {
	Length uint64
	SHA256 [32]byte
	HMAC   [32]byte
}

func ComputeArtifactDigest(artifactKeyID string, artifactKey []byte, version, format string, artifact []byte) (ArtifactDigest, error) {
	var out ArtifactDigest
	if len(artifactKey) != 32 || !keyIDPattern.MatchString(artifactKeyID) || version != ArtifactHMACVersion || format != ArtifactFormat {
		return out, fmt.Errorf("%w: artifact framing", ErrInvalid)
	}
	msg, err := ArtifactHMACMessage(artifactKeyID, version, format, artifact)
	if err != nil {
		return out, err
	}
	out.Length = uint64(len(artifact))
	out.SHA256 = sha256.Sum256(artifact)
	m := hmac.New(sha256.New, artifactKey)
	m.Write(msg)
	copy(out.HMAC[:], m.Sum(nil))
	return out, nil
}
func ComputeSeparatedArtifactDigest(artifactKeyID string, artifactKey []byte, evidenceKeyID string, evidenceKey []byte, version, format string, artifact []byte) (ArtifactDigest, error) {
	if len(evidenceKey) != 32 || !keyIDPattern.MatchString(evidenceKeyID) || artifactKeyID == evidenceKeyID || hmac.Equal(artifactKey, evidenceKey) {
		return ArtifactDigest{}, fmt.Errorf("%w: artifact separation", ErrInvalid)
	}
	return ComputeArtifactDigest(artifactKeyID, artifactKey, version, format, artifact)
}
func ArtifactHMACMessage(keyID, version, format string, artifact []byte) ([]byte, error) {
	if !keyIDPattern.MatchString(keyID) || version != ArtifactHMACVersion || format != ArtifactFormat {
		return nil, fmt.Errorf("%w: artifact framing", ErrInvalid)
	}
	var msg bytes.Buffer
	msg.WriteString(artifactDomain)
	if err := writeU16String(&msg, keyID); err != nil {
		return nil, err
	}
	if err := writeU16String(&msg, format); err != nil {
		return nil, err
	}
	if err := writeU64Bytes(&msg, artifact); err != nil {
		return nil, err
	}
	return msg.Bytes(), nil
}

func isLowerHex(s string) bool {
	return len(s)%2 == 0 && (s == "" || regexp.MustCompile(`^[0-9a-f]+$`).MatchString(s))
}
func rejectDuplicateKeys(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		switch v := t.(type) {
		case json.Delim:
			if v == '{' {
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return fmt.Errorf("%w: malformed JSON object", ErrInvalid)
					}
					ks, ok := k.(string)
					if !ok {
						return fmt.Errorf("%w: malformed JSON key", ErrInvalid)
					}
					if seen[ks] {
						return fmt.Errorf("%w: duplicate JSON key", ErrInvalid)
					}
					seen[ks] = true
					if e := walk(); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			}
			if v == '[' {
				for d.More() {
					if e := walk(); e != nil {
						return e
					}
				}
				_, e = d.Token()
				return e
			}
		}
		return nil
	}
	if e := walk(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrInvalid)
	}
	return nil
}
