package handler

// 节点池与套餐绑池接口的 handler 测试：不连库，只测在碰数据库之前就返回的路径
// （路径参数、请求体、字段校验、端口未装配、条件重认证）与 DTO 形状（对照前端 zod schema）。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// poolReauthStub 记下重认证中间件被调的次数；deny 时按「需要重新验证」中止。
type poolReauthStub struct {
	calls int
	deny  bool
}

func (s *poolReauthStub) handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.calls++
		if s.deny {
			httpx.Fail(c, errs.New(errs.ReauthRequired))
			return
		}
		c.Next()
	}
}

// newPoolEngine 用一个没有数据库的 service 挂好后台路由。
func newPoolEngine(reauth *poolReauthStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	pass := func(c *gin.Context) { c.Next() }
	kit := route.Kit{
		RequireAuth:  pass,
		RecentReauth: reauth.handler(),
		Idempotency:  func(string) gin.HandlerFunc { return pass },
		Switch:       func(string) gin.HandlerFunc { return pass },
	}
	New(service.New(nil, nil, nil, nil)).RegisterAdmin(e.Group("/v1"), kit)
	return e
}

func doPool(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func poolErrBody(t *testing.T, w *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	var env struct {
		Error struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("错误信封解析失败: %v %s", err, w.Body)
	}
	return env.Error.Code, env.Error.Fields
}

func poolExpectErr(t *testing.T, w *httptest.ResponseRecorder, code errs.Code, field string) {
	t.Helper()
	if w.Code != errs.New(code).Status() {
		t.Fatalf("状态码 %d，期望 %d：%s", w.Code, errs.New(code).Status(), w.Body)
	}
	got, fields := poolErrBody(t, w)
	if got != string(code) {
		t.Fatalf("错误码 %q，期望 %q", got, code)
	}
	if field != "" && fields[field] == "" {
		t.Fatalf("fields 缺 %s：%v", field, fields)
	}
}

// poolJSONKeys 把 v 编成 JSON 再取对象的键（升序）。
func poolJSONKeys(t *testing.T, v any) ([]string, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return poolSortedKeys(m), m
}

func poolSortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func poolExpectKeys(t *testing.T, got []string, want ...string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("JSON 键 %v，期望 %v", got, want)
	}
}

func TestPoolRoutesRegistered(t *testing.T) {
	e := newPoolEngine(&poolReauthStub{})
	have := map[string]bool{}
	for _, r := range e.Routes() {
		have[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /v1/node-pools", "POST /v1/node-pools", "POST /v1/node-pools/:id", "DELETE /v1/node-pools/:id",
		"GET /v1/plans/:id/pools",
	} {
		if !have[want] {
			t.Fatalf("缺路由 %s", want)
		}
	}
}

func TestCreatePoolWithGroupsNeedsReauth(t *testing.T) {
	stub := &poolReauthStub{deny: true}
	e := newPoolEngine(stub)
	w := doPool(e, http.MethodPost, "/v1/node-pools", `{"name":"x","allowed_user_group_ids":[]}`)
	poolExpectErr(t, w, errs.ReauthRequired, "")
	if stub.calls != 1 {
		t.Fatalf("重认证调用 %d 次", stub.calls)
	}
}

func TestCreatePoolWithoutGroupsSkipsReauth(t *testing.T) {
	stub := &poolReauthStub{deny: true}
	e := newPoolEngine(stub)
	// 名字为空在 service 开事务前就被拒，走不到数据库
	w := doPool(e, http.MethodPost, "/v1/node-pools", `{"name":"  "}`)
	poolExpectErr(t, w, errs.ValidationFailed, "name")
	if stub.calls != 0 {
		t.Fatalf("不带名单不该要求重认证，调用了 %d 次", stub.calls)
	}
}

func TestUpdatePoolReauthOnlyWithGroups(t *testing.T) {
	stub := &poolReauthStub{deny: true}
	e := newPoolEngine(stub)
	path := "/v1/node-pools/" + uuid.NewString()

	w := doPool(e, http.MethodPost, path, `{"status":"bogus"}`)
	poolExpectErr(t, w, errs.ValidationFailed, "status")
	if stub.calls != 0 {
		t.Fatalf("不带名单不该要求重认证，调用了 %d 次", stub.calls)
	}

	w = doPool(e, http.MethodPost, path, `{"allowed_user_group_ids":["x"]}`)
	poolExpectErr(t, w, errs.ReauthRequired, "")
	if stub.calls != 1 {
		t.Fatalf("带名单应要求重认证，调用了 %d 次", stub.calls)
	}

	ok := newPoolEngine(&poolReauthStub{})
	w = doPool(ok, http.MethodPost, path, `{"allowed_user_group_ids":["not-a-uuid"]}`)
	poolExpectErr(t, w, errs.ValidationFailed, "allowed_user_group_ids")

	many := make([]string, service.MaxPoolUserGroups+1)
	for i := range many {
		many[i] = uuid.NewString()
	}
	raw, _ := json.Marshal(map[string]any{"allowed_user_group_ids": many})
	w = doPool(ok, http.MethodPost, path, string(raw))
	poolExpectErr(t, w, errs.ValidationFailed, "allowed_user_group_ids")
}

func TestPoolPathIDNotFound(t *testing.T) {
	e := newPoolEngine(&poolReauthStub{})
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/node-pools/abc", `{"name":"x"}`},
		{http.MethodDelete, "/v1/node-pools/abc", ""},
		{http.MethodGet, "/v1/plans/abc/pools", ""},
	}
	for _, tc := range cases {
		w := doPool(e, tc.method, tc.path, tc.body)
		poolExpectErr(t, w, errs.NotFound, "")
	}
}

func TestPoolRouteNotRegistered(t *testing.T) {
	e := newPoolEngine(&poolReauthStub{})
	// POST /plans/:id/pools 已删除（改绑走 catalog 的 PUT /plans/:id/pools）
	w := doPool(e, http.MethodPost, "/v1/plans/"+uuid.NewString()+"/pools", `{}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("POST /plans/:id/pools 期望 404，得到 %d", w.Code)
	}
}

func TestPoolUnknownFieldRejected(t *testing.T) {
	e := newPoolEngine(&poolReauthStub{})
	w := doPool(e, http.MethodPost, "/v1/node-pools", `{"name":"x","bogus":1}`)
	poolExpectErr(t, w, errs.BadRequest, "")

	// 前端会发的字段都能解析（httpx.BindJSON 拒绝未知字段）
	strict := func(body string, dst any) {
		t.Helper()
		dec := json.NewDecoder(strings.NewReader(body))
		dec.DisallowUnknownFields()
		if err := dec.Decode(dst); err != nil {
			t.Fatalf("解析 %s: %v", body, err)
		}
	}
	var cr poolCreateRequest
	strict(`{"name":"x","code":"c","region":"r","status":"active","allowed_user_group_ids":null}`, &cr)
	if cr.AllowedUserGroupIDs != nil {
		t.Fatal("null 名单应视同未带")
	}
	var ur poolUpdateRequest
	strict(`{"name":"x","region":"r","status":"draining","code":"c","allowed_user_group_ids":[]}`, &ur)
	if ur.AllowedUserGroupIDs == nil || len(*ur.AllowedUserGroupIDs) != 0 {
		t.Fatalf("[] 名单应是非 nil 空切片：%v", ur.AllowedUserGroupIDs)
	}
}

func TestPoolListAndPlanPoolsNeedPorts(t *testing.T) {
	e := newPoolEngine(&poolReauthStub{})
	poolExpectErr(t, doPool(e, http.MethodGet, "/v1/node-pools", ""), errs.Unavailable, "")
	poolExpectErr(t, doPool(e, http.MethodGet, "/v1/plans/"+uuid.NewString()+"/pools", ""), errs.Unavailable, "")
}

// 对照前端 nodes/schemas.ts 的 poolSchema 与 poolsResponse。
func TestPoolResponseShape(t *testing.T) {
	keys, m := poolJSONKeys(t, poolListResponseFrom([]service.Pool{{ID: uuid.New(), Code: "hk", Name: "香港", Status: "active"}}))
	poolExpectKeys(t, keys, "pools")
	p := m["pools"].([]any)[0].(map[string]any)
	poolExpectKeys(t, poolSortedKeys(p), "id", "code", "name", "region", "status", "nodes", "active_nodes",
		"plans", "members", "plan_names", "allowed_user_groups")
	for _, k := range []string{"members", "plan_names", "allowed_user_groups"} {
		if arr, ok := p[k].([]any); !ok || len(arr) != 0 {
			t.Fatalf("%s 应是空数组，得到 %#v", k, p[k])
		}
	}

	_, m = poolJSONKeys(t, poolResponseFrom(service.Pool{
		ID:                uuid.New(),
		Members:           []service.PoolMember{{ID: uuid.New(), Name: "n1", NodeNo: 7}},
		AllowedUserGroups: []service.PoolUserGroup{{ID: uuid.New(), Name: "VIP"}},
	}))
	poolExpectKeys(t, poolSortedKeys(m["members"].([]any)[0].(map[string]any)), "id", "name", "node_no")
	poolExpectKeys(t, poolSortedKeys(m["allowed_user_groups"].([]any)[0].(map[string]any)), "id", "name")

	raw, _ := json.Marshal(poolListResponseFrom(nil))
	if string(raw) != `{"pools":[]}` {
		t.Fatalf("空列表: %s", raw)
	}
	keys, _ = poolJSONKeys(t, poolCreatedResponse{ID: uuid.NewString()})
	poolExpectKeys(t, keys, "id")
	keys, _ = poolJSONKeys(t, poolOKResponse{OK: true})
	poolExpectKeys(t, keys, "ok")
}

// 对照前端 plans/schemas.ts 的 planPoolsSchema。
func TestPlanPoolsResponseShape(t *testing.T) {
	keys, m := poolJSONKeys(t, planPoolsResponseFrom(service.PlanPools{}))
	poolExpectKeys(t, keys, "pools")
	if arr, ok := m["pools"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("pools 应是空数组: %#v", m["pools"])
	}

	_, m = poolJSONKeys(t, planPoolsResponseFrom(service.PlanPools{
		Pools: []service.PlanPoolOption{{ID: uuid.New(), Name: "A", ActiveNodes: 2, DeliverableNodes: 1, Bound: true}},
	}))
	poolExpectKeys(t, poolSortedKeys(m["pools"].([]any)[0].(map[string]any)),
		"id", "name", "active_nodes", "deliverable_nodes", "bound")
}

func TestParsePoolUserGroupIDs(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ids, err := parsePoolUserGroupIDs([]string{b.String(), a.String()})
	if err != nil || len(ids) != 2 || ids[0].String() > ids[1].String() {
		t.Fatalf("合法输入: %v %v", ids, err)
	}
	ids, err = parsePoolUserGroupIDs(nil)
	if err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("空输入应是非 nil 空切片: %v %v", ids, err)
	}
	_, err = parsePoolUserGroupIDs([]string{a.String(), a.String()})
	if errs.As(err).Fields["allowed_user_group_ids"] == "" {
		t.Fatalf("重复应拒绝: %v", err)
	}
}
