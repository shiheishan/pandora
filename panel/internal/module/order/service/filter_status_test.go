package service

import (
	"errors"
	"slices"
	"testing"
)

// 列表筛选只认库里的 5 态，外加等产品确认的两个退款态；draft、processing 这类库里没有的状态回 ErrUnknownStatus。
func TestNormalizeStatuses(t *testing.T) {
	got, err := normalizeStatuses([]string{" paid ", "fulfilled", "paid", "", "refunded"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"paid", "fulfilled", "refunded"}; !slices.Equal(got, want) {
		t.Fatalf("去空白、去重、保序后应为 %v，得到 %v", want, got)
	}
	for _, st := range []string{"draft", "processing", "trialing"} {
		if _, err := normalizeStatuses([]string{"paid", st}); !errors.Is(err, ErrUnknownStatus) {
			t.Fatalf("%s 应回 ErrUnknownStatus，得到 %v", st, err)
		}
	}
	if got, err := normalizeStatuses(nil); err != nil || got != nil {
		t.Fatalf("不筛状态应为 nil、nil，得到 %v、%v", got, err)
	}
}
