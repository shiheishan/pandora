package certs

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/miekg/dns"
)

// fakeDNS 是进程内的权威 DNS（UDP + TCP）：lego 用它找 zone（SOA）、查传播（TXT），pebble 用它
// 验证 DNS-01（TXT，走 TCP）。三家提供方的模拟 API 收到「建 TXT」就写进这里。
type fakeDNS struct {
	mu    sync.Mutex
	zones []string            // 不带末尾点
	txt   map[string][]string // fqdn（不带末尾点）→ 值
	addr  string
}

func startDNS(t *testing.T, zones ...string) *fakeDNS {
	t.Helper()
	f := &fakeDNS{zones: zones, txt: map[string][]string{}}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	f.addr = pc.LocalAddr().String()
	udp := &dns.Server{PacketConn: pc, Handler: f}
	tcp := &dns.Server{Listener: ln, Handler: f}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	return f
}

func (f *fakeDNS) addTXT(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	f.txt[name] = append(f.txt[name], strings.Trim(value, `"`))
}

func (f *fakeDNS) removeTXT(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	value = strings.Trim(value, `"`)
	vals := f.txt[name]
	for i, v := range vals {
		if v == value {
			f.txt[name] = append(vals[:i:i], vals[i+1:]...)
			break
		}
	}
}

func (f *fakeDNS) txtCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.txt {
		n += len(v)
	}
	return n
}

func (f *fakeDNS) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	if len(req.Question) == 1 {
		q := req.Question[0]
		name := strings.TrimSuffix(strings.ToLower(q.Name), ".")
		f.mu.Lock()
		switch q.Qtype {
		case dns.TypeSOA:
			for _, z := range f.zones {
				if z == name {
					m.Answer = append(m.Answer, &dns.SOA{
						Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
						Ns:  "ns1." + z + ".", Mbox: "hostmaster." + z + ".", Serial: 1, Refresh: 60, Retry: 60,
						Expire: 60, Minttl: 60,
					})
				}
			}
		case dns.TypeTXT:
			for _, v := range f.txt[name] {
				m.Answer = append(m.Answer, &dns.TXT{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60},
					Txt: []string{v},
				})
			}
		}
		f.mu.Unlock()
	}
	_ = w.WriteMsg(m)
}
