package nodefabric

import (
	"os"
	"strings"
	"testing"
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

	source, err := os.ReadFile("uniproxy.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	if strings.Count(body, `PoolAdmitsUserSQL("s.tenant_id", "$2::uuid", "s.user_id")`) != 1 {
		t.Fatal("ListNodeUsers must apply the pool user-group admission exactly once")
	}
	if strings.Contains(body, "$2::uuid IS NULL") {
		t.Fatal("ListNodeUsers must not treat pool-less nodes as public (R104)")
	}
}
