package ca42authorityprod

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type apiSignature struct {
	parameters []string
	results    []string
}

func TestProductionAPISurfaceIsExactAndHasNoScalarMutationOpener(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller source unavailable")
	}
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, filepath.Dir(filename), func(info os.FileInfo) bool {
		return filepath.Ext(info.Name()) == ".go" && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := packages["ca42authorityprod"]
	if pkg == nil {
		t.Fatal("authority production package missing")
	}
	allowedFunctions := map[string]apiSignature{
		"OpenProductionRoot": {results: []string{"*Root", "error"}},
		"IsBusy":             {parameters: []string{"error"}, results: []string{"bool"}},
		"IsCommitAmbiguous":  {parameters: []string{"error"}, results: []string{"bool"}},
	}
	allowedMethods := map[string]map[string]apiSignature{
		"Root": {
			"Revalidate": {results: []string{"error"}},
			"Close":      {results: []string{"error"}},
		},
		"Reservation": {
			"Planned":          {results: []string{"ca42authority.Ledger", "bool", "error"}},
			"Commit":           {results: []string{"ca42authority.Ledger", "bool", "error"}},
			"CommittedBinding": {results: []string{"Binding", "error"}},
			"Close":            {results: []string{"error"}},
		},
		"Binding": {
			"RevalidateCommitted": {parameters: []string{"context.Context"}, results: []string{"error"}},
			"ValuesAt":            {parameters: []string{"context.Context"}, results: []string{"BindingValues", "error"}},
		},
	}
	allowedTypes := map[string]bool{"Root": true, "Reservation": true, "Binding": true, "BindingValues": true}
	allowedConstants := map[string]bool{"RootPath": true, "RecordName": true}
	allowedValueFields := map[string]string{
		"LedgerBeforeSHA256": "[sha256.Size]byte", "LedgerPlannedSHA256": "[sha256.Size]byte",
		"LedgerCommittedSHA256": "[sha256.Size]byte", "DescriptorSHA256": "[sha256.Size]byte",
		"PreviousDescriptorSHA256": "[sha256.Size]byte", "ManifestSHA256": "[sha256.Size]byte",
		"AuthorityEpoch": "uint64", "AuthoritySequence": "uint64", "TrustedEpoch": "int64",
		"ClockFloorEpoch": "int64", "ExactPlan": "bool",
	}
	seenFunctions, seenMethods := map[string]bool{}, map[string]bool{}
	seenTypes, seenConstants, seenFields := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if !typed.Name.IsExported() {
					continue
				}
				got := apiSignature{parameters: fieldTypes(set, typed.Type.Params), results: fieldTypes(set, typed.Type.Results)}
				if typed.Recv == nil {
					want, allowed := allowedFunctions[typed.Name.Name]
					if !allowed || !sameSignature(got, want) {
						t.Fatalf("unexpected exported function %s signature %+v", typed.Name.Name, got)
					}
					seenFunctions[typed.Name.Name] = true
					continue
				}
				receiver := receiverName(typed.Recv.List[0].Type)
				want, allowed := allowedMethods[receiver][typed.Name.Name]
				if !allowed || !sameSignature(got, want) {
					t.Fatalf("unexpected exported method %s.%s signature %+v", receiver, typed.Name.Name, got)
				}
				seenMethods[receiver+"."+typed.Name.Name] = true
			case *ast.GenDecl:
				if typed.Tok == token.VAR {
					for _, spec := range typed.Specs {
						for _, name := range spec.(*ast.ValueSpec).Names {
							if name.IsExported() {
								t.Fatalf("rebindable exported variable forbidden: %s", name.Name)
							}
						}
					}
				}
				for _, spec := range typed.Specs {
					switch value := spec.(type) {
					case *ast.TypeSpec:
						if !value.Name.IsExported() {
							continue
						}
						if !allowedTypes[value.Name.Name] {
							t.Fatalf("unexpected exported type %s", value.Name.Name)
						}
						seenTypes[value.Name.Name] = true
						structure, ok := value.Type.(*ast.StructType)
						if !ok {
							t.Fatalf("exported type %s is not an opaque/value struct", value.Name.Name)
						}
						for _, field := range structure.Fields.List {
							if len(field.Names) == 0 {
								t.Fatalf("anonymous exported-type field forbidden in %s", value.Name.Name)
							}
							for _, name := range field.Names {
								if !name.IsExported() {
									continue
								}
								wantType, allowed := allowedValueFields[name.Name]
								gotType := expressionText(set, field.Type)
								if value.Name.Name != "BindingValues" || !allowed || gotType != wantType {
									t.Fatalf("unexpected exported field %s.%s %s", value.Name.Name, name.Name, gotType)
								}
								seenFields[name.Name] = true
							}
						}
					case *ast.ValueSpec:
						if typed.Tok != token.CONST {
							continue
						}
						for _, name := range value.Names {
							if name.IsExported() {
								if !allowedConstants[name.Name] {
									t.Fatalf("unexpected exported constant %s", name.Name)
								}
								seenConstants[name.Name] = true
							}
						}
					}
				}
			}
		}
	}
	for name := range allowedFunctions {
		if !seenFunctions[name] {
			t.Fatalf("required exported function missing: %s", name)
		}
	}
	for receiver, methods := range allowedMethods {
		for method := range methods {
			if !seenMethods[receiver+"."+method] {
				t.Fatalf("required opaque method missing: %s.%s", receiver, method)
			}
		}
	}
	for name := range allowedTypes {
		if !seenTypes[name] {
			t.Fatalf("required exported type missing: %s", name)
		}
	}
	for name := range allowedConstants {
		if !seenConstants[name] {
			t.Fatalf("required exported constant missing: %s", name)
		}
	}
	for name := range allowedValueFields {
		if !seenFields[name] {
			t.Fatalf("required diagnostic field missing: %s", name)
		}
	}
}

func sameSignature(actual, expected apiSignature) bool {
	return strings.Join(actual.parameters, ",") == strings.Join(expected.parameters, ",") &&
		strings.Join(actual.results, ",") == strings.Join(expected.results, ",")
}

func receiverName(expression ast.Expr) string {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	return ""
}

func fieldTypes(set *token.FileSet, fields *ast.FieldList) []string {
	if fields == nil {
		return nil
	}
	result := make([]string, 0, len(fields.List))
	for _, field := range fields.List {
		value := expressionText(set, field.Type)
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for index := 0; index < count; index++ {
			result = append(result, value)
		}
	}
	return result
}

func expressionText(set *token.FileSet, expression ast.Expr) string {
	var output bytes.Buffer
	if err := format.Node(&output, set, expression); err != nil {
		return "<format-error>"
	}
	return output.String()
}
