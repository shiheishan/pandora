package protocol

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func fieldEnums(schema Schema, path string) []string {
	if e, ok := schema.Enums[path]; ok {
		return e
	}
	leaf := path
	if i := strings.LastIndex(path, "."); i >= 0 {
		leaf = path[i+1:]
	}
	if e, ok := schema.Enums[leaf]; ok {
		return e
	}
	if path == "cipher" || path == "method" {
		return schema.Methods
	}
	return nil
}

func enumAllowed(schema Schema, path string, value string) bool {
	if value == "" {
		return true
	}
	enums := fieldEnums(schema, path)
	if enums == nil {
		return true
	}
	return containsString(enums, value)
}

func TestSchemaFormMetadata(t *testing.T) {
	for _, schema := range Schemas() {
		t.Run(schema.NodeType, func(t *testing.T) {
			assertFormBasics(t, schema)
			assertRequiredNotAdvanced(t, schema)
			assertShowIf(t, schema)
			assertOptionLabels(t, schema)
			assertDefaults(t, schema)
		})
	}
}

func TestSchemaFormSpotChecks(t *testing.T) {
	byType := map[string]Schema{}
	for _, s := range Schemas() {
		byType[s.NodeType] = s
	}
	vless := byType["vless"]
	assertFieldDefault(t, vless, "network", "tcp")
	assertFieldNoDefault(t, vless, "tls")

	st := byType["shadowtls"]
	assertFieldDefault(t, st, "method", "aes-256-gcm")
	assertFieldDefault(t, st, "version", 3)

	hy2 := byType["hysteria2"]
	f := findField(hy2, "obfs.password")
	if f == nil {
		t.Fatal("hysteria2 缺少 obfs.password 字段")
	}
	want := map[string][]string{"obfs.type": {"salamander"}}
	if !reflect.DeepEqual(f.ShowIf, want) {
		t.Fatalf("hysteria2 obfs.password 的 show_if=%#v，期望 %#v", f.ShowIf, want)
	}

	assertFieldDefault(t, hy2, "cert_mode", "acme")
	assertFieldShowIf(t, hy2, "cert_path", map[string][]string{"cert_mode": {"file"}})
	assertFieldShowIf(t, hy2, "tls_settings.allow_insecure", map[string][]string{"cert_mode": {"file"}})

	trojan := byType["trojan"]
	assertFieldShowIf(t, trojan, "cert_mode", map[string][]string{"tls": {"1"}})
	assertFieldShowIf(t, trojan, "cert_path", map[string][]string{"tls": {"1"}, "cert_mode": {"file"}})
	assertFieldShowIf(t, trojan, "tls_settings.server_name", map[string][]string{"tls": {"1"}})

	socks := byType["socks"]
	assertFieldShowIf(t, socks, "cert_mode", map[string][]string{"tls": {"true"}})

	if findField(vless, "cert_mode") != nil {
		t.Fatal("vless 不应有 cert_mode 字段")
	}
	if vless.Enums["cert_mode"] != nil {
		t.Fatal("vless 不应有 cert_mode 枚举")
	}

	certModeTypes := []string{"hysteria2", "juicity", "socks", "http", "naive", "tuic", "anytls", "trojan"}
	for _, typ := range certModeTypes {
		s := byType[typ]
		enums := s.Enums["cert_mode"]
		if !reflect.DeepEqual(enums, []string{"acme", "file"}) {
			t.Fatalf("协议 %s：cert_mode 枚举=%#v，期望 [acme file]", typ, enums)
		}
		if !containsString(s.Required, "cert_mode") {
			t.Fatalf("协议 %s：Required 应含 cert_mode", typ)
		}
	}
}

func TestWithFormsDoesNotMutateInputHints(t *testing.T) {
	orig := map[string]string{"keep": "a"}
	in := []Schema{{
		NodeType:          "shadowsocks",
		AllowedProperties: []string{"cipher"},
		Hints:             orig,
	}}
	out := withForms(in)
	if out[0].Note == "" {
		t.Fatal("shadowsocks 应有 Note")
	}
	if orig["keep"] != "a" {
		t.Fatal("传入切片的 Hints map 被改了")
	}
	if len(orig) != 1 {
		t.Fatal("传入切片的 Hints 多了键")
	}
}

func assertFormBasics(t *testing.T, schema Schema) {
	if strings.TrimSpace(schema.Note) == "" {
		t.Fatalf("协议 %s：Note 为空", schema.NodeType)
	}
	allowed := make(map[string]int, len(schema.AllowedProperties))
	for _, p := range schema.AllowedProperties {
		allowed[p]++
	}
	seen := make(map[string]int, len(schema.Fields))
	for _, f := range schema.Fields {
		if strings.TrimSpace(f.Label) == "" {
			t.Fatalf("协议 %s 路径 %s：Label 为空", schema.NodeType, f.Path)
		}
		seen[f.Path]++
	}
	for p := range allowed {
		if seen[p] != 1 {
			t.Fatalf("协议 %s：AllowedProperties 路径 %s 在 Fields 中出现 %d 次（应为 1）", schema.NodeType, p, seen[p])
		}
	}
	for p, n := range seen {
		if allowed[p] != 1 {
			t.Fatalf("协议 %s：Fields 多出路径 %s（出现 %d 次）", schema.NodeType, p, n)
		}
	}
}

func assertRequiredNotAdvanced(t *testing.T, schema Schema) {
	byPath := map[string]FormField{}
	for _, f := range schema.Fields {
		byPath[f.Path] = f
	}
	for _, req := range schema.Required {
		f, ok := byPath[req]
		if !ok {
			t.Fatalf("协议 %s：必填路径 %s 不在 Fields 里", schema.NodeType, req)
		}
		if f.Advanced {
			t.Fatalf("协议 %s：必填字段 %s 不能标为高级", schema.NodeType, req)
		}
	}
}

func assertShowIf(t *testing.T, schema Schema) {
	allowed := make(map[string]struct{}, len(schema.AllowedProperties))
	for _, p := range schema.AllowedProperties {
		allowed[p] = struct{}{}
	}
	for _, f := range schema.Fields {
		for dep, vals := range f.ShowIf {
			if dep == f.Path {
				t.Fatalf("协议 %s 字段 %s：ShowIf 不能引用自己", schema.NodeType, f.Path)
			}
			if _, ok := allowed[dep]; !ok {
				t.Fatalf("协议 %s 字段 %s：ShowIf 键 %s 不在 AllowedProperties", schema.NodeType, f.Path, dep)
			}
			isBool := schema.PropertyTypes[dep] == "boolean"
			for _, v := range vals {
				if isBool && v != "true" && v != "false" && v != "" {
					t.Fatalf("协议 %s 字段 %s：ShowIf[%s] 取值 %q 非法（布尔字段）", schema.NodeType, f.Path, dep, v)
				}
				if !enumAllowed(schema, dep, v) {
					t.Fatalf("协议 %s 字段 %s：ShowIf[%s] 取值 %q 不在枚举里", schema.NodeType, f.Path, dep, v)
				}
			}
		}
	}
}

func assertOptionLabels(t *testing.T, schema Schema) {
	req := make(map[string]struct{}, len(schema.Required))
	for _, r := range schema.Required {
		req[r] = struct{}{}
	}
	for _, f := range schema.Fields {
		if len(f.OptionLabels) == 0 {
			continue
		}
		for k := range f.OptionLabels {
			if k == "" {
				if _, mandatory := req[f.Path]; mandatory {
					t.Fatalf("协议 %s 字段 %s：必填字段不能有 OptionLabels 的空键", schema.NodeType, f.Path)
				}
				continue
			}
			if !enumAllowed(schema, f.Path, k) {
				t.Fatalf("协议 %s 字段 %s：OptionLabels 键 %q 不是合法枚举值", schema.NodeType, f.Path, k)
			}
		}
	}
}

func assertDefaults(t *testing.T, schema Schema) {
	for _, f := range schema.Fields {
		if f.Default == nil {
			continue
		}
		pt := schema.PropertyTypes[f.Path]
		switch pt {
		case "number":
			if _, ok := f.Default.(int); !ok {
				t.Fatalf("协议 %s 字段 %s：Default 类型应为 int，实际 %T", schema.NodeType, f.Path, f.Default)
			}
		case "boolean":
			if _, ok := f.Default.(bool); !ok {
				t.Fatalf("协议 %s 字段 %s：Default 类型应为 bool，实际 %T", schema.NodeType, f.Path, f.Default)
			}
		default:
			if _, ok := f.Default.(string); !ok {
				t.Fatalf("协议 %s 字段 %s：Default 类型应为 string，实际 %T", schema.NodeType, f.Path, f.Default)
			}
		}
		if enums := fieldEnums(schema, f.Path); enums != nil {
			if !enumAllowed(schema, f.Path, fmt.Sprint(f.Default)) {
				t.Fatalf("协议 %s 字段 %s：Default %v 不在枚举里", schema.NodeType, f.Path, f.Default)
			}
		}
	}
}

func findField(schema Schema, path string) *FormField {
	for i := range schema.Fields {
		if schema.Fields[i].Path == path {
			return &schema.Fields[i]
		}
	}
	return nil
}

func assertFieldDefault(t *testing.T, schema Schema, path string, want any) {
	f := findField(schema, path)
	if f == nil {
		t.Fatalf("协议 %s 缺少字段 %s", schema.NodeType, path)
	}
	if !reflect.DeepEqual(f.Default, want) {
		t.Fatalf("协议 %s 字段 %s：Default=%v，期望 %v", schema.NodeType, path, f.Default, want)
	}
}

func assertFieldNoDefault(t *testing.T, schema Schema, path string) {
	f := findField(schema, path)
	if f == nil {
		t.Fatalf("协议 %s 缺少字段 %s", schema.NodeType, path)
	}
	if f.Default != nil {
		t.Fatalf("协议 %s 字段 %s：不应有 Default，实际 %v", schema.NodeType, path, f.Default)
	}
}

func assertFieldShowIf(t *testing.T, schema Schema, path string, want map[string][]string) {
	f := findField(schema, path)
	if f == nil {
		t.Fatalf("协议 %s 缺少字段 %s", schema.NodeType, path)
	}
	if !reflect.DeepEqual(f.ShowIf, want) {
		t.Fatalf("协议 %s 字段 %s：show_if=%#v，期望 %#v", schema.NodeType, path, f.ShowIf, want)
	}
}
