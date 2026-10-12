package testkit

import "testing"

func TestTxCtxRollsBack(t *testing.T) {
	d := DB(t)
	ctx := TxCtx(t, d)
	if err := d.Conn(ctx).Exec("CREATE TEMP TABLE testkit_probe (id int)").Error; err != nil {
		t.Fatal(err)
	}
}

func TestRedis(t *testing.T) {
	_, c := Redis(t)
	if err := c.Set(t.Context(), "k", "v", 0).Err(); err != nil {
		t.Fatal(err)
	}
}
