package node

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"testing"
)

func TestUniProxyTokenPrefersBearerAndKeepsLegacyFallback(t *testing.T) {
	const target = "https://panel.test/config?node_id=4e3b09eb-153f-4cbe-9f82-14b723aa4543&token=query-secret"
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "absent keeps legacy query fallback", want: "query-secret"},
		{name: "valid bearer wins", values: []string{"Bearer header-secret"}, want: "header-secret"},
		{name: "bearer scheme is case insensitive", values: []string{"bearer header-secret"}, want: "header-secret"},
		{name: "explicit empty fails closed", values: []string{""}},
		{name: "explicit whitespace fails closed", values: []string{" \t "}},
		{name: "wrong scheme fails closed", values: []string{"Basic ignored"}},
		{name: "missing bearer token fails closed", values: []string{"Bearer"}},
		{name: "extra bearer fields fail closed", values: []string{"Bearer one two"}},
		{name: "repeated authorization fails closed", values: []string{"", "Bearer header-secret"}},
		{name: "repeated valid authorization fails closed", values: []string{"Bearer one", "Bearer two"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", target, nil)
			for _, value := range tc.values {
				req.Header.Add("Authorization", value)
			}
			if got := uniProxyToken(req); got != tc.want {
				t.Fatalf("uniProxyToken() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAuthNodeDelegatesCredentialSelection prevents authNode from rejecting
// legacy query credentials before uniProxyToken can apply the compatibility
// policy. Credential-source precedence belongs exclusively in uniProxyToken.
func TestAuthNodeDelegatesCredentialSelection(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "handlers.go", nil, 0)
	if err != nil {
		t.Fatalf("parse handlers.go: %v", err)
	}
	var authNode *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "authNode" {
			authNode = fn
			break
		}
	}
	if authNode == nil {
		t.Fatal("authNode declaration not found")
	}
	foundSelector := false
	ast.Inspect(authNode.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "uniProxyToken" {
				foundSelector = true
			}
		case *ast.SelectorExpr:
			if v.Sel.Name == "Header" {
				t.Errorf("authNode reads request headers directly; delegate credential selection to uniProxyToken")
			}
		}
		return true
	})
	if !foundSelector {
		t.Fatal("authNode no longer delegates credential selection to uniProxyToken")
	}
}
