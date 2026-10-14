package db

import "testing"

func TestRuntimeRoleValidate(t *testing.T) {
	ok := runtimeRole{current: "pandora_app", session: "pandora_app", searchPath: RequiredSearchPath}
	if err := ok.validate(); err != nil {
		t.Fatalf("收窄过的运行角色应通过: %v", err)
	}

	cases := map[string]func(r *runtimeRole){
		"切换角色":            func(r *runtimeRole) { r.session = "postgres" },
		"超级用户":            func(r *runtimeRole) { r.super = true },
		"会话角色是超级用户":       func(r *runtimeRole) { r.sessionSuper = true },
		"能建角色":            func(r *runtimeRole) { r.createRole = true },
		"能建库":             func(r *runtimeRole) { r.createDB = true },
		"有 TEMPORARY":     func(r *runtimeRole) { r.dbTemp = true },
		"能在库里建 schema":    func(r *runtimeRole) { r.dbCreate = true },
		"能在 public 建对象":   func(r *runtimeRole) { r.publicCreate = true },
		"是表属主":            func(r *runtimeRole) { r.ownsRelations = true },
		"search_path 没钉住": func(r *runtimeRole) { r.searchPath = `"$user", public` },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := ok
			mutate(&r)
			if err := r.validate(); err == nil {
				t.Fatal("应拒绝")
			}
		})
	}
}
