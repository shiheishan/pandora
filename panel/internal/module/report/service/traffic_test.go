package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestTrafficWindowDefaults(t *testing.T) {
	e := newTestEnv()
	w, limit, err := e.svc.trafficWindow(TrafficQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if w.Range != "7d" || limit != 10 {
		t.Fatalf("range/limit: %q %d", w.Range, limit)
	}
	if !w.SnapshotAt.Equal(testNow) {
		t.Fatalf("snapshot: %v want %v", w.SnapshotAt, testNow)
	}
	wantFrom := time.Date(2026, 3, 3, 3, 0, 0, 0, time.UTC)
	if !w.From.Equal(wantFrom) || !w.To.Equal(testNow) {
		t.Fatalf("window: from=%v to=%v", w.From, w.To)
	}
}

func TestTrafficWindowValidation(t *testing.T) {
	e := newTestEnv()

	cases := []struct {
		name  string
		q     TrafficQuery
		field string
		valid bool
		check func(TrafficWindow)
	}{
		{name: "range", q: TrafficQuery{Range: "1h"}, field: "range"},
		{name: "limit", q: TrafficQuery{Limit: 7}, field: "limit"},
		{name: "snapshot bad", q: TrafficQuery{SnapshotAt: "yesterday"}, field: "snapshot_at"},
		{
			name:  "snapshot future",
			q:     TrafficQuery{SnapshotAt: testNow.Add(time.Minute).Format(time.RFC3339Nano)},
			field: "snapshot_at",
		},
		{
			name:  "snapshot old",
			q:     TrafficQuery{SnapshotAt: testNow.Add(-32 * 24 * time.Hour).Format(time.RFC3339Nano)},
			field: "snapshot_at",
		},
		{
			name:  "snapshot tz ok",
			q:     TrafficQuery{Range: "24h", SnapshotAt: "2026-03-09T12:00:00+08:00"},
			valid: true,
			check: func(w TrafficWindow) {
				wantSnap := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
				wantFrom := time.Date(2026, 3, 8, 4, 0, 0, 0, time.UTC)
				if !w.SnapshotAt.Equal(wantSnap) || !w.From.Equal(wantFrom) {
					t.Fatalf("snap/from: %v %v", w.SnapshotAt, w.From)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, _, err := e.svc.trafficWindow(tc.q)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if tc.check != nil {
					tc.check(w)
				}
				return
			}
			var ve *errs.Error
			if !errors.As(err, &ve) || ve.Code != errs.ValidationFailed {
				t.Fatalf("err=%v", err)
			}
			if ve.Fields[tc.field] == "" {
				t.Fatalf("fields=%v want key %q", ve.Fields, tc.field)
			}
		})
	}
}

func TestNodeTrafficRanking(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	idLow := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	idHigh := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	idSmall := uuid.MustParse("00000000-0000-4000-8000-000000000003")

	e.ports.nodeTraffic = []nodesvc.NodeTraffic{
		{NodeID: idSmall, Name: "n3", Upload: 100, Download: 200, ActiveHours: 1, Cells: 2},
		{NodeID: idHigh, Name: "n2", Upload: 250, Download: 250, ActiveHours: 2, Cells: 3},
		{NodeID: idLow, Name: "n1", Upload: 250, Download: 250, ActiveHours: 2, Cells: 3},
	}
	e.ports.subTraffic = []nodesvc.SubscriptionTraffic{
		{SubscriptionID: uuid.New(), Upload: 200, Download: 100},
		{SubscriptionID: uuid.New(), Upload: 200, Download: 100},
	}
	e.ports.trafficQuality = nodesvc.TrafficQuality{InvalidReports: 2, InvalidEntries: 5}

	out, err := e.svc.NodeTraffic(ctx, TrafficQuery{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 3 {
		t.Fatalf("items len %d", len(out.Items))
	}
	if out.Items[0].NodeID != idLow || out.Items[1].NodeID != idHigh || out.Items[2].NodeID != idSmall {
		t.Fatalf("order: %s %s %s", out.Items[0].NodeID, out.Items[1].NodeID, out.Items[2].NodeID)
	}
	if out.Totals.Reported != 1300 || out.Totals.Attributed != 600 || out.Totals.Unattributed != 700 {
		t.Fatalf("totals %+v", out.Totals)
	}
	if out.Returned != 1300 || out.Other != 0 {
		t.Fatalf("returned/other %d %d", out.Returned, out.Other)
	}
	if out.Quality.DuplicateReports != 0 || out.Quality.InvalidReports != 2 {
		t.Fatalf("quality %+v", out.Quality)
	}
	if len(e.ports.trafficCalls) == 0 || !e.ports.trafficCalls[0][0].Equal(out.Window.From) || !e.ports.trafficCalls[0][1].Equal(out.Window.To) {
		t.Fatalf("trafficCalls[0]=%v window %v-%v", e.ports.trafficCalls[0], out.Window.From, out.Window.To)
	}

	// 六节点、Limit 5：Other 为未进榜的第 6 名流量（总量 95）
	e2 := newTestEnv()
	ids := []uuid.UUID{
		uuid.MustParse("00000000-0000-4000-8000-000000000010"),
		uuid.MustParse("00000000-0000-4000-8000-000000000011"),
		uuid.MustParse("00000000-0000-4000-8000-000000000012"),
		uuid.MustParse("00000000-0000-4000-8000-000000000013"),
		uuid.MustParse("00000000-0000-4000-8000-000000000014"),
		uuid.MustParse("00000000-0000-4000-8000-000000000015"),
	}
	six := make([]nodesvc.NodeTraffic, 6)
	for i, id := range ids {
		six[i] = nodesvc.NodeTraffic{NodeID: id, Name: "n", Upload: int64(100 - i), Download: 0}
	}
	e2.ports.nodeTraffic = six
	out2, err := e2.svc.NodeTraffic(ctx, TrafficQuery{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if out2.Other != 95 {
		t.Fatalf("other=%d want 95", out2.Other)
	}
}

func TestUserTrafficAggregatesAndMasks(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	u1 := uuid.MustParse("10000000-0000-4000-8000-000000000001")
	u2 := uuid.MustParse("10000000-0000-4000-8000-000000000002")
	subA := uuid.MustParse("20000000-0000-4000-8000-000000000001")
	subB := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	subC := uuid.MustParse("20000000-0000-4000-8000-000000000003")
	subD := uuid.MustParse("20000000-0000-4000-8000-000000000004")

	e.ports.nodeTraffic = []nodesvc.NodeTraffic{{NodeID: uuid.New(), Upload: 1000, Download: 0}}
	e.ports.subTraffic = []nodesvc.SubscriptionTraffic{
		{SubscriptionID: subA, Upload: 10, Download: 20, Cells: 1},
		{SubscriptionID: subB, Upload: 30, Download: 40, Cells: 2},
		{SubscriptionID: subC, Upload: 100, Download: 0, Cells: 1},
		{SubscriptionID: subD, Upload: 500, Download: 500, Cells: 1},
	}
	e.ports.owners = map[uuid.UUID]uuid.UUID{
		subA: u1, subB: u1, subC: u2,
	}
	e.ports.emails = map[uuid.UUID]string{u1: "Alice@Example.TEST"}

	out, err := e.svc.UserTraffic(ctx, TrafficQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if out.Totals.Attributed != 1200 {
		t.Fatalf("attributed %d", out.Totals.Attributed)
	}

	var u1row, u2row *UserTrafficItem
	for i := range out.Items {
		switch out.Items[i].UserID {
		case u1:
			u1row = &out.Items[i]
		case u2:
			u2row = &out.Items[i]
		}
	}
	if u1row == nil || u2row == nil {
		t.Fatal("missing user rows")
	}
	if u1row.Upload != 40 || u1row.Download != 60 || u1row.SubscriptionCount != 2 || u1row.Total != 100 {
		t.Fatalf("u1 %+v", *u1row)
	}
	if u1row.EmailMasked != "A***@example.test" {
		t.Fatalf("u1 email %q", u1row.EmailMasked)
	}
	if u2row.EmailMasked != "***" {
		t.Fatalf("u2 email %q", u2row.EmailMasked)
	}
	attributed := out.Totals.Attributed
	var returned int64
	for _, it := range out.Items {
		returned += it.Total
	}
	wantOther := attributed - returned
	if out.Other != wantOther || wantOther != 1000 {
		t.Fatalf("other=%d want %d (D=1000 unattributed to ranked users)", out.Other, wantOther)
	}
}

func TestUserTrafficChunksOwners(t *testing.T) {
	e := newTestEnv()
	ctx := context.Background()

	subs := make([]nodesvc.SubscriptionTraffic, 5001)
	for i := range subs {
		subs[i] = nodesvc.SubscriptionTraffic{SubscriptionID: uuid.New(), Upload: 1}
	}
	e.ports.subTraffic = subs

	_, err := e.svc.UserTraffic(ctx, TrafficQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{5000, 1}
	if len(e.ports.ownerBatches) != len(want) {
		t.Fatalf("batches %v", e.ports.ownerBatches)
	}
	for i, n := range want {
		if e.ports.ownerBatches[i] != n {
			t.Fatalf("batch[%d]=%d want %d", i, e.ports.ownerBatches[i], n)
		}
	}
}

func TestMaskEmail(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"bob@example.test", "b***@example.test"},
		{"@x", "***"},
		{"x@", "***"},
		{"无at", "***"},
		{"张三@例子.test", "张***@例子.test"},
	}
	for _, tc := range cases {
		if got := maskEmail(tc.in); got != tc.want {
			t.Fatalf("%q -> %q want %q", tc.in, got, tc.want)
		}
	}
}
