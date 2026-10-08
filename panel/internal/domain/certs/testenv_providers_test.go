package certs

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// 三家 DNS 提供方 API 的模拟：只实现预检与 lego 提供方真会调的那几个接口，形状照各家文档与
// SDK 的结构体。建 TXT 写进 fakeDNS，删 TXT 从 fakeDNS 拿掉；凭据不对按各家的错误形状拒绝。

type providerRecord struct {
	id, zone, name, value string
}

type fakeProvider struct {
	dns   *fakeDNS
	zones []string
	// secret 是唯一认的凭据（Cloudflare 的令牌 / 阿里云 AccessKeyId / 腾讯云 SecretId）
	secret string
	// readOnly 为真时写 TXT 被拒（模拟令牌缺 DNS 编辑权限）
	readOnly bool

	mu       sync.Mutex
	nextID   int
	records  map[string]providerRecord
	requests int
	writes   int
}

func newFakeProvider(dns *fakeDNS, secret string, zones ...string) *fakeProvider {
	return &fakeProvider{dns: dns, zones: zones, secret: secret, records: map[string]providerRecord{}}
}

func (p *fakeProvider) add(zone, name, value string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	id := strconv.Itoa(p.nextID)
	p.records[id] = providerRecord{id: id, zone: zone, name: name, value: value}
	p.writes++
	p.dns.addTXT(name, value)
	return id
}

func (p *fakeProvider) remove(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.records[id]
	if ok {
		delete(p.records, id)
		p.dns.removeTXT(r.name, r.value)
	}
	return ok
}

func (p *fakeProvider) count() (requests, writes, live int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests, p.writes, len(p.records)
}

func (p *fakeProvider) hit() {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// inMemoryClient 不开端口，直接把请求交给 handler（Cloudflare 与腾讯云的 SDK 都能注入 HTTP 客户端）。
func inMemoryClient(h http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

//------------------------------------------------------------------------------
// Cloudflare（/client/v4）
//------------------------------------------------------------------------------

const fakeCloudflareBase = "https://cf.test/client/v4"

func (p *fakeProvider) cloudflare() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hit()
		if r.Header.Get("Authorization") != "Bearer "+p.secret {
			writeJSON(w, http.StatusForbidden, map[string]any{"success": false,
				"errors": []map[string]any{{"code": 9109, "message": "Invalid access token"}}})
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/client/v4")
		parts := strings.Split(strings.Trim(path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && path == "/zones":
			var list []map[string]any
			for i, z := range p.zones {
				if name := r.URL.Query().Get("name"); name == "" || name == z {
					list = append(list, map[string]any{"id": fmt.Sprintf("zone%d", i), "name": z,
						"permissions": []string{"#zone:read", "#dns_records:edit"}})
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": list,
				"result_info": map[string]any{"total_count": len(list), "count": len(list), "page": 1, "per_page": 50}})
		case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "dns_records":
			if p.readOnly {
				writeJSON(w, http.StatusForbidden, map[string]any{"success": false,
					"errors": []map[string]any{{"code": 10000, "message": "Authentication error"}}})
				return
			}
			var rec struct {
				Type, Name, Content string
			}
			_ = json.NewDecoder(r.Body).Decode(&rec)
			id := p.add(parts[1], rec.Name, rec.Content)
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": map[string]any{"id": id}})
		case r.Method == http.MethodDelete && len(parts) == 4 && parts[2] == "dns_records":
			p.remove(parts[3])
			writeJSON(w, http.StatusOK, map[string]any{"success": true, "result": map[string]any{"id": parts[3]}})
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false,
				"errors": []map[string]any{{"code": 7003, "message": "no route"}}})
		}
	})
}

//------------------------------------------------------------------------------
// 阿里云 DNS（RPC 风格：Action 在查询串，参数在查询串或表单体）
//------------------------------------------------------------------------------

func (p *fakeProvider) alidns() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hit()
		_ = r.ParseForm()
		get := func(k string) string {
			if v := r.URL.Query().Get(k); v != "" {
				return v
			}
			return r.PostForm.Get(k)
		}
		fail := func(status int, code, msg string) {
			writeJSON(w, status, map[string]any{"RequestId": "req", "Code": code, "Message": msg})
		}
		// 新版 SDK 用 ACS3 签名：AccessKeyId 在 Authorization 头的 Credential=，Action 在 x-acs-action 头
		ak := get("AccessKeyId")
		if auth := r.Header.Get("Authorization"); strings.Contains(auth, "Credential=") {
			ak = strings.SplitN(strings.SplitN(auth, "Credential=", 2)[1], ",", 2)[0]
		}
		action := r.Header.Get("x-acs-action")
		if action == "" {
			action = get("Action")
		}
		if ak != p.secret {
			fail(http.StatusNotFound, "InvalidAccessKeyId.NotFound", "Specified access key is not found.")
			return
		}
		switch action {
		case "DescribeDomains":
			var list []map[string]any
			for i, z := range p.zones {
				list = append(list, map[string]any{"DomainId": fmt.Sprintf("d%d", i), "DomainName": z, "PunyCode": z})
			}
			writeJSON(w, http.StatusOK, map[string]any{"RequestId": "req", "TotalCount": len(list), "PageNumber": 1,
				"PageSize": 100, "Domains": map[string]any{"Domain": list}})
		case "AddDomainRecord":
			if p.readOnly {
				fail(http.StatusForbidden, "Forbidden.RAM", "User not authorized to operate on the specified resource.")
				return
			}
			zone := get("DomainName")
			id := p.add(zone, get("RR")+"."+zone, get("Value"))
			writeJSON(w, http.StatusOK, map[string]any{"RequestId": "req", "RecordId": id})
		case "DescribeDomainRecords":
			zone := get("DomainName")
			var list []map[string]any
			p.mu.Lock()
			for _, rec := range p.records {
				if rec.zone == zone {
					list = append(list, map[string]any{"RecordId": rec.id, "RR": strings.TrimSuffix(rec.name, "."+zone),
						"Type": "TXT", "Value": rec.value, "DomainName": zone})
				}
			}
			p.mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"RequestId": "req", "TotalCount": len(list), "PageNumber": 1,
				"PageSize": 500, "DomainRecords": map[string]any{"Record": list}})
		case "DeleteDomainRecord":
			p.remove(get("RecordId"))
			writeJSON(w, http.StatusOK, map[string]any{"RequestId": "req", "RecordId": get("RecordId")})
		default:
			fail(http.StatusBadRequest, "InvalidAction.NotFound", "unknown action")
		}
	})
}

//------------------------------------------------------------------------------
// 腾讯云 DNSPod（API 3.0：POST /，X-TC-Action 头，JSON 体，回包包在 Response 里）
//------------------------------------------------------------------------------

func (p *fakeProvider) tencent() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hit()
		respond := func(v map[string]any) {
			v["RequestId"] = "req"
			writeJSON(w, http.StatusOK, map[string]any{"Response": v})
		}
		fail := func(code, msg string) {
			respond(map[string]any{"Error": map[string]any{"Code": code, "Message": msg}})
		}
		if !strings.Contains(r.Header.Get("Authorization"), "Credential="+p.secret+"/") {
			fail("AuthFailure.SecretIdNotFound", "The SecretId is not found")
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		str := func(k string) string { s, _ := body[k].(string); return s }
		switch r.Header.Get("X-TC-Action") {
		case "DescribeDomainList":
			var list []map[string]any
			for i, z := range p.zones {
				list = append(list, map[string]any{"DomainId": i + 1, "Name": z, "Punycode": z, "Status": "ENABLE"})
			}
			respond(map[string]any{"DomainCountInfo": map[string]any{"AllTotal": len(list), "DomainTotal": len(list)},
				"DomainList": list})
		case "CreateRecord":
			if p.readOnly {
				fail("UnauthorizedOperation", "unauthorized operation")
				return
			}
			zone := str("Domain")
			id := p.add(zone, str("SubDomain")+"."+zone, str("Value"))
			n, _ := strconv.Atoi(id)
			respond(map[string]any{"RecordId": n})
		case "DescribeRecordList":
			zone, sub := str("Domain"), str("Subdomain")
			var list []map[string]any
			p.mu.Lock()
			for _, rec := range p.records {
				if rec.zone == zone && rec.name == sub+"."+zone {
					n, _ := strconv.Atoi(rec.id)
					list = append(list, map[string]any{"RecordId": n, "Name": sub, "Type": "TXT", "Value": rec.value,
						"Line": "默认"})
				}
			}
			p.mu.Unlock()
			if len(list) == 0 {
				fail("ResourceNotFound.NoDataOfRecord", "no record")
				return
			}
			respond(map[string]any{"RecordCountInfo": map[string]any{"TotalCount": len(list), "ListCount": len(list),
				"SubdomainCount": len(list)}, "RecordList": list})
		case "DeleteRecord":
			id, _ := body["RecordId"].(float64)
			p.remove(strconv.Itoa(int(id)))
			respond(map[string]any{})
		default:
			fail("InvalidAction", "unknown action")
		}
	})
}
