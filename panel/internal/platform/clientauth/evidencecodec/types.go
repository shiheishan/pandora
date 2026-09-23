// Package evidencecodec implements the side-effect-free canonical evidence
// codec frozen by CLIENT-AUTH-00044. It deliberately has no SQL or filesystem
// integration.
package evidencecodec

import (
	"errors"
	"hash"
)

const (
	CodecVersion   = "pandora-client-auth-00044-evidence-v1"
	rowDomain      = "pandora-client-auth-00044-row-v1\x00"
	rowsetDomain   = "pandora-client-auth-00044-rowset-v1\x00"
	manifestDomain = "pandora-client-auth-00044-relation-manifest-v1\x00"
)

var (
	ErrInvalid       = errors.New("evidencecodec: invalid value")
	ErrSchemaDrift   = errors.New("evidencecodec: schema drift")
	ErrOrder         = errors.New("evidencecodec: non-canonical order")
	ErrCountMismatch = errors.New("evidencecodec: count mismatch")
)

type TypeTag uint8

const (
	TypeBool        TypeTag = 0x01
	TypeInt2        TypeTag = 0x02
	TypeInt4        TypeTag = 0x03
	TypeInt8        TypeTag = 0x04
	TypeUUID        TypeTag = 0x05
	TypeBytea       TypeTag = 0x06
	TypeText        TypeTag = 0x07
	TypeEnum        TypeTag = 0x08
	TypeTimestamptz TypeTag = 0x09
	TypeTimestamp   TypeTag = 0x0a
	TypeDate        TypeTag = 0x0b
	TypeNumeric     TypeTag = 0x0c
	TypeJSONB       TypeTag = 0x0d
	TypeArray       TypeTag = 0x0e
	TypeBPChar      TypeTag = 0x0f
	TypeInet        TypeTag = 0x10
)

type UUID [16]byte

type TypeIdentity struct {
	Schema string
	Name   string
}

type ColumnSpec struct {
	Attnum             uint16
	Name               string
	Declared           TypeIdentity
	Base               TypeIdentity
	Tag                TypeTag
	Nullable           bool
	Typmod             int32
	ArrayElementTag    TypeTag
	ArrayElementTypmod int32
}

type SourceKeyKind uint8

const (
	SourceKeyID         SourceKeyKind = 1
	SourceKeyBundleNode SourceKeyKind = 2
)

type RelationSpec struct {
	Schema        string
	Name          string
	SourceKeyKind SourceKeyKind
	Columns       []ColumnSpec
}

type InetValue struct {
	Family  uint8
	Prefix  uint8
	Address []byte
}

type Dimension struct {
	Length     int32
	LowerBound int32
}

type ArrayValue struct {
	ElementTag    TypeTag
	ElementTypmod int32
	Dimensions    []Dimension
	Elements      []Value
}

type JSONKind uint8

const (
	JSONNull JSONKind = iota
	JSONFalse
	JSONTrue
	JSONNumber
	JSONString
	JSONArray
	JSONObject
)

type JSONMember struct {
	Key   string
	Value JSONNode
}

type JSONNode struct {
	Kind     JSONKind
	Number   string
	String   string
	Elements []JSONNode
	Members  []JSONMember
}

// Value is an explicit union. Tag selects the only meaningful payload field.
// Null values carry only Tag and Null.
type Value struct {
	Tag          TypeTag
	Null         bool
	Bool         bool
	Int          int64
	UUID         UUID
	Bytes        []byte
	Text         string
	Microseconds int64
	Days         int32
	Numeric      string
	JSON         JSONNode
	Array        *ArrayValue
	Inet         InetValue
}

type Row struct {
	Spec      RelationSpec
	Tenant    UUID
	SourceKey []byte
	Values    []Value
}

type EncodedRow struct {
	Schema    string
	Relation  string
	Tenant    UUID
	SourceKey []byte
	Bytes     []byte
	seal      [32]byte
}

type RowSetBuilder struct {
	expected  uint64
	count     uint64
	mac       hash.Hash
	lastKey   []byte
	finalized bool
}
