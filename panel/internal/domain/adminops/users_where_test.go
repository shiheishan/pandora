package adminops

import (
	"strings"
	"testing"
)

// 用户列表只拼用到的筛选（审计 P12）：没有万能条件，参数个数与占位符一一对应
func TestListUsersWhereEmitsOnlyUsedFilters(t *testing.T) {
	const tenant = "11111111-1111-4111-8111-111111111111"
	where := func(in ListUsersInput) (string, []any) {
		t.Helper()
		_, args, err := listUsersArgs(tenant, in)
		if err != nil {
			t.Fatal(err)
		}
		return listUsersWhere(args)
	}

	w, args := where(ListUsersInput{})
	if w != "u.tenant_id = $1" || len(args) != 1 || args[0] != tenant {
		t.Fatalf("no filter: where=%q args=%v", w, args)
	}

	w, args = where(ListUsersInput{Query: "Alice"})
	if !strings.Contains(w, "lower(u.email) LIKE $2") || !strings.Contains(w, "sc.token_hash = $3") ||
		strings.Contains(w, "u.id =") || len(args) != 3 || args[1] != "%alice%" {
		t.Fatalf("text query: where=%q args=%v", w, args)
	}

	// 规范 uuid 才按主键精确匹配；与原来 u.id::text = 小写原文 同一结果
	id := "22222222-2222-4222-8222-222222222222"
	w, args = where(ListUsersInput{Query: strings.ToUpper(id)})
	if !strings.Contains(w, "u.id = $3::uuid") || args[2] != id {
		t.Fatalf("uuid query: where=%q args=%v", w, args)
	}

	w, args = where(ListUsersInput{Status: "active, banned", GroupID: "none", SubState: "expired"})
	for _, want := range []string{"u.status::text = ANY($2::text[])", "u.user_group_id IS NULL", "$3 = 'expired'"} {
		if !strings.Contains(w, want) {
			t.Fatalf("filters: where=%q missing %q", w, want)
		}
	}
	if len(args) != 3 || strings.Contains(w, "= ''") {
		t.Fatalf("filters: where=%q args=%v", w, args)
	}
}
