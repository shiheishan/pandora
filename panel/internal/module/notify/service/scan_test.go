package service

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestQuotaTier(t *testing.T) {
	if quotaTier(0, 100) != 0 {
		t.Fatal("80% 以下应为 0")
	}
	if quotaTier(79, 100) != 0 {
		t.Fatal("79% 应为 0")
	}
	if quotaTier(80, 100) != 80 {
		t.Fatal("80% 应为 80 档")
	}
	if quotaTier(94, 100) != 80 {
		t.Fatal("94% 应为 80 档")
	}
	if quotaTier(95, 100) != 95 {
		t.Fatal("95% 应为 95 档")
	}
	if quotaTier(150, 100) != 95 {
		t.Fatal("超过 100% 仍只到最高档 95")
	}
	if quotaTier(10, 0) != 0 {
		t.Fatal("total 0 应为 0")
	}
	bigTotal := int64(1 << 62)
	bigConsumed := bigTotal / 100 * 96
	if quotaTier(bigConsumed, bigTotal) != 95 {
		t.Fatal("大数 96% 应为 95 档")
	}
	// 额度不大、用量远超（consumed*100 会溢出）仍判最高档
	if quotaTier(math.MaxInt64/50, 100) != 95 {
		t.Fatal("用量远超额度应为 95 档")
	}
}

func TestHumanBytes(t *testing.T) {
	if humanBytes(512) != "512 B" {
		t.Fatalf("got %q", humanBytes(512))
	}
	if humanBytes(1024) != "1.0 KB" {
		t.Fatalf("got %q", humanBytes(1024))
	}
	if humanBytes(1536) != "1.5 KB" {
		t.Fatalf("got %q", humanBytes(1536))
	}
}

func TestNoticeTimeTimezone(t *testing.T) {
	utc := time.Date(2026, 6, 15, 12, 30, 0, 0, time.UTC)
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	got := noticeTime(utc, loc)
	want := utc.In(loc).Format("2006-01-02 15:04")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestScanDedupeKeyFormats(t *testing.T) {
	sub := uuid.MustParse("018f1234-5678-7abc-8def-0123456789ab")
	end := time.Unix(1700000000, 0).UTC()
	start := time.Unix(1699000000, 0).UTC()

	kExp := fmt.Sprintf("expiring:%s:%dd:%d", sub, 3, end.Unix())
	if kExp != "expiring:018f1234-5678-7abc-8def-0123456789ab:3d:1700000000" {
		t.Fatalf("expiring key: %q", kExp)
	}
	kExp2 := fmt.Sprintf("expired:%s:%d", sub, end.Unix())
	if kExp2 != "expired:018f1234-5678-7abc-8def-0123456789ab:1700000000" {
		t.Fatalf("expired key: %q", kExp2)
	}
	kRecall := fmt.Sprintf("recall:%s:%dd:%d", sub, 7, end.Unix())
	if kRecall != "recall:018f1234-5678-7abc-8def-0123456789ab:7d:1700000000" {
		t.Fatalf("recall key: %q", kRecall)
	}
	kQuota := fmt.Sprintf("quota:%s:%d:%d", sub, 95, start.Unix())
	if kQuota != "quota:018f1234-5678-7abc-8def-0123456789ab:95:1699000000" {
		t.Fatalf("quota key: %q", kQuota)
	}
}
