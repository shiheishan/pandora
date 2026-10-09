package nodefabric

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// 合并缓冲：同一行只收一次、按租户分组、组内按（节点, uid, 哈希）排好（写入时加锁顺序一致）。
func TestAliveBufferDedupesAndOrdersRows(t *testing.T) {
	b := newAliveBuffer()
	h := func(ip string) []byte { s := sha256.Sum256([]byte(ip)); return s[:] }
	b.add("t1", "n2", []int64{5, 3}, [][]byte{h("a"), h("b")})
	b.add("t1", "n1", []int64{9}, [][]byte{h("c")})
	b.add("t1", "n2", []int64{5}, [][]byte{h("a")}) // 下一份上报里的同一行
	b.add("t2", "n1", []int64{1}, [][]byte{h("d")})
	got, dropped := b.take()
	if dropped != 0 || len(got) != 2 || len(got["t1"]) != 3 || len(got["t2"]) != 1 {
		t.Fatalf("take = %+v dropped=%d", got, dropped)
	}
	rows := got["t1"]
	if rows[0].nodeID != "n1" || rows[1].uid != 3 || rows[2].uid != 5 || !bytes.Equal(rows[2].hash[:], h("a")) {
		t.Fatalf("rows not ordered by node, uid: %+v", rows)
	}
	if again, _ := b.take(); len(again) != 0 {
		t.Fatalf("take returned rows twice: %+v", again)
	}
}
