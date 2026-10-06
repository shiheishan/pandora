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
