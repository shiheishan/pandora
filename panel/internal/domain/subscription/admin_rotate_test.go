// [INPUT]: 依赖同包 admin_rotate.go 的 AdminRotateOutput
// [OUTPUT]: 对外提供 TestAdminRotateOutputHoldsNoToken
// [POS]: domain/subscription 的单元测试：换发结果结构里没有能带令牌的字段（D-B-1）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"reflect"
	"testing"
)

// D-B-1：管理员换发链接的结果里不能有任何能携带令牌明文的字段，
// 这样无论处理器怎么拼响应，都拿不到新令牌。
func TestAdminRotateOutputHoldsNoToken(t *testing.T) {
	typ := reflect.TypeOf(AdminRotateOutput{})
	if typ.NumField() != 1 || typ.Field(0).Name != "UserEmail" {
		t.Fatalf("AdminRotateOutput fields changed: %v; a new field must not carry the subscription token", typ)
	}
}
