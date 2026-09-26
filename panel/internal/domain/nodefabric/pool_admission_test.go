// [INPUT]: 依赖 PoolAdmitsUserSQL，依赖 platform/sourcetest 按名取 Service.ListNodeUsers 与整包源码
// [OUTPUT]: 对外提供 TestPoolAdmitsUserSQLIsTheOnlyAdmissionRule
// [POS]: nodefabric 节点池用户组准入只有 PoolAdmitsUserSQL 一个出处，节点用户列表恰好用一次，无池节点不当公开（R104）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 池限定用户组（R104）的谓词只有一份：节点拉用户与订阅下载各用一次，
// 参数只收白名单里的静态表达式，其余一律 panic——拼进 SQL 的东西不能来自数据。
func TestPoolAdmitsUserSQLIsTheOnlyAdmissionRule(t *testing.T) {
	for _, args := range [][3]string{
		{"s.tenant_id", "$2::uuid", "s.user_id"},
		{"n.tenant_id", "n.pool_id", "$4::uuid"},
	} {
		sql := PoolAdmitsUserSQL(args[0], args[1], args[2])
		for _, want := range []string{"NOT EXISTS", "node_pool_user_groups", "npu.user_group_id = npug.user_group_id", "npu.id = " + args[2]} {
			if !strings.Contains(sql, want) {
				t.Fatalf("PoolAdmitsUserSQL%v missing %q: %s", args, want, sql)
			}
		}
	}
	for _, bad := range [][3]string{
		{"s.tenant_id", "$2::uuid", "'x' OR true"},
		{"n.tenant_id", "$2::uuid", "s.user_id"},
		{"", "", ""},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("PoolAdmitsUserSQL%v did not panic", bad)
				}
			}()
			PoolAdmitsUserSQL(bad[0], bad[1], bad[2])
		}()
	}

	pkg := sourcetest.Load(t, ".")
	body := pkg.Source()
	if !strings.Contains(pkg.Decl("Service.ListNodeUsers"), `PoolAdmitsUserSQL("s.tenant_id", "$2::uuid", "s.user_id")`) ||
		strings.Count(body, `PoolAdmitsUserSQL("s.tenant_id", "$2::uuid", "s.user_id")`) != 1 {
		t.Fatal("ListNodeUsers must apply the pool user-group admission exactly once")
	}
	if strings.Contains(body, "$2::uuid IS NULL") {
		t.Fatal("ListNodeUsers must not treat pool-less nodes as public (R104)")
	}
}
