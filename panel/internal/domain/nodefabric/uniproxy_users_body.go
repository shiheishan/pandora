package nodefabric

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"sync"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// UniProxy 名单正文按版本只编码一次（w10quiet）。
//
// 10k-r1 的 burst 之后，1000 个节点在一个拉取周期里先后拿到新版本的全量名单：每个请求
// 各自把 1 万人序列化一遍（约 0.8 MB JSON），nginx 再各自 gzip 一遍，节点端点整体停了
// 2–4 秒。同一个池、同一版名单的正文逐字节相同，所以挂在用户集缓存条目上：JSON 与 gzip
// 各算一次，同池节点共享同一份字节；新版本是新条目，旧正文随旧条目一起回收。

// UserSetWire 是 UniProxy /user 的响应结构：字段名与结构必须与 UniProxy 一致，节点端按
// users 数组解析。
type UserSetWire struct {
	Users []ProxyUser `json:"users"`
}

// userSetBody 是一版名单的已编码正文，懒算、并发安全、算完只读。
type userSetBody struct {
	jsonOnce sync.Once
	json     httpx.PreparedResponse
	jsonErr  error

	gzipOnce sync.Once
	gzip     []byte
	gzipErr  error
}

func (b *userSetBody) prepared(users []ProxyUser) (httpx.PreparedResponse, error) {
	b.jsonOnce.Do(func() {
		b.json, b.jsonErr = httpx.PrepareJSON(http.StatusOK, UserSetWire{Users: users})
	})
	return b.json, b.jsonErr
}

func (b *userSetBody) gzipped(users []ProxyUser) ([]byte, error) {
	b.gzipOnce.Do(func() {
		p, err := b.prepared(users)
		if err != nil {
			b.gzipErr = err
			return
		}
		var buf bytes.Buffer
		// 缺省级别（6）：1 万人约 0.8 MB，一版压一次几十毫秒
		zw, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
		if err == nil {
			_, err = zw.Write(p.BodyBytes())
		}
		if err == nil {
			err = zw.Close()
		}
		b.gzip, b.gzipErr = buf.Bytes(), err
	})
	return b.gzip, b.gzipErr
}

// UserSetResponse 是 UniProxy /user 的一次回答：版本（ETag）与按需取的正文。
type UserSetResponse struct {
	Version string
	users   []ProxyUser
	body    *userSetBody
}

// Prepared 返回 JSON 正文（同版本共享，不要改）。
func (r UserSetResponse) Prepared() (httpx.PreparedResponse, error) {
	if r.body == nil {
		return httpx.PrepareJSON(http.StatusOK, UserSetWire{Users: r.users})
	}
	return r.body.prepared(r.users)
}

// Gzipped 返回 gzip 后的 JSON 正文（同版本共享，不要改）。
func (r UserSetResponse) Gzipped() ([]byte, error) {
	b := r.body
	if b == nil {
		b = &userSetBody{}
	}
	return b.gzipped(r.users)
}

// NodeUserSetResponse 同 NodeUserSet，另带共享的已编码正文（缓存路径上）。
func (s *Service) NodeUserSetResponse(ctx context.Context, tenantID string, n *ServingNode) (UserSetResponse, error) {
	set, err := s.nodeUsers(ctx, tenantID, n)
	if err != nil {
		return UserSetResponse{}, err
	}
	if set.version == "" {
		set.version = UserSetVersion(set.users)
	}
	return UserSetResponse{Version: set.version, users: set.users, body: set.body}, nil
}
