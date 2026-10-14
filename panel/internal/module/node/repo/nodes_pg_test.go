package repo

// 节点写库错误翻译与列表读模型的 PG 测试。没有 PANDORA_TEST_DATABASE_URL 时自动跳过。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

func inventoryTestTag(t *testing.T) string {
	t.Helper()
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// inventorySavepoint 用保存点包住一次会撞库约束的写，出错时回到保存点，外层回滚事务还能接着用。
func inventorySavepoint(t *testing.T, r *Repo, ctx context.Context, fn func() error) error {
	t.Helper()
	if err := r.d.Conn(ctx).Exec(`SAVEPOINT inv_repo_sp`).Error; err != nil {
		t.Fatal(err)
	}
	err := fn()
	q := `RELEASE SAVEPOINT inv_repo_sp`
	if err != nil {
		q = `ROLLBACK TO SAVEPOINT inv_repo_sp`
	}
	if xerr := r.d.Conn(ctx).Exec(q).Error; xerr != nil {
		t.Fatal(xerr)
	}
	return err
}

func inventoryTestNode(serverID uuid.UUID, name string, port int, l4 string) *model.Node {
	typ, host := "shadowsocks", "203.0.113.10"
	return &model.Node{
		ServerID: &serverID, Name: name, ServingStatus: "draft", NodeType: &typ, ServerHost: &host,
		ServerPort: &port, ListenL4: &l4, ProtocolConfig: `{"cipher":"aes-256-gcm"}`,
		ProtocolSchemaVersion: 1, TrafficRate: decimal.NewFromInt(1),
	}
}

func TestInventoryErrTranslation(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	tag := inventoryTestTag(t)

	srv := model.Server{Name: "repo-srv-" + tag, CapacityNodes: 8}
	if err := r.InsertServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	if srv.ID == uuid.Nil || srv.Status != "draft" || srv.RowVersion != 1 {
		t.Fatalf("回填不对：%+v", srv)
	}

	first := inventoryTestNode(srv.ID, "a-"+tag, 23001, "tcp")
	if err := r.InsertNode(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.ID == uuid.Nil || first.NodeNo == 0 {
		t.Fatalf("回填不对：%+v", first)
	}

	// 同服务器同端口同 L4
	err := inventorySavepoint(t, r, ctx, func() error {
		return r.InsertNode(ctx, inventoryTestNode(srv.ID, "b-"+tag, 23001, "tcp"))
	})
	if !errors.Is(err, ErrListenClaimTaken) {
		t.Fatalf("期望 ErrListenClaimTaken，得到 %v", err)
	}
	// 同名不同端口
	err = inventorySavepoint(t, r, ctx, func() error {
		return r.InsertNode(ctx, inventoryTestNode(srv.ID, "a-"+tag, 23002, "tcp"))
	})
	if !errors.Is(err, ErrNodeNameTaken) {
		t.Fatalf("期望 ErrNodeNameTaken，得到 %v", err)
	}
	// 服务器重名
	err = inventorySavepoint(t, r, ctx, func() error {
		return r.InsertServer(ctx, &model.Server{Name: "repo-srv-" + tag, CapacityNodes: 8})
	})
	if !errors.Is(err, ErrServerNameTaken) {
		t.Fatalf("期望 ErrServerNameTaken，得到 %v", err)
	}
	// CHECK 约束：端口与 L4 必须同时有
	err = inventorySavepoint(t, r, ctx, func() error {
		n := inventoryTestNode(srv.ID, "c-"+tag, 23003, "tcp")
		n.ListenL4 = nil
		return r.InsertNode(ctx, n)
	})
	if !errors.Is(err, ErrInventoryCheck) {
		t.Fatalf("期望 ErrInventoryCheck，得到 %v", err)
	}

	// 退役释放端口
	ok, err := r.SetNodeServing(ctx, first.ID, "draft", first.RowVersion, "retired")
	if err != nil || !ok {
		t.Fatalf("退役: %v %v", ok, err)
	}
	if err := r.InsertNode(ctx, inventoryTestNode(srv.ID, "d-"+tag, 23001, "tcp")); err != nil {
		t.Fatalf("退役后同端口应能再建: %v", err)
	}

	holder, taken, err := r.NodePortHolder(ctx, srv.ID, 23001, "tcp", nil)
	if err != nil || !taken || holder != "d-"+tag {
		t.Fatalf("占用者 %q %v %v", holder, taken, err)
	}
	n, err := r.CountLiveNodesOnServer(ctx, srv.ID)
	if err != nil || n != 1 {
		t.Fatalf("未退役节点数 %d %v", n, err)
	}
}

func TestListNodePageEmpty(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	missing := uuid.New()
	rows, total, err := r.ListNodePage(ctx, NodeListFilter{ID: &missing, Limit: 10, IncludeRetired: true})
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 || total != 0 {
		t.Fatalf("空页应为非 nil 空切片、总数 0：%v %d", rows, total)
	}
	rows, total, err = r.ListNodePage(ctx, NodeListFilter{ID: &missing, Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if rows == nil || len(rows) != 0 || total != 0 {
		t.Fatalf("越界空页：%v %d", rows, total)
	}
}

func TestServerReadsHostAddress(t *testing.T) {
	d := testkit.DB(t)
	ctx := testkit.TxCtx(t, d)
	r := New(d)
	v4, v6 := "203.0.113.7", "2001:db8::7"
	srv := model.Server{Name: "repo-ip-" + inventoryTestTag(t), CapacityNodes: 4, PublicIPv4: &v4, PublicIPv6: &v6}
	if err := r.InsertServer(ctx, &srv); err != nil {
		t.Fatal(err)
	}
	got, found, err := r.ServerByID(ctx, srv.ID, false)
	if err != nil || !found {
		t.Fatalf("读服务器: %v %v", found, err)
	}
	if got.PublicIPv4 == nil || *got.PublicIPv4 != v4 || got.PublicIPv6 == nil || *got.PublicIPv6 != v6 || got.PrivateIPv4 != nil {
		t.Fatalf("inet 列应读成纯地址：%v %v %v", got.PublicIPv4, got.PublicIPv6, got.PrivateIPv4)
	}
}
