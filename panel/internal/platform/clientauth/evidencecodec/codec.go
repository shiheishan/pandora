package evidencecodec

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"unicode/utf8"
)

var canonicalNumeric = regexp.MustCompile(`^(?:0|-?(?:[1-9][0-9]*(?:\.[0-9]*[1-9])?|0\.[0-9]*[1-9]))$`)

func SourceKeyForID(id UUID) [16]byte { return id }

func SourceKeyForBundleNode(bundleID, nodeID UUID) [32]byte {
	var out [32]byte
	copy(out[:16], bundleID[:])
	copy(out[16:], nodeID[:])
	return out
}

func EncodeScalar(spec ColumnSpec, value Value) ([]byte, error) {
	if value.Null || value.Tag != spec.Tag || spec.Tag == TypeArray || spec.Tag == TypeJSONB {
		return nil, fmt.Errorf("%w: scalar tag/null mismatch", ErrInvalid)
	}
	var out []byte
	switch spec.Tag {
	case TypeBool:
		if value.Bool {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case TypeInt2:
		if value.Int < math.MinInt16 || value.Int > math.MaxInt16 {
			return nil, fmt.Errorf("%w: int2 overflow", ErrInvalid)
		}
		out = make([]byte, 2)
		binary.BigEndian.PutUint16(out, uint16(int16(value.Int)))
	case TypeInt4:
		if value.Int < math.MinInt32 || value.Int > math.MaxInt32 {
			return nil, fmt.Errorf("%w: int4 overflow", ErrInvalid)
		}
		out = make([]byte, 4)
		binary.BigEndian.PutUint32(out, uint32(int32(value.Int)))
	case TypeInt8:
		out = make([]byte, 8)
		binary.BigEndian.PutUint64(out, uint64(value.Int))
	case TypeUUID:
		out = append(out, value.UUID[:]...)
	case TypeBytea:
		out = append(out, value.Bytes...)
	case TypeText, TypeEnum, TypeBPChar:
		if !utf8.ValidString(value.Text) {
			return nil, fmt.Errorf("%w: invalid UTF-8", ErrInvalid)
		}
		out = append(out, value.Text...)
	case TypeTimestamptz, TypeTimestamp:
		if spec.Typmod != -1 && spec.Typmod != 6 {
			return nil, fmt.Errorf("%w: timestamp typmod", ErrSchemaDrift)
		}
		if value.Microseconds == math.MinInt64 || value.Microseconds == math.MaxInt64 {
			return nil, fmt.Errorf("%w: timestamp infinity", ErrInvalid)
		}
		out = make([]byte, 8)
		binary.BigEndian.PutUint64(out, uint64(value.Microseconds))
	case TypeDate:
		if value.Days == math.MinInt32 || value.Days == math.MaxInt32 {
			return nil, fmt.Errorf("%w: date infinity", ErrInvalid)
		}
		out = make([]byte, 4)
		binary.BigEndian.PutUint32(out, uint32(value.Days))
	case TypeNumeric:
		if !canonicalNumeric.MatchString(value.Numeric) || !numericFitsTypmod(value.Numeric, spec.Typmod) {
			return nil, fmt.Errorf("%w: non-canonical numeric", ErrInvalid)
		}
		out = []byte(value.Numeric)
	case TypeInet:
		if value.Inet.Family == 4 {
			if value.Inet.Prefix > 32 || len(value.Inet.Address) != 4 {
				return nil, fmt.Errorf("%w: IPv4 inet", ErrInvalid)
			}
		} else if value.Inet.Family == 6 {
			if value.Inet.Prefix > 128 || len(value.Inet.Address) != 16 {
				return nil, fmt.Errorf("%w: IPv6 inet", ErrInvalid)
			}
		} else {
			return nil, fmt.Errorf("%w: inet family", ErrInvalid)
		}
		out = append(out, value.Inet.Family, value.Inet.Prefix)
		out = append(out, value.Inet.Address...)
	default:
		return nil, fmt.Errorf("%w: unsupported scalar tag %d", ErrInvalid, spec.Tag)
	}
	return out, nil
}

func numericFitsTypmod(value string, typmod int32) bool {
	if typmod == -1 {
		unsigned := value
		if unsigned[0] == '-' {
			unsigned = unsigned[1:]
		}
		integer, fraction := unsigned, ""
		if dot := bytes.IndexByte([]byte(unsigned), '.'); dot >= 0 {
			integer, fraction = unsigned[:dot], unsigned[dot+1:]
		}
		integerDigits := len(integer)
		if integer == "0" {
			integerDigits = 0
		}
		return integerDigits <= 131072 && len(fraction) <= 16383
	}
	if typmod < 4 {
		return false
	}
	raw := typmod - 4
	precision := int((raw >> 16) & 0xffff)
	scaleRaw := int(raw & 0x7ff)
	if scaleRaw >= 1024 {
		scaleRaw -= 2048
	}
	if precision < 1 || precision > 1000 || scaleRaw < -1000 || scaleRaw > 1000 {
		return false
	}
	unsigned := value
	if unsigned[0] == '-' {
		unsigned = unsigned[1:]
	}
	integer, fraction := unsigned, ""
	if dot := bytes.IndexByte([]byte(unsigned), '.'); dot >= 0 {
		integer, fraction = unsigned[:dot], unsigned[dot+1:]
	}
	integerDigits := len(integer)
	if integer == "0" {
		integerDigits = 0
	}
	maxIntegerDigits := precision - scaleRaw
	if scaleRaw >= 0 {
		if len(fraction) > scaleRaw {
			return false
		}
		if maxIntegerDigits >= 0 {
			return integerDigits <= maxIntegerDigits
		}
		if integerDigits != 0 {
			return false
		}
		requiredLeadingZeros := -maxIntegerDigits
		if len(fraction) < requiredLeadingZeros {
			return false
		}
		for i := 0; i < requiredLeadingZeros; i++ {
			if fraction[i] != '0' {
				return false
			}
		}
		return true
	}
	if len(fraction) != 0 || integerDigits > maxIntegerDigits {
		return false
	}
	if value == "0" {
		return true
	}
	requiredTrailingZeros := -scaleRaw
	if len(integer) < requiredTrailingZeros {
		return false
	}
	for i := len(integer) - requiredTrailingZeros; i < len(integer); i++ {
		if integer[i] != '0' {
			return false
		}
	}
	return true
}

func EncodeJSONB(node JSONNode) ([]byte, error) {
	var b bytes.Buffer
	if err := encodeJSONNode(&b, node); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func encodeJSONNode(w io.Writer, node JSONNode) error {
	switch node.Kind {
	case JSONNull, JSONFalse, JSONTrue:
		_, err := w.Write([]byte{byte(node.Kind)})
		return err
	case JSONNumber:
		if !canonicalNumeric.MatchString(node.Number) || !numericFitsTypmod(node.Number, -1) {
			return fmt.Errorf("%w: JSON numeric", ErrInvalid)
		}
		w.Write([]byte{3})
		return writeU64Bytes(w, []byte(node.Number))
	case JSONString:
		if !utf8.ValidString(node.String) {
			return fmt.Errorf("%w: JSON string UTF-8", ErrInvalid)
		}
		w.Write([]byte{4})
		return writeU64Bytes(w, []byte(node.String))
	case JSONArray:
		w.Write([]byte{5})
		writeU64(w, uint64(len(node.Elements)))
		for _, child := range node.Elements {
			encoded, err := EncodeJSONB(child)
			if err != nil {
				return err
			}
			if err := writeU64Bytes(w, encoded); err != nil {
				return err
			}
		}
		return nil
	case JSONObject:
		w.Write([]byte{6})
		writeU64(w, uint64(len(node.Members)))
		var previous []byte
		for i, member := range node.Members {
			key := []byte(member.Key)
			if !utf8.Valid(key) {
				return fmt.Errorf("%w: JSON key UTF-8", ErrInvalid)
			}
			if i > 0 && bytes.Compare(previous, key) >= 0 {
				return fmt.Errorf("%w: JSON key order/duplicate", ErrOrder)
			}
			if err := writeU64Bytes(w, key); err != nil {
				return err
			}
			value, err := EncodeJSONB(member.Value)
			if err != nil {
				return err
			}
			if err := writeU64Bytes(w, value); err != nil {
				return err
			}
			previous = append(previous[:0], key...)
		}
		return nil
	default:
		return fmt.Errorf("%w: JSON kind", ErrInvalid)
	}
}

func EncodeArray(value ArrayValue) ([]byte, error) {
	if value.ElementTag == 0 || value.ElementTag == TypeArray || value.ElementTag == TypeJSONB {
		return nil, fmt.Errorf("%w: array element tag", ErrInvalid)
	}
	if len(value.Dimensions) > math.MaxUint32 {
		return nil, fmt.Errorf("%w: dimensions overflow", ErrInvalid)
	}
	expected := uint64(0)
	if len(value.Dimensions) > 0 {
		expected = 1
		for _, d := range value.Dimensions {
			if d.Length <= 0 {
				return nil, fmt.Errorf("%w: non-positive dimension", ErrInvalid)
			}
			if d.Length != 0 && expected > math.MaxUint64/uint64(d.Length) {
				return nil, fmt.Errorf("%w: array product overflow", ErrInvalid)
			}
			expected *= uint64(d.Length)
		}
	}
	if expected != uint64(len(value.Elements)) {
		return nil, fmt.Errorf("%w: array element count", ErrCountMismatch)
	}
	var b bytes.Buffer
	b.WriteByte(byte(value.ElementTag))
	writeU32(&b, uint32(len(value.Dimensions)))
	for _, d := range value.Dimensions {
		writeI32(&b, d.Length)
		writeI32(&b, d.LowerBound)
	}
	writeU64(&b, uint64(len(value.Elements)))
	spec := ColumnSpec{Tag: value.ElementTag, Typmod: value.ElementTypmod}
	for _, element := range value.Elements {
		if element.Tag != value.ElementTag {
			return nil, fmt.Errorf("%w: array element type", ErrInvalid)
		}
		if element.Null {
			b.WriteByte(0)
			continue
		}
		b.WriteByte(1)
		encoded, err := EncodeScalar(spec, element)
		if err != nil {
			return nil, err
		}
		writeU64Bytes(&b, encoded)
	}
	return b.Bytes(), nil
}

func EncodeRow(row Row) (EncodedRow, error) {
	if err := ValidateRelation(row.Spec); err != nil {
		return EncodedRow{}, err
	}
	if len(row.Values) != len(row.Spec.Columns) {
		return EncodedRow{}, fmt.Errorf("%w: row field count", ErrCountMismatch)
	}
	if err := validateSourceKey(row.Spec.SourceKeyKind, row.SourceKey); err != nil {
		return EncodedRow{}, err
	}
	if err := validateRowIdentity(row); err != nil {
		return EncodedRow{}, err
	}
	var b bytes.Buffer
	b.WriteString(rowDomain)
	if err := writeU16String(&b, row.Spec.Schema); err != nil {
		return EncodedRow{}, err
	}
	if err := writeU16String(&b, row.Spec.Name); err != nil {
		return EncodedRow{}, err
	}
	writeU32(&b, uint32(len(row.Spec.Columns)))
	for i, column := range row.Spec.Columns {
		value := row.Values[i]
		if value.Tag != column.Tag {
			return EncodedRow{}, fmt.Errorf("%w: field %s tag", ErrInvalid, column.Name)
		}
		if err := writeU16String(&b, column.Name); err != nil {
			return EncodedRow{}, err
		}
		b.WriteByte(byte(column.Tag))
		if value.Null {
			if !column.Nullable {
				return EncodedRow{}, fmt.Errorf("%w: field %s is NOT NULL", ErrInvalid, column.Name)
			}
			b.WriteByte(0)
			continue
		}
		b.WriteByte(1)
		var encoded []byte
		var err error
		switch column.Tag {
		case TypeJSONB:
			encoded, err = EncodeJSONB(value.JSON)
		case TypeArray:
			if value.Array == nil || value.Array.ElementTag != column.ArrayElementTag || value.Array.ElementTypmod != column.ArrayElementTypmod {
				return EncodedRow{}, fmt.Errorf("%w: array spec mismatch", ErrSchemaDrift)
			}
			encoded, err = EncodeArray(*value.Array)
		default:
			encoded, err = EncodeScalar(column, value)
		}
		if err != nil {
			return EncodedRow{}, err
		}
		writeU64Bytes(&b, encoded)
	}
	out := EncodedRow{Schema: row.Spec.Schema, Relation: row.Spec.Name, Tenant: row.Tenant, SourceKey: slices.Clone(row.SourceKey), Bytes: b.Bytes()}
	out.seal = sealRow(out)
	return out, nil
}

func validateRowIdentity(row Row) error {
	index := func(name string) int {
		for i, c := range row.Spec.Columns {
			if c.Name == name {
				return i
			}
		}
		return -1
	}
	tenantIndex := index("tenant_id")
	if tenantIndex < 0 || row.Values[tenantIndex].Null || row.Values[tenantIndex].Tag != TypeUUID || row.Values[tenantIndex].UUID != row.Tenant {
		return fmt.Errorf("%w: tenant identity mismatch", ErrInvalid)
	}
	if row.Spec.SourceKeyKind == SourceKeyID {
		i := index("id")
		if i < 0 || row.Values[i].Null || row.Values[i].Tag != TypeUUID || !bytes.Equal(row.SourceKey, row.Values[i].UUID[:]) {
			return fmt.Errorf("%w: id source key mismatch", ErrInvalid)
		}
		return nil
	}
	b, n := index("bundle_id"), index("node_id")
	if b < 0 || n < 0 || row.Values[b].Null || row.Values[n].Null {
		return fmt.Errorf("%w: bundle-node identity", ErrInvalid)
	}
	want := SourceKeyForBundleNode(row.Values[b].UUID, row.Values[n].UUID)
	if !bytes.Equal(row.SourceKey, want[:]) {
		return fmt.Errorf("%w: bundle-node source key mismatch", ErrInvalid)
	}
	return nil
}

func ValidateRelation(relation RelationSpec) error {
	if relation.Schema == "" || relation.Name == "" || len(relation.Schema) > math.MaxUint16 || len(relation.Name) > math.MaxUint16 ||
		(relation.SourceKeyKind != SourceKeyID && relation.SourceKeyKind != SourceKeyBundleNode) {
		return fmt.Errorf("%w: relation identity", ErrSchemaDrift)
	}
	for i, column := range relation.Columns {
		if column.Attnum == 0 || column.Name == "" || len(column.Name) > math.MaxUint16 ||
			column.Tag < TypeBool || column.Tag > TypeInet ||
			column.Declared.Schema == "" || column.Declared.Name == "" || column.Base.Schema == "" || column.Base.Name == "" {
			return fmt.Errorf("%w: column identity", ErrSchemaDrift)
		}
		if i > 0 && relation.Columns[i-1].Attnum >= column.Attnum {
			return fmt.Errorf("%w: attnum order", ErrOrder)
		}
		if column.Tag == TypeArray {
			if column.Typmod != -1 || column.ArrayElementTag < TypeBool || column.ArrayElementTag > TypeInet ||
				column.ArrayElementTag == TypeArray || column.ArrayElementTag == TypeJSONB {
				return fmt.Errorf("%w: array element manifest", ErrSchemaDrift)
			}
		} else if column.ArrayElementTag != 0 || column.ArrayElementTypmod != -1 {
			return fmt.Errorf("%w: non-array element manifest", ErrSchemaDrift)
		}
		if err := validateTypeIdentity(column); err != nil {
			return err
		}
		if (column.Tag == TypeTimestamptz || column.Tag == TypeTimestamp) && column.Typmod != -1 && column.Typmod != 6 {
			return fmt.Errorf("%w: timestamp typmod", ErrSchemaDrift)
		}
	}
	return nil
}

func validateTypeIdentity(c ColumnSpec) error {
	builtin := map[TypeTag]string{TypeBool: "bool", TypeInt2: "int2", TypeInt4: "int4", TypeInt8: "int8", TypeUUID: "uuid", TypeBytea: "bytea", TypeText: "text", TypeTimestamptz: "timestamptz", TypeTimestamp: "timestamp", TypeDate: "date", TypeNumeric: "numeric", TypeJSONB: "jsonb", TypeBPChar: "bpchar", TypeInet: "inet"}
	if c.Tag == TypeArray {
		expected := map[TypeTag]string{TypeText: "_text", TypeUUID: "_uuid", TypeInet: "_inet"}[c.ArrayElementTag]
		base := TypeIdentity{"pg_catalog", expected}
		if expected == "" || c.Base != base || c.ArrayElementTypmod != -1 ||
			(c.Declared != base && c.Declared.Schema == "pg_catalog") {
			return fmt.Errorf("%w: array type identity", ErrSchemaDrift)
		}
		return nil
	}
	if c.Tag == TypeEnum {
		if c.Base.Schema == "pg_catalog" || (c.Declared != c.Base && c.Declared.Schema == "pg_catalog") {
			return fmt.Errorf("%w: enum identity", ErrSchemaDrift)
		}
		return nil
	}
	expected := builtin[c.Tag]
	if expected == "" || c.Base != (TypeIdentity{"pg_catalog", expected}) {
		return fmt.Errorf("%w: base type identity", ErrSchemaDrift)
	}
	// A differing declared identity is allowed only for a user domain.
	if c.Declared != c.Base && c.Declared.Schema == "pg_catalog" {
		return fmt.Errorf("%w: declared domain identity", ErrSchemaDrift)
	}
	return nil
}

func EncodeRelationManifest(relations []RelationSpec) ([]byte, [32]byte, error) {
	var zero [32]byte
	if len(relations) > math.MaxUint32 {
		return nil, zero, fmt.Errorf("%w: relation count", ErrInvalid)
	}
	var b bytes.Buffer
	b.WriteString(manifestDomain)
	writeU32(&b, uint32(len(relations)))
	var previous []byte
	for i, relation := range relations {
		if err := ValidateRelation(relation); err != nil {
			return nil, zero, err
		}
		key := append(append([]byte(relation.Schema), 0), []byte(relation.Name)...)
		if i > 0 && bytes.Compare(previous, key) >= 0 {
			return nil, zero, fmt.Errorf("%w: relation order", ErrOrder)
		}
		if err := writeU16String(&b, relation.Schema); err != nil {
			return nil, zero, err
		}
		if err := writeU16String(&b, relation.Name); err != nil {
			return nil, zero, err
		}
		b.WriteByte(byte(relation.SourceKeyKind))
		writeU32(&b, uint32(len(relation.Columns)))
		for _, c := range relation.Columns {
			writeU16(&b, c.Attnum)
			if err := writeU16String(&b, c.Name); err != nil {
				return nil, zero, err
			}
			if err := writeIdentity(&b, c.Declared); err != nil {
				return nil, zero, err
			}
			if err := writeIdentity(&b, c.Base); err != nil {
				return nil, zero, err
			}
			b.WriteByte(byte(c.Tag))
			if c.Nullable {
				b.WriteByte(1)
			} else {
				b.WriteByte(0)
			}
			writeI32(&b, c.Typmod)
			b.WriteByte(byte(c.ArrayElementTag))
			writeI32(&b, c.ArrayElementTypmod)
		}
		previous = key
	}
	data := b.Bytes()
	return data, sha256.Sum256(data), nil
}

func NewRowSetBuilder(key [32]byte, expected uint64) *RowSetBuilder {
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(rowsetDomain))
	var count [8]byte
	binary.BigEndian.PutUint64(count[:], expected)
	mac.Write(count[:])
	return &RowSetBuilder{expected: expected, mac: mac}
}

func (b *RowSetBuilder) Add(row EncodedRow) error {
	expectedSeal := sealRow(row)
	if b.finalized || b.count >= b.expected || len(row.Bytes) == 0 || !hmac.Equal(row.seal[:], expectedSeal[:]) {
		return fmt.Errorf("%w: rowset state", ErrInvalid)
	}
	key := sortKey(row)
	if b.lastKey != nil && bytes.Compare(b.lastKey, key) >= 0 {
		return fmt.Errorf("%w: rowset order/duplicate", ErrOrder)
	}
	writeU16String(b.mac, row.Schema)
	writeU16String(b.mac, row.Relation)
	writeU64Bytes(b.mac, row.Bytes)
	b.lastKey = key
	b.count++
	return nil
}

func sealRow(row EncodedRow) [32]byte {
	h := sha256.New()
	h.Write([]byte("pandora-client-auth-00044-encoded-row-seal-v1\x00"))
	writeU16String(h, row.Schema)
	writeU16String(h, row.Relation)
	h.Write(row.Tenant[:])
	writeU64Bytes(h, row.SourceKey)
	writeU64Bytes(h, row.Bytes)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func (b *RowSetBuilder) Finalize() (uint64, [32]byte, error) {
	var digest [32]byte
	if b.finalized || b.count != b.expected {
		return b.count, digest, fmt.Errorf("%w: got %d want %d", ErrCountMismatch, b.count, b.expected)
	}
	b.finalized = true
	copy(digest[:], b.mac.Sum(nil))
	return b.count, digest, nil
}

func VerifyDigest(expected, actual [32]byte) bool { return hmac.Equal(expected[:], actual[:]) }

func sortKey(row EncodedRow) []byte {
	out := make([]byte, 0, len(row.Schema)+len(row.Relation)+2+16+len(row.SourceKey))
	out = append(out, row.Schema...)
	out = append(out, 0)
	out = append(out, row.Relation...)
	out = append(out, 0)
	out = append(out, row.Tenant[:]...)
	out = append(out, row.SourceKey...)
	return out
}

func validateSourceKey(kind SourceKeyKind, key []byte) error {
	want := 16
	if kind == SourceKeyBundleNode {
		want = 32
	}
	if len(key) != want {
		return fmt.Errorf("%w: source key length", ErrInvalid)
	}
	return nil
}

func writeIdentity(w io.Writer, identity TypeIdentity) error {
	if err := writeU16String(w, identity.Schema); err != nil {
		return err
	}
	return writeU16String(w, identity.Name)
}
func writeU16String(w io.Writer, value string) error {
	if !utf8.ValidString(value) || len(value) > math.MaxUint16 {
		return fmt.Errorf("%w: string length/UTF-8", ErrInvalid)
	}
	writeU16(w, uint16(len(value)))
	_, err := io.WriteString(w, value)
	return err
}
func writeU64Bytes(w io.Writer, value []byte) error {
	writeU64(w, uint64(len(value)))
	_, err := w.Write(value)
	return err
}
func writeU16(w io.Writer, v uint16) {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	w.Write(x[:])
}
func writeU32(w io.Writer, v uint32) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], v)
	w.Write(x[:])
}
func writeU64(w io.Writer, v uint64) {
	var x [8]byte
	binary.BigEndian.PutUint64(x[:], v)
	w.Write(x[:])
}
func writeI32(w io.Writer, v int32) { writeU32(w, uint32(v)) }
